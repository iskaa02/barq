package tui

import (
	"encoding/json"
	"os"
	"os/exec"
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
