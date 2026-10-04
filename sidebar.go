package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const sidebarWidth = 34

// Rows inside the sidebar pane above the list: a title and a blank line.
const sidebarListTop = 2

type rowKind int

const (
	rowFolder rowKind = iota
	rowRequest
	rowTop // "top level" destination, shown only while moving
)

type sideRow struct {
	kind  rowKind
	id    string
	depth int
}

// sideItem identifies a folder or request independently of its row index.
type sideItem struct {
	kind rowKind
	id   string
}

func (m model) sidebarHeight() int { return max(m.height-2, 3) } // title and help rows

// sidebarListHeight is how many rows fit in the list.
func (m model) sidebarListHeight() int { return max(m.sidebarHeight()-2-sidebarListTop, 1) }

// sideRows flattens the folder tree into the visible rows: at each level,
// folders first, then requests, both in creation order.
func (m model) sideRows() []sideRow {
	var rows []sideRow
	if m.moving != nil {
		rows = append(rows, sideRow{kind: rowTop})
	}
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		for _, f := range m.ws.Folders {
			if f.Parent != parent {
				continue
			}
			rows = append(rows, sideRow{rowFolder, f.ID, depth})
			if !f.Collapsed {
				walk(f.ID, depth+1)
			}
		}
		for _, r := range m.ws.Requests {
			if r.Folder == parent {
				rows = append(rows, sideRow{rowRequest, r.ID, depth})
			}
		}
	}
	walk("", 0)
	return rows
}

func (m model) selectedRow() (sideRow, bool) {
	rows := m.sideRows()
	if m.sideSel < 0 || m.sideSel >= len(rows) {
		return sideRow{}, false
	}
	return rows[m.sideSel], true
}

// selectItem moves the selection to an item if it's visible.
func (m *model) selectItem(kind rowKind, id string) bool {
	for i, r := range m.sideRows() {
		if r.kind == kind && r.id == id {
			m.sideSel = i
			m.ensureSideVisible()
			return true
		}
	}
	return false
}

// contextFolder is the folder the selection is in: the selected folder
// itself, or the selected request's folder. New folders and requests go here.
func (m model) contextFolder() string {
	row, ok := m.selectedRow()
	if !ok {
		return ""
	}
	switch row.kind {
	case rowFolder:
		return row.id
	case rowRequest:
		if i := m.ws.find(row.id); i >= 0 {
			return m.ws.Requests[i].Folder
		}
	}
	return ""
}

func (m *model) ensureSideVisible() {
	n := len(m.sideRows())
	m.sideSel = min(max(m.sideSel, 0), max(n-1, 0))
	h := m.sidebarListHeight()
	if m.sideSel < m.sideOffset {
		m.sideOffset = m.sideSel
	}
	if m.sideSel >= m.sideOffset+h {
		m.sideOffset = m.sideSel - h + 1
	}
	m.sideOffset = max(min(m.sideOffset, n-h), 0)
}

func (m *model) setCollapsed(id string, collapsed bool) {
	if i := m.ws.findFolder(id); i >= 0 && m.ws.Folders[i].Collapsed != collapsed {
		m.ws.Folders[i].Collapsed = collapsed
		m.ensureSideVisible()
		m.persist()
	}
}

// activate opens a request or toggles a folder.
func (m *model) activate(row sideRow) {
	switch row.kind {
	case rowFolder:
		if i := m.ws.findFolder(row.id); i >= 0 {
			m.setCollapsed(row.id, !m.ws.Folders[i].Collapsed)
		}
	case rowRequest:
		m.openSaved(m.ws.find(row.id))
	}
}

func (m *model) updateSidebar(msg tea.KeyMsg) {
	rows := m.sideRows()
	row, ok := m.selectedRow()

	if m.moving != nil {
		switch msg.String() {
		case "enter", "m", " ":
			if ok {
				m.dropMoving(row)
			}
			return
		}
	}

	switch msg.String() {
	case "up", "k":
		m.sideSel--
	case "down", "j":
		m.sideSel++
	case "home", "g":
		m.sideSel = 0
	case "end", "G":
		m.sideSel = len(rows) - 1
	case "pgup":
		m.sideSel -= m.sidebarListHeight()
	case "pgdown":
		m.sideSel += m.sidebarListHeight()
	case "enter", " ", "o":
		if ok {
			m.activate(row)
		}
		return
	case "right", "l":
		if ok && row.kind == rowFolder {
			if i := m.ws.findFolder(row.id); m.ws.Folders[i].Collapsed {
				m.setCollapsed(row.id, false)
			} else {
				m.sideSel++
			}
		}
	case "left", "h":
		if !ok {
			break
		}
		if i := m.ws.findFolder(row.id); row.kind == rowFolder && !m.ws.Folders[i].Collapsed {
			m.setCollapsed(row.id, true)
			break
		}
		// Jump to the parent folder.
		parent := m.contextFolder()
		if row.kind == rowFolder {
			parent = m.ws.Folders[m.ws.findFolder(row.id)].Parent
		}
		m.selectItem(rowFolder, parent)
	case "n":
		m.openTab(request{Method: "GET"}, "")
		return
	case "f":
		parent := m.contextFolder()
		label := "New folder:"
		if parent != "" {
			label = "New folder in " + m.ws.folderPath(parent) + ":"
		}
		m.ask(promptNewFolder, label, "", 0)
		m.prompt.id = parent
	case "r":
		if !ok {
			break
		}
		if row.kind == rowFolder {
			m.ask(promptRenameFolder, "Rename folder to:", m.ws.Folders[m.ws.findFolder(row.id)].Name, 0)
		} else {
			m.ask(promptRename, "Rename to:", m.ws.Requests[m.ws.find(row.id)].displayName(), 0)
		}
		m.prompt.id = row.id
	case "d", "x", "delete":
		if !ok {
			break
		}
		if row.kind == rowFolder {
			f := m.ws.Folders[m.ws.findFolder(row.id)]
			label := fmt.Sprintf("Delete folder “%s”?", f.Name)
			if n := m.ws.countIn(f.ID); n > 0 {
				label = fmt.Sprintf("Delete folder “%s” and the %d request(s) in it?", f.Name, n)
			}
			m.ask(promptDeleteFolder, label, "", 0)
		} else {
			m.ask(promptDelete, fmt.Sprintf("Delete “%s”?", m.ws.Requests[m.ws.find(row.id)].displayName()), "", 0)
		}
		m.prompt.id = row.id
	case "m":
		if ok && row.kind != rowTop {
			m.moving = &sideItem{row.kind, row.id}
			m.sideSel++ // account for the "top level" row that just appeared
		}
	}
	m.ensureSideVisible()
}

// Folder operations -----------------------------------------------------------

func (m *model) newFolder(parent, name string) {
	f := folder{ID: newID(), Name: name, Parent: parent}
	m.ws.Folders = append(m.ws.Folders, f)
	if i := m.ws.findFolder(parent); i >= 0 {
		m.ws.Folders[i].Collapsed = false
	}
	m.selectItem(rowFolder, f.ID)
	m.persist()
	m.flash("created folder “" + name + "”")
}

func (m *model) renameFolder(id, name string) {
	if i := m.ws.findFolder(id); i >= 0 {
		m.ws.Folders[i].Name = name
		m.persist()
	}
}

// deleteFolder removes a folder with everything in it. Open tabs of deleted
// requests are kept as unsaved drafts.
func (m *model) deleteFolder(id string) {
	i := m.ws.findFolder(id)
	if i < 0 {
		return
	}
	name := m.ws.Folders[i].Name
	gone := m.ws.subtree(id)

	var keep []request
	for _, r := range m.ws.Requests {
		if !gone[r.Folder] {
			keep = append(keep, r)
			continue
		}
		for _, t := range m.tabs {
			if t.savedID == r.ID {
				t.savedID, t.req.ID = "", ""
			}
		}
	}
	m.ws.Requests = keep

	var folders []folder
	for _, f := range m.ws.Folders {
		if !gone[f.ID] {
			folders = append(folders, f)
		}
	}
	m.ws.Folders = folders
	m.ensureSideVisible()
	m.persist()
	m.flash("deleted folder “" + name + "”")
}

// dropMoving moves the item being moved into the destination row's folder.
func (m *model) dropMoving(dest sideRow) {
	mv := *m.moving
	target := ""
	switch dest.kind {
	case rowFolder:
		target = dest.id
	case rowRequest:
		target = m.ws.Requests[m.ws.find(dest.id)].Folder
	}

	if mv.kind == rowFolder && m.ws.subtree(mv.id)[target] {
		m.notice = errorStyle.Render("can't move a folder into itself")
		return
	}
	m.moving = nil
	switch mv.kind {
	case rowFolder:
		m.ws.Folders[m.ws.findFolder(mv.id)].Parent = target
	case rowRequest:
		m.ws.Requests[m.ws.find(mv.id)].Folder = target
	}
	if i := m.ws.findFolder(target); i >= 0 {
		m.ws.Folders[i].Collapsed = false
	}
	m.selectItem(mv.kind, mv.id)
	m.persist()
	where := "top level"
	if target != "" {
		where = m.ws.folderPath(target)
	}
	m.flash("moved to " + where)
}

func (m *model) cancelMoving() {
	if m.moving == nil {
		return
	}
	mv := *m.moving
	m.moving = nil
	if !m.selectItem(mv.kind, mv.id) {
		m.ensureSideVisible()
	}
}

// Rendering -------------------------------------------------------------------

func methodLabel(method string) string {
	if method == "OPTIONS" {
		return "OPT"
	}
	if len(method) > 6 {
		return method[:6]
	}
	return method
}

func (m model) sidebarView() string {
	w, h := m.sidebarW(), m.sidebarHeight()
	inner := w - 4
	style := paneStyle
	if m.focus == focusSidebar {
		style = focusedPaneStyle
	}

	activeID := m.cur().savedID
	open := map[string]bool{}
	for _, t := range m.tabs {
		open[t.savedID] = true
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render("Saved") + mutedStyle.Render(fmt.Sprintf(" · %d", len(m.ws.Requests))))
	b.WriteString("\n")

	rows := m.sideRows()
	if len(rows) == 0 {
		b.WriteString("\n" + mutedStyle.Render(ansi.Wrap(
			"No saved requests in this directory yet.\n\nctrl+s saves the current tab. In this sidebar, f creates a folder.", inner, "")))
	}
	end := min(m.sideOffset+m.sidebarListHeight(), len(rows))
	for i := m.sideOffset; i < end; i++ {
		row := rows[i]
		selected := i == m.sideSel && m.focus == focusSidebar
		isMoving := m.moving != nil && m.moving.kind == row.kind && m.moving.id == row.id
		indent := strings.Repeat("  ", row.depth)
		b.WriteString("\n")

		// Plain text is used for the selected row so the highlight is solid.
		var plain, styled, marker string
		switch row.kind {
		case rowTop:
			plain = "⌂ top level"
			styled = mutedStyle.Render(plain)
		case rowFolder:
			f := m.ws.Folders[m.ws.findFolder(row.id)]
			arrow := "▾ "
			if f.Collapsed {
				arrow = "▸ "
			}
			name := ansi.Truncate(f.Name, max(inner-len(indent)-8, 4), "…")
			count := fmt.Sprintf(" %d", m.ws.countIn(f.ID))
			plain = indent + arrow + name + count
			styled = indent + mutedStyle.Render(arrow) + lipgloss.NewStyle().Bold(true).Render(name) + mutedStyle.Render(count)
		case rowRequest:
			r := m.ws.Requests[m.ws.find(row.id)]
			method := fmt.Sprintf("%-6s", methodLabel(r.Method))
			name := ansi.Truncate(r.displayName(), max(inner-len(indent)-8, 4), "…")
			plain = indent + method + " " + name
			ns := lipgloss.NewStyle()
			if r.ID == activeID {
				ns = ns.Foreground(colorAccent).Bold(true)
			}
			styled = indent + lipgloss.NewStyle().Bold(true).Foreground(methodColor(r.Method)).Render(method) + " " + ns.Render(name)
			if open[r.ID] {
				marker = "•"
			}
		}
		if isMoving {
			plain += " ✂"
			styled = mutedStyle.Italic(true).Render(plain)
		}

		pad := strings.Repeat(" ", max(inner-1-lipgloss.Width(plain), 0))
		if selected {
			b.WriteString(lipgloss.NewStyle().Reverse(true).Foreground(colorAccent).Render(plain+pad) + mutedStyle.Render(marker))
		} else {
			b.WriteString(styled + pad + mutedStyle.Render(marker))
		}
	}
	return style.Width(w - 2).Height(h - 2).MaxHeight(h).Render(b.String())
}

// sidebarClick handles a click on content row y of the sidebar.
func (m *model) sidebarClick(y int) {
	m.setFocus(focusSidebar)
	rows := m.sideRows()
	i := m.sideOffset + y - sidebarListTop
	if y < sidebarListTop || i >= len(rows) {
		return
	}
	m.sideSel = i
	if m.moving != nil {
		m.dropMoving(rows[i])
		return
	}
	m.activate(rows[i])
}

// revealInSidebar selects a saved request's row, if it's visible.
func (m *model) revealInSidebar(id string) {
	if id != "" && m.moving == nil {
		m.selectItem(rowRequest, id)
	}
}
