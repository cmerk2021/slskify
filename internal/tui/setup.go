package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmerk2021/slskify/internal/app"
	"github.com/cmerk2021/slskify/internal/config"
	"github.com/cmerk2021/slskify/internal/organize"
)

type connTestedMsg struct {
	res app.ConnResult
	cr  config.Credentials
	oc  config.OrganizeConfig
}

type slskdDirMsg struct{ dir string }

// Setup fields, in focus order. The organize inputs are only shown (and
// focusable) while organizing is enabled.
const (
	fSlskdURL = iota
	fSlskdKey
	fSpotifyID
	fSpotifySecret
	fOrganize // toggle, not a text input
	fDownloads
	fMusic
	fTemplate
	fPlaylistDir
	numFields
)

type setupModel struct {
	inputs   []textinput.Model // indexed by field; inputs[fOrganize] unused
	labels   []string
	hints    []string
	organize bool
	// playlistFile is kept as configured (edited in the config file).
	playlistFile bool
	dirHint      string
	focus        int
	testing      bool
	tested       bool
	result       app.ConnResult
	orgErr       error
	saveErr      error
	canCancel    bool
	missing      []string
}

func newSetupModel(a *app.App, canCancel bool) *setupModel {
	cr := a.Credentials()
	oc := a.Cfg.Organize
	tmpl := oc.PathTemplate
	if tmpl == "" {
		tmpl = organize.DefaultTemplate
	}
	s := &setupModel{
		canCancel:    canCancel,
		missing:      a.Cfg.MissingCredentials(),
		organize:     oc.Enabled,
		playlistFile: oc.PlaylistFile,
		labels: []string{"slskd URL", "slskd API key", "Spotify client ID", "Spotify client secret",
			"Organize downloads", "slskd downloads dir", "Music library dir", "Path template", "Playlist folders dir"},
		hints: []string{
			"e.g. http://localhost:5030",
			"slskd.yml → web.authentication.api_keys",
			"developer.spotify.com/dashboard → your app",
			"shown under the client ID (\"View client secret\")",
			"tag from Spotify & move into your library (slskd must run on this machine)",
			"where slskd saves finished downloads, as a path on this machine",
			"e.g. ~/Music; files go to <dir>/<template>.<ext>",
			"{" + strings.Join(organize.PlaceholderNames(), "} {") + "}",
			"playlist output puts files flat in <dir>/<playlist name>/; empty = music library dir",
		},
	}
	vals := []string{cr.SlskdURL, cr.SlskdAPIKey, cr.SpotifyClientID, cr.SpotifyClientSecret,
		"", oc.DownloadsDir, oc.MusicDir, tmpl, oc.PlaylistDir}
	for i, v := range vals {
		ti := textinput.New()
		ti.Prompt = ""
		ti.CharLimit = 512
		ti.SetValue(v)
		if i == fSlskdKey || i == fSpotifySecret {
			ti.EchoMode = textinput.EchoPassword
			ti.EchoCharacter = '•'
		}
		s.inputs = append(s.inputs, ti)
	}
	// Start at the first empty credential field.
	for i, v := range vals[:fOrganize] {
		if strings.TrimSpace(v) == "" {
			s.focus = i
			break
		}
	}
	return s
}

func (s *setupModel) init() tea.Cmd {
	for i := range s.inputs {
		s.inputs[i].Blur()
	}
	return s.focusCurrent()
}

func (s *setupModel) focusCurrent() tea.Cmd {
	if s.focus == fOrganize {
		return nil
	}
	return s.inputs[s.focus].Focus()
}

// fields returns the focusable fields in order.
func (s *setupModel) fields() []int {
	n := fOrganize + 1
	if s.organize {
		n = numFields
	}
	f := make([]int, n)
	for i := range f {
		f[i] = i
	}
	return f
}

func (s *setupModel) last() bool {
	f := s.fields()
	return s.focus == f[len(f)-1]
}

func (s *setupModel) creds() config.Credentials {
	v := func(i int) string { return strings.TrimSpace(s.inputs[i].Value()) }
	return config.Credentials{SlskdURL: strings.TrimRight(v(fSlskdURL), "/"), SlskdAPIKey: v(fSlskdKey),
		SpotifyClientID: v(fSpotifyID), SpotifyClientSecret: v(fSpotifySecret)}
}

func (s *setupModel) organizeConfig() config.OrganizeConfig {
	v := func(i int) string { return strings.TrimSpace(s.inputs[i].Value()) }
	return config.OrganizeConfig{Enabled: s.organize, DownloadsDir: v(fDownloads), MusicDir: v(fMusic), PathTemplate: v(fTemplate),
		PlaylistDir: v(fPlaylistDir), PlaylistFile: s.playlistFile}
}

func (s *setupModel) move(d int) tea.Cmd {
	if s.focus != fOrganize {
		s.inputs[s.focus].Blur()
	}
	f := s.fields()
	s.focus = (s.focus + d + len(f)) % len(f)
	return s.focusCurrent()
}

// toggleOrganize flips the organize switch. When it is turned on with no
// downloads directory, slskd is asked for its own (best effort).
func (s *setupModel) toggleOrganize(m *Model) tea.Cmd {
	s.organize = !s.organize
	s.orgErr = nil
	if !s.organize || strings.TrimSpace(s.inputs[fDownloads].Value()) != "" {
		return nil
	}
	cr := s.creds()
	if cr.SlskdURL == "" || cr.SlskdAPIKey == "" {
		return nil
	}
	ctx, a := m.ctx, m.app
	return func() tea.Msg {
		dir, err := a.SlskdDownloadsDir(ctx, cr)
		if err != nil {
			return slskdDirMsg{}
		}
		return slskdDirMsg{dir: dir}
	}
}

func (m *Model) updateSetup(msg tea.Msg) (tea.Model, tea.Cmd) {
	s := m.setup
	switch msg := msg.(type) {
	case slskdDirMsg:
		if msg.dir != "" && strings.TrimSpace(s.inputs[fDownloads].Value()) == "" {
			s.inputs[fDownloads].SetValue(msg.dir)
			s.dirHint = "reported by slskd; if slskd runs in Docker, use the host path instead"
		}
		return m, nil
	case connTestedMsg:
		s.testing, s.tested, s.result = false, true, msg.res
		if !msg.res.OK() {
			return m, nil
		}
		if err := m.app.ApplyCredentials(msg.cr); err != nil {
			s.saveErr = err
			return m, nil
		}
		if err := m.app.ApplyOrganize(msg.oc); err != nil {
			s.saveErr = err
			return m, nil
		}
		return m.leaveSetup(true)
	case tea.KeyMsg:
		if s.testing {
			return m, nil
		}
		switch msg.String() {
		case "esc":
			if s.canCancel {
				return m.leaveSetup(false)
			}
			return m.quit()
		case "tab", "down":
			return m, s.move(1)
		case "shift+tab", "up":
			return m, s.move(-1)
		case " ", "space":
			if s.focus == fOrganize {
				return m, s.toggleOrganize(m)
			}
		case "enter", "ctrl+t":
			if msg.String() == "enter" && !s.last() {
				return m, s.move(1)
			}
			s.saveErr, s.orgErr = nil, nil
			oc := s.organizeConfig()
			if err := m.app.CheckOrganize(oc); err != nil {
				s.orgErr = err
				return m, nil
			}
			s.testing = true
			cr := s.creds()
			ctx, a := m.ctx, m.app
			return m, func() tea.Msg {
				return connTestedMsg{res: a.TestConnections(ctx, cr), cr: cr, oc: oc}
			}
		}
	}
	if s.focus == fOrganize {
		return m, nil
	}
	var cmd tea.Cmd
	s.inputs[s.focus], cmd = s.inputs[s.focus].Update(msg)
	return m, cmd
}

func (s *setupModel) view(w, h int) (string, string) {
	lines := []string{stTitle.Render("slskify setup"), ""}
	if len(s.missing) > 0 {
		lines = append(lines, stWarn.Render("Missing: "+strings.Join(s.missing, ", ")), "")
	}
	lines = append(lines, stDim.Render("Both connections are tested live; the config is saved (mode 0600) only if both pass."), "")
	iw := w - 28
	if iw < 10 {
		iw = 10
	}
	label := func(i int) string {
		if i == s.focus {
			return stBold.Render("› " + pad(s.labels[i], 22))
		}
		return "  " + pad(s.labels[i], 22)
	}
	hint := func(text string) string { return strings.Repeat(" ", 25) + stDim.Render(text) }
	for _, i := range s.fields() {
		if i == fOrganize {
			box := "[ ] off (files stay where slskd puts them)"
			if s.organize {
				box = "[x] on"
			}
			lines = append(lines, "", label(i)+" "+box, hint(s.hints[i]))
			continue
		}
		s.inputs[i].Width = iw
		lines = append(lines, label(i)+" "+s.inputs[i].View())
		// Organize hints only for the focused field, to fit small terminals.
		switch {
		case i < fOrganize:
			lines = append(lines, hint(s.hints[i]))
		case i == s.focus && i == fDownloads && s.dirHint != "":
			lines = append(lines, hint(s.dirHint))
		case i == s.focus:
			lines = append(lines, hint(s.hints[i]))
		}
	}
	lines = append(lines, "")
	switch {
	case s.orgErr != nil:
		lines = append(lines, stErr.Render("✗ organize: "+s.orgErr.Error()))
	case s.testing:
		lines = append(lines, stInfo.Render("Testing connections…"))
	case s.tested:
		lines = append(lines, connLine("slskd", s.result.Slskd), connLine("Spotify", s.result.Spotify))
	}
	if s.saveErr != nil {
		lines = append(lines, stErr.Render("✗ "+s.saveErr.Error()))
	}
	esc := "quit"
	if s.canCancel {
		esc = "cancel"
	}
	keys := []string{"tab/↑↓", "field", "enter", "next / test & save", "ctrl+t", "test & save"}
	if s.focus == fOrganize {
		keys = append(keys, "space", "toggle")
	}
	keys = append(keys, "esc", esc)
	return fitLines(lines, w, h), helpLine(keys...)
}

func connLine(name string, err error) string {
	if err == nil {
		return stOK.Render("✓ " + name + ": connected")
	}
	return stErr.Render("✗ " + name + ": " + err.Error())
}
