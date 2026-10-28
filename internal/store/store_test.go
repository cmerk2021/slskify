package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/cmerk2021/slskify/internal/match"
	"github.com/cmerk2021/slskify/internal/spotify"
)

func TestStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	tracks := []spotify.Track{
		{SpotifyID: "a", ISRC: "GB1", Title: "One", Artists: []string{"X", "Y"}, DurationMs: 1000},
		{SpotifyID: "b", Title: "Two", Artists: []string{"X"}},
	}
	run, ts, err := s.CreateRun(ctx, Run{SourceURL: "u", Name: "List", QualityPreset: "lossless-first", SpeedPreset: "medium",
		Skipped: []spotify.SkippedItem{{Position: 3, Reason: "local file"}}}, tracks)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 2 || ts[0].State != StatePending || run.ID == 0 {
		t.Fatalf("create: %+v", ts)
	}
	ts[0].State, ts[0].Query, ts[0].Attempts, ts[0].ReviewSeq = StateNeedsReview, "x one", 2, 7
	ts[0].TransferID, ts[0].TransferUser = "tid", "peer"
	if err := s.UpdateTrack(ctx, ts[0]); err != nil {
		t.Fatal(err)
	}
	cands := []*match.Candidate{
		{File: match.File{Username: "u1", Path: `a\b.flac`, Size: 5, Length: 10}, Confidence: 90,
			Breakdown: []match.Signal{{Name: "title", Points: 45}}, Flags: []string{"x"}},
		{File: match.File{Username: "u2", Path: `c.mp3`}, Confidence: 40, Failed: "rejected"},
	}
	if err := s.SaveCandidates(ctx, ts[0].ID, cands); err != nil {
		t.Fatal(err)
	}
	// Replace works (no unique violation).
	if err := s.SaveCandidates(ctx, ts[0].ID, cands); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.SchemaVersion(ctx); v != len(migrations) {
		t.Fatalf("schema %d", v)
	}
	got, err := s.Tracks(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].State != StateNeedsReview || got[0].Query != "x one" || got[0].Attempts != 2 || got[0].ISRC != "GB1" ||
		len(got[0].Artists) != 2 || got[0].TransferID != "tid" || got[0].ReviewSeq != 7 {
		t.Fatalf("track: %+v", got[0])
	}
	cs, err := s.Candidates(ctx, ts[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || cs[0].Path != `a\b.flac` || cs[0].Breakdown[0].Points != 45 || cs[1].Failed != "rejected" {
		t.Fatalf("candidates: %+v", cs)
	}
	runs, err := s.ListRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Total != 2 || runs[0].Counts[StateNeedsReview] != 1 || len(runs[0].Skipped) != 1 {
		t.Fatalf("runs: %+v", runs)
	}
	if seq, _ := s.MaxReviewSeq(ctx, run.ID); seq != 7 {
		t.Fatalf("seq %d", seq)
	}
	if err := s.DeleteRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if cs, _ := s.Candidates(ctx, ts[0].ID); len(cs) != 0 {
		t.Fatal("cascade delete")
	}
}

func TestMemoryStore(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, _, err := s.CreateRun(context.Background(), Run{SourceURL: "x", QualityPreset: "a", SpeedPreset: "b"}, nil); err != nil {
		t.Fatal(err)
	}
}
