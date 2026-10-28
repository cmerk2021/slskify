package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmerk2021/slskify/internal/app"
	"github.com/cmerk2021/slskify/internal/config"
)

type connTestedMsg struct {
	res app.ConnResult
	cr  config.Credentials
}

type setupModel struct {
	inputs    []textinput.Model
	labels    []string
	hints     []string
	focus     int
	testing   bool
	tested    bool
	result    app.ConnResult
	saveErr   error
	canCancel bool
	missing   []string
}

func newSetupModel(a *app.App, canCancel bool) *setupModel {
	cr := a.Credentials()
	s := &setupModel{
		canCancel: canCancel,
		missing:   a.Cfg.MissingCredentials(),
		labels:    []string{"slskd URL", "slskd API key", "Spotify client ID", "Spotify client secret"},
		hints: []string{
			"e.g. http://localhost:5030",
			"slskd.yml → web.authentication.api_keys",
			"developer.spotify.com/dashboard → your app",
			"shown under the client ID (\"View client secret\")",
		},
	}
	vals := []string{cr.SlskdURL, cr.SlskdAPIKey, cr.SpotifyClientID, cr.SpotifyClientSecret}
	for i, v := range vals {
		ti := textinput.New()
		ti.Prompt = ""
		ti.CharLimit = 512
		ti.SetValue(v)
		if i == 1 || i == 3 {
			ti.EchoMode = textinput.EchoPassword
			ti.EchoCharacter = '•'
		}
		s.inputs = append(s.inputs, ti)
	}
	// Start at the first empty field.
	for i, v := range vals {
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
	return s.inputs[s.focus].Focus()
}

func (s *setupModel) creds() config.Credentials {
	v := func(i int) string { return strings.TrimSpace(s.inputs[i].Value()) }
	return config.Credentials{SlskdURL: strings.TrimRight(v(0), "/"), SlskdAPIKey: v(1), SpotifyClientID: v(2), SpotifyClientSecret: v(3)}
}

func (s *setupModel) move(d int) tea.Cmd {
	s.inputs[s.focus].Blur()
	s.focus = (s.focus + d + len(s.inputs)) % len(s.inputs)
	return s.inputs[s.focus].Focus()
}

func (m *Model) updateSetup(msg tea.Msg) (tea.Model, tea.Cmd) {
	s := m.setup
	switch msg := msg.(type) {
	case connTestedMsg:
		s.testing, s.tested, s.result = false, true, msg.res
		if !msg.res.OK() {
			return m, nil
		}
		if err := m.app.ApplyCredentials(msg.cr); err != nil {
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
		case "enter", "ctrl+t":
			if msg.String() == "enter" && s.focus < len(s.inputs)-1 {
				return m, s.move(1)
			}
			s.testing, s.saveErr = true, nil
			cr := s.creds()
			ctx, a := m.ctx, m.app
			return m, func() tea.Msg {
				return connTestedMsg{res: a.TestConnections(ctx, cr), cr: cr}
			}
		}
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
	for i := range s.inputs {
		s.inputs[i].Width = iw
		var label string
		if i == s.focus {
			label = stBold.Render("› " + pad(s.labels[i], 22))
		} else {
			label = "  " + pad(s.labels[i], 22)
		}
		lines = append(lines, label+" "+s.inputs[i].View())
		lines = append(lines, strings.Repeat(" ", 25)+stDim.Render(s.hints[i]))
	}
	lines = append(lines, "")
	switch {
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
	return fitLines(lines, w, h), helpLine("tab/↑↓", "field", "enter", "next / test & save", "ctrl+t", "test & save", "esc", esc)
}

func connLine(name string, err error) string {
	if err == nil {
		return stOK.Render("✓ " + name + ": connected")
	}
	return stErr.Render("✗ " + name + ": " + err.Error())
}
