package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const maxTabName = 22

var (
	colorTabBg = lipgloss.AdaptiveColor{Light: "#EDE9FE", Dark: "#2E1065"}
)

// tabSpan is a clickable region of the tab bar. idx is the tab index, or
// -1 for the "+" button.
type tabSpan struct {
	idx        int
	start, end int
	closeStart int // the ✕ occupies [closeStart, end-1)
}

// tabBar renders the tab strip and reports where each tab landed, scrolling
// so the active tab is always visible.
func (m model) tabBar() (string, []tabSpan) {
	type piece struct {
		idx   int
		text  string
		width int
		close int // offset of ✕ within text
	}
	pieces := make([]piece, len(m.tabs))
	for i := range m.tabs {
		active := i == m.active
		base := lipgloss.NewStyle()
		if active {
			base = base.Background(colorTabBg)
		}
		r := m.tabRequest(i)
		method := base.Bold(true).Foreground(methodColor(r.Method)).Render(r.Method)
		nameStyle := base.Foreground(colorMuted)
		if active {
			nameStyle = base.Bold(true)
		}
		name := nameStyle.Render(ansi.Truncate(m.tabName(i), maxTabName, "…"))
		dirty := base.Render("  ")
		if m.dirty(i) {
			dirty = base.Foreground(colorYellow).Render(" ●")
		}
		closeX := base.Foreground(colorMuted).Render(" ✕ ")
		head := base.Render(" ") + method + base.Render(" ") + name + dirty
		text := head + closeX
		pieces[i] = piece{i, text, lipgloss.Width(text), lipgloss.Width(head)}
	}

	plus := mutedStyle.Render(" + ")
	sep := mutedStyle.Render("│")
	width := m.mainW()

	// Drop tabs from the left until the active one fits.
	first := 0
	for {
		total := lipgloss.Width(plus)
		for _, p := range pieces[first : m.active+1] {
			total += p.width + 1
		}
		if total <= width || first >= m.active {
			break
		}
		first++
	}

	var b strings.Builder
	var spans []tabSpan
	x := 0
	if first > 0 {
		b.WriteString(mutedStyle.Render("‹"))
		x++
	}
	for _, p := range pieces[first:] {
		if x+p.width+1+lipgloss.Width(plus) > width && p.idx > m.active {
			b.WriteString(mutedStyle.Render("›"))
			x++
			break
		}
		b.WriteString(p.text + sep)
		spans = append(spans, tabSpan{idx: p.idx, start: x, end: x + p.width, closeStart: x + p.close})
		x += p.width + 1
	}
	b.WriteString(plus)
	spans = append(spans, tabSpan{idx: -1, start: x, end: x + lipgloss.Width(plus)})
	return ansi.Truncate(b.String(), width, ""), spans
}

func (m *model) tabBarClick(x int, middle bool) {
	_, spans := m.tabBar()
	for _, s := range spans {
		if x < s.start || x >= s.end {
			continue
		}
		switch {
		case s.idx < 0:
			m.openTab(request{Method: "GET"}, "")
		case middle || x >= s.closeStart:
			m.closeTab(s.idx, false)
		default:
			m.switchTab(s.idx)
		}
		return
	}
}
