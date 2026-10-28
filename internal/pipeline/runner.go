package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/cmerk2021/slskify/internal/config"
	"github.com/cmerk2021/slskify/internal/match"
	"github.com/cmerk2021/slskify/internal/slskd"
	"github.com/cmerk2021/slskify/internal/store"
)

// State aliases the persisted track state.
type State = store.State

// transitions is the per-track state machine. Side exits: needs_review,
// not_found, failed, skipped.
var transitions = map[State][]State{
	store.StatePending:     {store.StateSearching, store.StateSkipped},
	store.StateSearching:   {store.StateRanked, store.StateNeedsReview, store.StateNotFound, store.StateFailed, store.StatePending, store.StateSkipped},
	store.StateRanked:      {store.StateDownloading, store.StateNeedsReview, store.StatePending, store.StateSkipped},
	store.StateDownloading: {store.StateDownloading, store.StateDone, store.StateNeedsReview, store.StateFailed, store.StateRanked},
	store.StateNeedsReview: {store.StateNeedsReview, store.StateRanked, store.StatePending, store.StateSkipped},
	store.StateNotFound:    {store.StateNeedsReview, store.StateRanked, store.StatePending, store.StateSkipped},
	store.StateFailed:      {store.StateRanked, store.StatePending, store.StateSkipped},
	store.StateSkipped:     {store.StateRanked, store.StatePending},
	store.StateDone:        {},
}

// CanTransition reports whether from → to is allowed.
func CanTransition(from, to State) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Progress is the live transfer state of a downloading track.
type Progress struct {
	State        slskd.State
	Description  string
	Bytes, Size  int64
	Speed        float64 // bytes/s
	Percent      float64
	PlaceInQueue *int
	QueuedSince  time.Time
}

// Options configure a runner.
type Options struct {
	Matcher  *match.Matcher
	Policy   match.QualityPolicy
	Speed    config.SpeedPreset
	Download config.DownloadConfig
}

type trackState struct {
	store.Track
	cands    []*match.Candidate
	failed   map[string]string // candidate key → failure reason
	manual   string            // candidate key picked in review
	queries  []string          // last queries run
	activity string            // e.g. "searching: queen bohemian rhapsody"
	progress *Progress
	// resumeTransfer: on resume, adopt this existing transfer instead of
	// enqueueing again.
	resumeTransfer bool
}

// TrackView is an immutable snapshot of one track for frontends.
type TrackView struct {
	store.Track
	// Candidates in rank order (see match.Decision). Treat as read-only.
	Candidates []*match.Candidate
	Queries    []string
	Activity   string
	Progress   *Progress
}

// Snapshot is the state of the whole run.
type Snapshot struct {
	Run    store.Run
	Tracks []TrackView
	Paused bool
	Counts map[State]int
	// Errors are recent non-fatal errors, newest last.
	Errors []string
	// Idle is true when no track is waiting for or doing automatic work.
	Idle bool
}

// Runner orchestrates one run.
type Runner struct {
	sl   SlskdClient
	st   Store
	opts Options
	run  store.Run

	mu      sync.Mutex
	tracks  []*trackState
	byID    map[int64]*trackState
	seq     int64
	errs    []string
	updates chan struct{}

	searchQ, downloadQ *workQueue
	gate               *gate
	poller             *poller
	searchRate         *rateLimiter
	wg                 sync.WaitGroup
	now                func() time.Time
}

// New creates a runner for an existing run (fresh or resumed).
func New(sl SlskdClient, st Store, run store.Run, opts Options) *Runner {
	if opts.Download.PollInterval.Duration <= 0 {
		opts.Download.PollInterval = config.D(2 * time.Second)
	}
	if opts.Download.MaxCandidateAttempts <= 0 {
		opts.Download.MaxCandidateAttempts = 3
	}
	r := &Runner{
		sl: sl, st: st, run: run, opts: opts,
		byID:       map[int64]*trackState{},
		updates:    make(chan struct{}, 1),
		searchQ:    newWorkQueue(),
		downloadQ:  newWorkQueue(),
		gate:       newGate(),
		searchRate: &rateLimiter{interval: opts.Speed.SearchDelay.Duration},
		now:        time.Now,
	}
	r.poller = newPoller(sl, opts.Download.PollInterval.Duration, r.addError)
	return r
}

// Updates delivers a coalesced signal whenever the snapshot changes.
func (r *Runner) Updates() <-chan struct{} { return r.updates }

func (r *Runner) notify() {
	select {
	case r.updates <- struct{}{}:
	default:
	}
}

func (r *Runner) addError(msg string) {
	r.mu.Lock()
	r.errs = append(r.errs, time.Now().Format("15:04:05")+" "+msg)
	if len(r.errs) > 20 {
		r.errs = r.errs[len(r.errs)-20:]
	}
	r.mu.Unlock()
	r.notify()
}

// Load reads tracks and candidates from the store and prepares queues,
// reconciling tracks that were downloading against slskd's transfer list
// so nothing is enqueued twice.
func (r *Runner) Load(ctx context.Context) error {
	tracks, err := r.st.Tracks(ctx, r.run.ID)
	if err != nil {
		return err
	}
	seq, err := r.st.MaxReviewSeq(ctx, r.run.ID)
	if err != nil {
		return err
	}
	var transfers []slskd.Transfer
	needTransfers := false
	for _, t := range tracks {
		if t.State == store.StateDownloading {
			needTransfers = true
		}
	}
	if needTransfers {
		transfers, err = r.sl.Downloads(ctx)
		if err != nil {
			return fmt.Errorf("could not reconcile downloads with slskd: %w", err)
		}
	}

	r.mu.Lock()
	r.seq = seq
	r.mu.Unlock()
	for _, t := range tracks {
		ts := &trackState{Track: t, failed: map[string]string{}}
		cands, err := r.st.Candidates(ctx, t.ID)
		if err != nil {
			return err
		}
		ts.cands = cands
		for _, c := range cands {
			if c.Failed != "" {
				ts.failed[c.Key()] = c.Failed
			}
		}
		r.mu.Lock()
		r.tracks = append(r.tracks, ts)
		r.byID[t.ID] = ts
		r.mu.Unlock()

		switch t.State {
		case store.StatePending, store.StateSearching:
			if t.State == store.StateSearching {
				r.setState(ts, store.StatePending, "re-queued after restart")
			}
			r.searchQ.Push(t.ID)
		case store.StateRanked:
			r.downloadQ.Push(t.ID)
		case store.StateDownloading:
			r.reconcile(ts, transfers)
		}
	}
	r.notify()
	return nil
}

// reconcile decides what to do with a track that was downloading when
// slskify last stopped.
func (r *Runner) reconcile(ts *trackState, transfers []slskd.Transfer) {
	tr := findTransfer(transfers, ts.TransferID, ts.TransferUser, ts.TransferPath)
	if tr == nil {
		// Not listed: it may have completed and been cleared, or slskd lost
		// it. Re-enqueueing could download twice, so ask the user.
		ts.manual = ""
		r.toReview(ts, "transfer no longer listed by slskd (it may have finished and been cleared); pick again to re-download")
		return
	}
	state, reason := tr.Status()
	switch state {
	case slskd.StateSucceeded:
		r.setState(ts, store.StateDone, "downloaded (completed while slskify was closed)")
	case slskd.StateFailed:
		r.markFailed(ts, ts.ChosenUser, ts.ChosenPath, reason)
		r.setState(ts, store.StateRanked, "previous attempt "+reason+"; trying next candidate")
		r.downloadQ.Push(ts.ID)
	default:
		if ts.TransferID == "" {
			ts.TransferID = tr.ID
		}
		ts.resumeTransfer = true
		r.downloadQ.PushFront(ts.ID)
	}
}

func findTransfer(ts []slskd.Transfer, id, user, path string) *slskd.Transfer {
	if id != "" {
		for i := range ts {
			if ts[i].ID == id {
				return &ts[i]
			}
		}
	}
	if user == "" || path == "" {
		return nil
	}
	var best *slskd.Transfer
	for i := range ts {
		if ts[i].Username == user && ts[i].Filename == path {
			// Prefer a non-terminal one if several exist.
			if best == nil {
				best = &ts[i]
			} else if st, _ := ts[i].Status(); !st.Terminal() {
				best = &ts[i]
			}
		}
	}
	return best
}

// Run processes tracks until ctx is cancelled. It returns after all
// workers have stopped and state is persisted.
func (r *Runner) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r.wg.Add(3)
	go func() { defer r.wg.Done(); r.poller.run(ctx) }()
	go func() { defer r.wg.Done(); r.dispatch(ctx, r.searchQ, r.opts.Speed.ConcurrentSearches, r.searchTrack) }()
	go func() {
		defer r.wg.Done()
		r.dispatch(ctx, r.downloadQ, r.opts.Speed.ConcurrentDownloads, r.downloadTrack)
	}()
	<-ctx.Done()
	r.wg.Wait()
	r.shutdown()
	return nil
}

// shutdown persists in-flight searches as pending so a resume re-runs them.
// Downloading tracks keep their transfer reference for reconciliation.
func (r *Runner) shutdown() {
	r.mu.Lock()
	var dirty []store.Track
	for _, ts := range r.tracks {
		if ts.State == store.StateSearching {
			ts.State = store.StatePending
			ts.activity = ""
			dirty = append(dirty, ts.Track)
		}
	}
	r.mu.Unlock()
	for _, t := range dirty {
		r.persistTrack(t)
	}
}

func (r *Runner) dispatch(ctx context.Context, q *workQueue, n int, work func(context.Context, int64)) {
	if n < 1 {
		n = 1
	}
	sem := make(chan struct{}, n)
	for {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		if r.gate.Wait(ctx) != nil {
			return
		}
		id, ok := q.Pop(ctx)
		if !ok {
			return
		}
		// Pausing while blocked in Pop: hold the item until resumed.
		if r.gate.Wait(ctx) != nil {
			q.PushFront(id)
			return
		}
		r.wg.Add(1)
		go func() {
			defer func() { <-sem; r.wg.Done() }()
			work(ctx, id)
		}()
	}
}

// Pause stops dispatching new searches/downloads; in-flight work finishes.
func (r *Runner) Pause() { r.gate.Close(); r.notify() }

// Resume re-enables dispatching.
func (r *Runner) Resume() { r.gate.Open(); r.notify() }

// Paused reports the pause state.
func (r *Runner) Paused() bool { return r.gate.Closed() }

// TogglePause flips the pause state.
func (r *Runner) TogglePause() {
	if r.Paused() {
		r.Resume()
	} else {
		r.Pause()
	}
}

// ---- state changes (all write-through) ----

func (r *Runner) persistTrack(t store.Track) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.st.UpdateTrack(ctx, t); err != nil {
		r.addError(fmt.Sprintf("saving %q: %v", t.Title, err))
	}
}

func (r *Runner) persistCandidates(id int64, cs []*match.Candidate) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.st.SaveCandidates(ctx, id, cs); err != nil {
		r.addError(fmt.Sprintf("saving candidates: %v", err))
	}
}

// setState validates and persists a transition. Invalid transitions are
// refused and reported (they indicate a bug, never user error).
func (r *Runner) setState(ts *trackState, to State, reason string) bool {
	r.mu.Lock()
	from := ts.State
	if from != to && !CanTransition(from, to) {
		r.mu.Unlock()
		r.addError(fmt.Sprintf("internal: refused transition %s → %s for %q", from, to, ts.Title))
		return false
	}
	ts.State, ts.Reason = to, reason
	if to == store.StateNeedsReview || to == store.StateNotFound {
		r.seq++
		ts.ReviewSeq = r.seq
	}
	if to != store.StateDownloading {
		ts.progress = nil
	}
	if to != store.StateSearching {
		ts.activity = ""
	}
	t := ts.Track
	r.mu.Unlock()
	r.persistTrack(t)
	r.notify()
	return true
}

func (r *Runner) toReview(ts *trackState, reason string) {
	r.setState(ts, store.StateNeedsReview, reason)
}

func (r *Runner) get(id int64) *trackState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byID[id]
}

// ---- search ----

func (r *Runner) searchTrack(ctx context.Context, id int64) {
	ts := r.get(id)
	if ts == nil {
		return
	}
	r.mu.Lock()
	if ts.State != store.StatePending {
		r.mu.Unlock()
		return
	}
	custom := ts.Query
	target := TargetOf(ts.Track.Track)
	r.mu.Unlock()
	if !r.setState(ts, store.StateSearching, "") {
		return
	}

	s := &Searcher{
		Slskd: r.sl, Matcher: r.opts.Matcher, Policy: r.opts.Policy,
		Options:      SearchOptionsFor(r.opts.Speed),
		BeforeSearch: r.searchRate.Wait,
		OnQuery: func(q string) {
			r.mu.Lock()
			ts.activity = "searching: " + q
			r.mu.Unlock()
			r.notify()
		},
	}
	var res SearchResult
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		res, err = s.Search(ctx, target, custom)
		if err == nil || ctx.Err() != nil {
			break
		}
		r.addError(fmt.Sprintf("%q: %v (retrying)", ts.Title, err))
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(attempt+1) * 3 * time.Second):
		}
	}
	if ctx.Err() != nil {
		return // shutdown() resets to pending
	}
	if err != nil {
		r.setState(ts, store.StateFailed, err.Error())
		return
	}
	r.applyDecision(ts, res.Decision, res.Queries, custom != "")
}

// SearchOptionsFor converts a speed preset into slskd search options.
func SearchOptionsFor(sp config.SpeedPreset) slskd.SearchOptions {
	return slskd.SearchOptions{
		TimeoutSeconds: int(sp.SearchTimeout.Duration / time.Second),
		ResponseLimit:  sp.ResponseLimit,
		FileLimit:      sp.FileLimit,
	}
}

// applyDecision stores the candidates and routes the track. Re-searches
// requested from review always return to review so the user decides.
func (r *Runner) applyDecision(ts *trackState, d match.Decision, queries []string, manual bool) {
	r.mu.Lock()
	ts.cands = d.Candidates
	ts.queries = queries
	ts.failed = map[string]string{}
	ts.manual = ""
	ts.Attempts = 0
	ts.AutoCount, ts.Passed = d.AutoCount, d.Passed
	id := ts.ID
	r.mu.Unlock()
	r.persistCandidates(id, d.Candidates)

	switch {
	case d.Outcome == match.OutcomeAuto && !manual:
		if r.setState(ts, store.StateRanked, d.Reason) {
			r.downloadQ.Push(id)
		}
	case d.Outcome == match.OutcomeAuto:
		r.toReview(ts, "re-search: "+d.Reason)
	case d.Outcome == match.OutcomeNotFound:
		r.setState(ts, store.StateNotFound, d.Reason)
	default:
		r.toReview(ts, d.Reason)
	}
}

// ---- review actions ----

var reviewable = map[State]bool{
	store.StateNeedsReview: true, store.StateNotFound: true, store.StateFailed: true, store.StateSkipped: true,
}

// ErrNotReviewable is returned when a review action targets a track that
// is busy (searching/downloading) or done.
var ErrNotReviewable = errors.New("track is not awaiting review")

// Pick downloads the candidate with the given key (match.File.Key()).
func (r *Runner) Pick(trackID int64, key string) error {
	ts := r.get(trackID)
	if ts == nil {
		return fmt.Errorf("unknown track %d", trackID)
	}
	r.mu.Lock()
	if !reviewable[ts.State] {
		r.mu.Unlock()
		return ErrNotReviewable
	}
	var found *match.Candidate
	for _, c := range ts.cands {
		if c.Key() == key {
			found = c
		}
	}
	if found == nil {
		r.mu.Unlock()
		return fmt.Errorf("candidate not found")
	}
	if found.Rejected != "" && found.Locked {
		r.mu.Unlock()
		return fmt.Errorf("file is locked by the peer")
	}
	ts.manual = key
	ts.Attempts = 0
	delete(ts.failed, key)
	r.mu.Unlock()
	if !r.setState(ts, store.StateRanked, "picked in review") {
		return ErrNotReviewable
	}
	r.downloadQ.PushFront(trackID)
	return nil
}

// Skip marks a track as skipped.
func (r *Runner) Skip(trackID int64) error {
	ts := r.get(trackID)
	if ts == nil {
		return fmt.Errorf("unknown track %d", trackID)
	}
	r.mu.Lock()
	ok := reviewable[ts.State] || ts.State == store.StatePending || ts.State == store.StateRanked
	r.mu.Unlock()
	if !ok || !r.setState(ts, store.StateSkipped, "skipped by user") {
		return ErrNotReviewable
	}
	return nil
}

// Research re-searches a track with a user-edited query (empty = the
// generated queries). The result re-enters the review queue.
func (r *Runner) Research(trackID int64, query string) error {
	ts := r.get(trackID)
	if ts == nil {
		return fmt.Errorf("unknown track %d", trackID)
	}
	r.mu.Lock()
	if !reviewable[ts.State] {
		r.mu.Unlock()
		return ErrNotReviewable
	}
	ts.Query = query
	r.mu.Unlock()
	if !r.setState(ts, store.StatePending, "re-search requested") {
		return ErrNotReviewable
	}
	r.searchQ.PushFront(trackID)
	return nil
}

// ---- snapshot ----

// Snapshot returns a consistent copy of the run state.
func (r *Runner) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Snapshot{Run: r.run, Paused: r.gate.Closed(), Counts: map[State]int{}, Errors: append([]string(nil), r.errs...)}
	idle := true
	for _, ts := range r.tracks {
		v := TrackView{Track: ts.Track, Candidates: ts.cands, Queries: ts.queries, Activity: ts.activity}
		if ts.progress != nil {
			p := *ts.progress
			v.Progress = &p
		}
		s.Tracks = append(s.Tracks, v)
		s.Counts[ts.State]++
		switch ts.State {
		case store.StatePending, store.StateSearching, store.StateRanked, store.StateDownloading:
			idle = false
		}
	}
	s.Idle = idle
	return s
}

// ReviewOrder returns track IDs awaiting review (needs_review, not_found)
// ordered by when they entered the queue.
func (s Snapshot) ReviewOrder() []int64 {
	var vs []TrackView
	for _, t := range s.Tracks {
		if t.State == store.StateNeedsReview || t.State == store.StateNotFound {
			vs = append(vs, t)
		}
	}
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].ReviewSeq < vs[j].ReviewSeq })
	out := make([]int64, len(vs))
	for i, v := range vs {
		out[i] = v.ID
	}
	return out
}
