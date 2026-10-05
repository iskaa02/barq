package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Each pane has a jump key, alt+<letter>, or ctrl+x then the letter in
// terminals where alt is awkward. The key is printed in the pane's top
// border, above the thing it focuses.

type paneKey struct {
	letter string
	target focus
	name   string
}

var paneKeys = []paneKey{
	{"u", focusURL, "url"},
	{"p", focusParams, "params"},
	{"h", focusHeaders, "headers"},
	{"b", focusBody, "body"},
	{"r", focusResponse, "response"},
	{"s", focusSidebar, "sidebar"},
}

// paneForKey maps "alt+p" (or "p" after ctrl+x) to its pane.
func paneForKey(k string, afterChord bool) (focus, bool) {
	if !afterChord {
		var ok bool
		if k, ok = strings.CutPrefix(k, "alt+"); !ok {
			return 0, false
		}
	}
	for _, pk := range paneKeys {
		if pk.letter == k {
			return pk.target, true
		}
	}
	return 0, false
}

func paneKeyLabel(f focus) string {
	for _, pk := range paneKeys {
		if pk.target == f {
			return "alt+" + pk.letter
		}
	}
	return ""
}

// jumpTo focuses a pane, opening the sidebar if needed.
func (m *Model) jumpTo(f focus) {
	if f == focusSidebar && !m.showSidebar {
		m.showSidebar = true
		m.layout()
	}
	m.setFocus(f)
}

// chordHelp lists what can follow ctrl+x.
func chordHelp() string {
	parts := []string{"e $EDITOR"}
	for _, pk := range paneKeys {
		parts = append(parts, pk.letter+" "+pk.name)
	}
	return "ctrl+x … " + strings.Join(parts, " • ")
}

// Borders ---------------------------------------------------------------------

type borderLabel struct {
	col  int // column within the pane, counting the corner as 0
	text string
}

// topBorder draws a rounded top border w cells wide with labels written
// into it. Labels that don't fit are left out.
func topBorder(w int, focused bool, labels []borderLabel) string {
	if w < 2 {
		return ""
	}
	color := lipgloss.TerminalColor(colorBorder)
	labelStyle := mutedStyle
	if focused {
		color = colorAccent
		labelStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	}
	line := lipgloss.NewStyle().Foreground(color)

	var b strings.Builder
	b.WriteString(line.Render("╭"))
	pos := 1
	for _, l := range labels {
		lw := ansi.StringWidth(l.text)
		if l.col < pos || l.col+lw > w-2 { // keep at least one ─ before ╮
			continue
		}
		b.WriteString(line.Render(strings.Repeat("─", l.col-pos)))
		b.WriteString(labelStyle.Render(l.text))
		pos = l.col + lw
	}
	b.WriteString(line.Render(strings.Repeat("─", w-1-pos) + "╮"))
	return b.String()
}

// boxed renders content in a rounded box w wide (and h tall, if h > 0)
// whose top border carries labels.
func boxed(focused bool, w, h int, labels []borderLabel, content string) string {
	style := paneStyle
	if focused {
		style = focusedPaneStyle
	}
	style = style.BorderTop(false).Width(w - 2)
	if h > 0 {
		style = style.Height(h - 2).MaxHeight(h - 1)
	}
	return topBorder(w, focused, labels) + "\n" + style.Render(content)
}

// requestPaneLabels puts each tab's key above the tab's name.
func (m Model) requestPaneLabels() []borderLabel {
	var labels []borderLabel
	col := innerX // border + padding
	for i, name := range m.requestTabNames() {
		labels = append(labels, borderLabel{col, paneKeyLabel(requestTabFocus[i])})
		col += lipgloss.Width(name) + 5 // "  │  " between tabs
	}
	return labels
}
