package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/cmerk2021/slskify/internal/pipeline"
	"github.com/cmerk2021/slskify/internal/slskd"
	"github.com/cmerk2021/slskify/internal/store"
)

type tabDef struct {
	name   string
	states []store.State
}

// Tabs. "Downloading" includes tracks queued for download; "Failed" also
// lists skipped tracks; "Needs Review" includes not-found tracks.
var tabs = []tabDef{
	{"All", nil},
	{"Needs Review", []store.State{store.StateNeedsReview, store.StateNotFound}},
	{"Downloading", []store.State{store.StateRanked, store.StateDownloading}},
	{"Done", []store.State{store.StateDone}},
	{"Failed", []store.State{store.StateFailed, store.StateSkipped}},
}

const tabReview = 1

func (t tabDef) has(s store.State) bool {
	if t.states == nil {
		return true
	}
	for _, x := range t.states {
		if x == s {
			return true
		}
	}
	return false
}

func (t tabDef) count(c map[store.State]int, total int) int {
	if t.states == nil {
		return total
	}
	n := 0
	for _, s := range t.states {
		n += c[s]
	}
	return n
}

type reviewEntry struct{ id, seq int64 }

// reviewQueue is the TUI's own ordered review list. Arrivals (including
// tracks re-entering review after a re-search) append; nothing reshuffles.
type reviewQueue struct {
	entries []reviewEntry
	seen    map[reviewEntry]bool
	idx     int
}

type mainModel struct {
	tab            int
	cursor, offset int
	selID          int64
	rows           []pipeline.TrackView
	byID           map[int64]int
	snap           pipeline.Snapshot
	detail         *detailModel
	rq             reviewQueue
}

func newMainModel() *mainModel {
	return &mainModel{rq: reviewQueue{seen: map[reviewEntry]bool{}}}
}

// sync incorporates a new snapshot.
func (mm *mainModel) sync(s pipeline.Snapshot) {
	mm.snap = s
	mm.byID = make(map[int64]int, len(s.Tracks))
	for i, t := range s.Tracks {
		mm.byID[t.ID] = i
	}
	for _, id := range s.ReviewOrder() {
		e := reviewEntry{id, s.Tracks[mm.byID[id]].ReviewSeq}
		if !mm.rq.seen[e] {
			mm.rq.seen[e] = true
			mm.rq.entries = append(mm.rq.entries, e)
		}
	}
	mm.rebuild()
}

func (mm *mainModel) track(id int64) (pipeline.TrackView, bool) {
	i, ok := mm.byID[id]
	if !ok {
		return pipeline.TrackView{}, false
	}
	return mm.snap.Tracks[i], true
}

func (mm *mainModel) rebuild() {
	t := tabs[mm.tab]
	mm.rows = mm.rows[:0]
	if mm.tab == tabReview {
		for _, id := range mm.snap.ReviewOrder() {
			tv, _ := mm.track(id)
			mm.rows = append(mm.rows, tv)
		}
	} else {
		for _, tv := range mm.snap.Tracks {
			if t.has(tv.State) {
				mm.rows = append(mm.rows, tv)
			}
		}
	}
	mm.cursor = 0
	for i, r := range mm.rows {
		if r.ID == mm.selID {
			mm.cursor = i
			break
		}
	}
	mm.clamp()
}

func (mm *mainModel) clamp() {
	if mm.cursor >= len(mm.rows) {
		mm.cursor = len(mm.rows) - 1
	}
	if mm.cursor < 0 {
		mm.cursor = 0
	}
	if len(mm.rows) > 0 {
		mm.selID = mm.rows[mm.cursor].ID
	}
}

func (mm *mainModel) unresolved(e reviewEntry) bool {
	t, ok := mm.track(e.id)
	return ok && (t.State == store.StateNeedsReview || t.State == store.StateNotFound) && t.ReviewSeq == e.seq
}

func (mm *mainModel) unresolvedCount() int {
	n := 0
	for _, e := range mm.rq.entries {
		if mm.unresolved(e) {
			n++
		}
	}
	return n
}

// nextUnresolved finds the next unresolved entry after from (wrapping).
func (mm *mainModel) nextUnresolved(from int) int {
	n := len(mm.rq.entries)
	for k := 1; k <= n; k++ {
		i := (from + k) % n
		if mm.unresolved(mm.rq.entries[i]) {
			return i
		}
	}
	return -1
}

func (m *Model) updateMain(msg tea.Msg) (tea.Model, tea.Cmd) {
	mm := m.main
	if mm.detail != nil {
		return m.updateDetail(msg)
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	page := m.h - 8
	if page < 1 {
		page = 1
	}
	switch k.String() {
	case "q":
		return m.quit()
	case "tab", "right", "l":
		mm.tab = (mm.tab + 1) % len(tabs)
		mm.offset = 0
		mm.rebuild()
	case "shift+tab", "left", "h":
		mm.tab = (mm.tab - 1 + len(tabs)) % len(tabs)
		mm.offset = 0
		mm.rebuild()
	case "up", "k":
		mm.cursor--
		mm.clamp()
	case "down", "j":
		mm.cursor++
		mm.clamp()
	case "pgup":
		mm.cursor -= page
		mm.clamp()
	case "pgdown", " ":
		mm.cursor += page
		mm.clamp()
	case "home", "g":
		mm.cursor = 0
		mm.clamp()
	case "end", "G":
		mm.cursor = len(mm.rows) - 1
		mm.clamp()
	case "enter":
		if len(mm.rows) > 0 {
			mm.detail = newDetailModel(mm.rows[mm.cursor].ID, false)
		}
	case "r":
		i := mm.nextUnresolved(-1)
		if i < 0 {
			m.info("Nothing to review right now")
			return m, nil
		}
		mm.rq.idx = i
		mm.detail = newDetailModel(mm.rq.entries[i].id, true)
	case "s":
		if len(mm.rows) > 0 {
			m.fail(m.runner.Skip(mm.rows[mm.cursor].ID))
			m.refresh()
		}
	case "p":
		m.runner.TogglePause()
		m.refresh()
	case "ctrl+s":
		return m, m.openSetup()
	}
	return m, nil
}

func (mm *mainModel) header(s pipeline.Snapshot, w int) []string {
	total := len(s.Tracks)
	finished := s.Counts[store.StateDone] + s.Counts[store.StateSkipped] + s.Counts[store.StateFailed]
	title := stTitle.Render("slskify") + " ▸ " + stBold.Render(s.Run.Name)
	right := stDim.Render(fmt.Sprintf("quality: %s · speed: %s", s.Run.QualityPreset, s.Run.SpeedPreset))
	if s.Paused {
		right = stPaused.Render("PAUSED") + " " + right
	}
	l1 := title
	if gap := w - lenW(title) - lenW(right); gap >= 1 {
		l1 = title + strings.Repeat(" ", gap) + right
	} else {
		tw := lenW(title)
		if tw > w/2 {
			tw = w / 2
		}
		l1 = trunc(title, tw) + " " + trunc(right, w-tw-1)
	}
	frac := 0.0
	if total > 0 {
		frac = float64(finished) / float64(total)
	}
	stats := fmt.Sprintf(" %d/%d done", s.Counts[store.StateDone], total)
	if n := s.Counts[store.StateSearching] + s.Counts[store.StatePending]; n > 0 {
		stats += fmt.Sprintf(" · %d to search", n)
	}
	if n := s.Counts[store.StateDownloading]; n > 0 {
		stats += fmt.Sprintf(" · %d downloading", n)
	}
	if s.Idle {
		stats += " · " + stOK.Render("idle")
	}
	bw := w / 4
	if bw > 30 {
		bw = 30
	}
	l2 := progressBar(frac, bw) + stats

	var tl []string
	for i, t := range tabs {
		n := t.count(s.Counts, total)
		label := fmt.Sprintf("%s (%d)", t.name, n)
		if i == tabReview && n > 0 {
			label = t.name + " " + stReviewHi.Render(fmt.Sprintf(" %d ", n))
		}
		if i == mm.tab {
			tl = append(tl, stTabAct.Render(label))
		} else {
			tl = append(tl, stTab.Render(label))
		}
	}
	return []string{l1, l2, strings.Join(tl, stDim.Render("│"))}
}

func lenW(s string) int { return ansi.StringWidth(s) }

func (mm *mainModel) view(s pipeline.Snapshot, w, h int) (string, string) {
	if mm.detail != nil {
		return mm.detailView(s, w, h)
	}
	lines := mm.header(s, w)
	lines = append(lines, stDim.Render(strings.Repeat("─", w)))
	listH := h - len(lines)
	if len(mm.rows) == 0 {
		lines = append(lines, stDim.Render("  (empty)"))
	}
	var start, end int
	mm.offset, start, end = window(mm.cursor, mm.offset, len(mm.rows), listH)
	statusW := w * 2 / 5
	if statusW > 46 {
		statusW = 46
	}
	for i := start; i < end; i++ {
		t := mm.rows[i]
		name := fmt.Sprintf("%3d. %s — %s", t.Pos+1, t.Title, strings.Join(t.Artists, ", "))
		line := pad(name, w-statusW-1) + " " + pad(rowStatus(t, statusW), statusW)
		if i == mm.cursor {
			line = stSel.Render(pad(name, w-statusW-1)) + " " + pad(rowStatus(t, statusW), statusW)
		}
		lines = append(lines, line)
	}
	help := helpLine("←→/tab", "tabs", "↑↓", "move", "enter", "detail", "r", "review", "s", "skip", "p", "pause", "ctrl+s", "setup", "q", "quit")
	return fitLines(lines, w, h), help
}

func rowStatus(t pipeline.TrackView, w int) string {
	label := stateLabel(t.State)
	switch t.State {
	case store.StateDownloading:
		if p := t.Progress; p != nil {
			switch p.State {
			case slskd.StateQueued:
				q := "queued"
				if p.PlaceInQueue != nil {
					q += fmt.Sprintf(" #%d", *p.PlaceInQueue)
				}
				return label + " " + stDim.Render(q)
			case slskd.StateInProgress:
				bw := w - 26
				if bw > 12 {
					bw = 12
				}
				return progressBar(p.Percent/100, bw) + fmt.Sprintf(" %3.0f%% %s/s", p.Percent, humanBytes(int64(p.Speed)))
			default:
				return label + " " + stDim.Render(strings.ToLower(p.State.String()))
			}
		}
	case store.StateSearching:
		return label + " " + stDim.Render(strings.TrimPrefix(t.Activity, "searching: "))
	case store.StateNeedsReview, store.StateNotFound, store.StateFailed, store.StateSkipped:
		if t.Reason != "" {
			return label + " " + stDim.Render(t.Reason)
		}
	case store.StateDone:
		if t.ChosenPath != "" {
			return label + " " + stDim.Render(baseName(t.ChosenPath))
		}
	}
	return label
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[i+1:]
	}
	return p
}
