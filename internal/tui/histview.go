package tui

import (
	"fmt"
	"strings"
	"time"

	udiff "github.com/aymanbagabas/go-udiff"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/iskaa02/barq/internal/core"
)

// Tabs shown while viewing a past run.
const (
	viewBody = iota
	viewHeaders
	viewRequest
	viewChanges
)

var viewTabNames = []string{"Body", "Headers", "Request", "Changes"}

// histListTop is the number of lines above the first run in the list.
const histListTop = 2

// diffLimit is the largest text the Changes tab diffs; beyond it, it only
// reports that something changed.
const diffLimit = 1 << 20

func (m Model) histKey(t *tab) string {
	if t.savedID != "" {
		return t.savedID
	}
	return t.draftKey
}

func (m Model) runs() []core.HistMeta {
	if m.hist == nil {
		return nil
	}
	return m.hist.ForKey(m.histKey(m.cur()))
}

// respTabNames are the response pane's tabs for the current mode.
func (m Model) respTabNames() []string {
	if m.cur().viewing != nil {
		return viewTabNames
	}
	hist := "History"
	if n := len(m.runs()); n > 0 {
		hist = fmt.Sprintf("History (%d)", n)
	}
	return []string{"Body", "Headers", hist}
}

func (m Model) activeRespTab() int {
	if t := m.cur(); t.viewing != nil {
		return t.viewTab
	}
	return m.cur().respTab
}

func (m *Model) setRespTab(i int) {
	t := m.cur()
	if t.viewing != nil {
		t.viewTab = i
	} else {
		t.respTab = i
	}
	m.refreshResponse()
	m.resp.GotoTop()
	if t.viewing == nil && i == respTabHistory {
		m.revealHistSel()
	}
}

func (m *Model) cycleRespTab(delta int) {
	n := len(m.respTabNames())
	m.setRespTab((m.activeRespTab() + delta + n) % n)
}

func (m *Model) beginRun(t *tab, typed, sent core.Request) {
	t.pending = m.ws.NewRun(m.histKey(t), m.tabName(m.tabIndex(t.uid)), m.ws.ActiveEnv, typed, sent)
}

func (m *Model) finishRun(t *tab, msg responseMsg) {
	e := t.pending
	t.pending = nil
	if err := m.ws.RecordRun(m.hist, e, msg.resp, msg.err); err != nil {
		m.notice = errorStyle.Render("couldn't record history: " + err.Error())
	}
	t.histSel = 0
}

// Viewing -------------------------------------------------------------------

func (m *Model) revealHistSel() {
	row := histListTop + m.cur().histSel
	if row < m.resp.YOffset+histListTop || row >= m.resp.YOffset+m.resp.Height {
		m.resp.SetYOffset(max(row-m.resp.Height/2, 0))
	}
}

func (m *Model) moveHistSel(delta int) {
	t := m.cur()
	t.histSel = min(max(t.histSel+delta, 0), max(len(m.runs())-1, 0))
	m.refreshResponse()
	m.revealHistSel()
}

func (m *Model) viewRun(id string) {
	e, err := m.hist.Load(id)
	if err != nil {
		m.notice = errorStyle.Render("couldn't load run: " + err.Error())
		return
	}
	t := m.cur()
	t.viewing, t.viewTab = e, viewBody
	t.viewPretty, t.viewChanges = "", ""
	if body, isJSON := core.PrettyBody(e.Response()); isJSON {
		t.viewPretty = highlightJSON(body)
	} else {
		t.viewPretty = body
	}
	m.refreshResponse()
	m.resp.GotoTop()
}

func (m *Model) closeRun() {
	t := m.cur()
	t.viewing = nil
	t.respTab = respTabHistory
	m.refreshResponse()
	m.revealHistSel()
}

// restoreRun loads a run's request (as typed) into the editors.
func (m *Model) restoreRun(e *core.HistEntry) {
	t := m.cur()
	r := e.Request
	r.ID, r.Name = t.req.ID, t.req.Name
	t.req = r
	keep := t.viewing
	m.loadActive()
	t.viewing = keep
	m.refreshResponse()
	m.flash("restored the request from " + runTime(e.Meta.Time) + " — unsaved until you ctrl+s")
}

func (m *Model) selectedRun() (*core.HistEntry, bool) {
	t := m.cur()
	if t.viewing != nil {
		return t.viewing, true
	}
	runs := m.runs()
	if t.histSel >= len(runs) {
		return nil, false
	}
	e, err := m.hist.Load(runs[t.histSel].ID)
	if err != nil {
		m.notice = errorStyle.Render("couldn't load run: " + err.Error())
		return nil, false
	}
	return e, true
}

// updateHistory handles keys in the response pane's History tab and while
// viewing a run. It reports whether it used the key.
func (m *Model) updateHistory(k string) bool {
	t := m.cur()
	if t.viewing != nil {
		switch k {
		case "esc", "backspace":
			m.closeRun()
		case "r":
			m.restoreRun(t.viewing)
		default:
			return false
		}
		return true
	}
	if t.respTab != respTabHistory {
		return false
	}
	runs := m.runs()
	switch k {
	case "up", "k":
		m.moveHistSel(-1)
	case "down", "j":
		m.moveHistSel(1)
	case "home", "g":
		m.moveHistSel(-len(runs))
	case "end", "G":
		m.moveHistSel(len(runs))
	case "pgup":
		m.moveHistSel(-m.resp.Height)
	case "pgdown":
		m.moveHistSel(m.resp.Height)
	case "enter":
		if t.histSel < len(runs) {
			m.viewRun(runs[t.histSel].ID)
		}
	case "r":
		if e, ok := m.selectedRun(); ok {
			m.restoreRun(e)
		}
	case "d", "delete":
		if t.histSel < len(runs) {
			id := runs[t.histSel].ID
			_ = m.hist.Remove(func(h core.HistMeta) bool { return h.ID == id })
			m.moveHistSel(0)
		}
	default:
		return false
	}
	return true
}

// Rendering -------------------------------------------------------------------

func runTime(t time.Time) string {
	if y, mo, d := time.Now().Date(); t.Year() == y && t.Month() == mo && t.Day() == d {
		return t.Format("15:04:05")
	}
	return t.Format("Jan 2 15:04")
}

func (m Model) historyListText() string {
	runs := m.runs()
	if len(runs) == 0 {
		return mutedStyle.Render("No runs yet.\n\nEvery send (ctrl+r) is recorded here with the request as typed,\n" +
			"the request as sent, and the full response.")
	}
	t := m.cur()
	var b strings.Builder
	b.WriteString(mutedStyle.Render(fmt.Sprintf("%d run(s) • enter view • r restore request • d delete", len(runs))) + "\n\n")
	for i, r := range runs {
		marker, ts := "  ", lipgloss.NewStyle()
		if i == t.histSel {
			marker = lipgloss.NewStyle().Foreground(colorAccent).Render("▌ ")
			ts = ts.Bold(true).Foreground(colorAccent)
		}
		line := marker + ts.Render(fmt.Sprintf("%-11s", runTime(r.Time))) + " "
		if r.Error != "" {
			line += errorStyle.Render("✗ " + ansi.Truncate(r.Error, 40, "…"))
		} else {
			line += lipgloss.NewStyle().Bold(true).Foreground(statusColor(r.Code)).Render(fmt.Sprintf("%-3d", r.Code)) +
				mutedStyle.Render(fmt.Sprintf("  %6s  %8s", r.Duration.Round(time.Millisecond), core.HumanSize(r.Size)))
		}
		if r.Env != "" {
			line += "  " + lipgloss.NewStyle().Foreground(colorGreen).Render(r.Env)
		}
		if prev, ok := m.hist.Previous(r.ID); ok {
			if prev.ReqHash != r.ReqHash {
				line += "  " + lipgloss.NewStyle().Foreground(colorYellow).Render("✎ edited")
			}
			if prev.Env != r.Env {
				line += "  " + lipgloss.NewStyle().Foreground(colorYellow).Render("⇄ env")
			}
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) runRequestText(e *core.HistEntry) string {
	head := lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	var b strings.Builder
	b.WriteString(head.Render("As sent"))
	if e.Meta.Env != "" {
		b.WriteString(mutedStyle.Render("  (environment " + e.Meta.Env + ")"))
	}
	b.WriteString("\n" + core.RequestText(e.Sent))
	if core.RequestText(e.Sent) != core.RequestText(e.Request) {
		b.WriteString("\n" + head.Render("As typed") + "\n" + core.RequestText(e.Request))
	}
	return strings.TrimRight(b.String(), "\n")
}

func colorDiff(d string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(d, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			b.WriteString(mutedStyle.Render(line))
		case strings.HasPrefix(line, "+"):
			b.WriteString(lipgloss.NewStyle().Foreground(colorGreen).Render(line))
		case strings.HasPrefix(line, "-"):
			b.WriteString(errorStyle.Render(line))
		case strings.HasPrefix(line, "@@"):
			b.WriteString(lipgloss.NewStyle().Foreground(colorCyan).Render(line))
		default:
			b.WriteString(line)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func responseSummary(e *core.HistEntry) string {
	if e.Meta.Error != "" {
		return "error: " + e.Meta.Error + "\n"
	}
	body, _ := core.PrettyBody(e.Response())
	return e.Meta.Status + "\n\n" + body + "\n"
}

func (m Model) runChangesText(e *core.HistEntry) string {
	prevMeta, ok := m.hist.Previous(e.Meta.ID)
	if !ok {
		return mutedStyle.Render("This is the first recorded run of this request — nothing to compare yet.")
	}
	prev, err := m.hist.Load(prevMeta.ID)
	if err != nil {
		return errorStyle.Render("couldn't load the previous run: " + err.Error())
	}
	head := lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	section := func(title, before, after string) string {
		s := head.Render(title) + "\n"
		switch {
		case before == after:
			return s + mutedStyle.Render("no changes") + "\n"
		case len(before) > diffLimit || len(after) > diffLimit:
			return s + mutedStyle.Render(fmt.Sprintf("changed — too large to diff here (%s → %s)",
				core.HumanSize(len(before)), core.HumanSize(len(after)))) + "\n"
		}
		return s + colorDiff(udiff.Unified("previous", "this run", before, after)) + "\n"
	}

	var b strings.Builder
	b.WriteString(mutedStyle.Render("Compared with the previous run at "+runTime(prev.Meta.Time)) + "\n\n")
	b.WriteString(section("Request (as typed)", core.RequestText(prev.Request), core.RequestText(e.Request)) + "\n")
	if prev.Meta.Env != e.Meta.Env {
		b.WriteString(head.Render("Environment") + "\n" +
			errorStyle.Render("-"+prev.Meta.Env) + "\n" + lipgloss.NewStyle().Foreground(colorGreen).Render("+"+e.Meta.Env) + "\n\n")
	}
	b.WriteString(section("Request (as sent)", core.RequestText(prev.Sent), core.RequestText(e.Sent)) + "\n")
	b.WriteString(section("Response", responseSummary(prev), responseSummary(e)))
	return strings.TrimRight(b.String(), "\n")
}

// viewingText renders the run being viewed.
func (m Model) viewingText() string {
	t := m.cur()
	e := t.viewing
	switch t.viewTab {
	case viewHeaders:
		if e.Meta.Error != "" {
			return errorStyle.Render("Error: " + e.Meta.Error)
		}
		var b strings.Builder
		for _, line := range strings.Split(strings.TrimRight(formatHeaders(e.Response()), "\n"), "\n") {
			k, v, _ := strings.Cut(line, ": ")
			b.WriteString(headerKeyStyle.Render(k) + ": " + v + "\n")
		}
		return strings.TrimRight(b.String(), "\n")
	case viewRequest:
		return m.runRequestText(e)
	case viewChanges:
		// Diffing can take a while on big bodies; do it once per run.
		if t.viewChanges == "" {
			t.viewChanges = m.runChangesText(e)
		}
		return t.viewChanges
	}
	if e.Meta.Error != "" {
		return errorStyle.Render("Error: " + e.Meta.Error)
	}
	body := t.viewPretty
	if body == "" {
		body = mutedStyle.Render("(empty body)")
	}
	switch r := e.Response(); {
	case e.Meta.BodyPruned:
		body = mutedStyle.Render(fmt.Sprintf("(the %s body was pruned to keep history under its size budget)", core.HumanSize(e.Meta.Size)))
	case e.BodyTruncated:
		body += "\n" + errorStyle.Render("… history kept the first 10 MB of this body")
	case r.Partial():
		body += "\n" + partialNote(r, "barq history body "+e.Meta.ID+" reads all of it")
	}
	return body
}

// viewingStatus is shown at the right of the tab row while viewing a run.
func (m Model) viewingStatus() string {
	e := m.cur().viewing
	s := mutedStyle.Render("run " + runTime(e.Meta.Time) + "  ")
	if e.Meta.Error != "" {
		return s + errorStyle.Render("error")
	}
	return s + lipgloss.NewStyle().Bold(true).Foreground(statusColor(e.Meta.Code)).Render(e.Meta.Status)
}

// Palette -------------------------------------------------------------------

func allRunItems(m Model) []paletteItem {
	if m.hist == nil {
		return nil
	}
	var items []paletteItem
	for _, r := range m.hist.All() {
		r := r
		status := fmt.Sprint(r.Code)
		if r.Error != "" {
			status = "error"
		}
		hint := status + " · " + runTime(r.Time)
		if r.Env != "" {
			hint += " · " + r.Env
		}
		// Use the saved request's current name; runs keep the name they had.
		name := r.Name
		if i := m.ws.Find(r.Key); i >= 0 {
			name = m.ws.Requests[i].DisplayName()
		}
		items = append(items, paletteItem{method: r.Method, title: name, hint: hint,
			run: func(m *Model) tea.Cmd { m.openRun(r); return nil }})
	}
	return items
}

// openRun shows a run from the global list: in the tab of its request if
// it's open or saved, otherwise in a new tab built from the run.
func (m *Model) openRun(r core.HistMeta) {
	switch i := m.ws.Find(r.Key); {
	case i >= 0:
		m.openSaved(i)
	default:
		found := false
		for j, t := range m.tabs {
			if m.histKey(t) == r.Key {
				m.switchTab(j)
				found = true
				break
			}
		}
		if !found {
			e, err := m.hist.Load(r.ID)
			if err != nil {
				m.notice = errorStyle.Render("couldn't load run: " + err.Error())
				return
			}
			req := e.Request
			req.ID, req.Name = "", r.Name
			m.openTab(req, "")
			m.cur().draftKey = r.Key
		}
	}
	m.cur().respTab = respTabHistory
	for i, h := range m.runs() {
		if h.ID == r.ID {
			m.cur().histSel = i
		}
	}
	m.viewRun(r.ID)
	m.setFocus(focusResponse)
}
