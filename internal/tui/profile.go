package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmerk2021/slskify/internal/app"
	"github.com/cmerk2021/slskify/internal/config"
	"github.com/cmerk2021/slskify/internal/match"
)

// ---- quality profile editor ----

// quickRules are inserted with the number keys in the rules list.
var quickRules = []string{"lossless", "flac", "mp3 320", "mp3 v0", "mp3 256", "mp3", "aac 256", "lossy", "any"}

const (
	pfName = iota
	pfDesc
	pfFloor
	pfRules
	pfCount
)

type profileModel struct {
	orig   string // name being edited ("" for a new profile)
	base   match.QualityPolicy
	inputs [pfRules]textinput.Model
	rules  []string
	cursor int
	focus  int
	// editing is the rule index being edited, len(rules) for a new rule,
	// or -1 when no rule is being edited.
	editing int
	rule    textinput.Model
	err     string
}

func newProfileModel(a *app.App, orig string, copyFrom string) *profileModel {
	pm := &profileModel{orig: orig, editing: -1}
	for i := range pm.inputs {
		ti := textinput.New()
		ti.Prompt = ""
		ti.CharLimit = 120
		pm.inputs[i] = ti
	}
	pm.inputs[pfName].CharLimit = 40
	pm.inputs[pfName].Placeholder = "e.g. mp3-first"
	pm.inputs[pfDesc].Placeholder = "(optional; defaults to the ranking)"
	pm.inputs[pfFloor].Placeholder = "0 (none)"
	pm.inputs[pfFloor].CharLimit = 5
	pm.rule = textinput.New()
	pm.rule.Prompt = "› "
	pm.rule.CharLimit = 60
	pm.rule.Placeholder = "e.g. mp3 320, flac, mp3 v0, lossy 256, flac/alac"

	src := orig
	if src == "" {
		src = copyFrom
	}
	if p, ok := a.Cfg.QualityPresets[src]; ok {
		pm.rules = p.RankRules()
		if p.MinBitrate > 0 {
			pm.inputs[pfFloor].SetValue(strconv.Itoa(p.MinBitrate))
		}
		if len(p.Rank) > 0 && p.Description != p.RankSummary() {
			pm.inputs[pfDesc].SetValue(p.Description)
		}
		if orig != "" {
			pm.base = p
		}
	}
	if orig != "" {
		pm.inputs[pfName].SetValue(orig)
		pm.focus = pfRules
	} else if copyFrom != "" {
		pm.inputs[pfName].SetValue(copyFrom + "-copy")
	}
	return pm
}

func (pm *profileModel) init() tea.Cmd { return pm.setFocus(pm.focus) }

func (pm *profileModel) setFocus(f int) tea.Cmd {
	for i := range pm.inputs {
		pm.inputs[i].Blur()
	}
	pm.focus = (f + pfCount) % pfCount
	if pm.focus < pfRules {
		return pm.inputs[pm.focus].Focus()
	}
	return nil
}

func (pm *profileModel) name() string { return strings.TrimSpace(pm.inputs[pfName].Value()) }

func (pm *profileModel) profile() (config.QualityProfile, error) {
	qp := config.QualityProfile{Description: pm.inputs[pfDesc].Value(), Rank: append([]string{}, pm.rules...)}
	if s := strings.TrimSpace(pm.inputs[pfFloor].Value()); s != "" {
		n, err := strconv.Atoi(strings.TrimSuffix(strings.ToLower(s), "k"))
		if err != nil || n < 0 {
			return qp, fmt.Errorf("min bitrate must be a number of kbps (or empty)")
		}
		qp.MinBitrate = n
	}
	return qp, nil
}

// policy compiles the current state for the preview.
func (pm *profileModel) policy() (match.QualityPolicy, error) {
	qp, err := pm.profile()
	if err != nil {
		return match.QualityPolicy{}, err
	}
	return qp.Policy(pm.base)
}

func (pm *profileModel) startEdit(i int) tea.Cmd {
	pm.editing = i
	pm.err = ""
	if i < len(pm.rules) {
		pm.rule.SetValue(pm.rules[i])
	} else {
		pm.rule.SetValue("")
	}
	pm.rule.CursorEnd()
	return pm.rule.Focus()
}

// insert adds a rule after the cursor (or at the end of an empty list).
func (pm *profileModel) insert(r string) {
	at := pm.cursor + 1
	if len(pm.rules) == 0 {
		at = 0
	}
	pm.rules = append(pm.rules[:at], append([]string{r}, pm.rules[at:]...)...)
	pm.cursor = at
}

func (m *Model) updateProfile(msg tea.Msg) (tea.Model, tea.Cmd) {
	pm := m.profile
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if pm.editing >= 0 {
		switch k.String() {
		case "esc":
			pm.editing, pm.err = -1, ""
			pm.rule.Blur()
			return m, nil
		case "enter":
			t, err := match.ParseRankRule(pm.rule.Value())
			if err != nil {
				pm.err = err.Error()
				return m, nil
			}
			if pm.editing < len(pm.rules) {
				pm.rules[pm.editing] = t.Name
				pm.cursor = pm.editing
			} else {
				pm.insert(t.Name)
			}
			pm.editing, pm.err = -1, ""
			pm.rule.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		pm.rule, cmd = pm.rule.Update(msg)
		return m, cmd
	}
	switch k.String() {
	case "esc":
		return m.leaveProfile("", "")
	case "ctrl+s":
		return m.saveProfile()
	case "tab":
		return m, pm.setFocus(pm.focus + 1)
	case "shift+tab":
		return m, pm.setFocus(pm.focus - 1)
	}
	if pm.focus < pfRules {
		switch k.String() {
		case "down", "enter":
			return m, pm.setFocus(pm.focus + 1)
		case "up":
			return m, pm.setFocus(pm.focus - 1)
		}
		var cmd tea.Cmd
		pm.inputs[pm.focus], cmd = pm.inputs[pm.focus].Update(msg)
		return m, cmd
	}
	n := len(pm.rules)
	switch s := k.String(); s {
	case "up", "k":
		if pm.cursor > 0 {
			pm.cursor--
		} else {
			return m, pm.setFocus(pfFloor)
		}
	case "down", "j":
		if pm.cursor < n-1 {
			pm.cursor++
		}
	case "shift+up", "ctrl+up", "K", "[":
		if pm.cursor > 0 && pm.cursor < n {
			pm.rules[pm.cursor], pm.rules[pm.cursor-1] = pm.rules[pm.cursor-1], pm.rules[pm.cursor]
			pm.cursor--
		}
	case "shift+down", "ctrl+down", "J", "]":
		if pm.cursor < n-1 {
			pm.rules[pm.cursor], pm.rules[pm.cursor+1] = pm.rules[pm.cursor+1], pm.rules[pm.cursor]
			pm.cursor++
		}
	case "a", "+", "insert":
		return m, pm.startEdit(n)
	case "enter", "e":
		if n == 0 {
			return m, pm.startEdit(0)
		}
		return m, pm.startEdit(pm.cursor)
	case "d", "x", "delete", "backspace":
		if n > 0 {
			pm.rules = append(pm.rules[:pm.cursor], pm.rules[pm.cursor+1:]...)
			if pm.cursor >= len(pm.rules) && pm.cursor > 0 {
				pm.cursor--
			}
		}
	default:
		if len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
			if i := int(s[0] - '1'); i < len(quickRules) {
				pm.insert(quickRules[i])
			}
		}
	}
	return m, nil
}

func (m *Model) saveProfile() (tea.Model, tea.Cmd) {
	pm := m.profile
	name := pm.name()
	fail := func(err error) (tea.Model, tea.Cmd) {
		pm.err = err.Error()
		return m, nil
	}
	if err := config.ValidatePresetName(name); err != nil {
		return fail(err)
	}
	if _, exists := m.app.Cfg.QualityPresets[name]; exists && name != pm.orig {
		return fail(fmt.Errorf("a quality profile named %q already exists", name))
	}
	qp, err := pm.profile()
	if err != nil {
		return fail(err)
	}
	if err := m.app.SaveQualityPreset(name, qp); err != nil {
		return fail(err)
	}
	msg := fmt.Sprintf("Saved quality profile %q to %s", name, m.app.CfgPath)
	if pm.orig != "" && name != pm.orig {
		msg += fmt.Sprintf(" (%q is unchanged)", pm.orig)
	}
	return m.leaveProfile(name, msg)
}

func (m *Model) leaveProfile(selected, info string) (tea.Model, tea.Cmd) {
	m.profile = nil
	m.screen = scrPrerun
	m.prerun.reload(m.app, selected)
	if info != "" {
		m.info(info)
	}
	return m, nil
}

func (pm *profileModel) view(w, h int) (string, string) {
	title := "New quality profile"
	if pm.orig != "" {
		title = "Edit quality profile: " + pm.orig
		if config.IsBuiltinQualityPreset(pm.orig) {
			title += stDim.Render("  (built-in; saving overrides it, delete on the pre-run screen restores it)")
		}
	}
	lines := []string{stTitle.Render(title), ""}
	labels := [pfRules]string{"Name", "Description", "Min bitrate (kbps)"}
	iw := w - 26
	if iw < 10 {
		iw = 10
	}
	for i := 0; i < pfRules; i++ {
		pm.inputs[i].Width = iw
		label := "  " + pad(labels[i], 21)
		if pm.focus == i {
			label = stBold.Render("› " + pad(labels[i], 21))
		}
		lines = append(lines, label+" "+pm.inputs[i].View())
	}
	lines = append(lines, strings.Repeat(" ", 24)+stDim.Render("lossy files below the floor are rejected; lossless is never affected"), "")

	head := "  " + stBold.Render("Rank (best first)")
	if pm.focus == pfRules {
		head = stTitle.Render("▸ Rank (best first)")
	}
	lines = append(lines, head)
	if len(pm.rules) == 0 && pm.editing < 0 {
		lines = append(lines, stDim.Render("    (empty: press a to add a rule, or 1-9 for a common one)"))
	}
	for i, r := range pm.rules {
		row := fmt.Sprintf("    %d. %s", i+1, r)
		switch {
		case pm.editing == i:
			row = fmt.Sprintf("    %d. ", i+1) + pm.rule.View()
		case pm.focus == pfRules && i == pm.cursor:
			row = stSel.Render(fmt.Sprintf("  › %d. %s", i+1, r))
		}
		lines = append(lines, row)
	}
	if pm.editing >= len(pm.rules) {
		lines = append(lines, fmt.Sprintf("    %d. ", len(pm.rules)+1)+pm.rule.View())
	}
	var quick []string
	for i, r := range quickRules {
		quick = append(quick, fmt.Sprintf("%d %s", i+1, r))
	}
	lines = append(lines, stDim.Render("    quick add: "+strings.Join(quick, " · ")))
	if pm.err != "" {
		lines = append(lines, stErr.Render("✗ "+pm.err))
	}
	lines = append(lines, "", stBold.Render("Preview")+stDim.Render("  (how sample files would rank; matching confidence still comes first)"))
	if pol, err := pm.policy(); err != nil {
		lines = append(lines, stDim.Render("  "+err.Error()))
	} else {
		lines = append(lines, previewLines(pol)...)
	}

	var help string
	switch {
	case pm.editing >= 0:
		help = helpLine("enter", "confirm rule", "esc", "cancel rule")
	case pm.focus == pfRules:
		help = helpLine("a", "add", "enter", "edit", "d", "delete", "shift+↑↓/[ ]", "move", "1-9", "quick add", "tab", "fields", "ctrl+s", "save", "esc", "cancel")
	default:
		help = helpLine("tab/↑↓", "field", "ctrl+s", "save", "esc", "cancel")
	}
	return fitLines(lines, w, h), help
}

// previewLines groups the sample files by tier.
func previewLines(p match.QualityPolicy) []string {
	var lines, rejected []string
	cur, curName := -2, ""
	var labels []string
	flush := func() {
		if cur >= 0 {
			name := fmt.Sprintf("%d. %s", cur+1, curName)
			if cur >= len(p.Tiers) {
				name = "   other (allowed, no rule)"
			}
			lines = append(lines, "  "+pad(name, 22)+" "+strings.Join(labels, ", "))
		}
		labels = nil
	}
	for _, r := range p.Preview() {
		if r.Tier < 0 {
			rejected = append(rejected, r.Label)
			continue
		}
		if r.Tier != cur {
			flush()
			cur, curName = r.Tier, r.TierName
		}
		labels = append(labels, r.Label)
	}
	flush()
	if len(rejected) > 0 {
		lines = append(lines, stDim.Render("  "+pad("rejected", 22)+" "+strings.Join(rejected, ", ")))
	}
	return lines
}
