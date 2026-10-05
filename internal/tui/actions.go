package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/iskaa02/barq/internal/core"
)

// rootItems lists everything the palette can do: commands first, then open
// tabs, saved requests and folders to jump to.
func (m Model) rootItems() []paletteItem {
	t := m.cur()
	saved := t.savedID != "" && m.ws.Find(t.savedID) >= 0
	hasResp := t.result != nil && t.err == nil

	cmd := func(category, title, hint string, run func(m *Model) tea.Cmd) paletteItem {
		return paletteItem{key: "cmd:" + category + ":" + title, category: category, title: title, hint: hint, run: run}
	}
	do := func(f func(m *Model)) func(m *Model) tea.Cmd {
		return func(m *Model) tea.Cmd { f(m); return nil }
	}

	var items []paletteItem
	add := func(when bool, it paletteItem) {
		if when {
			items = append(items, it)
		}
	}

	// Request
	add(!t.loading, cmd("Request", "Send", "ctrl+r", func(m *Model) tea.Cmd { return m.send() }))
	add(t.loading, cmd("Request", "Cancel running request", "esc", do((*Model).cancelRequest)))
	add(true, paletteItem{key: "cmd:method", category: "Request", title: "Set method…", hint: m.method(),
		next: &paletteStep{title: "Set method", placeholder: "Method…", items: methodItems}})
	add(true, cmd("Request", "Import curl from clipboard", "", do((*Model).importCurlFromClipboard)))
	add(true, cmd("Request", "Copy as curl", "", do((*Model).copyAsCurl)))
	add(m.url.Value() != "", cmd("Request", "Copy URL", "", do(func(m *Model) {
		m.flashCopy(copyText(m.url.Value(), "URL"))
	})))
	add(len(m.headers.Rows()) > 0, cmd("Request", "Clear headers", "", do(func(m *Model) {
		m.headers.SetRows(nil)
	})))
	form := m.bodyMode == core.BodyForm
	add(!form && m.body.Value() != "", cmd("Request", "Clear body", "", do(func(m *Model) {
		m.body.SetValue("")
	})))
	add(form && len(m.form.Rows()) > 0, cmd("Request", "Clear form fields", "", do(func(m *Model) {
		m.form.SetRows(nil)
	})))
	add(!form && m.body.Value() != "", cmd("Request", "Format JSON body", "", do((*Model).formatBody)))
	bodySwitch := "Body: use form-data (multipart)"
	if form {
		bodySwitch = "Body: use raw"
	}
	add(true, paletteItem{key: "cmd:body-mode", category: "Request", title: bodySwitch, hint: "alt+m",
		run: func(m *Model) tea.Cmd { m.toggleBodyMode(); return nil }})
	add(true, cmd("Request", "Edit body in $EDITOR", "ctrl+x ctrl+e", func(m *Model) tea.Cmd { return m.openEditor(editBody) }))
	add(true, cmd("Request", "Edit URL in $EDITOR", "", func(m *Model) tea.Cmd { return m.openEditor(editURL) }))
	add(true, cmd("Request", "Edit headers in $EDITOR", "", func(m *Model) tea.Cmd { return m.openEditor(editHeaders) }))

	// Response
	add(hasResp, cmd("Response", "Copy body", "", do(func(m *Model) {
		if t := m.cur(); t.jq != "" && t.jqErr == "" {
			m.flashCopy(copyText(ansi.Strip(t.jqOut), "filtered body"))
			return
		}
		m.flashCopy(copyText(string(m.cur().result.Body), "response body"))
	})))
	add(hasResp, cmd("Response", "Find…", "/", do(func(m *Model) { m.openBar(barFind) })))
	add(hasResp, cmd("Response", "Filter with jq…", "|", do(func(m *Model) { m.openBar(barJQ) })))
	add(t.jq != "", cmd("Response", "Clear jq filter", "", do(func(m *Model) { m.setJQ("") })))
	add(hasResp && m.ws.CurrentEnv() != nil, cmd("Response", "Set variable from response…", "", do(func(m *Model) {
		m.ask(promptSetVar, "Set variable (name = jq filter):", "", 0)
		m.prompt.input.Placeholder = "token = .access_token"
	})))
	add(hasResp, cmd("Response", "Open in $EDITOR", "", func(m *Model) tea.Cmd { return m.openEditor(editResponse) }))
	add(hasResp, cmd("Response", "Copy headers", "", do(func(m *Model) {
		m.flashCopy(copyText(formatHeaders(m.cur().result), "response headers"))
	})))
	add(hasResp, cmd("Response", "Save body to file…", "", do(func(m *Model) {
		m.ask(promptSaveResponse, "Save response to:", defaultResponseFile(m.cur().result), 0)
	})))
	add(true, cmd("Response", "Toggle line wrap", onOff(m.wrap), do(func(m *Model) {
		m.wrap = !m.wrap
		m.refreshResponse()
		m.resp.SetXOffset(0)
		m.respXOff = 0
	})))
	add(true, cmd("Response", "Show body", "", do(func(m *Model) { m.showResponse(respTabBody) })))
	add(true, cmd("Response", "Show headers", "", do(func(m *Model) { m.showResponse(respTabHeaders) })))

	// Tabs
	add(true, cmd("Tab", "New tab", "ctrl+n", do(func(m *Model) { m.openTab(core.Request{Method: "GET"}, "") })))
	add(true, cmd("Tab", "Close tab", "ctrl+w", do(func(m *Model) { m.closeTab(m.active, false) })))
	add(len(m.tabs) > 1, cmd("Tab", "Close other tabs", "", do((*Model).closeOtherTabs)))
	add(true, cmd("Tab", "Close tabs without unsaved changes", "", do((*Model).closeCleanTabs)))
	add(len(m.tabs) > 1, cmd("Tab", "Next tab", "alt+→", do(func(m *Model) {
		m.switchTab((m.active + 1) % len(m.tabs))
	})))
	add(len(m.tabs) > 1, cmd("Tab", "Previous tab", "alt+←", do(func(m *Model) {
		m.switchTab((m.active + len(m.tabs) - 1) % len(m.tabs))
	})))
	add(true, cmd("Tab", "Duplicate tab", "", do((*Model).duplicateTab)))
	add(true, cmd("Tab", "Rename tab…", "f2", do((*Model).renameCurrent)))

	// Saved requests and folders
	add(true, cmd("Saved", "Save", "ctrl+s", do((*Model).saveCurrent)))
	add(saved, cmd("Saved", "Save as new…", "", do((*Model).saveAsNew)))
	add(saved && m.dirty(m.active), cmd("Saved", "Revert to saved", "", do((*Model).revertToSaved)))
	add(true, cmd("Saved", "Import OpenAPI spec…", "", do(func(m *Model) {
		m.ask(promptImportSpec, "Import OpenAPI spec (file or URL):", "", 0)
		m.prompt.input.Placeholder = "openapi.json"
	})))
	add(true, cmd("Saved", "New folder…", "", do(func(m *Model) {
		m.ask(promptNewFolder, "New folder:", "", 0)
		m.prompt.id = ""
	})))
	add(saved, paletteItem{key: "cmd:move", category: "Saved", title: "Move to folder…",
		next: &paletteStep{title: "Move “" + m.tabName(m.active) + "” to", placeholder: "Folder…", items: folderItems}})
	if saved {
		n := len(m.ws.Requests[m.ws.Find(t.savedID)].Captures)
		add(m.ws.CurrentEnv() != nil, cmd("Saved", "Add capture… (store part of each response in a variable)", fmt.Sprintf("%d set", n), do(func(m *Model) {
			m.ask(promptAddCapture, "Capture (name = jq filter):", "", 0)
			m.prompt.input.Placeholder = "token = .data.accessToken"
		})))
		add(n > 0, paletteItem{key: "cmd:captures", category: "Saved", title: "Remove capture…",
			next: &paletteStep{title: "Remove a capture", placeholder: "Capture…", items: captureItems}})
	}
	add(saved, cmd("Saved", "Delete saved request", "", do(func(m *Model) {
		m.ask(promptDelete, fmt.Sprintf("Delete “%s”?", m.tabName(m.active)), "", 0)
	})))
	add(len(m.ws.Folders) > 0, cmd("Saved", "Collapse all folders", "", do(func(m *Model) { m.setAllCollapsed(true) })))
	add(len(m.ws.Folders) > 0, cmd("Saved", "Expand all folders", "", do(func(m *Model) { m.setAllCollapsed(false) })))

	items = append(items, m.envCommands()...)

	// History
	add(true, paletteItem{key: "cmd:history-all", category: "History", title: "Browse all runs…",
		hint: fmt.Sprintf("%d", len(m.hist.Index)),
		next: &paletteStep{title: "All runs", placeholder: "Search by name, status, env…", items: allRunItems}})
	add(true, cmd("History", "Show runs of this request", "", do(func(m *Model) {
		m.cur().viewing = nil
		m.showResponse(respTabHistory)
		m.revealHistSel()
	})))
	add(t.viewing != nil, cmd("History", "Restore request from this run", "r", do(func(m *Model) {
		m.restoreRun(m.cur().viewing)
	})))
	add(t.viewing != nil, cmd("History", "Back to run list", "esc", do((*Model).closeRun)))
	add(len(m.runs()) > 0, cmd("History", "Clear runs of this request", "", do(func(m *Model) {
		m.ask(promptClearHistory, fmt.Sprintf("Delete all %d run(s) of “%s”?", len(m.runs()), m.tabName(m.active)), "", 0)
	})))

	// App
	add(true, cmd("View", "Toggle sidebar", "ctrl+b", do(func(m *Model) {
		m.showSidebar = !m.showSidebar
		if !m.showSidebar {
			m.cancelMoving()
			if m.focus == focusSidebar {
				m.setFocus(focusURL)
			}
		}
		m.layout()
	})))
	for _, f := range []struct {
		name string
		f    focus
	}{{"URL", focusURL}, {"params", focusParams}, {"headers", focusHeaders}, {"body", focusBody}, {"response", focusResponse}} {
		add(true, cmd("View", "Focus "+f.name, paneKeyLabel(f.f), do(func(m *Model) { m.jumpTo(f.f) })))
	}
	add(true, paletteItem{key: "cmd:keys", category: "Help", title: "Keyboard shortcuts",
		next: &paletteStep{title: "Keyboard shortcuts", placeholder: "Search shortcuts…", items: shortcutItems}})
	add(true, cmd("App", "Show workspace file location", "", do(func(m *Model) {
		if msg := copyText(m.ws.Path, "path"); msg != "" {
			m.flash(m.ws.Path + "  (" + msg + ")")
		} else {
			m.flash(m.ws.Path)
		}
	})))
	add(true, cmd("App", "Quit", "ctrl+c", func(m *Model) tea.Cmd { return m.quit() }))

	// Open tabs
	for i, tb := range m.tabs {
		if i == m.active {
			continue
		}
		uid := tb.uid
		r := m.tabRequest(i)
		items = append(items, paletteItem{category: "Tab", title: m.tabName(i), method: r.Method, hint: "open tab",
			run: func(m *Model) tea.Cmd {
				if j := m.tabIndex(uid); j >= 0 {
					m.switchTab(j)
				}
				return nil
			}})
	}

	// Saved requests
	for _, r := range m.ws.Requests {
		id := r.ID
		items = append(items, paletteItem{key: "req:" + id, title: r.DisplayName(), method: r.Method,
			hint: m.ws.FolderPath(r.Folder),
			run: func(m *Model) tea.Cmd {
				m.openSaved(m.ws.Find(id))
				return nil
			}})
	}

	// Folders
	for _, f := range m.ws.Folders {
		id := f.ID
		items = append(items, paletteItem{key: "folder:" + id, category: "Folder", title: m.ws.FolderPath(id),
			run: func(m *Model) tea.Cmd {
				m.revealFolder(id)
				return nil
			}})
	}
	return items
}

func methodItems(m Model) []paletteItem {
	var items []paletteItem
	for i, mt := range core.Methods {
		i := i
		hint := ""
		if i == m.methodIdx {
			hint = "current"
		}
		items = append(items, paletteItem{method: mt, hint: hint, current: i == m.methodIdx, run: func(m *Model) tea.Cmd {
			m.methodIdx = i
			return nil
		}})
	}
	return items
}

func folderItems(m Model) []paletteItem {
	items := []paletteItem{{title: "⌂ top level", run: func(m *Model) tea.Cmd {
		m.moveCurrentTo("")
		return nil
	}}}
	paths := make([]paletteItem, 0, len(m.ws.Folders))
	for _, f := range m.ws.Folders {
		id := f.ID
		paths = append(paths, paletteItem{title: m.ws.FolderPath(id), run: func(m *Model) tea.Cmd {
			m.moveCurrentTo(id)
			return nil
		}})
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i].title < paths[j].title })
	return append(items, paths...)
}

func shortcutItems(Model) []paletteItem {
	keys := [][2]string{
		{"Command palette", "ctrl+p"},
		{"Send request", "ctrl+r / alt+enter / enter in URL"},
		{"Cancel request", "esc"},
		{"Save", "ctrl+s"},
		{"New tab", "ctrl+n"},
		{"Close tab", "ctrl+w"},
		{"Next / previous tab", "alt+→ / alt+←"},
		{"Go to tab 1–9", "alt+1 … alt+9"},
		{"Rename tab", "f2"},
		{"Body: raw / form-data", "alt+m"},
		{"Switch environment", "alt+e"},
		{"Edit environment variables", "alt+v"},
		{"Jump to URL / params / headers / body / response / sidebar", "alt+u/p/h/b/r/s"},
		{"…in terminals where alt is awkward", "ctrl+x then u/p/h/b/r/s"},
		{"Show / focus / hide sidebar", "ctrl+b"},
		{"Move focus", "tab / shift+tab"},
		{"Quit", "ctrl+c"},
		{"Method: change", "←/→, or first letter"},
		{"Headers: next cell", "enter"},
		{"Headers: accept suggestion", "tab / enter"},
		{"Headers: toggle / delete row", "ctrl+t / ctrl+d"},
		{"Open focused field in $EDITOR", "ctrl+x ctrl+e"},
		{"Response: switch body/headers", "t"},
		{"Response: find (enter/↑/↓ to step)", "/ or ctrl+f"},
		{"Response: jq filter", "|"},
		{"Response: clear jq filter", "esc"},
		{"History tab: select / view run", "↑/↓, enter"},
		{"History: back / restore request", "esc / r"},
		{"Response: scroll", "↑/↓ pgup/pgdn"},
		{"Sidebar: open / fold", "enter, ←/→"},
		{"Sidebar: new folder", "f"},
		{"Sidebar: move", "m"},
		{"Sidebar: rename / delete", "r / d"},
		{"Sidebar: new tab", "n"},
	}
	items := make([]paletteItem, len(keys))
	for i, k := range keys {
		items[i] = paletteItem{title: k[0], hint: k[1]}
	}
	return items
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// Actions -------------------------------------------------------------------

func (m *Model) flashCopy(msg string) {
	if msg == "" {
		m.notice = errorStyle.Render("couldn't reach the clipboard — install xclip, xsel or wl-clipboard")
		return
	}
	m.flash(msg)
}

func (m *Model) cancelRequest() {
	if t := m.cur(); t.loading && t.cancel != nil {
		t.cancel()
	}
}

func (m *Model) quit() tea.Cmd {
	for _, t := range m.tabs {
		if t.cancel != nil {
			t.cancel()
		}
	}
	m.Persist()
	return tea.Quit
}

func (m *Model) importCurlFromClipboard() {
	text, err := pasteText()
	switch {
	case err != nil:
		m.notice = errorStyle.Render("couldn't read the clipboard — install xclip, xsel or wl-clipboard")
	case !core.LooksLikeCurl(text):
		m.notice = errorStyle.Render("the clipboard doesn't contain a curl command")
	default:
		m.ImportCurl(text)
	}
}

func (m *Model) copyAsCurl() {
	// Variables are filled in; undefined ones stay as {{name}}.
	r, _ := m.resolve(m.snapshot())
	m.flashCopy(copyText(core.ToCurl(r), "as curl"))
}

func (m *Model) formatBody() {
	var buf bytes.Buffer
	if err := json.Indent(&buf, bytes.TrimSpace([]byte(m.body.Value())), "", "  "); err != nil {
		m.notice = errorStyle.Render("body isn't valid JSON: " + err.Error())
		return
	}
	m.body.SetValue(buf.String())
	m.flash("formatted JSON body")
}

func formatHeaders(r *core.Response) string {
	keys := make([]string, 0, len(r.Headers))
	for k := range r.Headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		for _, v := range r.Headers[k] {
			fmt.Fprintf(&b, "%s: %s\n", k, v)
		}
	}
	return b.String()
}

func defaultResponseFile(r *core.Response) string {
	ct := r.Headers.Get("Content-Type")
	switch {
	case strings.Contains(ct, "json"):
		return "response.json"
	case strings.Contains(ct, "html"):
		return "response.html"
	case strings.Contains(ct, "xml"):
		return "response.xml"
	}
	return "response.txt"
}

func (m *Model) saveResponse(path string) {
	t := m.cur()
	if t.result == nil {
		return
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, rest)
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(m.ws.CWD, path)
	}
	if err := os.WriteFile(path, t.result.Body, 0o644); err != nil {
		m.notice = errorStyle.Render("couldn't save: " + err.Error())
		return
	}
	m.flash(fmt.Sprintf("wrote %s to %s", core.HumanSize(len(t.result.Body)), path))
}

func (m *Model) showResponse(tab int) {
	m.cur().respTab = tab
	m.refreshResponse()
	m.resp.GotoTop()
	m.setFocus(focusResponse)
}

// closeWhere closes every tab matching keep == false, without prompting.
func (m *Model) closeWhere(close func(i int) bool) int {
	m.captureActive()
	activeUID := m.cur().uid
	var kept []*tab
	n := 0
	for i, t := range m.tabs {
		if close(i) {
			if t.cancel != nil {
				t.cancel()
			}
			n++
			continue
		}
		kept = append(kept, t)
	}
	if len(kept) == 0 {
		kept = append(kept, m.makeTab(core.Request{Method: "GET"}, ""))
	}
	m.tabs = kept
	m.active = max(m.tabIndex(activeUID), 0)
	m.loadActive()
	m.Persist()
	return n
}

func (m *Model) closeOtherTabs() {
	dirtyKept := 0
	n := m.closeWhere(func(i int) bool {
		if i == m.active {
			return false
		}
		if m.dirty(i) {
			dirtyKept++
			return false
		}
		return true
	})
	msg := fmt.Sprintf("closed %d tab(s)", n)
	if dirtyKept > 0 {
		msg += fmt.Sprintf(", kept %d with unsaved changes", dirtyKept)
	}
	m.flash(msg)
}

func (m *Model) closeCleanTabs() {
	n := m.closeWhere(func(i int) bool { return !m.dirty(i) })
	m.flash(fmt.Sprintf("closed %d tab(s)", n))
}

func (m *Model) duplicateTab() {
	r := m.snapshot()
	r.ID = ""
	r.Name = m.tabName(m.active) + " copy"
	m.openTab(r, "")
	m.flash("duplicated as an unsaved tab")
}

// saveAsNew forks the current saved request into a new one next to it; the
// tab then edits the copy.
func (m *Model) saveAsNew() {
	t := m.cur()
	dest := m.contextFolder()
	if j := m.ws.Find(t.savedID); j >= 0 {
		dest = m.ws.Requests[j].Folder
	}
	label := "Save as new:"
	if dest != "" {
		label = "Save as new in " + m.ws.FolderPath(dest) + ":"
	}
	m.ask(promptSaveAs, label, m.tabName(m.active)+" copy", 0)
	m.prompt.id = dest
}

func (m *Model) revertToSaved() {
	t := m.cur()
	j := m.ws.Find(t.savedID)
	if j < 0 {
		return
	}
	t.req = m.ws.Requests[j]
	m.loadActive()
	m.Persist()
	m.flash("reverted to saved")
}

func (m *Model) moveCurrentTo(folderID string) {
	t := m.cur()
	j := m.ws.Find(t.savedID)
	if j < 0 {
		return
	}
	id := t.savedID
	if !m.mutate(func(w *core.Workspace) error { return w.MoveRequest(id, folderID) }) {
		return
	}
	m.revealInSidebar(id)
	where := "top level"
	if folderID != "" {
		where = m.ws.FolderPath(folderID)
	}
	m.flash("moved to " + where)
}

func (m *Model) setAllCollapsed(c bool) {
	m.mutate(func(w *core.Workspace) error { w.SetAllCollapsed(c); return nil })
}

// revealFolder shows the sidebar with a folder selected, unfolding its
// parents.
func (m *Model) revealFolder(id string) {
	m.mutate(func(w *core.Workspace) error {
		i := w.FindFolder(id)
		if i < 0 {
			return fmt.Errorf("folder %s: %w", id, core.ErrNotFound)
		}
		for p := w.Folders[i].Parent; p != ""; p = w.Folders[w.FindFolder(p)].Parent {
			w.Unfold(p)
		}
		return nil
	})
	if !m.showSidebar {
		m.showSidebar = true
		m.layout()
	}
	m.selectItem(rowFolder, id)
	m.setFocus(focusSidebar)
	m.Persist()
}
