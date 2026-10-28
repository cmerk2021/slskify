package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/cmerk2021/slskify/internal/store"
)

var (
	colAccent = lipgloss.AdaptiveColor{Light: "#5A3FC0", Dark: "#A78BFA"}
	colDim    = lipgloss.AdaptiveColor{Light: "#777777", Dark: "#888888"}
	colOK     = lipgloss.AdaptiveColor{Light: "#1B7F3B", Dark: "#4ADE80"}
	colWarn   = lipgloss.AdaptiveColor{Light: "#9A6700", Dark: "#FACC15"}
	colErr    = lipgloss.AdaptiveColor{Light: "#B42318", Dark: "#F87171"}
	colInfo   = lipgloss.AdaptiveColor{Light: "#0B6BCB", Dark: "#60A5FA"}

	stTitle    = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	stBold     = lipgloss.NewStyle().Bold(true)
	stDim      = lipgloss.NewStyle().Foreground(colDim)
	stOK       = lipgloss.NewStyle().Foreground(colOK)
	stWarn     = lipgloss.NewStyle().Foreground(colWarn)
	stErr      = lipgloss.NewStyle().Foreground(colErr)
	stInfo     = lipgloss.NewStyle().Foreground(colInfo)
	stSel      = lipgloss.NewStyle().Reverse(true)
	stHelp     = lipgloss.NewStyle().Foreground(colDim)
	stTab      = lipgloss.NewStyle().Padding(0, 1)
	stTabAct   = lipgloss.NewStyle().Padding(0, 1).Bold(true).Underline(true).Foreground(colAccent)
	stReviewHi = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(colWarn)
	stBannerE  = lipgloss.NewStyle().Foreground(colErr).Bold(true)
	stBannerI  = lipgloss.NewStyle().Foreground(colInfo)
	stPaused   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(colWarn).Padding(0, 1)
)

// trunc cuts s (which may contain ANSI styling) to width w cells.
func trunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}

// truncLeft keeps the end of s (useful for paths).
func truncLeft(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= w {
		return s
	}
	return ansi.TruncateLeft(s, ansi.StringWidth(s)-w+1, "…")
}

// pad right-pads s to width w (ANSI-aware), truncating if longer.
func pad(s string, w int) string {
	s = trunc(s, w)
	if n := ansi.StringWidth(s); n < w {
		s += strings.Repeat(" ", w-n)
	}
	return s
}

// fitLines truncates every line to w and pads/cuts to exactly h lines.
func fitLines(lines []string, w, h int) string {
	if h < 0 {
		h = 0
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	out := make([]string, 0, h)
	for _, l := range lines {
		out = append(out, trunc(l, w))
	}
	for len(out) < h {
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}

// window returns [start,end) of a scroll window of size h around cursor.
func window(cursor, offset, n, h int) (int, int, int) {
	if h <= 0 {
		return offset, offset, offset
	}
	if cursor < offset {
		offset = cursor
	}
	if cursor >= offset+h {
		offset = cursor - h + 1
	}
	if offset > n-h {
		offset = n - h
	}
	if offset < 0 {
		offset = 0
	}
	end := offset + h
	if end > n {
		end = n
	}
	return offset, offset, end
}

func helpLine(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, stBold.Render(pairs[i])+" "+pairs[i+1])
	}
	return stHelp.Render(strings.Join(parts, " · "))
}

func stateLabel(s store.State) string {
	switch s {
	case store.StatePending:
		return stDim.Render("pending")
	case store.StateSearching:
		return stInfo.Render("searching")
	case store.StateRanked:
		return stInfo.Render("queued")
	case store.StateDownloading:
		return stInfo.Render("downloading")
	case store.StateDone:
		return stOK.Render("done")
	case store.StateNeedsReview:
		return stWarn.Render("review")
	case store.StateNotFound:
		return stErr.Render("not found")
	case store.StateFailed:
		return stErr.Render("failed")
	case store.StateSkipped:
		return stDim.Render("skipped")
	}
	return string(s)
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func fmtDur(sec int) string {
	if sec <= 0 {
		return "?:??"
	}
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

func progressBar(frac float64, w int) string {
	if w <= 0 {
		return ""
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	n := int(frac*float64(w) + 0.5)
	return stOK.Render(strings.Repeat("█", n)) + stDim.Render(strings.Repeat("░", w-n))
}
