package main

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmerk2021/slskify/internal/app"
	"github.com/cmerk2021/slskify/internal/config"
	"github.com/cmerk2021/slskify/internal/fakeapi"
)

func TestDryRunAgainstFakes(t *testing.T) {
	fake := fakeapi.Demo()
	srv := httptest.NewServer(fake.Handler())
	defer srv.Close()

	cfg := config.Default()
	cfg.Slskd.URL, cfg.Slskd.APIKey = srv.URL, "key"
	cfg.Spotify.ClientID, cfg.Spotify.ClientSecret = "id", "secret"
	a := &app.App{Cfg: cfg, SpotifyAPIURL: srv.URL + "/v1", SpotifyAccountsURL: srv.URL}
	a.Reconnect()

	var out bytes.Buffer
	err := dryRun(context.Background(), &out, a, "https://open.spotify.com/playlist/"+fake.PlaylistID, dryRunOpts{top: 3, rejected: 2})
	if err != nil {
		t.Fatal(err)
	}
	s := out.String()
	t.Log(s)
	sections := strings.Split(s, "\n[")
	if len(sections) != 5 {
		t.Fatalf("want 4 track sections, got %d", len(sections)-1)
	}
	want := []string{"DECISION: AUTO", "DECISION: AUTO", "DECISION: REVIEW", "DECISION: NOT_FOUND"}
	for i, w := range want {
		if !strings.Contains(sections[i+1], w) {
			t.Errorf("track %d: want %q in:\n%s", i+1, w, sections[i+1])
		}
	}
	for _, w := range []string{"1 skipped", "local file", "Summary: 2 auto, 1 review, 1 not found (of 4)", "title ", "duration "} {
		if !strings.Contains(s, w) {
			t.Errorf("output missing %q", w)
		}
	}
	// Wrong versions must never be ranked first for Bohemian Rhapsody.
	first := sections[1][strings.Index(sections[1], "#1 "):]
	first = first[:strings.Index(first, "\n")+1] + first[strings.Index(first, "\n")+1:][:strings.Index(first[strings.Index(first, "\n")+1:], "\n")]
	if strings.Contains(first, "Karaoke") || strings.Contains(first, "Live") {
		t.Errorf("wrong version ranked first: %s", first)
	}
	if len(fake.Transfers()) != 0 {
		t.Error("dry-run must not enqueue downloads")
	}
	// --track limits to one track.
	out.Reset()
	if err := dryRun(context.Background(), &out, a, "spotify:playlist:"+fake.PlaylistID, dryRunOpts{top: 1, only: 4}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "(of 1)") || !strings.Contains(out.String(), "Nonexistent") {
		t.Errorf("--track: %s", out.String())
	}
}
