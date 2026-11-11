package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmerk2021/slskify/internal/app"
	"github.com/cmerk2021/slskify/internal/config"
)

func TestSetupOrganizeSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfg := config.Default()
	cfg.Slskd.APIKey, cfg.Spotify.ClientID, cfg.Spotify.ClientSecret = "k", "i", "s"
	a := app.New(cfg, path, nil)
	m := newModel(context.Background(), a, Options{Setup: true})
	m.Init()
	s := m.setup

	// Disabled: the organize inputs are hidden and not focusable.
	if out, _ := s.view(100, 40); strings.Contains(out, "Music library dir") || !strings.Contains(out, "Organize downloads") {
		t.Fatalf("disabled view:\n%s", out)
	}
	send(m, "tab", "tab", "tab", "tab")
	if s.focus != fOrganize {
		t.Fatalf("focus %d", s.focus)
	}
	send(m, "tab")
	if s.focus != fSlskdURL {
		t.Fatalf("should wrap to first field, focus %d", s.focus)
	}
	send(m, "up", " ")
	if !s.organize {
		t.Fatal("space should toggle organize on")
	}
	m.Update(slskdDirMsg{dir: "/var/slskd/downloads"})
	if got := s.inputs[fDownloads].Value(); got != "/var/slskd/downloads" {
		t.Fatalf("downloads prefill %q", got)
	}
	if out, _ := s.view(100, 40); !strings.Contains(out, "Music library dir") || !strings.Contains(out, "{album_artist}/{album}/{title}") {
		t.Fatalf("enabled view:\n%s", out)
	}

	// Enter walks through the organize fields; at the end the local check
	// rejects a downloads dir that doesn't exist here (no network test).
	send(m, "enter")
	if s.focus != fDownloads {
		t.Fatalf("focus %d", s.focus)
	}
	send(m, "enter", "enter", "enter")
	if s.focus != fPlaylistDir {
		t.Fatalf("focus %d", s.focus)
	}
	send(m, "enter")
	if s.orgErr == nil || !strings.Contains(s.orgErr.Error(), "music directory") {
		t.Fatalf("orgErr %v", s.orgErr)
	}
	s.inputs[fMusic].SetValue("~/Music")
	send(m, "enter")
	if s.orgErr == nil || s.testing || !strings.Contains(s.orgErr.Error(), "does not exist") {
		t.Fatalf("orgErr %v testing %v", s.orgErr, s.testing)
	}

	// Fix the dirs and simulate a passing connection test: both sections save.
	dl := filepath.Join(dir, "dl")
	os.Mkdir(dl, 0o755)
	s.inputs[fDownloads].SetValue(dl)
	s.inputs[fMusic].SetValue(filepath.Join(dir, "Music"))
	s.inputs[fTemplate].SetValue("{artist}/{title}")
	s.inputs[fPlaylistDir].SetValue(" ~/Playlists ")
	m.Update(connTestedMsg{cr: s.creds(), oc: s.organizeConfig()})
	if s.saveErr != nil {
		t.Fatal(s.saveErr)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if o := loaded.Organize; !o.Enabled || o.DownloadsDir != dl || o.PathTemplate != "{artist}/{title}" || o.PlaylistDir != "~/Playlists" || a.Cfg.Organize != o {
		t.Fatalf("saved %+v, app %+v", o, a.Cfg.Organize)
	}

	// Renders without panicking in small terminals.
	for _, w := range []int{20, 40, 80} {
		for _, h := range []int{4, 10, 24} {
			s.view(w, h)
		}
	}
}
