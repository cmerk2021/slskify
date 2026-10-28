package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmerk2021/slskify/internal/app"
	"github.com/cmerk2021/slskify/internal/config"
	"github.com/cmerk2021/slskify/internal/spotify"
)

func key(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+s":
		return tea.KeyMsg{Type: tea.KeyCtrlS}
	case "shift+up":
		return tea.KeyMsg{Type: tea.KeyShiftUp}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func send(m *Model, keys ...string) {
	for _, k := range keys {
		m.Update(key(k))
	}
}

func typeText(m *Model, s string) {
	for _, r := range s {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func testModel(t *testing.T) (*Model, string) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg := config.Default()
	cfg.Slskd.APIKey, cfg.Spotify.ClientID, cfg.Spotify.ClientSecret = "k", "i", "s"
	a := app.New(cfg, path, nil)
	m := newModel(context.Background(), a, Options{})
	m.prerun = newPrerunModel(a, &spotify.Collection{Name: "x", Tracks: []spotify.Track{{Title: "t"}}}, nil)
	m.screen = scrPrerun
	return m, path
}

func TestProfileEditorCreateEditDelete(t *testing.T) {
	m, path := testModel(t)

	// New profile: name, then rules mp3 320 > flac > mp3 via typing,
	// quick-add and reordering.
	send(m, "n")
	if m.screen != scrProfile {
		t.Fatalf("screen %v", m.screen)
	}
	typeText(m, "mp3-first")
	send(m, "tab", "tab")
	typeText(m, "192")
	send(m, "tab") // rules
	send(m, "3")   // quick add "mp3 320"
	send(m, "6")   // quick add "mp3"
	send(m, "a")   // add typed rule after cursor
	typeText(m, "FLAC")
	send(m, "enter")
	send(m, "shift+up") // move flac above mp3
	if got := strings.Join(m.profile.rules, " | "); got != "mp3 320 | flac | mp3" {
		t.Fatalf("rules %q", got)
	}
	// An invalid rule is refused with an error and can be cancelled.
	send(m, "a")
	typeText(m, "flac 320")
	send(m, "enter")
	if m.profile.err == "" || m.profile.editing < 0 {
		t.Fatal("invalid rule accepted")
	}
	send(m, "esc")
	view, _ := m.profile.view(120, 40)
	if !strings.Contains(view, "MP3 320") || !strings.Contains(view, "rejected") {
		t.Errorf("preview missing:\n%s", view)
	}
	send(m, "ctrl+s")
	if m.screen != scrPrerun || m.prerun.quality() != "mp3-first" {
		t.Fatalf("after save: screen %v quality %q banner %q", m.screen, m.prerun.quality(), m.banner.text)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.QualityPresets["mp3-first"]
	if strings.Join(p.Rank, ",") != "mp3 320,flac,mp3" || p.MinBitrate != 192 {
		t.Fatalf("saved: %+v", p)
	}
	if pol, _ := m.app.Policy("mp3-first"); len(pol.Tiers) != 3 {
		t.Error("not applied in memory")
	}

	// Duplicate names are refused.
	send(m, "n")
	typeText(m, "mp3-first")
	send(m, "tab", "tab", "tab", "1", "ctrl+s")
	if m.screen != scrProfile || !strings.Contains(m.profile.err, "already exists") {
		t.Fatalf("duplicate: %q", m.profile.err)
	}
	send(m, "esc")

	// Edit: drop the last rule.
	send(m, "e")
	if m.profile.orig != "mp3-first" || len(m.profile.rules) != 3 {
		t.Fatalf("edit prefill: %+v", m.profile.rules)
	}
	m.profile.cursor = 2
	send(m, "d", "ctrl+s")
	if pol, _ := m.app.Policy("mp3-first"); len(pol.Tiers) != 2 {
		t.Errorf("edit not applied: %+v", pol.Tiers)
	}

	// Delete needs confirmation.
	send(m, "d")
	if _, ok := m.app.Cfg.QualityPresets["mp3-first"]; !ok {
		t.Fatal("deleted without confirmation")
	}
	send(m, "d")
	if _, ok := m.app.Cfg.QualityPresets["mp3-first"]; ok {
		t.Fatal("not deleted")
	}
	if cfg, _ := config.Load(path); cfg.QualityPresets["mp3-first"].Tiers != nil {
		t.Error("still in file")
	}
}

func TestProfileEditorRendersSmall(t *testing.T) {
	m, _ := testModel(t)
	send(m, "c")
	for _, w := range []int{20, 40, 80} {
		for _, h := range []int{4, 10, 40} {
			m.profile.view(w, h)
		}
	}
	if len(m.profile.rules) == 0 || m.profile.name() != "lossless-first-copy" {
		t.Errorf("copy prefill: %q %v", m.profile.name(), m.profile.rules)
	}
}
