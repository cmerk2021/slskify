// Package tui is the Bubble Tea frontend. All business logic lives in the
// core packages; this package only renders snapshots and forwards actions.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmerk2021/slskify/internal/app"
	"github.com/cmerk2021/slskify/internal/pipeline"
	"github.com/cmerk2021/slskify/internal/spotify"
	"github.com/cmerk2021/slskify/internal/store"
)

// Options configure the TUI entry point.
type Options struct {
	// URL, when set, skips the run picker and resolves it immediately.
	URL string
	// Setup opens the setup screen even when credentials are present.
	Setup bool
	// SetupOnly exits after setup is saved (the `slskify setup` command).
	SetupOnly bool
}

type screen int

const (
	scrSetup screen = iota
	scrPicker
	scrLoading
	scrPrerun
	scrMain
	scrProfile
	scrQuitting
)

type (
	resolvedMsg struct {
		coll *spotify.Collection
		err  error
	}
	runsMsg struct {
		runs []store.RunSummary
		err  error
	}
	runnerStartedMsg struct {
		runner *pipeline.Runner
		err    error
	}
	runnerUpdateMsg struct{}
	runnerDoneMsg   struct{}
	tickMsg         time.Time
)

type banner struct {
	text string
	err  bool
	at   time.Time
}

// Model is the root Bubble Tea model.
type Model struct {
	app  *app.App
	opts Options
	ctx  context.Context

	w, h   int
	screen screen
	// setupReturn is where the setup screen goes after saving/cancelling.
	setupReturn screen

	setup   *setupModel
	picker  *pickerModel
	prerun  *prerunModel
	profile *profileModel
	main    *mainModel
	spin    spinner.Model
	loading string
	banner  banner

	runner    *pipeline.Runner
	runCancel context.CancelFunc
	runDone   chan struct{}
	snap      pipeline.Snapshot
	lastErr   string
	quitAfter bool
}

// Run starts the TUI and blocks until it exits. The runner (if any) is
// stopped and its state persisted before returning.
func Run(ctx context.Context, a *app.App, opts Options) error {
	m := newModel(ctx, a, opts)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := p.Run()
	m.stopRunner()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil
	}
	return err
}

func newModel(ctx context.Context, a *app.App, opts Options) *Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	m := &Model{app: a, opts: opts, ctx: ctx, spin: sp, w: 80, h: 24}
	switch {
	case opts.Setup || opts.SetupOnly || len(a.Cfg.MissingCredentials()) > 0:
		m.screen = scrSetup
		m.setupReturn = scrPicker
		m.setup = newSetupModel(a, len(a.Cfg.MissingCredentials()) == 0 && !opts.SetupOnly)
	case opts.URL != "":
		m.screen = scrLoading
	default:
		m.screen = scrPicker
	}
	m.picker = newPickerModel()
	return m
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spin.Tick, tick()}
	switch m.screen {
	case scrSetup:
		cmds = append(cmds, m.setup.init())
	case scrLoading:
		cmds = append(cmds, m.resolve(m.opts.URL))
	case scrPicker:
		cmds = append(cmds, m.picker.input.Focus(), m.loadRuns())
	}
	return tea.Batch(cmds...)
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) info(s string) { m.banner = banner{text: s, at: time.Now()} }
func (m *Model) fail(err error) {
	if err != nil {
		m.banner = banner{text: err.Error(), err: true, at: time.Now()}
	}
}

// ---- commands ----

func (m *Model) resolve(url string) tea.Cmd {
	m.loading = "Resolving " + url
	return func() tea.Msg {
		c, err := m.app.Spotify.Resolve(m.ctx, url)
		return resolvedMsg{c, err}
	}
}

func (m *Model) loadRuns() tea.Cmd {
	return func() tea.Msg {
		rs, err := m.app.Store.ListRuns(m.ctx)
		return runsMsg{rs, err}
	}
}

func (m *Model) startRun(p *prerunModel) tea.Cmd {
	quality, speed := p.quality(), p.speed()
	m.loading = "Starting run (checking slskd transfers)"
	return func() tea.Msg {
		var run store.Run
		var err error
		if p.resume != nil {
			run = p.resume.Run
			if run.QualityPreset != quality || run.SpeedPreset != speed {
				if err = m.app.Store.UpdateRunPresets(m.ctx, run.ID, quality, speed); err != nil {
					return runnerStartedMsg{err: err}
				}
			}
			run.QualityPreset, run.SpeedPreset = quality, speed
		} else if run, err = m.app.CreateRun(m.ctx, p.coll, quality, speed); err != nil {
			return runnerStartedMsg{err: err}
		}
		r, err := m.app.NewRunner(m.ctx, run)
		return runnerStartedMsg{runner: r, err: err}
	}
}

func waitUpdate(r *pipeline.Runner) tea.Cmd {
	return func() tea.Msg {
		<-r.Updates()
		return runnerUpdateMsg{}
	}
}

func waitDone(ch chan struct{}) tea.Cmd {
	return func() tea.Msg {
		<-ch
		return runnerDoneMsg{}
	}
}

// stopRunner cancels the runner and waits for it to persist state.
func (m *Model) stopRunner() {
	if m.runCancel != nil {
		m.runCancel()
		<-m.runDone
	}
}

// quit stops the runner (if any) gracefully, then exits.
func (m *Model) quit() (tea.Model, tea.Cmd) {
	if m.runner == nil {
		return m, tea.Quit
	}
	if m.screen == scrQuitting {
		return m, nil
	}
	m.screen = scrQuitting
	m.loading = "Stopping: letting workers finish and saving state"
	m.runCancel()
	return m, waitDone(m.runDone)
}

// ---- update ----

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		if m.banner.text != "" && time.Since(m.banner.at) > 10*time.Second {
			m.banner = banner{}
		}
		return m, tick()
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m.quit()
		}
	case resolvedMsg:
		if msg.err != nil {
			m.fail(msg.err)
			m.screen = scrPicker
			return m, tea.Batch(m.picker.input.Focus(), m.loadRuns())
		}
		if len(msg.coll.Tracks) == 0 {
			m.fail(fmt.Errorf("%q has no playable tracks (%d skipped)", msg.coll.Name, len(msg.coll.Skipped)))
			m.screen = scrPicker
			return m, tea.Batch(m.picker.input.Focus(), m.loadRuns())
		}
		m.prerun = newPrerunModel(m.app, msg.coll, nil)
		m.screen = scrPrerun
		return m, nil
	case runsMsg:
		if msg.err != nil {
			m.fail(msg.err)
		}
		m.picker.setRuns(msg.runs)
		return m, nil
	case runnerStartedMsg:
		if msg.err != nil {
			m.fail(msg.err)
			m.screen = scrPrerun
			return m, nil
		}
		m.runner = msg.runner
		rctx, cancel := context.WithCancel(m.ctx)
		m.runCancel = cancel
		m.runDone = make(chan struct{})
		go func(r *pipeline.Runner, done chan struct{}) {
			defer close(done)
			_ = r.Run(rctx)
		}(m.runner, m.runDone)
		m.snap = m.runner.Snapshot()
		m.main = newMainModel()
		m.main.sync(m.snap)
		m.screen = scrMain
		if n := len(m.snap.Run.Skipped); n > 0 {
			m.info(fmt.Sprintf("%d source item(s) skipped (local files, podcasts or unavailable); see README", n))
		}
		return m, waitUpdate(m.runner)
	case runnerUpdateMsg:
		if m.runner == nil {
			return m, nil
		}
		m.refresh()
		return m, waitUpdate(m.runner)
	case runnerDoneMsg:
		return m, tea.Quit
	}

	switch m.screen {
	case scrSetup:
		return m.updateSetup(msg)
	case scrPicker:
		return m.updatePicker(msg)
	case scrPrerun:
		return m.updatePrerun(msg)
	case scrProfile:
		return m.updateProfile(msg)
	case scrMain:
		return m.updateMain(msg)
	case scrLoading, scrQuitting:
		if k, ok := msg.(tea.KeyMsg); ok && k.String() == "q" {
			return m.quit()
		}
	}
	return m, nil
}

// refresh pulls a new snapshot and surfaces new runner errors as banners.
func (m *Model) refresh() {
	m.snap = m.runner.Snapshot()
	if n := len(m.snap.Errors); n > 0 && m.snap.Errors[n-1] != m.lastErr {
		m.lastErr = m.snap.Errors[n-1]
		m.banner = banner{text: m.lastErr, err: true, at: time.Now()}
	}
	if m.main != nil {
		m.main.sync(m.snap)
	}
}

func (m *Model) openSetup() tea.Cmd {
	m.setupReturn = m.screen
	m.setup = newSetupModel(m.app, true)
	m.screen = scrSetup
	return m.setup.init()
}

func (m *Model) leaveSetup(saved bool) (tea.Model, tea.Cmd) {
	if saved && m.opts.SetupOnly {
		return m, tea.Quit
	}
	if saved {
		msg := "Configuration saved to " + m.app.CfgPath
		if m.runner != nil {
			msg += " (applies to the next run)"
		}
		m.info(msg)
	}
	m.screen = m.setupReturn
	switch m.screen {
	case scrPicker:
		if m.opts.URL != "" && m.runner == nil && m.prerun == nil {
			m.screen = scrLoading
			url := m.opts.URL
			m.opts.URL = ""
			return m, m.resolve(url)
		}
		return m, tea.Batch(m.picker.input.Focus(), m.loadRuns())
	}
	return m, nil
}

// ---- view ----

// View implements tea.Model.
func (m *Model) View() string {
	w, h := m.w, m.h
	if w < 20 || h < 6 {
		return "terminal too small"
	}
	var body string
	var help string
	switch m.screen {
	case scrSetup:
		body, help = m.setup.view(w, h-2)
	case scrPicker:
		body, help = m.picker.view(w, h-2)
	case scrPrerun:
		body, help = m.prerun.view(w, h-2)
	case scrProfile:
		body, help = m.profile.view(w, h-2)
	case scrMain:
		body, help = m.main.view(m.snap, w, h-2)
	case scrLoading, scrQuitting:
		body = fitLines([]string{"", "  " + m.spin.View() + " " + m.loading + "…"}, w, h-2)
		help = helpLine("ctrl+c", "quit")
	}
	return body + "\n" + m.bannerLine(w) + "\n" + trunc(help, w)
}

func (m *Model) bannerLine(w int) string {
	if m.banner.text == "" {
		return ""
	}
	text := strings.ReplaceAll(m.banner.text, "\n", " ")
	if m.banner.err {
		return trunc(stBannerE.Render("! "+text), w)
	}
	return trunc(stBannerI.Render("› "+text), w)
}
