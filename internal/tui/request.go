package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/iskaa02/barq/internal/core"
)

type responseMsg struct {
	tabUID int
	resp   *core.Response
	pretty string // formatted body, prepared off the UI thread
	err    error
}

// sendRequest sends a resolved request in the background for the TUI.
func sendRequest(ctx context.Context, tabUID int, r core.Request, cwd string) tea.Cmd {
	return func() tea.Msg {
		resp, err := core.RunRequest(ctx, r, cwd)
		msg := responseMsg{tabUID: tabUID, resp: resp, err: err}
		// Formatting a large body takes a while; do it here rather than
		// in Update so the UI stays responsive.
		if msg.resp != nil {
			pretty, isJSON := core.PrettyBody(msg.resp)
			if isJSON {
				pretty = highlightJSON(pretty)
			}
			msg.pretty = pretty
		}
		return msg
	}
}
