package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

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
	focusSidebar focus = iota
	focusMethod
	focusURL
	focusParams
	focusHeaders
	focusBody
	focusResponse
	focusCount
)

const (
	respTabBody = iota
	respTabHeaders
	respTabHistory
)

// stackBelow is the main-area width under which panes are stacked vertically.
const stackBelow = 90

// sidebarAutoShow is the terminal width from which the sidebar starts open.
const sidebarAutoShow = 110

// tab is one open request. The active tab's request lives in the shared
// editors; the others keep theirs in req.
type tab struct {
	uid     int
	savedID string // ID of the saved request this tab edits, "" for a draft
	req     request
	reqTab  focus // focusParams, focusHeaders or focusBody

	result  *response
	pretty  string // rendered (possibly highlighted) response body
	err     error
	respTab int
	respY   int // response scroll offset while inactive

	draftKey string     // history key for a draft; runs move to the saved ID on save
	pending  *histEntry // run being sent, recorded when the response arrives
	histSel  int        // selected run in the History tab
	viewing  *histEntry // past run being viewed, nil for the latest response
	viewTab  int
	// rendered body and Changes text of the viewed run, computed once
	viewPretty  string
	viewChanges string

	jq    string // jq filter applied to the response body, "" for none
	jqOut string // rendered filter output
	jqErr string
	// parsed body for jq, kept while a filter is on
	jqInput    any
	jqInputErr error
	jqInputFor *response
	loading    bool
	cancel     context.CancelFunc
}

type model struct {
	width, height int
	sized         bool
	focus         focus

	ws      *workspace
	hist    *history
	tabs    []*tab
	active  int
	nextUID int

	showSidebar bool
	sideSel     int // index into sideRows()
	sideOffset  int
	moving      *sideItem // item being moved to another folder, if any

	sideFilter    textinput.Model // sidebar filter query
	sideFiltering bool            // typing into the filter

	methodIdx int
	url       textinput.Model
	params    headerEditor
	headers   headerEditor
	body      textarea.Model
	form      headerEditor // multipart form-data fields
	bodyMode  string       // "" (raw) or bodyForm

	resp    viewport.Model
	spinner spinner.Model

	wrap    bool // hard-wrap long response lines
	palette *palette
	envEdit *envEditor

	editSeq int // bumped on input; autosave fires once it settles

	warnedKeyring bool

	rcache      renderCache
	bar         *respBar // find / jq input over the response pane
	find        string
	findIdx     int
	findMatches []findMatch
	findIn      *wrapped // text the matches were computed for
	findFor     string   // query the matches were computed for
	respXOff    int      // horizontal scroll we last set (wrap off)
	chord       bool     // ctrl+x pressed, waiting for ctrl+e
	prompt      *prompt
	notice      string // one-off message shown in the help bar until the next key
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

func newModel(ws *workspace, loadErr error) model {
	url := textinput.New()
	url.Placeholder = "https://httpbin.org/get  (or paste a curl command)"
	url.Prompt = ""
	url.CharLimit = 0
	url.PlaceholderStyle = mutedStyle

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colorAccent)

	m := model{
		focus:   focusURL,
		ws:      ws,
		url:     url,
		params:  newKVEditor("Key", "Value", "+ add param", false),
		headers: newHeaderEditor(),
		body:    newBodyEditor(),
		form:    newFormEditor(ws.CWD),

		sideFilter: newSideFilter(),
		resp:       viewport.New(0, 0),
		spinner:    sp,
		wrap:       true,
	}
	m.resp.SetHorizontalStep(4)

	// Restore the tabs that were open last time in this directory.
	for _, st := range ws.Tabs {
		if st.SavedID != "" && ws.find(st.SavedID) < 0 {
			st.SavedID = ""
		}
		t := m.makeTab(st.Request, st.SavedID)
		if st.DraftKey != "" {
			t.draftKey = st.DraftKey
		}
		m.tabs = append(m.tabs, t)
	}
	if len(m.tabs) == 0 {
		m.tabs = append(m.tabs, m.makeTab(request{Method: "GET"}, ""))
	}
	m.active = min(max(ws.ActiveTab, 0), len(m.tabs)-1)
	hist, histErr := openHistory(ws)
	m.hist = hist
	if histErr != nil {
		loadErr = errors.Join(loadErr, histErr)
	}
	m.loadActive()
	m.setFocus(focusURL)

	if loadErr != nil {
		m.notice = errorStyle.Render("couldn't read " + ws.path + ": " + loadErr.Error())
	}
	return m
}

func (m *model) makeTab(r request, savedID string) *tab {
	m.nextUID++
	return &tab{uid: m.nextUID, savedID: savedID, req: r, reqTab: focusParams, draftKey: newID()}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, syncTick())
}

// syncTickMsg checks, about once a second, whether the CLI changed the
// workspace or recorded runs, and picks the changes up.
type syncTickMsg struct{}

const syncInterval = time.Second

func syncTick() tea.Cmd {
	return tea.Tick(syncInterval, func(time.Time) tea.Msg { return syncTickMsg{} })
}

// syncFromDisk reloads content written by someone else and reconciles the
// open tabs: deleted requests' tabs become drafts, tabs without unsaved
// changes take the new version, and tabs with unsaved changes keep them.
func (m *model) syncFromDisk() {
	if m.hist != nil && m.hist.changedOnDisk() {
		_ = m.hist.loadIndex()
		if m.cur().respTab == respTabHistory && m.cur().viewing == nil {
			m.refreshResponse()
		}
	}
	if !m.ws.changedOnDisk() {
		return
	}
	old := map[string]request{}
	for _, r := range m.ws.Requests {
		old[r.ID] = r
	}
	m.captureActive()
	if err := withWriteLock(m.ws.writeLockPath(), m.ws.reloadContent); err != nil {
		m.notice = errorStyle.Render("couldn't reload workspace: " + err.Error())
		return
	}
	for i, t := range m.tabs {
		before, had := old[t.savedID]
		j := m.ws.find(t.savedID)
		if t.savedID == "" || !had || j < 0 {
			continue
		}
		now := m.ws.Requests[j]
		if t.req.sameContent(before) && !now.sameContent(before) {
			t.req = now
			if i == m.active {
				m.loadActive()
			}
		}
	}
	m.contentChanged()
}

func (m model) cur() *tab      { return m.tabs[m.active] }
func (m model) method() string { return methods[m.methodIdx] }

func (m *model) setFocus(f focus) {
	if f == focusSidebar && !m.showSidebar {
		f = focusMethod
	}
	if f != focusSidebar && m.sideFiltering {
		m.stopEditingFilter()
	}
	if f != focusResponse && m.bar != nil {
		if m.bar.mode == barFind {
			m.find = ""
		}
		m.bar = nil
		m.refreshResponse()
	}
	m.focus = f
	m.url.Blur()
	m.params.Blur()
	m.headers.Blur()
	m.body.Blur()
	m.form.Blur()
	switch f {
	case focusURL:
		m.url.Focus()
	case focusParams:
		m.cur().reqTab = focusParams
		m.params.Focus()
	case focusHeaders:
		m.cur().reqTab = focusHeaders
		m.headers.Focus()
	case focusBody:
		m.cur().reqTab = focusBody
		if m.bodyMode == bodyForm {
			m.form.Focus()
		} else {
			m.body.Focus()
		}
	}
}

func (m *model) cycleFocus(delta int) {
	f := m.focus
	for {
		f = (f + focus(delta) + focusCount) % focusCount
		if f != focusSidebar || m.showSidebar {
			break
		}
	}
	m.setFocus(f)
}

// Tabs ----------------------------------------------------------------------

func toSavedHeaders(rows []headerRow) []savedHeader {
	var out []savedHeader
	for _, r := range rows {
		out = append(out, savedHeader{Key: r.key, Value: r.value, Enabled: r.enabled, Secret: r.secret})
	}
	return out
}

func fromSavedHeaders(hs []savedHeader) []headerRow {
	var out []headerRow
	for _, h := range hs {
		out = append(out, headerRow{key: h.Key, value: h.Value, enabled: h.Enabled, secret: h.Secret})
	}
	return out
}

// snapshot reads the active tab's request out of the editors.
func (m model) snapshot() request {
	t := m.cur()
	return request{
		ID:      t.req.ID,
		Name:    t.req.Name,
		Method:  m.method(),
		URL:     m.url.Value(),
		Headers: toSavedHeaders(m.headers.Rows()),
		Body:    m.body.Value(),

		BodyMode: m.bodyMode,
		Form:     toSavedHeaders(m.form.Rows()),

		DisabledParams: toSavedHeaders(disabledRows(m.params.Rows())),
	}
}

func (m model) tabRequest(i int) request {
	if i == m.active {
		return m.snapshot()
	}
	return m.tabs[i].req
}

// tabName is the tab's label: the saved request's name, or the draft's.
func (m model) tabName(i int) string {
	t := m.tabs[i]
	if j := m.ws.find(t.savedID); t.savedID != "" && j >= 0 {
		return m.ws.Requests[j].displayName()
	}
	return t.req.displayName()
}

// dirty reports whether a tab has changes that aren't saved.
func (m model) dirty(i int) bool {
	t, r := m.tabs[i], m.tabRequest(i)
	if t.savedID == "" {
		return r.URL != "" || r.Body != "" || len(r.Headers) > 0
	}
	j := m.ws.find(t.savedID)
	return j < 0 || !m.ws.Requests[j].sameContent(r)
}

func (m *model) captureActive() {
	t := m.cur()
	t.req = m.snapshot()
	t.respY = m.resp.YOffset
}

func methodIndex(method string) int {
	for i, mt := range methods {
		if mt == method {
			return i
		}
	}
	methods = append(methods, method)
	return len(methods) - 1
}

// loadActive puts the active tab into the editors.
func (m *model) loadActive() {
	t := m.cur()
	if t.req.Method == "" {
		t.req.Method = "GET"
	}
	m.methodIdx = methodIndex(t.req.Method)
	m.url.SetValue(t.req.URL)
	m.url.CursorEnd()
	m.params.SetRows(paramRows(t.req.URL, fromSavedHeaders(t.req.DisabledParams)))
	m.headers.SetRows(fromSavedHeaders(t.req.Headers))
	m.body.SetValue(t.req.Body)
	m.bodyMode = t.req.BodyMode
	m.form.SetRows(fromSavedHeaders(t.req.Form))
	m.bar, m.find, m.findIdx = nil, "", 0
	m.refreshResponse()
	m.resp.SetYOffset(t.respY)
	if isRequestPane(m.focus) {
		m.setFocus(t.reqTab)
	}
	m.revealInSidebar(t.savedID)
}

func (m *model) switchTab(i int) {
	if i < 0 || i >= len(m.tabs) || i == m.active {
		return
	}
	m.captureActive()
	m.active = i
	m.loadActive()
	m.persist()
}

func (m *model) openTab(r request, savedID string) {
	m.captureActive()
	m.tabs = append(m.tabs, m.makeTab(r, savedID))
	m.active = len(m.tabs) - 1
	m.loadActive()
	m.setFocus(focusURL)
	m.persist()
}

func (m *model) closeTab(i int, force bool) {
	if !force && m.dirty(i) {
		m.ask(promptCloseTab, fmt.Sprintf("“%s” has unsaved changes. Close anyway?", m.tabName(i)), "", m.tabs[i].uid)
		return
	}
	if t := m.tabs[i]; t.cancel != nil {
		t.cancel()
	}
	if i != m.active {
		m.tabs = append(m.tabs[:i], m.tabs[i+1:]...)
		if i < m.active {
			m.active--
		}
		m.persist()
		return
	}
	m.tabs = append(m.tabs[:i], m.tabs[i+1:]...)
	if len(m.tabs) == 0 {
		m.tabs = append(m.tabs, m.makeTab(request{Method: "GET"}, ""))
	}
	m.active = min(i, len(m.tabs)-1)
	m.loadActive()
	m.persist()
}

func (m model) tabIndex(uid int) int {
	for i, t := range m.tabs {
		if t.uid == uid {
			return i
		}
	}
	return -1
}

// Saved requests ------------------------------------------------------------

// persist writes the open tabs (the session). Saved requests, folders and
// environments are written through mutate.
func (m *model) persist() {
	m.ws.Tabs = m.ws.Tabs[:0]
	for i, t := range m.tabs {
		m.ws.Tabs = append(m.ws.Tabs, savedTab{SavedID: t.savedID, DraftKey: t.draftKey, Request: m.tabRequest(i)})
	}
	m.ws.ActiveTab = m.active
	if err := m.ws.saveSession(); err != nil {
		m.notice = errorStyle.Render("couldn't save tabs: " + err.Error())
	}
}

// mutate changes the saved content (requests, folders, environments)
// safely against the CLI writing at the same time, then brings the UI in
// line with the result. It reports whether the change was saved.
func (m *model) mutate(fn func(*workspace) error) bool {
	if err := m.ws.mutate(fn); err != nil {
		m.notice = errorStyle.Render(err.Error())
		m.contentChanged()
		return false
	}
	m.contentChanged()
	m.persist()
	if err := m.ws.secretErr; err != nil && !m.warnedKeyring {
		m.warnedKeyring = true
		m.notice = errorStyle.Render("⚠ " + err.Error())
	}
	return true
}

// contentChanged fixes up UI state after the content changed: tabs of
// requests that no longer exist become drafts, and selections stay valid.
func (m *model) contentChanged() {
	for _, t := range m.tabs {
		if t.savedID != "" && m.ws.find(t.savedID) < 0 {
			t.savedID, t.req.ID = "", ""
		}
	}
	if mv := m.moving; mv != nil {
		if (mv.kind == rowFolder && m.ws.findFolder(mv.id) < 0) || (mv.kind == rowRequest && m.ws.find(mv.id) < 0) {
			m.moving = nil
		}
	}
	if m.envEdit != nil && m.ws.findEnv(m.envEdit.envID) < 0 {
		m.envEdit = nil
	}
	m.ensureSideVisible()
}

func (m *model) saveCurrent() {
	t := m.cur()
	if t.savedID != "" && m.ws.find(t.savedID) >= 0 {
		edits, id := m.snapshot(), t.savedID
		var saved request
		if !m.mutate(func(w *workspace) error {
			return w.updateRequest(id, func(r *request) { r.applyEdits(edits); saved = *r })
		}) {
			return
		}
		t.req = saved
		m.flash("saved “" + saved.displayName() + "”")
		return
	}
	name := m.snapshot().Name
	if name == "" {
		name = m.snapshot().suggestedName()
	}
	// New requests go into the folder selected in the sidebar.
	dest := m.contextFolder()
	label := "Save as:"
	if dest != "" {
		label = "Save in " + m.ws.folderPath(dest) + " as:"
	}
	m.ask(promptSaveAs, label, name, 0)
	m.prompt.id = dest
}

func (m *model) saveAs(name, folderID string) {
	t := m.cur()
	r := m.snapshot()
	r.ID, r.Name, r.Folder = newID(), name, folderID
	var saved request
	if !m.mutate(func(w *workspace) error { saved = w.addRequest(r); return nil }) {
		return
	}
	// The draft's runs now belong to the saved request.
	if t.savedID == "" && m.hist != nil {
		_ = m.hist.rekey(t.draftKey, saved.ID)
	}
	t.savedID, t.req = saved.ID, saved
	m.selectItem(rowRequest, saved.ID)
	m.persist()
	m.flash("saved “" + name + "”")
}

func (m *model) renameSaved(id, name string) {
	if !m.mutate(func(w *workspace) error {
		return w.updateRequest(id, func(r *request) { r.Name = name })
	}) {
		return
	}
	for _, t := range m.tabs {
		if t.savedID == id {
			t.req.Name = name
		}
	}
	m.persist()
}

func (m *model) deleteSaved(id string) {
	j := m.ws.find(id)
	if j < 0 {
		return
	}
	name := m.ws.Requests[j].displayName()
	// Open tabs keep their contents as unsaved drafts (contentChanged).
	if m.mutate(func(w *workspace) error { return w.deleteRequest(id) }) {
		m.flash("deleted “" + name + "”")
	}
}

// openSaved switches to the tab editing saved request i, opening one if
// needed.
func (m *model) openSaved(i int) {
	if i < 0 || i >= len(m.ws.Requests) {
		return
	}
	r := m.ws.Requests[i]
	for j, t := range m.tabs {
		if t.savedID == r.ID {
			m.switchTab(j)
			return
		}
	}
	// Reuse an untouched empty draft instead of piling up tabs.
	if m.cur().savedID == "" && !m.dirty(m.active) {
		t := m.cur()
		t.savedID, t.req = r.ID, r
		m.loadActive()
		m.persist()
		return
	}
	m.openTab(r, r.ID)
}

func (m *model) renameCurrent() {
	t := m.cur()
	if t.savedID != "" {
		m.ask(promptRename, "Rename to:", m.tabName(m.active), 0)
		return
	}
	m.ask(promptRenameDraft, "Name this tab:", t.req.Name, 0)
}

func (m *model) flash(s string) {
	m.notice = lipgloss.NewStyle().Foreground(colorGreen).Render("✓ " + s)
}

// Requests ------------------------------------------------------------------

func (m *model) send() tea.Cmd {
	t := m.cur()
	if t.loading || strings.TrimSpace(m.url.Value()) == "" {
		return nil
	}
	typed := m.snapshot()
	r, missing := m.resolve(typed)
	if len(missing) > 0 {
		m.notice = errorStyle.Render("undefined variable(s): {{" + strings.Join(missing, "}}, {{") +
			"}} — define them in the environment (alt+v)")
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	t.loading = true
	t.err = nil
	m.beginRun(t, typed, r)
	m.persist()
	return tea.Batch(
		m.spinner.Tick,
		sendRequest(ctx, t.uid, r, m.ws.CWD),
	)
}

func (m model) anyLoading() bool {
	for _, t := range m.tabs {
		if t.loading {
			return true
		}
	}
	return false
}

// openURL puts a URL in the current tab if it's an empty draft, or in a
// new tab otherwise.
func (m *model) openURL(u string) {
	r := request{Method: "GET", URL: u}
	if m.cur().savedID != "" || m.dirty(m.active) {
		m.openTab(r, "")
		return
	}
	m.cur().req = r
	m.loadActive()
}

// importCurl loads a curl command into the current tab, or into a new tab
// when the current one already holds something.
func (m *model) importCurl(cmd string) {
	cr, warnings, err := parseCurl(cmd)
	if err != nil {
		m.notice = errorStyle.Render("curl import failed: " + err.Error())
		return
	}
	r := request{Method: cr.Method, URL: cr.URL, Headers: toSavedHeaders(cr.Headers), Body: cr.Body}
	if len(cr.Form) > 0 {
		r.BodyMode, r.Form = bodyForm, toSavedHeaders(cr.Form)
	}
	if m.cur().savedID != "" || m.dirty(m.active) {
		m.openTab(r, "")
	} else {
		m.cur().req = r
		m.loadActive()
	}
	m.setFocus(focusURL)
	if cr.Body != "" && len(cr.Headers) == 0 {
		m.cur().reqTab = focusBody
	}

	msg := fmt.Sprintf("imported curl: %s, %d header(s)", cr.Method, len(cr.Headers))
	if cr.Body != "" {
		msg += ", body"
	}
	m.flash(msg + " — press enter to send")
	if len(warnings) > 0 {
		m.notice += errorStyle.Render("  ⚠ " + strings.Join(warnings, "; "))
	}
}

// Update --------------------------------------------------------------------

// autosaveMsg asks for a save if no input has arrived since it was scheduled.
type autosaveMsg struct{ seq int }

const autosaveDelay = 800 * time.Millisecond

// keyNames are what Bubble Tea calls special keys. Typed text that arrives
// in one batch (fast typing, unbracketed paste) comes as a single key event
// per word, so the word "home" would look exactly like the Home key.
var keyNames = map[string]bool{
	"home": true, "end": true, "up": true, "down": true, "left": true, "right": true,
	"tab": true, "enter": true, "esc": true, "delete": true, "backspace": true,
	"insert": true, "pgup": true, "pgdown": true, "space": true,
}

// literalText marks a batch of typed characters that spells a key name as
// pasted text, so text inputs insert it instead of treating it as that key.
func literalText(msg tea.Msg) tea.Msg {
	if k, ok := msg.(tea.KeyMsg); ok && k.Type == tea.KeyRunes && len(k.Runes) > 1 && !k.Paste && keyNames[string(k.Runes)] {
		k.Paste = true
		return k
	}
	return msg
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	msg = literalText(msg)
	if as, ok := msg.(autosaveMsg); ok {
		if as.seq == m.editSeq {
			m.persist()
		}
		return m, nil
	}

	urlBefore, paramsBefore := m.url.Value(), m.params.Rows()
	next, cmd := m.update(msg)
	nm, ok := next.(model)
	if !ok {
		return next, cmd
	}
	// Keep the URL and the Params tab in step: whichever one the user
	// changed wins. (When both change, e.g. switching tabs, they were set
	// together and already agree.)
	urlChanged := nm.url.Value() != urlBefore
	paramsChanged := !slices.Equal(nm.params.Rows(), paramsBefore)
	switch {
	case urlChanged && !paramsChanged:
		nm.syncParamsFromURL()
	case paramsChanged && !urlChanged:
		nm.syncURLFromParams()
	}

	switch msg.(type) {
	case tea.KeyMsg, tea.MouseMsg:
		nm.editSeq++
		seq := nm.editSeq
		cmd = tea.Batch(cmd, tea.Tick(autosaveDelay, func(time.Time) tea.Msg { return autosaveMsg{seq} }))
	}
	return nm, cmd
}

func (m model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if !m.sized {
			m.sized = true
			m.showSidebar = m.width >= sidebarAutoShow
		}
		m.layout()
		return m, nil

	case responseMsg:
		i := m.tabIndex(msg.tabUID)
		if i < 0 {
			return m, nil // tab was closed
		}
		t := m.tabs[i]
		t.loading = false
		if t.cancel != nil {
			t.cancel()
			t.cancel = nil
		}
		t.err = msg.err
		if msg.err == nil {
			t.result = msg.resp
			t.pretty = msg.pretty
			t.respY = 0
			t.applyJQ()
		}
		m.finishRun(t, msg)
		m.runCaptures(t, msg.resp)
		if i == m.active {
			m.refreshResponse()
			m.resp.GotoTop()
		}
		return m, nil

	case spinner.TickMsg:
		if !m.anyLoading() {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case editorDoneMsg:
		m.editorDone(msg)
		return m, nil

	case syncTickMsg:
		m.syncFromDisk()
		return m, syncTick()

	case specLoadedMsg:
		m.finishImport(msg)
		return m, nil

	case tea.MouseMsg:
		if m.prompt != nil {
			return m, nil
		}
		if m.envEdit != nil {
			return m.envEditorMouse(msg)
		}
		if m.palette != nil {
			return m.paletteMouse(msg)
		}
		return m.handleMouse(msg)

	case tea.KeyMsg:
		m.notice = ""
		if m.prompt != nil {
			return m.updatePrompt(msg)
		}
		if m.palette != nil {
			return m.updatePalette(msg)
		}
		if m.envEdit != nil {
			return m.updateEnvEditor(msg)
		}
		if m.chord {
			m.chord = false
			if msg.String() == "ctrl+e" {
				return m, m.openEditor(m.editTargetForFocus())
			}
			if f, ok := paneForKey(msg.String(), true); ok {
				m.jumpTo(f)
				return m, nil
			}
		} else if msg.String() == "ctrl+x" {
			m.chord = true
			return m, nil
		}
		if m.bar != nil && msg.String() != "ctrl+p" {
			switch msg.String() {
			case "tab", "shift+tab":
				m.bar = nil // leaving the pane; setFocus clears the search
				if m.find != "" {
					m.clearFind()
				}
			case "ctrl+c", "ctrl+r", "ctrl+s", "alt+enter",
				"alt+u", "alt+p", "alt+h", "alt+b", "alt+r", "alt+s":
			default:
				return m.updateBar(msg)
			}
		}
		switch msg.String() {
		case "alt+e":
			m.cancelMoving()
			m.openPalette()
			m.pushStep(envStep)
			return m, nil
		case "alt+v":
			m.cancelMoving()
			m.openEnvEditor()
			return m, nil
		}
		if msg.String() == "ctrl+p" {
			m.cancelMoving()
			m.openPalette()
			return m, nil
		}
		if msg.Paste && looksLikeCurl(string(msg.Runes)) {
			m.importCurl(string(msg.Runes))
			return m, nil
		}
		if m.moving != nil && msg.String() == "esc" {
			m.cancelMoving()
			return m, nil
		}
		if m.focus == focusHeaders {
			switch msg.String() {
			case "tab":
				if m.headers.AcceptSuggestion() {
					return m, nil
				}
			case "esc":
				if !m.cur().loading && m.headers.CloseSuggestions() {
					return m, nil
				}
			}
		}
		if f, ok := paneForKey(msg.String(), false); ok {
			m.jumpTo(f)
			return m, nil
		}
		switch msg.String() {
		case "ctrl+c":
			return m, m.quit()
		case "ctrl+r", "alt+enter":
			return m, m.send()
		case "ctrl+s":
			m.saveCurrent()
			return m, nil
		case "ctrl+n":
			m.openTab(request{Method: "GET"}, "")
			return m, nil
		case "ctrl+w":
			m.closeTab(m.active, false)
			return m, nil
		case "alt+right", "ctrl+pgdown":
			m.switchTab((m.active + 1) % len(m.tabs))
			return m, nil
		case "alt+left", "ctrl+pgup":
			m.switchTab((m.active + len(m.tabs) - 1) % len(m.tabs))
			return m, nil
		case "alt+1", "alt+2", "alt+3", "alt+4", "alt+5", "alt+6", "alt+7", "alt+8", "alt+9":
			m.switchTab(int(msg.String()[4] - '1'))
			return m, nil
		case "ctrl+b":
			switch {
			case !m.showSidebar:
				m.showSidebar = true
				m.layout()
				m.setFocus(focusSidebar)
			case m.focus != focusSidebar:
				m.setFocus(focusSidebar)
			default:
				m.cancelMoving()
				m.showSidebar = false
				m.layout()
				m.setFocus(focusURL)
			}
			return m, nil
		case "alt+m":
			m.toggleBodyMode()
			return m, nil
		case "f2":
			m.renameCurrent()
			return m, nil
		case "esc":
			if t := m.cur(); t.loading && t.cancel != nil {
				t.cancel()
				return m, nil
			}
		case "tab":
			m.cycleFocus(1)
			return m, nil
		case "shift+tab":
			m.cycleFocus(-1)
			return m, nil
		}
		return m.updateFocused(msg)
	}

	// Anything else (cursor blinks, clipboard pastes) goes to the focused input.
	var cmd tea.Cmd
	switch m.focus {
	case focusURL:
		m.url, cmd = m.url.Update(msg)
	case focusParams:
		m.params, cmd = m.params.Update(msg)
	case focusHeaders:
		m.headers, cmd = m.headers.Update(msg)
	case focusBody:
		if m.bodyMode == bodyForm {
			m.form, cmd = m.form.Update(msg)
		} else {
			m.body, cmd = m.body.Update(msg)
		}
	}
	if m.prompt != nil {
		m.prompt.input, cmd = m.prompt.input.Update(msg)
	}
	if m.palette != nil {
		m.palette.input, cmd = m.palette.input.Update(msg)
	}
	if m.envEdit != nil {
		m.envEdit.table, cmd = m.envEdit.table.Update(msg)
	}
	if m.bar != nil {
		m.bar.input, cmd = m.bar.input.Update(msg)
	}
	return m, cmd
}

func (m model) updateFocused(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.focus {
	case focusSidebar:
		m.updateSidebar(msg)
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
	case focusParams:
		m.params, cmd = m.params.Update(msg)
	case focusHeaders:
		m.headers, cmd = m.headers.Update(msg)
	case focusBody:
		if m.bodyMode == bodyForm {
			m.form, cmd = m.form.Update(msg)
		} else {
			m.body, cmd = m.body.Update(msg)
		}
	case focusResponse:
		// With wrapping off, ←/→ scroll sideways instead of switching tabs.
		switch k := msg.String(); {
		case m.updateHistory(k):
		case k == "/" || k == "ctrl+f":
			m.openBar(barFind)
		case k == "|":
			m.openBar(barJQ)
		case k == "esc" && m.cur().jq != "":
			m.setJQ("")
		case k == "t" || (m.wrap && k == "right"):
			m.cycleRespTab(1)
		case m.wrap && k == "left":
			m.cycleRespTab(-1)
		default:
			m.resp, cmd = m.resp.Update(msg)
		}
	}
	return m, cmd
}

// Layout ------------------------------------------------------------------

const methodWidth = 9 // "◀ PATCH ▶"

func (m model) sidebarW() int {
	if !m.showSidebar {
		return 0
	}
	return min(sidebarWidth, m.width/3)
}

func (m model) mainW() int { return m.width - m.sidebarW() }

func (m model) stacked() bool { return m.mainW() < stackBelow }

// paneSizes returns outer width/height of the request and response panes.
func (m model) paneSizes() (reqW, reqH, respW, respH int) {
	avail := max(m.height-1-1-3-1, 6) // title, tab bar, url bar, help
	mw := m.mainW()
	if m.stacked() {
		reqH = avail / 2
		return mw, reqH, mw, avail - reqH
	}
	reqW = mw / 2
	return reqW, avail, mw - reqW, avail
}

func (m *model) layout() {
	// Bar border and padding (4), method badge, gap (2), and one column for
	// the cursor, which the input draws past its Width.
	m.url.Width = max(m.mainW()-4-methodWidth-2-1, 10)

	reqW, reqH, respW, respH := m.paneSizes()
	edW, edH := max(reqW-4, 1), max(reqH-4, 1)
	m.params.SetSize(edW, edH)
	m.headers.SetSize(edW, edH)
	// SetWidth reserves 4 columns for line numbers; the gutter is wider.
	// The body tab's first line is the raw / form-data switch.
	m.body.SetWidth(edW - (lineNumberGutter - 4))
	m.body.SetHeight(max(edH-1, 1))
	m.form.SetSize(edW, max(edH-1, 1))

	y := m.resp.YOffset
	m.resp.Width = max(respW-4, 1)
	m.resp.Height = max(respH-4, 1)
	m.refreshResponse()
	m.resp.SetYOffset(y)
	m.ensureSideVisible()
	m.layoutEnvEditor()
}

// View --------------------------------------------------------------------

func (m model) View() string {
	if m.width == 0 {
		return ""
	}
	return clipScreen(m.render(), m.width, m.height)
}

// render draws the frame without clipping, so tests can check the layout
// really fits.
func (m model) render() string {
	reqW, reqH, respW, respH := m.paneSizes()

	req := m.pane(isRequestPane(m.focus), reqW, reqH, m.requestPaneLabels(),
		m.requestTabs(), m.activeEditorView())
	resp := m.pane(m.focus == focusResponse, respW, respH, []borderLabel{{innerX, paneKeyLabel(focusResponse)}},
		m.responseHeader(respW-4), m.respContentView())

	var panes string
	if m.stacked() {
		panes = lipgloss.JoinVertical(lipgloss.Left, req, resp)
	} else {
		panes = lipgloss.JoinHorizontal(lipgloss.Top, req, resp)
	}
	tabBar, _ := m.tabBar()
	main := lipgloss.JoinVertical(lipgloss.Left, tabBar, m.urlBar(), panes)
	if m.showSidebar {
		main = lipgloss.JoinHorizontal(lipgloss.Top, m.sidebarView(), main)
	}
	screen := lipgloss.JoinVertical(lipgloss.Left, m.titleBar(), main, m.help())
	if m.envEdit != nil {
		x, y := m.envEditorGeometry()
		screen = overlay(screen, m.envEditorView(), x, y)
	}
	if m.palette != nil {
		x, y, _ := m.paletteGeometry()
		screen = overlay(screen, m.paletteView(), x, y)
	}
	return screen
}

// clipScreen trims a frame to the terminal. A frame that's too tall or has
// a line that wraps scrolls the terminal, pushing the title bar off the top
// and making it flicker; clipping keeps a layout slip at the bottom edge.
func clipScreen(s string, w, h int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, l := range lines {
		if ansi.StringWidth(l) > w {
			lines[i] = ansi.Truncate(l, w, "")
		}
	}
	return strings.Join(lines, "\n")
}

func (m model) titleBar() string {
	cwd := m.ws.CWD
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(cwd, home) {
		cwd = "~" + strings.TrimPrefix(cwd, home)
	}
	left := titleStyle.Render("⚡ barq") + mutedStyle.Render("  "+cwd)
	right := m.envIndicator()
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		left = ansi.Truncate(left, max(m.width-lipgloss.Width(right)-1, 0), "…")
		gap = max(m.width-lipgloss.Width(left)-lipgloss.Width(right), 1)
	}
	return left + strings.Repeat(" ", gap) + right
}

func isRequestPane(f focus) bool {
	return f == focusParams || f == focusHeaders || f == focusBody
}

func (m model) activeEditorView() string {
	switch m.cur().reqTab {
	case focusBody:
		if m.bodyMode == bodyForm {
			return m.bodyModeLine() + "\n" + m.form.View()
		}
		return m.bodyModeLine() + "\n" + m.body.View()
	case focusParams:
		return m.params.View()
	}
	return m.headers.View()
}

func (m model) pane(focused bool, w, h int, labels []borderLabel, header, content string) string {
	return boxed(focused, w, h, labels, lipgloss.JoinVertical(lipgloss.Left, header, content))
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

	focused := m.focus == focusMethod || m.focus == focusURL
	labels := []borderLabel{{urlTextX, paneKeyLabel(focusURL)}}
	return boxed(focused, m.mainW(), 0, labels, badge+"  "+m.url.View())
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

// requestTabFocus lists the request pane's tabs in display order.
var requestTabFocus = []focus{focusParams, focusHeaders, focusBody}

func (m model) requestTabNames() []string {
	label := func(name string, n int) string {
		if n > 0 {
			return fmt.Sprintf("%s (%d)", name, n)
		}
		return name
	}
	params := 0
	for _, r := range m.params.Rows() {
		if r.enabled && r.key != "" {
			params++
		}
	}
	body := "Body"
	if m.bodyMode == bodyForm {
		body = "Body (form)"
	}
	return []string{label("Params", params), label("Headers", m.headers.EnabledCount()), body}
}

func (m model) requestTabs() string {
	active := 0
	for i, f := range requestTabFocus {
		if m.cur().reqTab == f {
			active = i
		}
	}
	return tabs(m.requestTabNames(), active) + "\n"
}

func (m model) help() string {
	var line string
	switch {
	case m.prompt != nil:
		line = " " + m.prompt.view()
	case m.palette != nil:
		line = mutedStyle.Render(" ↑/↓ select • enter run • esc close")
	case m.moving != nil:
		name := ""
		if m.moving.kind == rowFolder {
			name = m.ws.Folders[m.ws.findFolder(m.moving.id)].Name
		} else {
			name = m.ws.Requests[m.ws.find(m.moving.id)].displayName()
		}
		line = lipgloss.NewStyle().Bold(true).Foreground(colorAccent).Render(" Moving “"+name+"”") +
			mutedStyle.Render(" — select a folder (or top level) and press enter, or click it • esc cancel")
	case m.notice != "":
		line = " " + m.notice
	default:
		var keys string
		switch m.focus {
		case focusSidebar:
			keys = "/ filter • ↑/↓ select • enter open/fold • ←/→ fold • f new folder • m move • r rename • d delete • n new tab"
			if m.sideFiltering {
				keys = "type to filter • ↑/↓ select • enter open • esc clear"
			} else if m.sideFilter.Value() != "" {
				keys = "/ edit filter • esc clear filter • ↑/↓ select • enter open • m move • r rename • d delete"
			}
		case focusMethod:
			keys = "←/→ change method • g/p/d… jump"
		case focusURL:
			keys = "enter send"
		case focusParams:
			keys = "enter next cell • ↑/↓ row • ctrl+t toggle • ctrl+d delete row • synced with the URL"
		case focusHeaders:
			keys = "enter next cell • ↑/↓ row • tab complete • ctrl+t toggle • ctrl+d delete row"
		case focusBody:
			keys = "alt+m raw/form-data • ctrl+x ctrl+e edit in $EDITOR • ctrl+r send"
			if m.bodyMode == bodyForm {
				keys = "value @path sends a file (relative to the project dir, or absolute) • enter next cell • ctrl+t toggle • ctrl+d delete • alt+m raw"
			}
		case focusResponse:
			keys = fmt.Sprintf("↑/↓ scroll • t body/headers • / find • | jq filter • %3.0f%%", m.resp.ScrollPercent()*100)
			if t := m.cur(); t.viewing != nil {
				keys = "esc back to history • r restore this request • t Body/Headers/Request/Changes"
			} else if t.respTab == respTabHistory {
				keys = "↑/↓ select • enter view run • r restore request • d delete run • t tabs"
			}
			if m.bar != nil && m.bar.mode == barFind {
				keys = "enter/↓ next • ↑ previous • esc close"
			} else if m.bar != nil {
				keys = "type a jq filter • enter keep • esc clear"
			} else if m.cur().jq != "" {
				keys = "| edit filter • esc clear filter • / find • t body/headers"
			}
		}
		if m.chord {
			keys = chordHelp()
		}
		line = mutedStyle.Render(" " + keys + " • alt+u/p/h/b/r/s jump (shown on each pane) • ctrl+p commands • ctrl+s save • ctrl+r send • ctrl+n new tab • ctrl+w close • alt+←/→ switch tab • ctrl+b sidebar • f2 rename • ctrl+c quit")
	}
	return ansi.Truncate(line, m.width, "…")
}
