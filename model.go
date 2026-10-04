package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type focus int

const (
	focusMethod focus = iota
	focusURL
	focusHeaders
	focusBody
	focusResponse
	focusCount
)

const (
	respTabBody = iota
	respTabHeaders
)

// stackBelow is the terminal width under which panes are stacked vertically.
const stackBelow = 90

type model struct {
	width, height int
	focus         focus

	methodIdx int
	url       textinput.Model
	headers   headerEditor
	body      textarea.Model
	reqTab    focus // focusHeaders or focusBody: which editor is shown

	resp    viewport.Model
	respTab int
	result  *response
	pretty  string // rendered (possibly highlighted) response body
	err     error
	loading bool
	cancel  context.CancelFunc
	spinner spinner.Model

	notice string // one-off message shown in the help bar until the next key
}

func newEditor(placeholder string, lineNumbers bool) textarea.Model {
	ta := textarea.New()
	ta.Placeholder = placeholder
	ta.ShowLineNumbers = lineNumbers
	ta.CharLimit = 0
	ta.MaxHeight = 0
	ta.Prompt = ""
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.FocusedStyle.Placeholder = mutedStyle
	ta.BlurredStyle.Placeholder = mutedStyle
	ta.Blur()
	return ta
}

// bodyMaxLines caps the body editor's line count. The textarea sizes its
// line-number gutter from MaxHeight, so a fixed cap keeps the gutter width
// stable instead of growing once the body reaches 10 lines.
const bodyMaxLines = 9999

// lineNumberGutter is the width of the body editor's line-number column:
// the digits plus a space on each side.
var lineNumberGutter = len(strconv.Itoa(bodyMaxLines)) + 2

func newBodyEditor() textarea.Model {
	ta := newEditor(`{"hello": "world"}`, true)
	ta.MaxHeight = bodyMaxLines
	return ta
}

func newModel() model {
	url := textinput.New()
	url.Placeholder = "https://httpbin.org/get  (or paste a curl command)"
	url.Prompt = ""
	url.CharLimit = 0
	url.PlaceholderStyle = mutedStyle
	url.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colorAccent)

	return model{
		focus:   focusURL,
		url:     url,
		headers: newHeaderEditor(),
		body:    newBodyEditor(),
		reqTab:  focusHeaders,
		resp:    viewport.New(0, 0),
		spinner: sp,
	}
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

func (m model) method() string { return methods[m.methodIdx] }

func (m *model) setFocus(f focus) {
	m.focus = f
	m.url.Blur()
	m.headers.Blur()
	m.body.Blur()
	switch f {
	case focusURL:
		m.url.Focus()
	case focusHeaders:
		m.reqTab = focusHeaders
		m.headers.Focus()
	case focusBody:
		m.reqTab = focusBody
		m.body.Focus()
	}
}

func (m *model) send() tea.Cmd {
	if m.loading || strings.TrimSpace(m.url.Value()) == "" {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.loading = true
	m.err = nil
	return tea.Batch(
		m.spinner.Tick,
		sendRequest(ctx, m.method(), m.url.Value(), m.headers.Header(), m.body.Value()),
	)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case responseMsg:
		m.loading = false
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		m.err = msg.err
		if msg.err == nil {
			m.result = msg.resp
			body, isJSON := prettyBody(msg.resp)
			if isJSON {
				body = highlightJSON(body)
			}
			m.pretty = body
			m.resp.GotoTop()
		}
		m.refreshResponse()
		return m, nil

	case spinner.TickMsg:
		if !m.loading {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.KeyMsg:
		m.notice = ""
		if msg.Paste && looksLikeCurl(string(msg.Runes)) {
			m.importCurl(string(msg.Runes))
			return m, nil
		}
		if m.focus == focusHeaders {
			switch msg.String() {
			case "tab":
				if m.headers.AcceptSuggestion() {
					return m, nil
				}
			case "esc":
				if !m.loading && m.headers.CloseSuggestions() {
					return m, nil
				}
			}
		}
		switch msg.String() {
		case "ctrl+c":
			if m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit
		case "ctrl+s", "alt+enter":
			return m, m.send()
		case "esc":
			if m.loading && m.cancel != nil {
				m.cancel()
			}
			return m, nil
		case "tab":
			m.setFocus((m.focus + 1) % focusCount)
			return m, nil
		case "shift+tab":
			m.setFocus((m.focus + focusCount - 1) % focusCount)
			return m, nil
		}
		return m.updateFocused(msg)
	}

	// Anything else (cursor blinks, clipboard pastes) goes to the focused input.
	var cmd tea.Cmd
	switch m.focus {
	case focusURL:
		m.url, cmd = m.url.Update(msg)
	case focusHeaders:
		m.headers, cmd = m.headers.Update(msg)
	case focusBody:
		m.body, cmd = m.body.Update(msg)
	}
	return m, cmd
}

func (m model) updateFocused(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.focus {
	case focusMethod:
		switch msg.String() {
		case "right", "down", "l", "j", " ":
			m.methodIdx = (m.methodIdx + 1) % len(methods)
		case "left", "up", "h", "k":
			m.methodIdx = (m.methodIdx + len(methods) - 1) % len(methods)
		case "enter":
			return m, m.send()
		default:
			// Jump to a method by its first letter (p cycles POST/PUT/PATCH).
			if s := strings.ToUpper(msg.String()); len(s) == 1 {
				for i := 1; i <= len(methods); i++ {
					j := (m.methodIdx + i) % len(methods)
					if strings.HasPrefix(methods[j], s) {
						m.methodIdx = j
						break
					}
				}
			}
		}
	case focusURL:
		if msg.String() == "enter" {
			if looksLikeCurl(m.url.Value()) {
				m.importCurl(m.url.Value())
				return m, nil
			}
			return m, m.send()
		}
		m.url, cmd = m.url.Update(msg)
		// Terminals without bracketed paste deliver a paste as one batch of
		// runes; import as soon as a whole curl command lands in the field.
		if len(msg.Runes) > 1 && looksLikeCurl(m.url.Value()) {
			m.importCurl(m.url.Value())
		}
	case focusHeaders:
		m.headers, cmd = m.headers.Update(msg)
	case focusBody:
		m.body, cmd = m.body.Update(msg)
	case focusResponse:
		switch msg.String() {
		case "left", "right", "t":
			m.respTab = 1 - m.respTab
			m.resp.GotoTop()
			m.refreshResponse()
		default:
			m.resp, cmd = m.resp.Update(msg)
		}
	}
	return m, cmd
}

// Layout ------------------------------------------------------------------

const methodWidth = 9 // "◀ PATCH ▶"

func (m model) stacked() bool { return m.width < stackBelow }

// paneSizes returns outer width/height of the request and response panes.
func (m model) paneSizes() (reqW, reqH, respW, respH int) {
	avail := max(m.height-1-3-1, 6) // title, url bar, help
	if m.stacked() {
		reqH = avail / 2
		return m.width, reqH, m.width, avail - reqH
	}
	reqW = m.width / 2
	return reqW, avail, m.width - reqW, avail
}

func (m *model) layout() {
	m.url.Width = max(m.width-4-methodWidth-2, 10)

	reqW, reqH, respW, respH := m.paneSizes()
	edW, edH := max(reqW-4, 1), max(reqH-4, 1)
	m.headers.SetSize(edW, edH)
	// SetWidth reserves 4 columns for line numbers; the gutter is wider.
	m.body.SetWidth(edW - (lineNumberGutter - 4))
	m.body.SetHeight(edH)

	m.resp.Width = max(respW-4, 1)
	m.resp.Height = max(respH-4, 1)
	m.refreshResponse()
}

func (m *model) refreshResponse() {
	var content string
	switch {
	case m.err != nil:
		content = errorStyle.Render("Error: " + m.err.Error())
	case m.result == nil:
		content = mutedStyle.Render("Enter a URL and press enter or ctrl+s to send.")
	case m.respTab == respTabHeaders:
		keys := make([]string, 0, len(m.result.Headers))
		for k := range m.result.Headers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, k := range keys {
			for _, v := range m.result.Headers[k] {
				fmt.Fprintf(&b, "%s: %s\n", headerKeyStyle.Render(k), v)
			}
		}
		content = strings.TrimRight(b.String(), "\n")
	default:
		content = m.pretty
		if content == "" {
			content = mutedStyle.Render("(empty body)")
		}
		if m.result.Truncated {
			content += "\n" + errorStyle.Render(fmt.Sprintf("… truncated at %s", humanSize(maxBodySize)))
		}
	}
	content = strings.ReplaceAll(content, "\t", "    ")
	m.resp.SetContent(ansi.Hardwrap(content, max(m.resp.Width, 1), true))
}

// View --------------------------------------------------------------------

func (m model) View() string {
	if m.width == 0 {
		return ""
	}
	reqW, reqH, respW, respH := m.paneSizes()

	title := titleStyle.Render("⚡ barq") + mutedStyle.Render("  api client")

	req := m.pane(m.focus == focusHeaders || m.focus == focusBody, reqW, reqH,
		m.requestTabs(), m.activeEditorView())
	resp := m.pane(m.focus == focusResponse, respW, respH,
		m.responseHeader(respW-4), m.resp.View())

	var panes string
	if m.stacked() {
		panes = lipgloss.JoinVertical(lipgloss.Left, req, resp)
	} else {
		panes = lipgloss.JoinHorizontal(lipgloss.Top, req, resp)
	}

	return lipgloss.JoinVertical(lipgloss.Left, title, m.urlBar(), panes, m.help())
}

func (m model) activeEditorView() string {
	if m.reqTab == focusBody {
		return m.body.View()
	}
	return m.headers.View()
}

func (m model) pane(focused bool, w, h int, header, content string) string {
	style := paneStyle
	if focused {
		style = focusedPaneStyle
	}
	inner := lipgloss.JoinVertical(lipgloss.Left, header, content)
	return style.Width(w - 2).Height(h - 2).MaxHeight(h).Render(inner)
}

func (m model) urlBar() string {
	method := m.method()
	label := method
	ms := lipgloss.NewStyle().Bold(true).Foreground(methodColor(method))
	if m.focus == focusMethod {
		label = "◀ " + method + " ▶"
		ms = ms.Reverse(true)
	}
	badge := lipgloss.NewStyle().Width(methodWidth).Align(lipgloss.Center).Render(ms.Render(label))

	style := paneStyle
	if m.focus == focusMethod || m.focus == focusURL {
		style = focusedPaneStyle
	}
	return style.Width(m.width - 2).Render(badge + "  " + m.url.View())
}

func tabs(names []string, active int) string {
	out := make([]string, len(names))
	for i, n := range names {
		if i == active {
			out[i] = activeTabStyle.Render(n)
		} else {
			out[i] = tabStyle.Render(n)
		}
	}
	return strings.Join(out, mutedStyle.Render("  │  "))
}

func (m model) requestTabNames() []string {
	headers := "Headers"
	if n := m.headers.EnabledCount(); n > 0 {
		headers = fmt.Sprintf("Headers (%d)", n)
	}
	return []string{headers, "Body"}
}

func (m model) requestTabs() string {
	active := 0
	if m.reqTab == focusBody {
		active = 1
	}
	return tabs(m.requestTabNames(), active) + "\n"
}

func (m model) responseHeader(width int) string {
	left := tabs([]string{"Body", "Headers"}, m.respTab)
	var right string
	switch {
	case m.loading:
		right = m.spinner.View() + mutedStyle.Render(" sending… (esc to cancel)")
	case m.result != nil && m.err == nil:
		r := m.result
		status := lipgloss.NewStyle().Bold(true).Foreground(statusColor(r.StatusCode)).Render(r.Status)
		right = status + mutedStyle.Render(fmt.Sprintf("  %s  %s", r.Duration.Round(1e6), humanSize(len(r.Body))))
	}
	gap := max(width-lipgloss.Width(left)-lipgloss.Width(right), 1)
	return left + strings.Repeat(" ", gap) + right + "\n"
}

// importCurl replaces the current request with one parsed from a curl
// command.
func (m *model) importCurl(cmd string) {
	req, warnings, err := parseCurl(cmd)
	if err != nil {
		m.notice = errorStyle.Render("curl import failed: " + err.Error())
		return
	}
	idx := -1
	for i, mt := range methods {
		if mt == req.Method {
			idx = i
		}
	}
	if idx < 0 {
		methods = append(methods, req.Method)
		idx = len(methods) - 1
	}
	m.methodIdx = idx
	m.url.SetValue(req.URL)
	m.url.CursorEnd()
	m.headers.SetRows(req.Headers)
	m.body.SetValue(req.Body)
	m.setFocus(focusURL)
	if req.Body != "" && len(req.Headers) == 0 {
		m.reqTab = focusBody
	}

	msg := fmt.Sprintf("imported curl: %s, %d header(s)", req.Method, len(req.Headers))
	if req.Body != "" {
		msg += ", body"
	}
	m.notice = lipgloss.NewStyle().Foreground(colorGreen).Render("✓ " + msg + " — press enter to send")
	if len(warnings) > 0 {
		m.notice += errorStyle.Render("  ⚠ " + strings.Join(warnings, "; "))
	}
}

func (m model) help() string {
	if m.notice != "" {
		return " " + m.notice
	}
	var keys string
	switch m.focus {
	case focusMethod:
		keys = "←/→ change method • g/p/d… jump"
	case focusURL:
		keys = "enter send"
	case focusHeaders:
		keys = "enter next cell • ↑/↓ row/suggestion • tab complete • ctrl+t toggle • ctrl+d delete • paste Key: Value lines"
	case focusBody:
		keys = "ctrl+s send"
	case focusResponse:
		keys = fmt.Sprintf("↑/↓ scroll • t switch tab • %3.0f%%", m.resp.ScrollPercent()*100)
	}
	return mutedStyle.Render(" " + keys + " • tab/shift+tab focus • ctrl+s send • ctrl+c quit")
}
