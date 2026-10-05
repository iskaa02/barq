package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/itchyny/gojq"
)

// The response pane can be searched (/) and filtered with jq (|). Both use
// an input bar that replaces the pane's tab row while it's open.

type barMode int

const (
	barFind barMode = iota
	barJQ
)

type respBar struct {
	mode  barMode
	input textinput.Model
}

var (
	matchStyle   = lipgloss.NewStyle().Background(lipgloss.AdaptiveColor{Light: "#FDE68A", Dark: "#854D0E"})
	currentMatch = lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#7C3AED"})
)

const (
	jqTimeout   = 500 * time.Millisecond
	jqMaxOutput = 2 << 20
)

// Content -------------------------------------------------------------------

// responseText is what the response pane shows, before wrapping.
func (m model) responseText() string {
	t := m.cur()
	switch {
	case t.viewing != nil:
		return m.viewingText()
	case t.respTab == respTabHistory:
		return m.historyListText()
	case t.err != nil:
		return errorStyle.Render("Error: " + t.err.Error())
	case t.result == nil:
		return mutedStyle.Render("Enter a URL and press enter or ctrl+r to send.")
	case t.respTab == respTabHeaders:
		var b strings.Builder
		for _, line := range strings.Split(strings.TrimRight(formatHeaders(t.result), "\n"), "\n") {
			k, v, _ := strings.Cut(line, ": ")
			b.WriteString(headerKeyStyle.Render(k) + ": " + v + "\n")
		}
		return strings.TrimRight(b.String(), "\n")
	case t.jq != "" && t.jqErr == "":
		return t.jqOut
	}
	content := t.pretty
	if content == "" {
		content = mutedStyle.Render("(empty body)")
	}
	if t.result.Truncated {
		content += "\n" + errorStyle.Render(fmt.Sprintf("… truncated at %s", humanSize(maxBodySize)))
	}
	return content
}

// wrapped is a response rendered for the viewport at one width. Big bodies
// take hundreds of milliseconds to wrap and load, so recent renders are
// kept and reused while their source text and width stay the same.
type wrapped struct {
	in    string
	width int
	wrap  bool
	plain bool // ANSI stripped, for searching
	out   string

	lineStarts []int  // byte offset of each line in out (plain only)
	lower      string // lowercased out, made on first search (plain only)
}

type renderCache struct {
	recent []*wrapped // most recent first
	shown  string     // what the viewport currently holds
}

const renderCacheSize = 4

func (c *renderCache) get(in string, width int, wrap, plain bool) *wrapped {
	for i, w := range c.recent {
		// in == w.in is a pointer check when it's the same string, so
		// this is cheap even for large bodies.
		if w.width == width && w.wrap == wrap && w.plain == plain && w.in == in {
			copy(c.recent[1:i+1], c.recent[:i])
			c.recent[0] = w
			return w
		}
	}
	out := strings.ReplaceAll(in, "\t", "    ")
	if plain {
		out = ansi.Strip(out)
	}
	if wrap {
		out = wrapLines(out, width)
	}
	w := &wrapped{in: in, width: width, wrap: wrap, plain: plain, out: out}
	if plain {
		w.lineStarts = []int{0}
		for i := 0; i < len(out); i++ {
			if out[i] == '\n' {
				w.lineStarts = append(w.lineStarts, i+1)
			}
		}
	}
	c.recent = append([]*wrapped{w}, c.recent...)
	if len(c.recent) > renderCacheSize {
		c.recent = c.recent[:renderCacheSize]
	}
	return w
}

// wrapLines hard-wraps text to width, only running the (slow, ANSI-aware)
// wrapper on lines that are actually too wide. Most lines of pretty JSON
// aren't.
func wrapLines(s string, width int) string {
	var b strings.Builder
	b.Grow(len(s) + len(s)/50)
	for len(s) > 0 {
		line, rest, found := strings.Cut(s, "\n")
		if len(line) <= width || ansi.StringWidth(line) <= width {
			b.WriteString(line)
		} else {
			b.WriteString(ansi.Hardwrap(line, width, true))
		}
		if found {
			b.WriteByte('\n')
		}
		s = rest
	}
	return b.String()
}

func (m *model) refreshResponse() {
	w := m.rcache.get(m.responseText(), max(m.resp.Width, 1), m.wrap, m.find != "")
	if m.rcache.shown != w.out {
		m.resp.SetContent(w.out)
		m.rcache.shown = w.out
	}
	if m.find == "" {
		m.findMatches, m.findIn = nil, nil
		return
	}
	if m.findIn != w || m.findFor != m.find {
		m.findMatches = searchMatches(w, m.find)
		m.findIn, m.findFor = w, m.find
		m.findIdx = min(m.findIdx, max(len(m.findMatches)-1, 0))
	}
}

type findMatch struct {
	row, col int // visual row in the viewport content, display column
}

const maxFindMatches = 100_000

// searchMatches finds q case-insensitively in the plain rendered text.
// Matches that straddle a wrapped line break aren't found.
func searchMatches(w *wrapped, q string) []findMatch {
	if w.lower == "" {
		w.lower = strings.ToLower(w.out)
	}
	var offsets []int
	if len(w.lower) == len(w.out) {
		lq := strings.ToLower(q)
		for i := 0; len(offsets) < maxFindMatches; {
			j := strings.Index(w.lower[i:], lq)
			if j < 0 {
				break
			}
			offsets = append(offsets, i+j)
			i += j + max(len(lq), 1)
		}
	} else {
		// Lowercasing changed byte lengths (rare scripts); fall back to a
		// slower case-insensitive regexp on the original text.
		re := regexp.MustCompile("(?i)" + regexp.QuoteMeta(q))
		for _, loc := range re.FindAllStringIndex(w.out, maxFindMatches) {
			offsets = append(offsets, loc[0])
		}
	}

	// Offsets are sorted, so rows and columns can be computed in one pass.
	matches := make([]findMatch, 0, len(offsets))
	row, col, pos := 0, 0, 0
	for _, off := range offsets {
		for row+1 < len(w.lineStarts) && w.lineStarts[row+1] <= off {
			row++
			pos, col = w.lineStarts[row], 0
		}
		col += ansi.StringWidth(w.out[pos:off])
		pos = off
		matches = append(matches, findMatch{row: row, col: col})
	}
	return matches
}

// respContentView draws the response viewport, highlighting search
// matches on the visible lines only.
func (m model) respContentView() string {
	v := m.resp.View()
	if m.find == "" || len(m.findMatches) == 0 {
		return v
	}
	lq := strings.ToLower(m.find)
	cur := m.findMatches[m.findIdx]
	lines := strings.Split(v, "\n")
	for i, line := range lines {
		ll := strings.ToLower(line)
		if len(ll) != len(line) || !strings.Contains(ll, lq) {
			continue
		}
		row := m.resp.YOffset + i
		var b strings.Builder
		last := 0
		for {
			j := strings.Index(ll[last:], lq)
			if j < 0 {
				break
			}
			start, end := last+j, last+j+len(lq)
			style := matchStyle
			if row == cur.row && ansi.StringWidth(line[:start])+m.respXOff == cur.col {
				style = currentMatch
			}
			b.WriteString(line[last:start] + style.Render(line[start:end]))
			last = end
		}
		b.WriteString(line[last:])
		lines[i] = b.String()
	}
	return strings.Join(lines, "\n")
}

// revealMatch scrolls so the current match is on screen.
func (m *model) revealMatch() {
	if m.findIdx >= len(m.findMatches) {
		return
	}
	mt := m.findMatches[m.findIdx]
	if mt.row < m.resp.YOffset || mt.row >= m.resp.YOffset+m.resp.Height {
		m.resp.SetYOffset(max(mt.row-m.resp.Height/3, 0))
	}
	if !m.wrap {
		m.respXOff = max(mt.col-m.resp.Width/3, 0)
		m.resp.SetXOffset(m.respXOff)
	}
}

func (m *model) setFind(q string) {
	m.find = q
	m.findIdx = 0
	m.refreshResponse()
	m.revealMatch()
}

// stepMatch moves between matches; only the scroll position changes.
func (m *model) stepMatch(delta int) {
	if n := len(m.findMatches); n > 0 {
		m.findIdx = (m.findIdx + delta + n) % n
		m.revealMatch()
	}
}

func (m *model) clearFind() {
	m.find = ""
	m.findIdx = 0
	m.refreshResponse()
}

// jq --------------------------------------------------------------------------

// normalizeJSON converts json.Number values into the int/float/big.Int
// types gojq expects, keeping large integers exact.
func normalizeJSON(v any) any {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil && i == int64(int(i)) {
			return int(i)
		}
		if b, ok := new(big.Int).SetString(x.String(), 10); ok {
			return b
		}
		f, _ := x.Float64()
		return f
	case []any:
		for i := range x {
			x[i] = normalizeJSON(x[i])
		}
	case map[string]any:
		for k := range x {
			x[k] = normalizeJSON(x[k])
		}
	}
	return v
}

func parseJSONBody(body []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, errors.New("response isn't JSON")
	}
	return normalizeJSON(v), nil
}

// runJQ evaluates a jq filter against a JSON body and returns its outputs.
func runJQ(filter string, body []byte) ([]any, error) {
	if _, err := gojq.Parse(filter); err != nil {
		return nil, err // report syntax errors before parsing the body
	}
	input, err := parseJSONBody(body)
	if err != nil {
		return nil, err
	}
	return runJQOn(filter, input)
}

// runJQOn evaluates a jq filter against already-parsed JSON.
func runJQOn(filter string, input any) ([]any, error) {
	q, err := gojq.Parse(filter)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), jqTimeout)
	defer cancel()

	var out []any
	iter := q.RunWithContext(ctx, input)
	for {
		v, ok := iter.Next()
		if !ok {
			return out, nil
		}
		if err, isErr := v.(error); isErr {
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, errors.New("filter took too long")
			}
			return nil, err
		}
		out = append(out, v)
		if len(out) > 10000 {
			return nil, errors.New("too many results")
		}
	}
}

func marshalJQ(v any, indent bool) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// applyJQ runs the tab's filter and caches the rendered output.
func (t *tab) applyJQ() {
	t.jqOut, t.jqErr = "", ""
	if t.jq == "" || t.result == nil {
		t.jqInput, t.jqInputFor = nil, nil // don't hold a big parsed body
		return
	}
	// Parse the body once per response, not on every keystroke.
	if t.jqInputFor != t.result {
		t.jqInput, t.jqInputErr = parseJSONBody(t.result.Body)
		t.jqInputFor = t.result
	}
	if t.jqInputErr != nil {
		t.jqErr = t.jqInputErr.Error()
		return
	}
	outs, err := runJQOn(t.jq, t.jqInput)
	if err != nil {
		t.jqErr = err.Error()
		return
	}
	parts := make([]string, len(outs))
	size := 0
	for i, o := range outs {
		parts[i] = marshalJQ(o, true)
		if size += len(parts[i]); size > jqMaxOutput {
			t.jqErr = "result too large"
			return
		}
	}
	t.jqOut = highlightJSON(strings.Join(parts, "\n"))
	if len(outs) == 0 {
		t.jqOut = mutedStyle.Render("(no results)")
	}
}

func (m *model) setJQ(filter string) {
	t := m.cur()
	t.jq = strings.TrimSpace(filter)
	t.applyJQ()
	m.refreshResponse()
	m.resp.GotoTop()
}

// setVarFromResponse handles "name = filter": the first jq result becomes
// the variable's value (strings raw, anything else as compact JSON).
func (m *model) setVarFromResponse(spec string) {
	name, filter, ok := strings.Cut(spec, "=")
	name, filter = strings.TrimSpace(name), strings.TrimSpace(filter)
	if !ok || name == "" || filter == "" {
		m.notice = errorStyle.Render(`expected "name = jq filter", e.g. token = .access_token`)
		return
	}
	t := m.cur()
	if t.result == nil {
		return
	}
	outs, err := runJQ(filter, t.result.Body)
	if err == nil && len(outs) == 0 {
		err = errors.New("filter returned nothing")
	}
	if err != nil {
		m.notice = errorStyle.Render("jq: " + err.Error())
		return
	}
	val, isStr := outs[0].(string)
	if !isStr {
		val = marshalJQ(outs[0], false)
	}
	m.setVar(name, val)
	shown := val
	if len(shown) > 40 {
		shown = shown[:40] + "…"
	}
	m.flash(fmt.Sprintf("set {{%s}} = %s in “%s”", name, shown, m.ws.currentEnv().Name))
}

// Input bar -------------------------------------------------------------------

func (m *model) openBar(mode barMode) {
	in := textinput.New()
	in.Prompt = ""
	in.PlaceholderStyle = mutedStyle
	t := m.cur()
	if mode == barFind {
		in.Placeholder = "find in response"
		in.SetValue(m.find)
	} else {
		if t.viewing != nil {
			m.notice = errorStyle.Render("jq filters the latest response — press esc to leave this run first")
			return
		}
		in.Placeholder = ".data[0].id"
		in.SetValue(t.jq)
		if t.respTab != respTabBody {
			t.respTab = respTabBody
			m.refreshResponse()
		}
	}
	in.CursorEnd()
	in.Focus()
	m.bar = &respBar{mode: mode, input: in}
	m.setFocus(focusResponse)
}

func (m model) updateBar(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	b := m.bar
	switch msg.String() {
	case "esc":
		m.bar = nil
		if b.mode == barFind {
			m.clearFind()
		} else {
			m.setJQ("") // esc while editing the filter drops it
		}
		return m, nil
	case "enter":
		if b.mode == barFind {
			m.stepMatch(1)
			return m, nil
		}
		m.bar = nil // keep the filter applied
		return m, nil
	case "down", "ctrl+n":
		if b.mode == barFind {
			m.stepMatch(1)
		}
		return m, nil
	case "up", "ctrl+p":
		if b.mode == barFind {
			m.stepMatch(-1)
		}
		return m, nil
	case "pgup", "pgdown":
		var cmd tea.Cmd
		m.resp, cmd = m.resp.Update(msg)
		return m, cmd
	}
	before := b.input.Value()
	var cmd tea.Cmd
	b.input, cmd = b.input.Update(msg)
	if v := b.input.Value(); v != before {
		if b.mode == barFind {
			m.setFind(v)
		} else {
			m.setJQ(v)
		}
	}
	return m, cmd
}

// responseHeader is the response pane's first row: tabs and status, or the
// find/jq bar while it's open.
func (m model) responseHeader(width int) string {
	t := m.cur()
	if b := m.bar; b != nil {
		label, right := "find ", ""
		if b.mode == barFind {
			if m.find != "" {
				if n := len(m.findMatches); n > 0 {
					total := fmt.Sprint(n)
					if n >= maxFindMatches {
						total += "+"
					}
					right = mutedStyle.Render(fmt.Sprintf("%d/%s", m.findIdx+1, total))
				} else {
					right = errorStyle.Render("no matches")
				}
			}
		} else {
			label = "jq "
			if t.jqErr != "" {
				right = errorStyle.Render(ansi.Truncate(t.jqErr, width/2, "…"))
			}
		}
		b.input.Width = max(width-lipgloss.Width(label)-lipgloss.Width(right)-4, 5)
		left := lipgloss.NewStyle().Bold(true).Foreground(colorAccent).Render(label+"› ") + b.input.View()
		gap := max(width-lipgloss.Width(left)-lipgloss.Width(right), 1)
		return ansi.Truncate(left+strings.Repeat(" ", gap)+right, width, "") + "\n"
	}

	left := tabs(m.respTabNames(), m.activeRespTab())
	if t.jq != "" && t.respTab == respTabBody && t.viewing == nil {
		f := "jq " + ansi.Truncate(t.jq, 24, "…")
		if t.jqErr != "" {
			left += "  " + errorStyle.Render(f+" ✗")
		} else {
			left += "  " + lipgloss.NewStyle().Foreground(colorAccent).Render(f)
		}
	}
	var right string
	switch {
	case t.viewing != nil:
		right = m.viewingStatus()
	case t.loading:
		right = m.spinner.View() + mutedStyle.Render(" sending… (esc to cancel)")
	case t.result != nil && t.err == nil:
		r := t.result
		status := lipgloss.NewStyle().Bold(true).Foreground(statusColor(r.StatusCode)).Render(r.Status)
		right = status + mutedStyle.Render(fmt.Sprintf("  %s  %s", r.Duration.Round(time.Millisecond), humanSize(len(r.Body))))
	}
	gap := max(width-lipgloss.Width(left)-lipgloss.Width(right), 1)
	return ansi.Truncate(left+strings.Repeat(" ", gap)+right, width, "") + "\n"
}
