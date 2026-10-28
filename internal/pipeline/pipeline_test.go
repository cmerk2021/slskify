package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cmerk2021/slskify/internal/config"
	"github.com/cmerk2021/slskify/internal/match"
	"github.com/cmerk2021/slskify/internal/slskd"
	"github.com/cmerk2021/slskify/internal/spotify"
	"github.com/cmerk2021/slskify/internal/store"
)

// fakeSlskd simulates slskd: searches return canned responses; transfers
// advance one step per Downloads() poll according to per-file behaviour.
type fakeSlskd struct {
	mu        sync.Mutex
	responses func(q string) []slskd.Response
	behavior  map[string]string // user → succeed|reject|queue|enqueue-error|error
	transfers []*slskd.Transfer
	enqueued  []string
	cancelled []string
	searches  []string
	nextID    int
}

func (f *fakeSlskd) Search(ctx context.Context, q string, _ slskd.SearchOptions) ([]slskd.Response, error) {
	f.mu.Lock()
	f.searches = append(f.searches, q)
	fn := f.responses
	f.mu.Unlock()
	if fn == nil {
		return nil, nil
	}
	return fn(q), nil
}

func (f *fakeSlskd) Enqueue(ctx context.Context, user, file string, size int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enqueued = append(f.enqueued, user)
	if f.behavior[user] == "enqueue-error" {
		return "", errors.New("user offline")
	}
	f.nextID++
	id := fmt.Sprintf("t%d", f.nextID)
	f.transfers = append(f.transfers, &slskd.Transfer{ID: id, Username: user, Filename: file, Size: size, State: "Queued, Remotely"})
	return id, nil
}

func (f *fakeSlskd) Downloads(ctx context.Context) ([]slskd.Transfer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []slskd.Transfer
	for _, t := range f.transfers {
		st, _ := t.Status()
		if !st.Terminal() {
			switch f.behavior[t.Username] {
			case "succeed", "":
				if st == slskd.StateQueued {
					t.State = "InProgress"
					t.BytesTransferred = t.Size / 2
				} else {
					t.State = "Completed, Succeeded"
					t.BytesTransferred = t.Size
				}
			case "reject":
				t.State = "Completed, Rejected"
			case "error":
				t.State = "Completed, Errored"
			case "queue":
			}
		}
		out = append(out, *t)
	}
	return out, nil
}

func (f *fakeSlskd) CancelDownload(ctx context.Context, user, id string, remove bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, user)
	for i, t := range f.transfers {
		if t.ID == id {
			if remove {
				f.transfers = append(f.transfers[:i], f.transfers[i+1:]...)
			} else {
				t.State = "Completed, Cancelled"
			}
			break
		}
	}
	return nil
}

func (f *fakeSlskd) Enqueued() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.enqueued...)
}

var wonderwall = spotify.Track{SpotifyID: "ww", Title: "Wonderwall", Artists: []string{"Oasis"},
	Album: "(What's The Story) Morning Glory?", AlbumType: "album", TrackNumber: 3, DurationMs: 258_000}

func intp(v int) *int { return &v }

// goodResponse is a confident FLAC from user u; speed orders the ranking.
func goodResponse(user string, speed int64) slskd.Response {
	return slskd.Response{Username: user, HasFreeUploadSlot: true, UploadSpeed: speed, Files: []slskd.File{{
		Filename: `@@` + user + `\Music\Oasis\(What's The Story) Morning Glory\03 - Wonderwall.flac`,
		Size:     30_000_000, Extension: "flac", Length: intp(258), BitDepth: intp(16), SampleRate: intp(44100)}}}
}

func testOptions() Options {
	return Options{
		Matcher: match.NewMatcher(match.DefaultConfig()),
		Policy:  match.DefaultQualityPresets()["lossless-first"],
		Speed:   config.SpeedPreset{ConcurrentSearches: 2, ConcurrentDownloads: 2, SearchTimeout: config.D(time.Second)},
		Download: config.DownloadConfig{
			QueueTimeout: config.D(time.Hour), MaxCandidateAttempts: 3, PollInterval: config.D(2 * time.Millisecond),
		},
	}
}

type harness struct {
	t      *testing.T
	sl     *fakeSlskd
	st     *store.Store
	run    store.Run
	r      *Runner
	cancel context.CancelFunc
	done   chan struct{}
}

func newHarness(t *testing.T, sl *fakeSlskd, opts Options, tracks ...spotify.Track) *harness {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	run, _, err := st.CreateRun(context.Background(), store.Run{SourceURL: "x", QualityPreset: "lossless-first", SpeedPreset: "medium"}, tracks)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, sl: sl, st: st, run: run}
}

func (h *harness) start(opts Options, before func(r *Runner)) {
	h.t.Helper()
	h.r = New(h.sl, h.st, h.run, opts)
	if err := h.r.Load(context.Background()); err != nil {
		h.t.Fatal(err)
	}
	if before != nil {
		before(h.r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel, h.done = cancel, make(chan struct{})
	go func() { h.r.Run(ctx); close(h.done) }()
	h.t.Cleanup(h.stop)
}

func (h *harness) stop() {
	if h.cancel != nil {
		h.cancel()
		<-h.done
		h.cancel = nil
	}
}

func (h *harness) waitState(i int, want State) TrackView {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := h.r.Snapshot()
		if s.Tracks[i].State == want {
			return s.Tracks[i]
		}
		time.Sleep(2 * time.Millisecond)
	}
	s := h.r.Snapshot()
	h.t.Fatalf("track %d: want %s, got %s (%s); errors %v", i, want, s.Tracks[i].State, s.Tracks[i].Reason, s.Errors)
	return TrackView{}
}

func TestPipelineSuccess(t *testing.T) {
	sl := &fakeSlskd{responses: func(string) []slskd.Response {
		return []slskd.Response{goodResponse("fast", 9e6), goodResponse("slow", 1e5)}
	}}
	h := newHarness(t, sl, testOptions(), wonderwall)
	h.start(testOptions(), nil)
	v := h.waitState(0, store.StateDone)
	if got := sl.Enqueued(); len(got) != 1 || got[0] != "fast" {
		t.Fatalf("enqueued %v", got)
	}
	if v.ChosenUser != "fast" || len(v.Candidates) != 2 {
		t.Fatalf("view %+v", v)
	}
	// Persisted.
	h.stop()
	ts, _ := h.st.Tracks(context.Background(), h.run.ID)
	if ts[0].State != store.StateDone {
		t.Fatalf("persisted state %s", ts[0].State)
	}
	// Early stop: the first query was auto, so only one search ran.
	if len(sl.searches) != 1 {
		t.Fatalf("searches %v", sl.searches)
	}
}

func TestPipelineRejectionFallsBackToNext(t *testing.T) {
	sl := &fakeSlskd{
		responses: func(string) []slskd.Response {
			return []slskd.Response{goodResponse("a", 9e6), goodResponse("b", 5e6)}
		},
		behavior: map[string]string{"a": "reject"},
	}
	h := newHarness(t, sl, testOptions(), wonderwall)
	h.start(testOptions(), nil)
	v := h.waitState(0, store.StateDone)
	if got := sl.Enqueued(); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("enqueued %v", got)
	}
	var failed string
	for _, c := range v.Candidates {
		if c.Username == "a" {
			failed = c.Failed
		}
	}
	if failed != "rejected" {
		t.Fatalf("failure reason %q", failed)
	}
}

func TestPipelineQueueTimeout(t *testing.T) {
	sl := &fakeSlskd{
		responses: func(string) []slskd.Response {
			return []slskd.Response{goodResponse("stuck", 9e6), goodResponse("ok", 5e6)}
		},
		behavior: map[string]string{"stuck": "queue"},
	}
	opts := testOptions()
	opts.Download.QueueTimeout = config.D(30 * time.Millisecond)
	h := newHarness(t, sl, opts, wonderwall)
	h.start(opts, nil)
	v := h.waitState(0, store.StateDone)
	if v.ChosenUser != "ok" {
		t.Fatalf("chosen %s", v.ChosenUser)
	}
	sl.mu.Lock()
	defer sl.mu.Unlock()
	if len(sl.cancelled) == 0 || sl.cancelled[0] != "stuck" {
		t.Fatalf("stuck transfer should be cancelled: %v", sl.cancelled)
	}
}

func TestPipelineExhaustionGoesToReview(t *testing.T) {
	sl := &fakeSlskd{
		responses: func(string) []slskd.Response {
			return []slskd.Response{goodResponse("a", 9e6), goodResponse("b", 8e6), goodResponse("c", 7e6), goodResponse("d", 6e6)}
		},
		behavior: map[string]string{"a": "reject", "b": "error", "c": "enqueue-error", "d": "reject"},
	}
	h := newHarness(t, sl, testOptions(), wonderwall)
	h.start(testOptions(), nil)
	v := h.waitState(0, store.StateNeedsReview)
	if got := sl.Enqueued(); len(got) != 3 {
		t.Fatalf("max 3 attempts, got %v", got)
	}
	reasons := map[string]string{}
	for _, c := range v.Candidates {
		reasons[c.Username] = c.Failed
	}
	if reasons["a"] != "rejected" || reasons["b"] != "errored" || reasons["c"] == "" || reasons["d"] != "" {
		t.Fatalf("reasons %v", reasons)
	}
	// d is still offered; failed ones sort after it.
	if v.Candidates[0].Username != "d" {
		t.Fatalf("first candidate %s", v.Candidates[0].Username)
	}
	// Review: pick d manually → downloads directly.
	sl.mu.Lock()
	sl.behavior["d"] = "succeed"
	sl.mu.Unlock()
	if err := h.r.Pick(v.ID, v.Candidates[0].Key()); err != nil {
		t.Fatal(err)
	}
	v = h.waitState(0, store.StateDone)
	if v.ChosenUser != "d" {
		t.Fatalf("chosen %s", v.ChosenUser)
	}
}

func TestPipelineNotFoundAndReviewActions(t *testing.T) {
	other := spotify.Track{SpotifyID: "x", Title: "Some Unknown Song", Artists: []string{"Nobody"}, DurationMs: 200_000}
	sl := &fakeSlskd{responses: func(q string) []slskd.Response {
		if q == "custom words" {
			return []slskd.Response{goodResponse("u", 1e6)}
		}
		return nil
	}}
	h := newHarness(t, sl, testOptions(), wonderwall, other)
	h.start(testOptions(), nil)
	v0 := h.waitState(0, store.StateNotFound)
	v1 := h.waitState(1, store.StateNotFound)
	s := h.r.Snapshot()
	order := s.ReviewOrder()
	if len(order) != 2 {
		t.Fatalf("review order %v", order)
	}
	if err := h.r.Skip(v1.ID); err != nil {
		t.Fatal(err)
	}
	h.waitState(1, store.StateSkipped)
	// Re-search with an edited query; even a confident result returns to
	// review because the user asked to choose.
	if err := h.r.Research(v0.ID, "Custom: words!"); err != nil {
		t.Fatal(err)
	}
	v := h.waitState(0, store.StateNeedsReview)
	if len(v.Candidates) != 1 || v.ReviewSeq <= v0.ReviewSeq {
		t.Fatalf("after research: %+v", v)
	}
	if err := h.r.Pick(v.ID, v.Candidates[0].Key()); err != nil {
		t.Fatal(err)
	}
	h.waitState(0, store.StateDone)
	if err := h.r.Pick(v.ID, v.Candidates[0].Key()); !errors.Is(err, ErrNotReviewable) {
		t.Fatalf("pick on done: %v", err)
	}
}

func TestPipelinePause(t *testing.T) {
	sl := &fakeSlskd{responses: func(string) []slskd.Response { return []slskd.Response{goodResponse("a", 1)} }}
	h := newHarness(t, sl, testOptions(), wonderwall)
	h.start(testOptions(), func(r *Runner) { r.Pause() })
	time.Sleep(30 * time.Millisecond)
	if s := h.r.Snapshot(); !s.Paused || s.Tracks[0].State != store.StatePending {
		t.Fatalf("should be paused & pending: %+v", s.Tracks[0].State)
	}
	h.r.Resume()
	h.waitState(0, store.StateDone)
}

func TestPipelineResumeReconciliation(t *testing.T) {
	ctx := context.Background()
	sl := &fakeSlskd{behavior: map[string]string{}}
	tracks := []spotify.Track{wonderwall, wonderwall, wonderwall, wonderwall, wonderwall}
	h := newHarness(t, sl, testOptions(), tracks...)
	ts, _ := h.st.Tracks(ctx, h.run.ID)
	set := func(i int, state State, id, user string) {
		ts[i].State, ts[i].TransferID, ts[i].TransferUser = state, id, user
		ts[i].TransferPath, ts[i].ChosenUser = "file-"+user, user
		ts[i].ChosenPath = ts[i].TransferPath
		if err := h.st.UpdateTrack(ctx, ts[i]); err != nil {
			t.Fatal(err)
		}
	}
	// 0: finished while closed. 1: still running (adopt, don't re-enqueue).
	// 2: no longer listed → review. 3: was mid-search → re-searched.
	// 4: failed while closed with no other candidates → review.
	set(0, store.StateDownloading, "t-done", "u0")
	set(1, store.StateDownloading, "t-run", "u1")
	set(2, store.StateDownloading, "t-gone", "u2")
	set(3, store.StateSearching, "", "")
	set(4, store.StateDownloading, "", "u4") // located by user+filename
	sl.transfers = []*slskd.Transfer{
		{ID: "t-done", Username: "u0", Filename: "file-u0", State: "Completed, Succeeded"},
		{ID: "t-run", Username: "u1", Filename: "file-u1", State: "InProgress", Size: 10},
		{ID: "t-x", Username: "u4", Filename: "file-u4", State: "Completed, Rejected"},
	}
	sl.responses = func(string) []slskd.Response { return nil }

	h.start(testOptions(), nil)
	h.waitState(0, store.StateDone)
	h.waitState(1, store.StateDone)
	if v := h.waitState(2, store.StateNeedsReview); v.Reason == "" {
		t.Fatal("reason expected")
	}
	h.waitState(3, store.StateNotFound)
	h.waitState(4, store.StateNeedsReview)
	if got := sl.Enqueued(); len(got) != 0 {
		t.Fatalf("nothing should be re-enqueued, got %v", got)
	}
}

func TestShutdownPersistsSearchingAsPending(t *testing.T) {
	block := make(chan struct{})
	sl := &fakeSlskd{}
	sl.responses = func(string) []slskd.Response { <-block; return nil }
	h := newHarness(t, sl, testOptions(), wonderwall)
	h.start(testOptions(), nil)
	h.waitState(0, store.StateSearching)
	// Cancel while the search is in flight; the fake ignores ctx so
	// unblock it after cancelling.
	h.cancel()
	close(block)
	<-h.done
	h.cancel = nil
	ts, _ := h.st.Tracks(context.Background(), h.run.ID)
	if ts[0].State != store.StatePending {
		t.Fatalf("want pending after shutdown, got %s", ts[0].State)
	}
}

func TestTransitions(t *testing.T) {
	if CanTransition(store.StateDone, store.StatePending) {
		t.Error("done is terminal")
	}
	if !CanTransition(store.StateNeedsReview, store.StateRanked) || !CanTransition(store.StateDownloading, store.StateNeedsReview) {
		t.Error("review/fallback transitions must be allowed")
	}
	if CanTransition(store.StatePending, store.StateDownloading) {
		t.Error("must search before downloading")
	}
}

func TestFilesFromResponses(t *testing.T) {
	vbr := true
	fs := FilesFromResponses([]slskd.Response{{Username: "u", HasFreeUploadSlot: true, UploadSpeed: 5, QueueLength: 2,
		Files:       []slskd.File{{Filename: "a.mp3", Size: 1, Extension: ".MP3", BitRate: intp(245), IsVariableBitRate: &vbr, Length: intp(100)}},
		LockedFiles: []slskd.File{{Filename: "b.flac", Size: 2}}}})
	if len(fs) != 2 || fs[0].Ext != "mp3" || !fs[0].VBR || fs[0].Length != 100 || !fs[0].FreeSlot || fs[0].QueueLength != 2 || !fs[1].Locked {
		t.Fatalf("%+v", fs)
	}
}
