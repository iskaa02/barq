package tui

import (
	"github.com/iskaa02/barq/internal/core"
)

// syncParamsFromURL refreshes the table after the URL changed.
func (m *Model) syncParamsFromURL() {
	m.params.SetRows(core.ParamRows(m.url.Value(), core.DisabledRows(m.params.Rows())))
}

// syncURLFromParams rewrites the URL's query after the table changed.
func (m *Model) syncURLFromParams() {
	pos := m.url.Position()
	m.url.SetValue(core.BuildURL(m.url.Value(), m.params.Rows()))
	m.url.SetCursor(pos)
}
