package main

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
)

// rootItems lists everything the palette can do: commands first, then open
// tabs, saved requests and folders to jump to.
func (m model) rootItems() []paletteItem {
	t := m.cur()
	saved := t.savedID != "" && m.ws.find(t.savedID) >= 0
	hasResp := t.result != nil && t.err == nil

	cmd := func(category, title, hint string, run func(m *model) tea.Cmd) paletteItem {
		return paletteItem{key: "cmd:" + category + ":" + title, category: category, title: title, hint: hint, run: run}
	}
	do := func(f func(m *model)) func(m *model) tea.Cmd {
		return func(m *model) tea.Cmd { f(m); return nil }
	}

	var items []paletteItem
	add := func(when bool, it paletteItem) {
		if when {
			items = append(items, it)
		}
	}

	// Request
	add(!t.loading, cmd("Request", "Send", "ctrl+r", func(m *model) tea.Cmd { return m.send() }))
	add(t.loading, cmd("Request", "Cancel running request", "esc", do((*model).cancelRequest)))
	add(true, paletteItem{key: "cmd:method", category: "Request", title: "Set method…", hint: m.method(),
		next: &paletteStep{title: "Set method", placeholder: "Method…", items: methodItems}})
	add(true, cmd("Request", "Import curl from clipboard", "", do((*model).importCurlFromClipboard)))
	add(true, cmd("Request", "Copy as curl", "", do((*model).copyAsCurl)))
	add(m.url.Value() != "", cmd("Request", "Copy URL", "", do(func(m *model) {
		m.flashCopy(copyText(m.url.Value(), "URL"))
	})))
	add(len(m.headers.Rows()) > 0, cmd("Request", "Clear headers", "", do(func(m *model) {
		m.headers.SetRows(nil)
	})))
	form := m.bodyMode == bodyForm
	add(!form && m.body.Value() != "", cmd("Request", "Clear body", "", do(func(m *model) {
		m.body.SetValue("")
	})))
	add(form && len(m.form.Rows()) > 0, cmd("Request", "Clear form fields", "", do(func(m *model) {
		m.form.SetRows(nil)
	})))
	add(!form && m.body.Value() != "", cmd("Request", "Format JSON body", "", do((*model).formatBody)))
	bodySwitch := "Body: use form-data (multipart)"
	if form {
		bodySwitch = "Body: use raw"
	}
	add(true, paletteItem{key: "cmd:body-mode", category: "Request", title: bodySwitch, hint: "alt+m",
		run: func(m *model) tea.Cmd { m.toggleBodyMode(); return nil }})
	add(true, cmd("Request", "Edit body in $EDITOR", "ctrl+x ctrl+e", func(m *model) tea.Cmd { return m.openEditor(editBody) }))
	add(true, cmd("Request", "Edit URL in $EDITOR", "", func(m *model) tea.Cmd { return m.openEditor(editURL) }))
	add(true, cmd("Request", "Edit headers in $EDITOR", "", func(m *model) tea.Cmd { return m.openEditor(editHeaders) }))

	// Response
	add(hasResp, cmd("Response", "Copy body", "", do(func(m *model) {
		if t := m.cur(); t.jq != "" && t.jqErr == "" {
			m.flashCopy(copyText(ansi.Strip(t.jqOut), "filtered body"))
			return
		}
		m.flashCopy(copyText(string(m.cur().result.Body), "response body"))
	})))
	add(hasResp, cmd("Response", "Find…", "/", do(func(m *model) { m.openBar(barFind) })))
	add(hasResp, cmd("Response", "Filter with jq…", "|", do(func(m *model) { m.openBar(barJQ) })))
	add(t.jq != "", cmd("Response", "Clear jq filter", "", do(func(m *model) { m.setJQ("") })))
	add(hasResp && m.ws.activeEnv() != nil, cmd("Response", "Set variable from response…", "", do(func(m *model) {
		m.ask(promptSetVar, "Set variable (name = jq filter):", "", 0)
		m.prompt.input.Placeholder = "token = .access_token"
	})))
	add(hasResp, cmd("Response", "Open in $EDITOR", "", func(m *model) tea.Cmd { return m.openEditor(editResponse) }))
	add(hasResp, cmd("Response", "Copy headers", "", do(func(m *model) {
		m.flashCopy(copyText(formatHeaders(m.cur().result), "response headers"))
	})))
	add(hasResp, cmd("Response", "Save body to file…", "", do(func(m *model) {
		m.ask(promptSaveResponse, "Save response to:", defaultResponseFile(m.cur().result), 0)
	})))
	add(true, cmd("Response", "Toggle line wrap", onOff(m.wrap), do(func(m *model) {
		m.wrap = !m.wrap
		m.refreshResponse()
		m.resp.SetXOffset(0)
		m.respXOff = 0
	})))
	add(true, cmd("Response", "Show body", "", do(func(m *model) { m.showResponse(respTabBody) })))
	add(true, cmd("Response", "Show headers", "", do(func(m *model) { m.showResponse(respTabHeaders) })))

	// Tabs
	add(true, cmd("Tab", "New tab", "ctrl+n", do(func(m *model) { m.openTab(request{Method: "GET"}, "") })))
	add(true, cmd("Tab", "Close tab", "ctrl+w", do(func(m *model) { m.closeTab(m.active, false) })))
	add(len(m.tabs) > 1, cmd("Tab", "Close other tabs", "", do((*model).closeOtherTabs)))
	add(true, cmd("Tab", "Close tabs without unsaved changes", "", do((*model).closeCleanTabs)))
	add(len(m.tabs) > 1, cmd("Tab", "Next tab", "alt+→", do(func(m *model) {
		m.switchTab((m.active + 1) % len(m.tabs))
	})))
	add(len(m.tabs) > 1, cmd("Tab", "Previous tab", "alt+←", do(func(m *model) {
		m.switchTab((m.active + len(m.tabs) - 1) % len(m.tabs))
	})))
	add(true, cmd("Tab", "Duplicate tab", "", do((*model).duplicateTab)))
	add(true, cmd("Tab", "Rename tab…", "f2", do((*model).renameCurrent)))

	// Saved requests and folders
	add(true, cmd("Saved", "Save", "ctrl+s", do((*model).saveCurrent)))
	add(saved, cmd("Saved", "Save as new…", "", do((*model).saveAsNew)))
	add(saved && m.dirty(m.active), cmd("Saved", "Revert to saved", "", do((*model).revertToSaved)))
	add(true, cmd("Saved", "Import OpenAPI spec…", "", do(func(m *model) {
		m.ask(promptImportSpec, "Import OpenAPI spec (file or URL):", "", 0)
		m.prompt.input.Placeholder = "openapi.json"
	})))
	add(true, cmd("Saved", "New folder…", "", do(func(m *model) {
		m.ask(promptNewFolder, "New folder:", "", 0)
		m.prompt.id = ""
	})))
	add(saved, paletteItem{key: "cmd:move", category: "Saved", title: "Move to folder…",
		next: &paletteStep{title: "Move “" + m.tabName(m.active) + "” to", placeholder: "Folder…", items: folderItems}})
	add(saved, cmd("Saved", "Delete saved request", "", do(func(m *model) {
		m.ask(promptDelete, fmt.Sprintf("Delete “%s”?", m.tabName(m.active)), "", 0)
	})))
	add(len(m.ws.Folders) > 0, cmd("Saved", "Collapse all folders", "", do(func(m *model) { m.setAllCollapsed(true) })))
	add(len(m.ws.Folders) > 0, cmd("Saved", "Expand all folders", "", do(func(m *model) { m.setAllCollapsed(false) })))

	items = append(items, m.envCommands()...)

	// History
	add(true, paletteItem{key: "cmd:history-all", category: "History", title: "Browse all runs…",
		hint: fmt.Sprintf("%d", len(m.hist.index)),
		next: &paletteStep{title: "All runs", placeholder: "Search by name, status, env…", items: allRunItems}})
	add(true, cmd("History", "Show runs of this request", "", do(func(m *model) {
		m.cur().viewing = nil
		m.showResponse(respTabHistory)
		m.revealHistSel()
	})))
	add(t.viewing != nil, cmd("History", "Restore request from this run", "r", do(func(m *model) {
		m.restoreRun(m.cur().viewing)
	})))
	add(t.viewing != nil, cmd("History", "Back to run list", "esc", do((*model).closeRun)))
	add(len(m.runs()) > 0, cmd("History", "Clear runs of this request", "", do(func(m *model) {
		m.ask(promptClearHistory, fmt.Sprintf("Delete all %d run(s) of “%s”?", len(m.runs()), m.tabName(m.active)), "", 0)
	})))

	// App
	add(true, cmd("View", "Toggle sidebar", "ctrl+b", do(func(m *model) {
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
		add(true, cmd("View", "Focus "+f.name, "", do(func(m *model) { m.setFocus(f.f) })))
	}
	add(true, paletteItem{key: "cmd:keys", category: "Help", title: "Keyboard shortcuts",
		next: &paletteStep{title: "Keyboard shortcuts", placeholder: "Search shortcuts…", items: shortcutItems}})
	add(true, cmd("App", "Show workspace file location", "", do(func(m *model) {
		if msg := copyText(m.ws.path, "path"); msg != "" {
			m.flash(m.ws.path + "  (" + msg + ")")
		} else {
			m.flash(m.ws.path)
		}
	})))
	add(true, cmd("App", "Quit", "ctrl+c", func(m *model) tea.Cmd { return m.quit() }))

	// Open tabs
	for i, tb := range m.tabs {
		if i == m.active {
			continue
		}
		uid := tb.uid
		r := m.tabRequest(i)
		items = append(items, paletteItem{category: "Tab", title: m.tabName(i), method: r.Method, hint: "open tab",
			run: func(m *model) tea.Cmd {
				if j := m.tabIndex(uid); j >= 0 {
					m.switchTab(j)
				}
				return nil
			}})
	}

	// Saved requests
	for _, r := range m.ws.Requests {
		id := r.ID
		items = append(items, paletteItem{key: "req:" + id, title: r.displayName(), method: r.Method,
			hint: m.ws.folderPath(r.Folder),
			run: func(m *model) tea.Cmd {
				m.openSaved(m.ws.find(id))
				return nil
			}})
	}

	// Folders
	for _, f := range m.ws.Folders {
		id := f.ID
		items = append(items, paletteItem{key: "folder:" + id, category: "Folder", title: m.ws.folderPath(id),
			run: func(m *model) tea.Cmd {
				m.revealFolder(id)
				return nil
			}})
	}
	return items
}

func methodItems(m model) []paletteItem {
	var items []paletteItem
	for i, mt := range methods {
		i := i
		hint := ""
		if i == m.methodIdx {
			hint = "current"
		}
		items = append(items, paletteItem{method: mt, hint: hint, current: i == m.methodIdx, run: func(m *model) tea.Cmd {
			m.methodIdx = i
			return nil
		}})
	}
	return items
}

func folderItems(m model) []paletteItem {
	items := []paletteItem{{title: "⌂ top level", run: func(m *model) tea.Cmd {
		m.moveCurrentTo("")
		return nil
	}}}
	paths := make([]paletteItem, 0, len(m.ws.Folders))
	for _, f := range m.ws.Folders {
		id := f.ID
		paths = append(paths, paletteItem{title: m.ws.folderPath(id), run: func(m *model) tea.Cmd {
			m.moveCurrentTo(id)
			return nil
		}})
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i].title < paths[j].title })
	return append(items, paths...)
}

func shortcutItems(model) []paletteItem {
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

func (m *model) flashCopy(msg string) {
	if msg == "" {
		m.notice = errorStyle.Render("couldn't reach the clipboard — install xclip, xsel or wl-clipboard")
		return
	}
	m.flash(msg)
}

func (m *model) cancelRequest() {
	if t := m.cur(); t.loading && t.cancel != nil {
		t.cancel()
	}
}

func (m *model) quit() tea.Cmd {
	for _, t := range m.tabs {
		if t.cancel != nil {
			t.cancel()
		}
	}
	m.persist()
	return tea.Quit
}

func (m *model) importCurlFromClipboard() {
	text, err := pasteText()
	switch {
	case err != nil:
		m.notice = errorStyle.Render("couldn't read the clipboard — install xclip, xsel or wl-clipboard")
	case !looksLikeCurl(text):
		m.notice = errorStyle.Render("the clipboard doesn't contain a curl command")
	default:
		m.importCurl(text)
	}
}

func (m *model) copyAsCurl() {
	// Variables are filled in; undefined ones stay as {{name}}.
	r, _ := m.resolve(m.snapshot())
	m.flashCopy(copyText(toCurl(r), "as curl"))
}

func (m *model) formatBody() {
	var buf bytes.Buffer
	if err := json.Indent(&buf, bytes.TrimSpace([]byte(m.body.Value())), "", "  "); err != nil {
		m.notice = errorStyle.Render("body isn't valid JSON: " + err.Error())
		return
	}
	m.body.SetValue(buf.String())
	m.flash("formatted JSON body")
}

func formatHeaders(r *response) string {
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

func defaultResponseFile(r *response) string {
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

func (m *model) saveResponse(path string) {
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
	m.flash(fmt.Sprintf("wrote %s to %s", humanSize(len(t.result.Body)), path))
}

func (m *model) showResponse(tab int) {
	m.cur().respTab = tab
	m.refreshResponse()
	m.resp.GotoTop()
	m.setFocus(focusResponse)
}

// closeWhere closes every tab matching keep == false, without prompting.
func (m *model) closeWhere(close func(i int) bool) int {
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
		kept = append(kept, m.makeTab(request{Method: "GET"}, ""))
	}
	m.tabs = kept
	m.active = max(m.tabIndex(activeUID), 0)
	m.loadActive()
	m.persist()
	return n
}

func (m *model) closeOtherTabs() {
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

func (m *model) closeCleanTabs() {
	n := m.closeWhere(func(i int) bool { return !m.dirty(i) })
	m.flash(fmt.Sprintf("closed %d tab(s)", n))
}

func (m *model) duplicateTab() {
	r := m.snapshot()
	r.ID = ""
	r.Name = m.tabName(m.active) + " copy"
	m.openTab(r, "")
	m.flash("duplicated as an unsaved tab")
}

// saveAsNew forks the current saved request into a new one next to it; the
// tab then edits the copy.
func (m *model) saveAsNew() {
	t := m.cur()
	dest := m.contextFolder()
	if j := m.ws.find(t.savedID); j >= 0 {
		dest = m.ws.Requests[j].Folder
	}
	label := "Save as new:"
	if dest != "" {
		label = "Save as new in " + m.ws.folderPath(dest) + ":"
	}
	m.ask(promptSaveAs, label, m.tabName(m.active)+" copy", 0)
	m.prompt.id = dest
}

func (m *model) revertToSaved() {
	t := m.cur()
	j := m.ws.find(t.savedID)
	if j < 0 {
		return
	}
	t.req = m.ws.Requests[j]
	m.loadActive()
	m.persist()
	m.flash("reverted to saved")
}

func (m *model) moveCurrentTo(folderID string) {
	t := m.cur()
	j := m.ws.find(t.savedID)
	if j < 0 {
		return
	}
	m.ws.Requests[j].Folder = folderID
	if i := m.ws.findFolder(folderID); i >= 0 {
		m.ws.Folders[i].Collapsed = false
	}
	m.revealInSidebar(t.savedID)
	m.persist()
	where := "top level"
	if folderID != "" {
		where = m.ws.folderPath(folderID)
	}
	m.flash("moved to " + where)
}

func (m *model) setAllCollapsed(c bool) {
	for i := range m.ws.Folders {
		m.ws.Folders[i].Collapsed = c
	}
	m.ensureSideVisible()
	m.persist()
}

// revealFolder shows the sidebar with a folder selected, unfolding its
// parents.
func (m *model) revealFolder(id string) {
	for i := m.ws.findFolder(m.ws.Folders[m.ws.findFolder(id)].Parent); i >= 0; i = m.ws.findFolder(m.ws.Folders[i].Parent) {
		m.ws.Folders[i].Collapsed = false
	}
	if !m.showSidebar {
		m.showSidebar = true
		m.layout()
	}
	m.selectItem(rowFolder, id)
	m.setFocus(focusSidebar)
	m.persist()
}
