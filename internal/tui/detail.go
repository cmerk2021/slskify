package tui

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/cmerk2021/slskify/internal/match"
	"github.com/cmerk2021/slskify/internal/pipeline"
	"github.com/cmerk2021/slskify/internal/store"
)

var sortModes = []string{"rank", "confidence", "quality", "peer speed"}

type detailModel struct {
	id             int64
	review         bool
	cursor, offset int
	sortMode       int
	showRejected   bool
	editing        bool
	input          textinput.Model
}

func newDetailModel(id int64, review bool) *detailModel {
	ti := textinput.New()
	ti.Prompt = "query › "
	ti.CharLimit = 300
	return &detailModel{id: id, review: review, input: ti}
}

// rows returns the candidates shown in the table: every candidate that
// passed hard filters, plus rejected ones when toggled (or when nothing
// passed, so "not found" tracks show the best rejected files for context).
func (d *detailModel) rows(t pipeline.TrackView) []*match.Candidate {
	cs := t.Candidates
	passed := t.Passed
	if passed > len(cs) || passed < 0 {
		passed = len(cs)
	}
	out := append([]*match.Candidate(nil), cs[:passed]...)
	switch d.sortMode {
	case 1:
		sort.SliceStable(out, func(i, j int) bool { return out[i].Confidence > out[j].Confidence })
	case 2:
		sort.SliceStable(out, func(i, j int) bool {
			a, b := out[i], out[j]
			if a.Tier != b.Tier {
				return a.Tier < b.Tier
			}
			return a.Quality.Bitrate > b.Quality.Bitrate
		})
	case 3:
		sort.SliceStable(out, func(i, j int) bool { return out[i].UploadSpeed > out[j].UploadSpeed })
	}
	if d.showRejected || passed == 0 {
		out = append(out, cs[passed:]...)
	}
	return out
}

func (d *detailModel) clamp(n int) {
	if d.cursor >= n {
		d.cursor = n - 1
	}
	if d.cursor < 0 {
		d.cursor = 0
	}
}

func (m *Model) updateDetail(msg tea.Msg) (tea.Model, tea.Cmd) {
	mm := m.main
	d := mm.detail
	t, ok := mm.track(d.id)
	if !ok {
		mm.detail = nil
		return m, nil
	}
	if d.editing {
		if k, ok := msg.(tea.KeyMsg); ok {
			switch k.String() {
			case "esc":
				d.editing = false
				d.input.Blur()
				return m, nil
			case "enter":
				d.editing = false
				d.input.Blur()
				q := strings.TrimSpace(d.input.Value())
				return m.reviewAction(func() error { return m.runner.Research(d.id, q) }, "re-searching")
			}
		}
		var cmd tea.Cmd
		d.input, cmd = d.input.Update(msg)
		return m, cmd
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	rows := d.rows(t)
	page := m.h / 3
	if page < 1 {
		page = 1
	}
	switch key := k.String(); key {
	case "esc", "backspace":
		mm.detail = nil
		if d.review {
			mm.tab = tabReview
			mm.rebuild()
		}
	case "q":
		return m.quit()
	case "up", "k":
		d.cursor--
	case "down", "j":
		d.cursor++
	case "pgup":
		d.cursor -= page
	case "pgdown":
		d.cursor += page
	case "home", "g":
		d.cursor = 0
	case "end", "G":
		d.cursor = len(rows) - 1
	case "o":
		d.sortMode = (d.sortMode + 1) % len(sortModes)
		d.cursor = 0
	case "x":
		d.showRejected = !d.showRejected
	case "p":
		m.runner.TogglePause()
		m.refresh()
	case "s":
		return m.reviewAction(func() error { return m.runner.Skip(d.id) }, "skipped")
	case "e":
		q := t.Query
		if q == "" && len(t.Queries) > 0 {
			q = t.Queries[0]
		}
		if q == "" {
			q = match.QueryText(t.Artists[0] + " " + t.Title)
		}
		d.input.SetValue(q)
		d.input.CursorEnd()
		d.editing = true
		return m, d.input.Focus()
	case "n", "N":
		if d.review && len(mm.rq.entries) > 0 {
			step := 1
			if key == "N" {
				step = -1
			}
			n := len(mm.rq.entries)
			mm.rq.idx = (mm.rq.idx + step + n) % n
			mm.detail = newDetailModel(mm.rq.entries[mm.rq.idx].id, true)
			mm.detail.sortMode, mm.detail.showRejected = d.sortMode, d.showRejected
		}
	case "enter", "1", "2", "3", "4", "5", "6", "7", "8", "9":
		i := d.cursor
		if key != "enter" {
			i = int(key[0] - '1')
		}
		if i < 0 || i >= len(rows) {
			m.fail(fmt.Errorf("no candidate #%d", i+1))
			return m, nil
		}
		c := rows[i]
		return m.reviewAction(func() error { return m.runner.Pick(d.id, c.Key()) }, "downloading "+baseName(c.Path))
	}
	d.clamp(len(rows))
	return m, nil
}

// reviewAction runs a pick/skip/re-search and, in review mode, advances to
// the next unresolved track.
func (m *Model) reviewAction(f func() error, what string) (tea.Model, tea.Cmd) {
	mm := m.main
	d := mm.detail
	if err := f(); err != nil {
		if errors.Is(err, pipeline.ErrNotReviewable) {
			err = errors.New("this track is busy (searching/downloading) or done; it can't be changed right now")
		}
		m.fail(err)
		return m, nil
	}
	t, _ := mm.track(d.id)
	m.info(fmt.Sprintf("%s: %s", t.Title, what))
	m.refresh()
	if !d.review {
		return m, nil
	}
	if i := mm.nextUnresolved(mm.rq.idx); i >= 0 {
		mm.rq.idx = i
		nd := newDetailModel(mm.rq.entries[i].id, true)
		nd.sortMode, nd.showRejected = d.sortMode, d.showRejected
		mm.detail = nd
	}
	return m, nil
}

func (mm *mainModel) detailView(s pipeline.Snapshot, w, h int) (string, string) {
	d := mm.detail
	t, _ := mm.track(d.id)
	var lines []string

	if d.review {
		left := mm.unresolvedCount()
		ind := fmt.Sprintf("Review %d of %d", mm.rq.idx+1, len(mm.rq.entries))
		status := stWarn.Render(fmt.Sprintf("  %d unresolved", left))
		if left == 0 {
			status = stOK.Render("  all reviewed ✓")
		} else if !mm.unresolved(mm.rq.entries[mm.rq.idx]) {
			status += stDim.Render("  (this one is handled)")
		}
		lines = append(lines, stTitle.Render(ind)+status+pausedTag(s.Paused))
	} else {
		lines = append(lines, stTitle.Render("Track detail")+pausedTag(s.Paused))
	}
	lines = append(lines, stBold.Render(t.Title)+" — "+strings.Join(t.Artists, ", "))
	meta := []string{t.Album}
	if t.Year > 0 {
		meta[0] += fmt.Sprintf(" (%d)", t.Year)
	}
	meta = append(meta, fmt.Sprintf("disc %d track %d", t.DiscNumber, t.TrackNumber), fmtDur(t.DurationMs/1000))
	if t.ISRC != "" {
		meta = append(meta, "ISRC "+t.ISRC)
	}
	if t.Explicit {
		meta = append(meta, "explicit")
	}
	meta = append(meta, "spotify:track:"+t.SpotifyID)
	lines = append(lines, stDim.Render(strings.Join(meta, " · ")))
	state := "State: " + stateLabel(t.State)
	if t.Reason != "" {
		state += " — " + t.Reason
	}
	if t.Attempts > 0 {
		state += stDim.Render(fmt.Sprintf("  (attempts %d)", t.Attempts))
	}
	lines = append(lines, state)
	q := "Queries: " + stDim.Render(strings.Join(t.Queries, "  |  "))
	if t.Query != "" {
		q = "Custom query: " + stInfo.Render(t.Query)
	}
	lines = append(lines, q)
	if t.ChosenPath != "" {
		dl := "Chosen: " + t.ChosenUser + " · " + truncLeft(t.ChosenPath, w-20-len(t.ChosenUser))
		lines = append(lines, dl)
	}
	if p := t.Progress; p != nil && t.State == store.StateDownloading {
		pl := fmt.Sprintf("Transfer: %s", p.State)
		if p.PlaceInQueue != nil {
			pl += fmt.Sprintf(" (place %d)", *p.PlaceInQueue)
		}
		pl += fmt.Sprintf("  %s %3.0f%%  %s / %s  %s/s", progressBar(p.Percent/100, 20), p.Percent, humanBytes(p.Bytes), humanBytes(p.Size), humanBytes(int64(p.Speed)))
		lines = append(lines, pl)
	}
	if d.editing {
		d.input.Width = w - 12
		lines = append(lines, d.input.View(), stDim.Render("enter: re-search (empty = generated queries) · esc: cancel"))
	}

	rows := d.rows(t)
	d.clamp(len(rows))
	nRej := len(t.Candidates) - t.Passed
	if t.Passed > len(t.Candidates) {
		nRej = 0
	}
	title := fmt.Sprintf("Candidates: %d passed hard filters, %d rejected · sort: %s", min(t.Passed, len(t.Candidates)), nRej, sortModes[d.sortMode])
	lines = append(lines, "", stBold.Render(title))

	// Breakdown panel for the highlighted candidate.
	var bd []string
	if len(rows) > 0 {
		c := rows[d.cursor]
		bd = append(bd, stDim.Render(strings.Repeat("─", w)))
		bd = append(bd, stBold.Render(c.Username)+" "+stDim.Render(truncLeft(c.Path, w-len(c.Username)-2)))
		ex := strings.TrimRight(c.Explain("  "), "\n")
		if ex != "" {
			bd = append(bd, strings.Split(ex, "\n")...)
		}
		if c.Rejected != "" {
			bd = append(bd, stErr.Render("  rejected: "+c.Rejected))
		}
		if c.Failed != "" {
			bd = append(bd, stErr.Render("  download failed: "+c.Failed))
		}
	}
	tableH := h - len(lines) - 1
	maxBD := tableH / 2
	if maxBD < 3 {
		maxBD = 3
	}
	if len(bd) > maxBD {
		bd = bd[:maxBD]
	}
	tableH -= len(bd)
	if tableH < 1 {
		tableH = 1
	}

	confW, qualW, tierW, peerW, noteW := 5, 16, 12, 18, 18
	pathW := w - 4 - confW - qualW - tierW - peerW - noteW - 6
	if pathW < 12 {
		tierW, peerW = 0, 0
		pathW = w - 4 - confW - qualW - noteW - 4
	}
	hdr := pad("#", 4) + pad("conf", confW) + " " + pad("quality", qualW) + " "
	if tierW > 0 {
		hdr += pad("tier", tierW) + " " + pad("peer", peerW) + " "
	}
	hdr += pad("notes", noteW) + " " + "file"
	lines = append(lines, stDim.Render(hdr))
	if len(rows) == 0 {
		lines = append(lines, stDim.Render("  no candidates — press e to edit the query and re-search"))
	}
	var start, end int
	d.offset, start, end = window(d.cursor, d.offset, len(rows), tableH-1)
	for i := start; i < end; i++ {
		c := rows[i]
		num := fmt.Sprintf("%d", i+1)
		if c.Username == t.ChosenUser && c.Path == t.ChosenPath {
			num = "▶" + num
		}
		conf := fmt.Sprintf("%3d", c.Confidence)
		switch {
		case c.Confidence >= 80:
			conf = stOK.Render(conf)
		case c.Confidence >= 50:
			conf = stWarn.Render(conf)
		default:
			conf = stErr.Render(conf)
		}
		row := pad(num, 4) + pad(conf, confW) + " " + pad(c.Quality.Label, qualW) + " "
		if tierW > 0 {
			row += pad(c.TierName, tierW) + " " + pad(peerInfo(c), peerW) + " "
		}
		row += pad(candNotes(c), noteW) + " " + truncLeft(c.Path, pathW)
		if i == d.cursor {
			row = stSel.Render(pad(ansi.Strip(row), w))
		}
		lines = append(lines, row)
	}
	body := fitLines(lines, w, h-len(bd))
	if len(bd) > 0 {
		body += "\n" + fitLines(bd, w, len(bd))
	}

	var help string
	if d.editing {
		help = helpLine("enter", "re-search", "esc", "cancel")
	} else if d.review {
		help = helpLine("1-9/enter", "pick", "s", "skip", "e", "edit query", "n/N", "next/prev", "↑↓", "select", "o", "sort", "x", "rejected", "p", "pause", "esc", "exit review")
	} else {
		help = helpLine("1-9/enter", "pick", "s", "skip", "e", "edit query", "↑↓", "select", "o", "sort", "x", "rejected", "p", "pause", "esc", "back")
	}
	return body, help
}

func pausedTag(p bool) string {
	if p {
		return "  " + stPaused.Render("PAUSED")
	}
	return ""
}

func peerInfo(c *match.Candidate) string {
	slot := "busy"
	if c.FreeSlot {
		slot = "free"
	}
	s := slot
	if c.UploadSpeed > 0 {
		s += " " + humanBytes(c.UploadSpeed) + "/s"
	}
	if c.QueueLength > 0 {
		s += fmt.Sprintf(" q%d", c.QueueLength)
	}
	return s
}

var flagShort = map[string]string{
	match.FlagDurationUnknown:   "dur?",
	match.FlagDurationEstimated: "dur~",
	match.FlagVersionMismatch:   "version≠",
	match.FlagTitleMismatch:     "title≠",
	match.FlagTitleExtraWords:   "title+",
	match.FlagArtistMissing:     "artist?",
	match.FlagNegative:          "neg",
	match.FlagMultiTrack:        "multi",
	match.FlagBitrateUnknown:    "kbps?",
	match.FlagAmbiguous:         "ambiguous",
}

func candNotes(c *match.Candidate) string {
	switch {
	case c.Failed != "":
		return stErr.Render("✗ " + c.Failed)
	case c.Rejected != "":
		return stDim.Render("rej: " + c.Rejected)
	}
	var parts []string
	if c.AutoEligible {
		parts = append(parts, stOK.Render("auto"))
	}
	for _, f := range c.Flags {
		if s, ok := flagShort[f]; ok {
			parts = append(parts, s)
		} else {
			parts = append(parts, f)
		}
	}
	return strings.Join(parts, " ")
}
