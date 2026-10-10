package ntui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var (
	activeBorder = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	idleBorder   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	muted        = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	errStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	okStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	boldStyle    = lipgloss.NewStyle().Bold(true)
	selStyle     = lipgloss.NewStyle().Reverse(true)
)

// fit truncates or pads s to exactly w cells.
func fit(s string, w int) string {
	if w < 0 {
		w = 0
	}
	s = ansi.Truncate(s, w, "…")
	if n := ansi.StringWidth(s); n < w {
		s += strings.Repeat(" ", w-n)
	}
	return s
}

// box draws a rounded border of outer size w×h around lines, with title (may
// be styled) in the top edge.
func box(title string, w, h int, lines []string, active bool) string {
	bs := idleBorder
	if active {
		bs = activeBorder
	}
	if w < 4 || h < 2 {
		return ""
	}
	if title != "" {
		title = " " + ansi.Truncate(title, w-6, "…") + " "
	}
	rest := w - 3 - ansi.StringWidth(title)
	rows := []string{bs.Render("╭─") + title + bs.Render(strings.Repeat("─", max(rest, 0))+"╮")}
	side := bs.Render("│")
	for i := 0; i < h-2; i++ {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		rows = append(rows, side+fit(l, w-2)+side)
	}
	return strings.Join(append(rows, bs.Render("╰"+strings.Repeat("─", w-2)+"╯")), "\n")
}
