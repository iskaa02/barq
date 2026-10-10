package tui

import (
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/iskaa02/barq/internal/core"
)

// Editing in $EDITOR works like ctrl+x ctrl+e in a shell: the focused field
// is written to a temp file, the editor takes over the terminal, and the
// file is read back when it exits.

type editTarget int

const (
	editBody editTarget = iota
	editURL
	editHeaders
	editRequest  // the whole request as one .http file
	editResponse // read-only: opened for viewing, changes are ignored
)

type editorDoneMsg struct {
	target editTarget
	tabUID int
	path   string
	err    error
}

// editorCommand resolves the editor the way shells do: $VISUAL, then
// $EDITOR, then vi. Values may include arguments, e.g. "code --wait".
func editorCommand() []string {
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			if args, err := core.ShellSplit(v); err == nil && len(args) > 0 {
				return args
			}
		}
	}
	return []string{"vi"}
}

func (m Model) editTargetForFocus() editTarget {
	switch m.focus {
	case focusURL, focusParams:
		return editURL
	case focusHeaders:
		return editHeaders
	case focusResponse:
		return editResponse
	}
	return editBody
}

const headersHelp = "## One header per line as Key: Value. Start a line with # to disable it.\n" +
	"## Lines starting with ## are ignored.\n"

const formHelp = "## One form field per line as name: value. A value starting with @ is a file,\n" +
	"## relative to the project directory or absolute. Start a line with # to disable it.\n"

const requestHelp = "## The first line is METHOD URL, then one header per line as Key: Value\n" +
	"## (start a line with # to disable it), then a blank line, then the body.\n" +
	"## Lines starting with ## above the body are ignored.\n" +
	"## \"@capture name = jq-filter\" lines among the headers store part of each\n" +
	"## successful response in an environment variable (saved requests only).\n"

const requestFormHelp = "## The body is form-data: one field per line as name: value. A value\n" +
	"## starting with @ is a file. Start a line with # to disable it.\n"

// httpRequest is a request in the .http layout edited with ctrl+o.
type httpRequest struct {
	Method, URL string
	Headers     []core.HeaderRow
	Body        string
	Captures    []core.Capture
	// CaptureErr is set when an @capture line doesn't parse; Captures is
	// then incomplete and shouldn't replace the saved ones.
	CaptureErr error
}

const captureDirective = "@capture"

func requestToHTTP(r httpRequest, form bool) string {
	var b strings.Builder
	b.WriteString(requestHelp)
	if form {
		b.WriteString(requestFormHelp)
	}
	b.WriteString(r.Method + " " + r.URL + "\n")
	b.WriteString(strings.TrimPrefix(headersToText(r.Headers), headersHelp))
	for _, c := range r.Captures {
		b.WriteString(captureDirective + " " + c.String() + "\n")
	}
	b.WriteString("\n" + r.Body)
	return b.String()
}

// httpToRequest reads back what requestToHTTP wrote. The headers end at the
// first blank line and everything after it is the body, kept as is. A
// request line without a method keeps the current one, reported as "".
func httpToRequest(text string) (httpRequest, bool) {
	var r httpRequest
	lines := strings.SplitAfter(text, "\n")
	i := 0
	for ; i < len(lines); i++ {
		if l := strings.TrimSpace(lines[i]); l != "" && !strings.HasPrefix(l, "##") {
			break
		}
	}
	if i == len(lines) {
		return r, false
	}
	switch f := strings.Fields(lines[i]); {
	case len(f) == 1 && slices.Contains(core.Methods, strings.ToUpper(f[0])):
		r.Method = strings.ToUpper(f[0]) // no URL yet
	case len(f) == 1:
		r.URL = f[0]
	default:
		r.Method, r.URL = strings.ToUpper(f[0]), strings.Join(f[1:], " ")
	}
	i++
	start := i
	for ; i < len(lines) && strings.TrimSpace(lines[i]) != ""; i++ {
	}
	var headers strings.Builder
	for _, l := range lines[start:i] {
		spec, ok := strings.CutPrefix(strings.TrimSpace(l), captureDirective)
		if !ok {
			headers.WriteString(l)
			continue
		}
		c, err := core.ParseCapture(spec)
		if err != nil {
			r.CaptureErr = err
			continue
		}
		// A later line for the same variable replaces the earlier one.
		r.Captures = slices.DeleteFunc(r.Captures, func(x core.Capture) bool { return x.Var == c.Var })
		r.Captures = append(r.Captures, c)
	}
	r.Headers = textToHeaders(headers.String())
	if i < len(lines) {
		r.Body = strings.TrimSuffix(strings.Join(lines[i+1:], ""), "\n")
	}
	return r, true
}

func headersToText(rows []core.HeaderRow) string { return kvToText(headersHelp, rows) }

func kvToText(help string, rows []core.HeaderRow) string {
	var b strings.Builder
	b.WriteString(help)
	for _, r := range rows {
		if !r.Enabled {
			b.WriteString("# ")
		}
		b.WriteString(r.Key + ": " + r.Value + "\n")
	}
	return b.String()
}

func textToHeaders(text string) []core.HeaderRow {
	var rows []core.HeaderRow
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "##") {
			continue
		}
		enabled := true
		if rest, ok := strings.CutPrefix(line, "#"); ok {
			enabled, line = false, strings.TrimSpace(rest)
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) != "" {
			rows = append(rows, core.HeaderRow{Key: strings.TrimSpace(k), Value: strings.TrimSpace(v), Enabled: enabled})
		}
	}
	return rows
}

// openEditor starts $EDITOR on the given part of the current tab.
func (m *Model) openEditor(target editTarget) tea.Cmd {
	t := m.cur()
	var content, ext string
	switch target {
	case editURL:
		content, ext = m.url.Value()+"\n", ".txt"
	case editHeaders:
		content, ext = headersToText(m.headers.Rows()), ".txt"
	case editRequest:
		r := httpRequest{Method: m.method(), URL: m.url.Value(), Headers: m.headers.Rows(), Body: m.body.Value()}
		if j := m.ws.Find(t.savedID); t.savedID != "" && j >= 0 {
			r.Captures = m.ws.Requests[j].Captures
		}
		form := m.bodyMode == core.BodyForm
		if form {
			r.Body = strings.TrimPrefix(kvToText(formHelp, m.form.Rows()), formHelp)
		}
		content, ext = requestToHTTP(r, form), ".http"
	case editResponse:
		if t.result == nil {
			m.notice = errorStyle.Render("no response to open yet")
			return nil
		}
		content, ext = ansi.Strip(m.responseText()), defaultExt(t.result)
	case editBody:
		if m.bodyMode == core.BodyForm {
			content, ext = kvToText(formHelp, m.form.Rows()), ".txt"
			break
		}
		fallthrough
	default:
		content, ext = m.body.Value(), ".txt"
		if json.Valid([]byte(strings.TrimSpace(content))) {
			ext = ".json"
		}
	}

	f, err := os.CreateTemp("", "barq-*"+ext)
	if err == nil {
		_, err = f.WriteString(content)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		m.notice = errorStyle.Render("couldn't create temp file: " + err.Error())
		return nil
	}

	args := editorCommand()
	cmd := exec.Command(args[0], append(args[1:], f.Name())...)
	uid, path := t.uid, f.Name()
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return editorDoneMsg{target: target, tabUID: uid, path: path, err: err}
	})
}

func defaultExt(r *core.Response) string {
	name := defaultResponseFile(r)
	return name[strings.LastIndexByte(name, '.'):]
}

func (m *Model) editorDone(msg editorDoneMsg) {
	defer os.Remove(msg.path)
	if msg.err != nil {
		m.notice = errorStyle.Render("editor failed: " + msg.err.Error() +
			" — set $EDITOR (e.g. export EDITOR=nvim)")
		return
	}
	if msg.target == editResponse || m.tabIndex(msg.tabUID) != m.active {
		return
	}
	data, err := os.ReadFile(msg.path)
	if err != nil {
		m.notice = errorStyle.Render("couldn't read back the edit: " + err.Error())
		return
	}
	text := string(data)

	switch msg.target {
	case editURL:
		u := strings.TrimSpace(text)
		if core.LooksLikeCurl(u) {
			m.ImportCurl(u)
			return
		}
		// A URL is one line; take the first non-empty one.
		first, _, _ := strings.Cut(u, "\n")
		m.url.SetValue(strings.TrimSpace(first))
		m.url.CursorEnd()
	case editHeaders:
		m.headers.SetRows(textToHeaders(text))
	case editRequest:
		r, ok := httpToRequest(text)
		if !ok {
			m.notice = errorStyle.Render("no request line in the file; nothing changed")
			return
		}
		if r.Method != "" {
			m.methodIdx = methodIndex(r.Method)
		}
		m.url.SetValue(r.URL)
		m.url.CursorEnd()
		m.syncParamsFromURL()
		m.headers.SetRows(r.Headers)
		if m.bodyMode == core.BodyForm {
			m.form.SetRows(textToHeaders(r.Body))
		} else {
			m.body.SetValue(r.Body)
		}
		if !m.applyCaptures(r) {
			m.Persist()
			return
		}
	case editBody:
		if m.bodyMode == core.BodyForm {
			m.form.SetRows(textToHeaders(text))
			break
		}
		m.body.SetValue(strings.TrimSuffix(text, "\n"))
	default:
		// Editors usually end files with a newline the body didn't have.
		m.body.SetValue(strings.TrimSuffix(text, "\n"))
	}
	m.Persist()
	m.flash("updated from editor")
}

// applyCaptures replaces the current saved request's captures with the
// ones from an edited .http file. It reports false, with a notice, when
// they couldn't be applied.
func (m *Model) applyCaptures(r httpRequest) bool {
	if r.CaptureErr != nil {
		m.notice = errorStyle.Render("captures unchanged: " + r.CaptureErr.Error())
		return false
	}
	id := m.cur().savedID
	j := m.ws.Find(id)
	if id == "" || j < 0 {
		if len(r.Captures) > 0 {
			m.notice = errorStyle.Render("captures ignored: save the request first (ctrl+s)")
			return false
		}
		return true
	}
	if slices.Equal(m.ws.Requests[j].Captures, r.Captures) {
		return true
	}
	return m.mutate(func(w *core.Workspace) error {
		return w.UpdateRequest(id, func(req *core.Request) { req.Captures = r.Captures })
	})
}
