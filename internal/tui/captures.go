package tui

import (
	"slices"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/iskaa02/barq/internal/core"
)

// TUI -------------------------------------------------------------------------

// runCaptures applies a saved request's captures after a successful send.
func (m *Model) runCaptures(t *tab, resp *core.Response) {
	if t.savedID == "" || resp == nil || resp.StatusCode >= 400 {
		return
	}
	j := m.ws.Find(t.savedID)
	if j < 0 || len(m.ws.Requests[j].Captures) == 0 {
		return
	}
	vals := core.EvalCaptures(m.ws.Requests[j].Captures, resp.Body)
	envID := m.ws.ActiveEnv
	if m.mutate(func(w *core.Workspace) error { return w.StoreCaptured(envID, vals) }) {
		m.flash(core.CapturedSummary(vals))
	}
}

func (m *Model) addCapture(spec string) {
	c, err := core.ParseCapture(spec)
	if err != nil {
		m.notice = errorStyle.Render(err.Error())
		return
	}
	id := m.cur().savedID
	if m.mutate(func(w *core.Workspace) error {
		return w.UpdateRequest(id, func(r *core.Request) {
			r.Captures = slices.DeleteFunc(r.Captures, func(x core.Capture) bool { return x.Var == c.Var })
			r.Captures = append(r.Captures, c)
		})
	}) {
		m.flash("after each successful send: " + c.String())
	}
}

func captureItems(m Model) []paletteItem {
	j := m.ws.Find(m.cur().savedID)
	if j < 0 {
		return nil
	}
	id := m.cur().savedID
	var items []paletteItem
	for _, c := range m.ws.Requests[j].Captures {
		v := c.Var
		items = append(items, paletteItem{title: c.String(), run: func(m *Model) tea.Cmd {
			m.mutate(func(w *core.Workspace) error {
				return w.UpdateRequest(id, func(r *core.Request) {
					r.Captures = slices.DeleteFunc(r.Captures, func(x core.Capture) bool { return x.Var == v })
				})
			})
			return nil
		}})
	}
	return items
}
