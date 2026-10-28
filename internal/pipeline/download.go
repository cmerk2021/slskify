package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/cmerk2021/slskify/internal/match"
	"github.com/cmerk2021/slskify/internal/slskd"
	"github.com/cmerk2021/slskify/internal/store"
)

// markFailed records a download failure and re-ranks the track's
// candidates so the failed one drops out of the auto group.
func (r *Runner) markFailed(ts *trackState, user, path, reason string) {
	r.mu.Lock()
	key := match.File{Username: user, Path: path}.Key()
	ts.failed[key] = reason
	files := make([]match.File, 0, len(ts.cands))
	for _, c := range ts.cands {
		files = append(files, c.File)
	}
	d := r.opts.Matcher.Evaluate(TargetOf(ts.Track.Track), files, r.opts.Policy, ts.failed)
	ts.cands = d.Candidates
	ts.AutoCount, ts.Passed = d.AutoCount, d.Passed
	ts.TransferID, ts.TransferUser, ts.TransferPath = "", "", ""
	id := ts.ID
	r.mu.Unlock()
	r.persistCandidates(id, d.Candidates)
}

// nextCandidate returns the manual pick (once) or the best remaining
// auto-eligible candidate. Only auto-eligible candidates are used for
// automatic fallback; anything less certain goes back to review.
func (r *Runner) nextCandidate(ts *trackState) *match.Candidate {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ts.manual != "" {
		key := ts.manual
		ts.manual = ""
		for _, c := range ts.cands {
			if c.Key() == key {
				return c
			}
		}
	}
	for _, c := range ts.cands {
		if c.AutoEligible && ts.failed[c.Key()] == "" {
			return c
		}
	}
	return nil
}

// downloadTrack drives one track through download attempts with fallback.
func (r *Runner) downloadTrack(ctx context.Context, id int64) {
	ts := r.get(id)
	if ts == nil {
		return
	}
	r.mu.Lock()
	resume := ts.resumeTransfer && ts.State == store.StateDownloading
	ts.resumeTransfer = false
	ok := ts.State == store.StateRanked || resume
	r.mu.Unlock()
	if !ok {
		return
	}
	maxAttempts := r.opts.Download.MaxCandidateAttempts
	for ctx.Err() == nil {
		var cand *match.Candidate
		var transferID string
		if resume {
			resume = false
			r.mu.Lock()
			transferID = ts.TransferID
			cand = &match.Candidate{File: match.File{Username: ts.TransferUser, Path: ts.TransferPath}}
			r.mu.Unlock()
		} else {
			r.mu.Lock()
			attempts := ts.Attempts
			r.mu.Unlock()
			if attempts >= maxAttempts {
				r.toReview(ts, fmt.Sprintf("%d download attempts failed; pick another candidate", attempts))
				return
			}
			cand = r.nextCandidate(ts)
			if cand == nil {
				r.mu.Lock()
				tried := ts.Attempts
				r.mu.Unlock()
				if tried == 0 {
					r.toReview(ts, "no auto-eligible candidate left")
				} else {
					r.toReview(ts, "download failed and no other confident candidate; pick another")
				}
				return
			}
			r.mu.Lock()
			ts.Attempts++
			ts.ChosenUser, ts.ChosenPath = cand.Username, cand.Path
			ts.TransferID, ts.TransferUser, ts.TransferPath = "", cand.Username, cand.Path
			ts.progress = &Progress{State: slskd.StateQueued, Size: cand.Size, QueuedSince: r.now()}
			r.mu.Unlock()
			if !r.setState(ts, store.StateDownloading, "enqueueing "+shortPath(cand.Path)) {
				return
			}
			tid, err := r.sl.Enqueue(ctx, cand.Username, cand.Path, cand.Size)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				r.markFailed(ts, cand.Username, cand.Path, "enqueue failed: "+err.Error())
				continue
			}
			transferID = tid
			r.mu.Lock()
			ts.TransferID = tid
			r.mu.Unlock()
			r.setState(ts, store.StateDownloading, fmt.Sprintf("attempt %d: %s from %s", ts.Attempts, shortPath(cand.Path), cand.Username))
		}

		ok, reason := r.watch(ctx, ts, transferID, cand.Username, cand.Path)
		if ctx.Err() != nil {
			return // stays "downloading"; reconciled on resume
		}
		if ok {
			r.mu.Lock()
			ts.progress = nil
			r.mu.Unlock()
			r.setState(ts, store.StateDone, "downloaded "+shortPath(cand.Path))
			return
		}
		r.markFailed(ts, cand.Username, cand.Path, reason)
		r.mu.Lock()
		ts.ChosenUser, ts.ChosenPath = cand.Username, cand.Path
		r.mu.Unlock()
	}
}

// transferVerdict is what to do about a transfer after a poll. Keeping this
// a pure function of (transfer, history, config) is where queue-position
// awareness can be added later (e.g. extend the timeout while
// PlaceInQueue is shrinking).
type transferVerdict int

const (
	verdictWait transferVerdict = iota
	verdictSucceeded
	verdictFailed
	verdictCancel
)

type watchState struct {
	queuedSince  time.Time
	lastBytes    int64
	lastProgress time.Time
	missing      int
}

func (r *Runner) judge(tr *slskd.Transfer, w *watchState, now time.Time) (transferVerdict, string) {
	if tr == nil {
		w.missing++
		// Give slskd a few polls to list a just-enqueued transfer.
		if w.missing > 10 {
			return verdictFailed, "transfer disappeared from slskd"
		}
		return verdictWait, ""
	}
	w.missing = 0
	state, reason := tr.Status()
	switch state {
	case slskd.StateSucceeded:
		return verdictSucceeded, ""
	case slskd.StateFailed:
		return verdictFailed, reason
	case slskd.StateQueued:
		if qt := r.opts.Download.QueueTimeout.Duration; qt > 0 && now.Sub(w.queuedSince) > qt {
			return verdictCancel, fmt.Sprintf("still queued after %s", qt.Round(time.Second))
		}
	default: // Initializing / InProgress
		w.queuedSince = now
		if tr.BytesTransferred != w.lastBytes || w.lastProgress.IsZero() {
			w.lastBytes, w.lastProgress = tr.BytesTransferred, now
		} else if st := r.opts.Download.StallTimeout.Duration; st > 0 && now.Sub(w.lastProgress) > st {
			return verdictCancel, fmt.Sprintf("stalled for %s", st.Round(time.Second))
		}
	}
	return verdictWait, ""
}

// watch follows a transfer until it finishes. It returns ok, or a failure
// reason (after cancelling the transfer when slskify gave up on it).
func (r *Runner) watch(ctx context.Context, ts *trackState, id, user, path string) (bool, string) {
	w := &watchState{queuedSince: r.now()}
	sub := r.poller.subscribe()
	defer r.poller.unsubscribe(sub)
	for {
		select {
		case <-ctx.Done():
			return false, ""
		case <-sub:
		}
		tr := r.poller.find(id, user, path)
		if tr != nil {
			r.mu.Lock()
			if id == "" && tr.ID != "" {
				id = tr.ID
				ts.TransferID = id
			}
			st, _ := tr.Status()
			ts.progress = &Progress{State: st, Description: tr.StateDescription, Bytes: tr.BytesTransferred, Size: tr.Size,
				Speed: tr.AverageSpeed, Percent: tr.PercentComplete, PlaceInQueue: tr.PlaceInQueue, QueuedSince: w.queuedSince}
			r.mu.Unlock()
			r.notify()
		}
		v, reason := r.judge(tr, w, r.now())
		switch v {
		case verdictSucceeded:
			return true, ""
		case verdictFailed:
			r.removeTransfer(user, id)
			return false, reason
		case verdictCancel:
			r.cancelTransfer(ctx, user, id)
			return false, reason
		}
	}
}

func (r *Runner) cancelTransfer(ctx context.Context, user, id string) {
	if id == "" {
		return
	}
	if err := r.sl.CancelDownload(ctx, user, id, true); err != nil && ctx.Err() == nil {
		r.addError(fmt.Sprintf("cancelling transfer from %s: %v", user, err))
	}
}

// removeTransfer clears a failed transfer from slskd's list (best-effort).
func (r *Runner) removeTransfer(user, id string) {
	if id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = r.sl.CancelDownload(ctx, user, id, true)
}

func shortPath(p string) string {
	m := match.ParsePath(p)
	if m.Ext != "" {
		return m.Base + "." + m.Ext
	}
	return m.Base
}
