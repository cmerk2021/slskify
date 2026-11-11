package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmerk2021/slskify/internal/app"
	"github.com/cmerk2021/slskify/internal/config"
	"github.com/cmerk2021/slskify/internal/spotify"
	"github.com/cmerk2021/slskify/internal/store"
)

func prerunFixture(t *testing.T, organize bool) (*Model, *prerunModel) {
	t.Helper()
	cfg := config.Default()
	cfg.Slskd.APIKey, cfg.Spotify.ClientID, cfg.Spotify.ClientSecret = "k", "i", "s"
	cfg.Organize = config.OrganizeConfig{Enabled: organize, DownloadsDir: "/dl", MusicDir: "/music", PathTemplate: "{artist}/{title}", PlaylistFile: true}
	a := app.New(cfg, filepath.Join(t.TempDir(), "c.toml"), nil)
	m := newModel(context.Background(), a, Options{})
	m.prerun = newPrerunModel(a, &spotify.Collection{Kind: spotify.KindPlaylist, Name: "Road Trip", Tracks: []spotify.Track{{Title: "x"}}}, nil)
	m.screen = scrPrerun
	return m, m.prerun
}

func TestPrerunOutputOptions(t *testing.T) {
	m, p := prerunFixture(t, true)
	if rs := p.settings(); rs.Output != store.OutputLibrary || !rs.PlaylistFile {
		t.Fatalf("defaults %+v", rs)
	}
	out, _ := p.view(160, 60)
	for _, want := range []string{"Output", "library", "playlist", filepath.Join("/music", "Road Trip", "{artist} - {title}"), filepath.Join("/music", "Road Trip.m3u8")} {
		if !strings.Contains(out, want) {
			t.Fatalf("view lacks %q:\n%s", want, out)
		}
	}
	send(m, "tab", "tab")
	if p.focus != secOutput {
		t.Fatalf("focus %d", p.focus)
	}
	send(m, "down")
	if p.settings().Output != store.OutputPlaylist {
		t.Fatal("down should select playlist output")
	}
	send(m, "tab", " ")
	if p.focus != secPlaylistFile || p.settings().PlaylistFile {
		t.Fatalf("space should toggle the playlist file off: focus %d %+v", p.focus, p.settings())
	}
	send(m, "tab")
	if p.focus != secQuality {
		t.Fatalf("should wrap, focus %d", p.focus)
	}
	send(m, "shift+tab")
	if p.focus != secPlaylistFile {
		t.Fatalf("shift+tab should wrap back, focus %d", p.focus)
	}
}

func TestPrerunOutputNeedsOrganize(t *testing.T) {
	m, p := prerunFixture(t, false)
	send(m, "tab", "tab")
	if p.focus != secQuality {
		t.Fatalf("output sections should not be focusable, focus %d", p.focus)
	}
	if rs := p.settings(); rs.Output != store.OutputLibrary || rs.PlaylistFile {
		t.Fatalf("settings %+v", rs)
	}
	if out, _ := p.view(160, 60); !strings.Contains(out, "turn on organizing") {
		t.Fatalf("view:\n%s", out)
	}
}

func TestPrerunResumeKeepsOutput(t *testing.T) {
	cfg := config.Default()
	cfg.Organize = config.OrganizeConfig{Enabled: true, DownloadsDir: "/dl", MusicDir: "/music"}
	a := app.New(cfg, "", nil)
	rs := store.RunSummary{Run: store.Run{Name: "Old", QualityPreset: "mp3-best", SpeedPreset: "slow", Output: store.OutputPlaylist, PlaylistFile: true}}
	p := newPrerunModel(a, nil, &rs)
	if s := p.settings(); s.Output != store.OutputPlaylist || !s.PlaylistFile || s.Quality != "mp3-best" || s.Speed != "slow" {
		t.Fatalf("resume settings %+v", s)
	}
}
