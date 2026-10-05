package tui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/iskaa02/barq/internal/core"
)

// UI ----------------------------------------------------------------------------

func newFormEditor(cwd string) headerEditor {
	e := newKVEditor("Key", "Value  (@path for a file)", "+ add field", false)
	e.fileRoot = cwd
	return e
}

func (m *Model) toggleBodyMode() {
	if m.bodyMode == core.BodyForm {
		m.bodyMode = ""
	} else {
		m.bodyMode = core.BodyForm
	}
	m.setFocus(focusBody)
}

// bodyModeLine is the switch shown at the top of the Body tab.
func (m Model) bodyModeLine() string {
	on := lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	raw, form := mutedStyle.Render("○ raw"), mutedStyle.Render("○ form-data")
	if m.bodyMode == core.BodyForm {
		form = on.Render("● form-data")
	} else {
		raw = on.Render("● raw")
	}
	return raw + "  " + form + mutedStyle.Render("   alt+m")
}

// bodyModeClick handles a click on the mode line at column x.
func (m *Model) bodyModeClick(x int) {
	const rawEnd, formStart, formEnd = 5, 7, 18 // "○ raw  ○ form-data"
	switch {
	case x < rawEnd && m.bodyMode == core.BodyForm, x >= formStart && x < formEnd && m.bodyMode != core.BodyForm:
		m.toggleBodyMode()
	default:
		m.setFocus(focusBody)
	}
}
