package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmerk2021/slskify/internal/app"
	"github.com/cmerk2021/slskify/internal/config"
	"github.com/cmerk2021/slskify/internal/spotify"
	"github.com/cmerk2021/slskify/internal/store"
)

// ---- run picker ----

type pickerModel struct {
	input          textinput.Model
	runs           []store.RunSummary
	cursor, offset int
	loaded         bool
}

func newPickerModel() *pickerModel {
	ti := textinput.New()
	ti.Placeholder = "paste a Spotify playlist / album / track URL"
	ti.Prompt = "URL › "
	ti.CharLimit = 1024
	return &pickerModel{input: ti}
}

func (p *pickerModel) setRuns(rs []store.RunSummary) {
	p.runs, p.loaded = rs, true
	if p.cursor >= len(rs) {
		p.cursor = len(rs) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

func (m *Model) updatePicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	p := m.picker
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc":
			if p.input.Value() != "" {
				p.input.SetValue("")
				return m, nil
			}
			return m.quit()
		case "up":
			if p.cursor > 0 {
				p.cursor--
			}
			return m, nil
		case "down":
			if p.cursor < len(p.runs)-1 {
				p.cursor++
			}
			return m, nil
		case "ctrl+s":
			return m, m.openSetup()
		case "enter":
			if url := strings.TrimSpace(p.input.Value()); url != "" {
				if _, _, err := spotify.ParseURL(url); err != nil {
					m.fail(err)
					return m, nil
				}
				p.input.SetValue("")
				m.screen = scrLoading
				return m, m.resolve(url)
			}
			if len(p.runs) > 0 {
				rs := p.runs[p.cursor]
				m.prerun = newPrerunModel(m.app, nil, &rs)
				m.screen = scrPrerun
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return m, cmd
}

func (p *pickerModel) view(w, h int) (string, string) {
	p.input.Width = w - 8
	lines := []string{stTitle.Render("slskify") + stDim.Render("  Spotify → Soulseek via slskd"), "", p.input.View(), ""}
	switch {
	case !p.loaded:
		lines = append(lines, stDim.Render("loading past runs…"))
	case len(p.runs) == 0:
		lines = append(lines, stDim.Render("No past runs. Paste a URL and press enter."))
	default:
		lines = append(lines, stBold.Render("Past runs")+stDim.Render("  (enter with an empty URL resumes the highlighted run)"))
		listH := h - len(lines)
		var start, end int
		p.offset, start, end = window(p.cursor, p.offset, len(p.runs), listH)
		for i := start; i < end; i++ {
			r := p.runs[i]
			review := r.Counts[store.StateNeedsReview] + r.Counts[store.StateNotFound]
			line := fmt.Sprintf("%s  %-8s %s  %d/%d done", r.CreatedAt.Local().Format("2006-01-02 15:04"), r.Kind, r.Name, r.Counts[store.StateDone], r.Total)
			if review > 0 {
				line += fmt.Sprintf(", %d review", review)
			}
			if f := r.Counts[store.StateFailed]; f > 0 {
				line += fmt.Sprintf(", %d failed", f)
			}
			line += stDim.Render(fmt.Sprintf("  [%s · %s]", r.QualityPreset, r.SpeedPreset))
			if i == p.cursor {
				line = stSel.Render("› " + trunc(line, w-3))
			} else {
				line = "  " + line
			}
			lines = append(lines, line)
		}
	}
	return fitLines(lines, w, h), helpLine("enter", "load URL / resume run", "↑↓", "select run", "ctrl+s", "setup", "esc", "clear/quit")
}

// ---- pre-run ----

type prerunModel struct {
	coll           *spotify.Collection
	resume         *store.RunSummary
	qNames, sNames []string
	qp             map[string]string
	sp             map[string]config.SpeedPreset
	qi, si, focus  int
	// confirmDel is the preset awaiting a second "d" to be deleted.
	confirmDel string
}

func newPrerunModel(a *app.App, coll *spotify.Collection, resume *store.RunSummary) *prerunModel {
	p := &prerunModel{coll: coll, resume: resume, sNames: a.Cfg.SpeedPresetNames(), sp: a.Cfg.SpeedPresets}
	q, s := a.Cfg.QualityPreset, a.Cfg.SpeedPreset
	if resume != nil {
		q, s = resume.QualityPreset, resume.SpeedPreset
	}
	p.reload(a, q)
	p.si = indexOf(p.sNames, s)
	return p
}

// reload refreshes the quality presets (after the profile editor saved or
// deleted one) and selects sel, or keeps the current selection if sel is
// empty or gone.
func (p *prerunModel) reload(a *app.App, sel string) {
	cur := ""
	if len(p.qNames) > 0 {
		cur = p.qNames[p.qi]
	}
	p.qNames, p.qp = a.Cfg.QualityPresetNames(), map[string]string{}
	for n, q := range a.Cfg.QualityPresets {
		p.qp[n] = q.Description
	}
	if _, ok := a.Cfg.QualityPresets[sel]; !ok {
		sel = cur
	}
	if _, ok := a.Cfg.QualityPresets[sel]; !ok {
		sel = a.Cfg.QualityPreset
	}
	p.qi = indexOf(p.qNames, sel)
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return 0
}

func (p *prerunModel) quality() string { return p.qNames[p.qi] }
func (p *prerunModel) speed() string   { return p.sNames[p.si] }

func (m *Model) updatePrerun(msg tea.Msg) (tea.Model, tea.Cmd) {
	p := m.prerun
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	cur, n := &p.qi, len(p.qNames)
	if p.focus == 1 {
		cur, n = &p.si, len(p.sNames)
	}
	confirm := p.confirmDel
	p.confirmDel = ""
	switch k.String() {
	case "n", "e", "c":
		orig, from := "", ""
		switch k.String() {
		case "e":
			orig = p.quality()
		case "c":
			from = p.quality()
		}
		m.profile = newProfileModel(m.app, orig, from)
		m.screen = scrProfile
		return m, m.profile.init()
	case "d", "delete":
		if p.focus != 0 {
			return m, nil
		}
		name := p.quality()
		builtin := config.IsBuiltinQualityPreset(name)
		if confirm != name {
			p.confirmDel = name
			if builtin {
				m.info(fmt.Sprintf("Press d again to reset %q to the built-in definition", name))
			} else {
				m.info(fmt.Sprintf("Press d again to delete quality profile %q", name))
			}
			return m, nil
		}
		if err := m.app.DeleteQualityPreset(name); err != nil {
			m.fail(err)
			return m, nil
		}
		p.reload(m.app, "")
		if builtin {
			m.info(fmt.Sprintf("Reset %q to the built-in definition", name))
		} else {
			m.info(fmt.Sprintf("Deleted quality profile %q", name))
		}
	case "tab", "shift+tab", "left", "right", "h", "l":
		p.focus = 1 - p.focus
	case "up", "k":
		*cur = (*cur - 1 + n) % n
	case "down", "j":
		*cur = (*cur + 1) % n
	case "esc":
		m.screen = scrPicker
		return m, tea.Batch(m.picker.input.Focus(), m.loadRuns())
	case "q":
		return m.quit()
	case "enter":
		m.screen = scrLoading
		return m, m.startRun(p)
	}
	return m, nil
}

func (p *prerunModel) view(w, h int) (string, string) {
	var lines []string
	if p.resume != nil {
		r := p.resume
		lines = append(lines, stTitle.Render("Resume run: ")+stBold.Render(r.Name),
			stDim.Render(fmt.Sprintf("%s · %d tracks · %d done · created %s", r.Kind, r.Total, r.Counts[store.StateDone], r.CreatedAt.Local().Format("2006-01-02 15:04"))))
	} else {
		c := p.coll
		meta := fmt.Sprintf("%s · %d tracks", c.Kind, len(c.Tracks))
		if c.Owner != "" {
			meta += " · by " + c.Owner
		}
		if len(c.Skipped) > 0 {
			meta += fmt.Sprintf(" · %d skipped", len(c.Skipped))
		}
		lines = append(lines, stTitle.Render("New run: ")+stBold.Render(c.Name), stDim.Render(meta))
		for i, s := range c.Skipped {
			if i == 3 {
				lines = append(lines, stDim.Render(fmt.Sprintf("  … and %d more skipped", len(c.Skipped)-3)))
				break
			}
			lines = append(lines, stWarn.Render(fmt.Sprintf("  skipped #%d %s: %s", s.Position, s.Name, s.Reason)))
		}
	}
	lines = append(lines, "")
	section := func(title string, focused bool, names []string, sel int, desc func(string) (string, string)) {
		t := stBold.Render(title)
		if focused {
			t = stTitle.Render("▸ " + title)
		} else {
			t = "  " + t
		}
		lines = append(lines, t)
		for i, n := range names {
			d, warn := desc(n)
			mark := "( )"
			if i == sel {
				mark = "(•)"
			}
			line := fmt.Sprintf("   %s %-16s %s", mark, n, stDim.Render(d))
			if i == sel && focused {
				line = stBold.Render(fmt.Sprintf("   %s %-16s ", mark, n)) + d
			}
			lines = append(lines, line)
			if i == sel && warn != "" {
				lines = append(lines, "       "+stWarn.Render("⚠ "+warn))
			}
		}
		lines = append(lines, "")
	}
	section("Quality preset", p.focus == 0, p.qNames, p.qi, func(n string) (string, string) { return p.qp[n], "" })
	section("Speed preset", p.focus == 1, p.sNames, p.si, func(n string) (string, string) {
		sp := p.sp[n]
		d := sp.Description
		if d == "" {
			d = fmt.Sprintf("%d searches, %d downloads, %s delay", sp.ConcurrentSearches, sp.ConcurrentDownloads, sp.SearchDelay.Duration)
		}
		return d, sp.Warning
	})
	return fitLines(lines, w, h), helpLine("tab", "switch list", "↑↓", "choose", "enter", "start", "n/e/c/d", "new/edit/copy/delete quality profile", "esc", "back", "q", "quit")
}
