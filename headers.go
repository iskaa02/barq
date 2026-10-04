package main

import (
	"net/http"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var commonHeaders = []string{
	"Accept", "Accept-Encoding", "Accept-Language", "Authorization",
	"Cache-Control", "Connection", "Content-Type", "Cookie", "If-Match",
	"If-Modified-Since", "If-None-Match", "Origin", "Referer", "User-Agent",
	"X-API-Key", "X-Forwarded-For", "X-Request-ID",
}

var mediaTypes = []string{
	"application/json", "application/x-www-form-urlencoded",
	"multipart/form-data", "text/plain", "text/html", "application/xml",
	"application/octet-stream",
}

var commonValues = map[string][]string{
	"Accept":          append([]string{"*/*"}, mediaTypes...),
	"Accept-Encoding": {"gzip", "deflate", "br", "identity", "gzip, deflate, br"},
	"Accept-Language": {"en-US,en;q=0.9", "en", "*"},
	"Authorization":   {"Bearer ", "Basic "},
	"Cache-Control":   {"no-cache", "no-store", "max-age=0"},
	"Connection":      {"keep-alive", "close"},
	"Content-Type":    mediaTypes,
}

const (
	maxSuggestions = 5
	checkWidth     = 4 // "[✓] "
	deleteWidth    = 2 // " ✕"
)

type headerRow struct {
	key, value string
	enabled    bool
}

func (r headerRow) empty() bool { return r.key == "" && r.value == "" }

// headerEditor is a key/value table for request headers. One textinput
// edits the active cell; other cells render as plain text. A trailing empty
// row is always kept so there's somewhere to type a new header.
type headerEditor struct {
	rows     []headerRow
	row, col int // active cell; col 0 is the key, 1 the value
	input    textinput.Model
	focused  bool

	width, height int
	keyW, valueW  int
	offset        int // first visible line

	suggestions []string
	suggIdx     int

	// Labels and behavior, so the table can edit other key/value lists.
	keyLabel, valueLabel, addLabel string
	suggest                        bool
	// fileRoot, when set, marks "@path" values as files relative to it
	// (form-data), colored by whether the file exists.
	fileRoot string
}

func newHeaderEditor() headerEditor {
	return newKVEditor("Key", "Value", "+ add header", true)
}

func newKVEditor(keyLabel, valueLabel, addLabel string, suggest bool) headerEditor {
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 0
	in.PlaceholderStyle = mutedStyle
	h := headerEditor{rows: []headerRow{{enabled: true}}, input: in,
		keyLabel: keyLabel, valueLabel: valueLabel, addLabel: addLabel, suggest: suggest}
	h.load()
	return h
}

// Rows returns every non-empty row, including disabled ones.
func (h headerEditor) Rows() []headerRow {
	var out []headerRow
	for _, r := range h.rows {
		if !r.empty() {
			out = append(out, r)
		}
	}
	return out
}

// SetRows replaces all headers.
func (h *headerEditor) SetRows(rows []headerRow) {
	h.rows = append([]headerRow{}, rows...)
	if len(h.rows) == 0 {
		h.rows = []headerRow{{enabled: true}}
	}
	h.ensureTrailingRow()
	h.row, h.col, h.offset = 0, 0, 0
	h.suggestions = nil
	h.load()
	h.scroll()
}

func (h *headerEditor) Focus() {
	h.focused = true
	h.input.Focus()
}

func (h *headerEditor) Blur() {
	h.focused = false
	h.input.Blur()
	h.suggestions = nil
}

func (h *headerEditor) SetSize(w, ht int) {
	h.width, h.height = w, ht
	h.keyW = max((w-checkWidth-deleteWidth-1)*2/5, 6)
	h.valueW = max(w-checkWidth-deleteWidth-1-h.keyW, 6)
	h.load()
	h.scroll()
}

// Header returns the enabled, non-empty headers.
func (h headerEditor) Header() http.Header {
	hdr := http.Header{}
	for _, r := range h.rows {
		if k := strings.TrimSpace(r.key); r.enabled && k != "" {
			hdr.Add(k, strings.TrimSpace(r.value))
		}
	}
	return hdr
}

func (h headerEditor) EnabledCount() int { return len(h.Header()) }

// commit writes the input's value back into the active cell.
func (h *headerEditor) commit() {
	if h.col == 0 {
		h.rows[h.row].key = h.input.Value()
	} else {
		h.rows[h.row].value = h.input.Value()
	}
}

// load puts the active cell into the input.
func (h *headerEditor) load() {
	r := h.rows[h.row]
	if h.col == 0 {
		h.input.SetValue(r.key)
		h.input.Placeholder = h.keyLabel
		h.input.Width = max(h.keyW-1, 1)
	} else {
		h.input.SetValue(r.value)
		h.input.Placeholder = h.valueLabel
		h.input.Width = max(h.valueW-1, 1)
	}
	h.input.CursorEnd()
}

func (h *headerEditor) ensureTrailingRow() {
	if !h.rows[len(h.rows)-1].empty() {
		h.rows = append(h.rows, headerRow{enabled: true})
	}
}

func (h *headerEditor) moveTo(row, col int) {
	h.commit()
	h.ensureTrailingRow()
	h.row = min(max(row, 0), len(h.rows)-1)
	h.col = col
	h.suggestions = nil
	h.load()
	h.scroll()
}

func (h *headerEditor) deleteRow() {
	h.rows = append(h.rows[:h.row], h.rows[h.row+1:]...)
	if len(h.rows) == 0 {
		h.rows = []headerRow{{enabled: true}}
	}
	h.ensureTrailingRow()
	h.row = min(h.row, len(h.rows)-1)
	h.suggestions = nil
	h.load()
	h.scroll()
}

func (h *headerEditor) toggleRow() {
	h.rows[h.row].enabled = !h.rows[h.row].enabled
}

// AcceptSuggestion fills the active cell with the highlighted suggestion.
// It reports whether there was one to accept.
func (h *headerEditor) AcceptSuggestion() bool {
	if len(h.suggestions) == 0 {
		return false
	}
	s := h.suggestions[h.suggIdx]
	h.input.SetValue(s)
	h.input.CursorEnd()
	h.commit()
	h.suggestions = nil
	switch {
	case h.col == 0:
		// Jump to the value and offer values known for this header.
		h.moveTo(h.row, 1)
		h.refreshSuggestions()
	case !strings.HasSuffix(s, " "):
		// A complete value; prefixes like "Bearer " still need typing.
		h.moveTo(h.row+1, 0)
	}
	h.scroll()
	return true
}

// CloseSuggestions hides the dropdown, reporting whether it was open.
func (h *headerEditor) CloseSuggestions() bool {
	open := len(h.suggestions) > 0
	h.suggestions = nil
	return open
}

func (h *headerEditor) refreshSuggestions() {
	if !h.suggest {
		h.suggestions = nil
		return
	}
	q := strings.ToLower(strings.TrimSpace(h.input.Value()))
	var pool []string
	if h.col == 0 {
		if q == "" {
			h.suggestions = nil
			return
		}
		pool = commonHeaders
	} else {
		pool = commonValues[http.CanonicalHeaderKey(strings.TrimSpace(h.rows[h.row].key))]
	}

	var prefix, contains []string
	for _, s := range pool {
		ls := strings.ToLower(s)
		switch {
		case strings.HasPrefix(ls, q):
			prefix = append(prefix, s)
		case strings.Contains(ls, q):
			contains = append(contains, s)
		}
	}
	h.suggestions = append(prefix, contains...)
	if len(h.suggestions) > maxSuggestions {
		h.suggestions = h.suggestions[:maxSuggestions]
	}
	h.suggIdx = 0
}

// paste turns "Key: Value" lines into rows, starting at the active row.
func (h *headerEditor) paste(text string) {
	// Terminals usually deliver pasted line breaks as \r.
	text = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(text)
	var parsed []headerRow
	for _, line := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if k = strings.TrimSpace(k); ok && k != "" && !strings.HasPrefix(k, "#") {
			parsed = append(parsed, headerRow{key: k, value: strings.TrimSpace(v), enabled: true})
		}
	}
	if len(parsed) == 0 {
		return
	}
	h.commit()
	at := h.row
	if !h.rows[at].empty() {
		at++
	}
	rest := append([]headerRow{}, h.rows[at:]...)
	if len(rest) > 0 && rest[0].empty() {
		rest = rest[1:]
	}
	h.rows = append(append(h.rows[:at], parsed...), rest...)
	// Not moveTo: the input still holds the pre-paste cell and committing
	// it would overwrite the first pasted row.
	h.ensureTrailingRow()
	h.row, h.col = min(at+len(parsed), len(h.rows)-1), 0
	h.suggestions = nil
	h.load()
	h.scroll()
}

func (h headerEditor) Update(msg tea.Msg) (headerEditor, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		// Cursor blinks, clipboard pastes, etc.
		var cmd tea.Cmd
		h.input, cmd = h.input.Update(msg)
		h.commit()
		return h, cmd
	}

	if key.Paste && (strings.ContainsAny(string(key.Runes), "\r\n") ||
		(h.col == 0 && strings.Contains(string(key.Runes), ":"))) {
		h.paste(string(key.Runes))
		return h, nil
	}

	open := len(h.suggestions) > 0
	switch key.String() {
	case "up":
		if open {
			h.suggIdx = (h.suggIdx + len(h.suggestions) - 1) % len(h.suggestions)
		} else {
			h.moveTo(h.row-1, h.col)
		}
		return h, nil
	case "down":
		if open {
			h.suggIdx = (h.suggIdx + 1) % len(h.suggestions)
		} else {
			h.moveTo(h.row+1, h.col)
		}
		return h, nil
	case "enter":
		if h.AcceptSuggestion() {
			return h, nil
		}
		if h.col == 0 {
			h.moveTo(h.row, 1)
			h.refreshSuggestions()
		} else {
			h.moveTo(h.row+1, 0)
		}
		return h, nil
	case "ctrl+d":
		h.deleteRow()
		return h, nil
	case "ctrl+t":
		h.toggleRow()
		return h, nil
	}

	// Typing "Content-Type:" moves straight on to the value. Runes can
	// arrive batched, so split around the colon rather than matching ":".
	if before, after, ok := strings.Cut(string(key.Runes), ":"); ok && h.col == 0 && key.Type == tea.KeyRunes {
		if before != "" {
			h.input, _ = h.input.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(before)})
		}
		h.moveTo(h.row, 1)
		if after = strings.TrimLeft(after, " "); after != "" {
			h.input, _ = h.input.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(after)})
			h.commit()
		}
		h.refreshSuggestions()
		h.scroll()
		return h, nil
	}

	// "Key: value" typed out: skip the space after the colon jump.
	if h.col == 1 && h.input.Value() == "" && key.String() == " " {
		return h, nil
	}

	var cmd tea.Cmd
	h.input, cmd = h.input.Update(msg)
	h.commit()
	h.ensureTrailingRow()
	h.refreshSuggestions()
	h.scroll()
	return h, cmd
}

// Rendering -----------------------------------------------------------------

type lineKind int

const (
	lineRow lineKind = iota
	lineSuggestion
)

type headerLine struct {
	kind lineKind
	idx  int // row index or suggestion index
}

// lines lays out the table body: one line per row, with the suggestion
// dropdown inserted under the active row.
func (h headerEditor) lines() []headerLine {
	out := make([]headerLine, 0, len(h.rows)+len(h.suggestions))
	for i := range h.rows {
		out = append(out, headerLine{lineRow, i})
		if i == h.row && h.focused {
			for j := range h.suggestions {
				out = append(out, headerLine{lineSuggestion, j})
			}
		}
	}
	return out
}

// bodyHeight is the number of table lines that fit under the column titles.
func (h headerEditor) bodyHeight() int { return max(h.height-1, 1) }

// scroll keeps the active row and its dropdown visible.
func (h *headerEditor) scroll() {
	first := h.row // the active row's line; suggestions only follow it
	last := first
	if h.focused {
		last += len(h.suggestions)
	}
	bh := h.bodyHeight()
	if first < h.offset {
		h.offset = first
	}
	if last >= h.offset+bh {
		h.offset = last - bh + 1
	}
	h.offset = max(min(h.offset, len(h.lines())-bh), 0)
}

func fit(s string, w int) string {
	s = ansi.Truncate(s, w, "…")
	return s + strings.Repeat(" ", max(w-lipgloss.Width(s), 0))
}

func (h headerEditor) cell(r headerRow, i, col int, text string, w int) string {
	if h.focused && i == h.row && col == h.col {
		return fit(h.input.View(), w)
	}
	switch {
	case text == "" && i == len(h.rows)-1:
		if col == 0 {
			return fit(mutedStyle.Render(h.addLabel), w)
		}
		return fit("", w)
	case !r.enabled:
		return mutedStyle.Render(fit(text, w))
	case col == 0:
		return headerKeyStyle.Render(fit(text, w))
	}
	if h.fileRoot != "" {
		if isFile, exists := formFileState(text, h.fileRoot); isFile {
			if strings.TrimSpace(text) == "@" {
				return errorStyle.Render(fit("@  ← add a file path", w))
			}
			if !exists {
				return errorStyle.Render(fit(text+"  ✗ not found", w))
			}
			return lipgloss.NewStyle().Foreground(colorCyan).Render(fit(text, w))
		}
	}
	return fit(text, w)
}

func (h headerEditor) View() string {
	var b strings.Builder
	b.WriteString(mutedStyle.Render(fit(strings.Repeat(" ", checkWidth)+fit(h.keyLabel, h.keyW)+" "+h.valueLabel, h.width)))

	lines := h.lines()
	end := min(h.offset+h.bodyHeight(), len(lines))
	for _, l := range lines[h.offset:end] {
		b.WriteByte('\n')
		if l.kind == lineSuggestion {
			indent := checkWidth
			if h.col == 1 {
				indent += h.keyW + 1
			}
			s := h.suggestions[l.idx]
			if l.idx == h.suggIdx {
				s = activeSuggStyle.Render("▸ " + s)
			} else {
				s = mutedStyle.Render("  " + s)
			}
			b.WriteString(strings.Repeat(" ", indent) + s)
			continue
		}

		r := h.rows[l.idx]
		check := mutedStyle.Render("[ ] ")
		if r.enabled {
			check = lipgloss.NewStyle().Foreground(colorAccent).Render("[✓] ")
		}
		if r.empty() {
			check = "    "
		}
		del := "  "
		if !r.empty() {
			del = mutedStyle.Render(" ✕")
		}
		b.WriteString(check +
			h.cell(r, l.idx, 0, r.key, h.keyW) + " " +
			h.cell(r, l.idx, 1, r.value, h.valueW) + del)
	}
	return b.String()
}

// Mouse ----------------------------------------------------------------------

// Click handles a left click at (x, y) relative to the editor, where y=0 is
// the column-title line.
func (h *headerEditor) Click(x, y int) {
	lines := h.lines()
	i := h.offset + y - 1
	if y < 1 || i >= len(lines) {
		return
	}
	l := lines[i]
	if l.kind == lineSuggestion {
		h.suggIdx = l.idx
		h.AcceptSuggestion()
		return
	}

	valueX := checkWidth + h.keyW + 1
	switch {
	case x < checkWidth-1:
		h.moveTo(l.idx, h.col)
		if !h.rows[l.idx].empty() {
			h.toggleRow()
		}
	case x >= valueX+h.valueW:
		h.moveTo(l.idx, h.col)
		if !h.rows[l.idx].empty() {
			h.deleteRow()
		}
	case x >= valueX:
		h.moveTo(l.idx, 1)
		h.placeCursor(x - valueX)
	default:
		h.moveTo(l.idx, 0)
		h.placeCursor(x - checkWidth)
	}
}

func (h *headerEditor) placeCursor(x int) {
	if n := len([]rune(h.input.Value())); n < h.input.Width {
		h.input.SetCursor(max(x, 0))
	}
}

func (h *headerEditor) Wheel(delta int) {
	h.moveTo(h.row+delta, h.col)
}
