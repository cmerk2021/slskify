package tui

import (
	"testing"

	"github.com/cmerk2021/slskify/internal/pipeline"
	"github.com/cmerk2021/slskify/internal/spotify"
	"github.com/cmerk2021/slskify/internal/store"
)

func tv(id int64, st store.State, seq int64) pipeline.TrackView {
	return pipeline.TrackView{Track: store.Track{ID: id, State: st, ReviewSeq: seq, Track: spotify.Track{Title: "t"}}}
}

func TestReviewQueueAppendsWithoutReshuffle(t *testing.T) {
	mm := newMainModel()
	mm.sync(pipeline.Snapshot{Tracks: []pipeline.TrackView{
		tv(1, store.StateNeedsReview, 2), tv(2, store.StateNeedsReview, 1), tv(3, store.StateDone, 0),
	}})
	if got := mm.rq.entries; len(got) != 2 || got[0].id != 2 || got[1].id != 1 {
		t.Fatalf("initial order by review seq: %+v", got)
	}
	// Track 2 is picked (now downloading); track 3 arrives later.
	mm.sync(pipeline.Snapshot{Tracks: []pipeline.TrackView{
		tv(1, store.StateNeedsReview, 2), tv(2, store.StateDownloading, 1), tv(3, store.StateNotFound, 3),
	}})
	if got := mm.rq.entries; len(got) != 3 || got[0].id != 2 || got[2].id != 3 {
		t.Fatalf("arrival must append: %+v", got)
	}
	if mm.unresolved(mm.rq.entries[0]) || !mm.unresolved(mm.rq.entries[1]) {
		t.Fatal("resolution state wrong")
	}
	if i := mm.nextUnresolved(0); i != 1 {
		t.Fatalf("next unresolved after 0 = %d", i)
	}
	if i := mm.nextUnresolved(2); i != 1 {
		t.Fatalf("wrap-around: got %d", i)
	}
	// Track 2's re-search lands back in review with a new seq: re-enters at the end.
	mm.sync(pipeline.Snapshot{Tracks: []pipeline.TrackView{
		tv(1, store.StateNeedsReview, 2), tv(2, store.StateNeedsReview, 4), tv(3, store.StateNotFound, 3),
	}})
	if got := mm.rq.entries; len(got) != 4 || got[3] != (reviewEntry{2, 4}) || got[0] != (reviewEntry{2, 1}) {
		t.Fatalf("re-entry must append: %+v", got)
	}
	if mm.unresolvedCount() != 3 {
		t.Fatalf("unresolved = %d", mm.unresolvedCount())
	}
}

func TestViewsRenderInSmallTerminals(t *testing.T) {
	mm := newMainModel()
	s := pipeline.Snapshot{Run: store.Run{Name: "A very long playlist name that does not fit"}, Counts: map[store.State]int{store.StateNeedsReview: 1},
		Tracks: []pipeline.TrackView{tv(1, store.StateNeedsReview, 1)}}
	mm.sync(s)
	for _, w := range []int{20, 40, 80, 200} {
		for _, h := range []int{4, 10, 40} {
			mm.detail = nil
			mm.view(s, w, h)
			mm.detail = newDetailModel(1, true)
			mm.view(s, w, h)
		}
	}
}
