package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cmerk2021/slskify/internal/organize"
	"github.com/cmerk2021/slskify/internal/slskd"
	"github.com/cmerk2021/slskify/internal/spotify"
	"github.com/cmerk2021/slskify/internal/store"
)

type organizeCall struct {
	title, path string
	size        int64
}

type fakeOrganizer struct {
	mu    sync.Mutex
	calls []organizeCall
	err   error
	warn  []string
	block chan struct{}
}

func (f *fakeOrganizer) Organize(ctx context.Context, t spotify.Track, remote string, size int64) (organize.Result, error) {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, organizeCall{t.Title, remote, size})
	if f.err != nil {
		return organize.Result{}, f.err
	}
	return organize.Result{Path: "/music/" + t.Title + ".flac", Warnings: f.warn}, nil
}

func (f *fakeOrganizer) Calls() []organizeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]organizeCall(nil), f.calls...)
}

func TestPipelineOrganizesFinishedDownload(t *testing.T) {
	sl := &fakeSlskd{responses: func(string) []slskd.Response { return []slskd.Response{goodResponse("a", 9e6)} }}
	org := &fakeOrganizer{warn: []string{"no cover art on Spotify"}}
	opts := testOptions()
	opts.Organizer = org
	h := newHarness(t, sl, opts, wonderwall)
	h.start(opts, nil)
	v := h.waitState(0, store.StateDone)
	calls := org.Calls()
	if len(calls) != 1 || calls[0].title != "Wonderwall" || calls[0].size != 30_000_000 || !strings.HasSuffix(calls[0].path, "Wonderwall.flac") {
		t.Fatalf("calls %+v", calls)
	}
	if !strings.HasPrefix(v.Reason, "saved to /music/Wonderwall.flac") {
		t.Fatalf("reason %q", v.Reason)
	}
	if s := h.r.Snapshot(); len(s.Errors) != 1 {
		t.Fatalf("warning should be surfaced: %v", s.Errors)
	}
}

func TestPipelineOrganizeFailureStillDone(t *testing.T) {
	sl := &fakeSlskd{responses: func(string) []slskd.Response { return []slskd.Response{goodResponse("a", 9e6)} }}
	opts := testOptions()
	opts.Organizer = &fakeOrganizer{err: errors.New("downloaded file not found")}
	h := newHarness(t, sl, opts, wonderwall)
	h.start(opts, nil)
	v := h.waitState(0, store.StateDone)
	if !strings.Contains(v.Reason, "not organized: downloaded file not found") {
		t.Fatalf("reason %q", v.Reason)
	}
	if got := sl.Enqueued(); len(got) != 1 {
		t.Fatalf("no retry download expected: %v", got)
	}
}

func TestPipelineResumeOrganizesCompletedTransfer(t *testing.T) {
	ctx := context.Background()
	sl := &fakeSlskd{}
	org := &fakeOrganizer{}
	opts := testOptions()
	opts.Organizer = org
	h := newHarness(t, sl, opts, wonderwall)
	ts, _ := h.st.Tracks(ctx, h.run.ID)
	ts[0].State, ts[0].TransferID, ts[0].TransferUser, ts[0].TransferPath = store.StateDownloading, "t-done", "u0", `x\file.flac`
	ts[0].ChosenUser, ts[0].ChosenPath = "u0", `x\file.flac`
	if err := h.st.UpdateTrack(ctx, ts[0]); err != nil {
		t.Fatal(err)
	}
	sl.transfers = []*slskd.Transfer{{ID: "t-done", Username: "u0", Filename: `x\file.flac`, Size: 42, State: "Completed, Succeeded"}}
	h.start(opts, nil)
	v := h.waitState(0, store.StateDone)
	if calls := org.Calls(); len(calls) != 1 || calls[0].path != `x\file.flac` || calls[0].size != 42 {
		t.Fatalf("calls %+v", calls)
	}
	if !strings.HasPrefix(v.Reason, "saved to") || len(sl.Enqueued()) != 0 {
		t.Fatalf("reason %q, enqueued %v", v.Reason, sl.Enqueued())
	}
}

func TestPipelineOrganizeFinishedDuringShutdownIsRecorded(t *testing.T) {
	sl := &fakeSlskd{responses: func(string) []slskd.Response { return []slskd.Response{goodResponse("a", 9e6)} }}
	org := &fakeOrganizer{block: make(chan struct{})}
	opts := testOptions()
	opts.Organizer = org
	h := newHarness(t, sl, opts, wonderwall)
	h.start(opts, nil)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.HasPrefix(h.r.Snapshot().Tracks[0].Reason, "organizing") {
		if time.Now().After(deadline) {
			t.Fatal("never started organizing")
		}
		time.Sleep(2 * time.Millisecond)
	}
	h.cancel() // quit while the file is being moved; the move completes
	close(org.block)
	<-h.done
	h.cancel = nil
	ts, _ := h.st.Tracks(context.Background(), h.run.ID)
	if ts[0].State != store.StateDone || !strings.HasPrefix(ts[0].Reason, "saved to") {
		t.Fatalf("persisted %s %q", ts[0].State, ts[0].Reason)
	}
}
