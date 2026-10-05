package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The sidebar filter (/) narrows the tree to requests whose name, method,
// URL or folder path contain every word typed. Matches are shown inside
// their folders, unfolded for the filter only.

func newSideFilter() textinput.Model {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = "name, method, url, folder…"
	in.PlaceholderStyle = mutedStyle
	return in
}

func (m Model) filterTerms() []string {
	return strings.Fields(strings.ToLower(m.sideFilter.Value()))
}

// filterShown reports whether the filter line is on screen.
func (m Model) filterShown() bool {
	return m.sideFiltering || m.sideFilter.Value() != ""
}

func containsAll(haystack string, terms []string) bool {
	for _, t := range terms {
		if !strings.Contains(haystack, t) {
			return false
		}
	}
	return true
}

// filterMatches returns the requests matching the filter, and the folders
// to show: those holding a match, and those whose own path matches.
func (m Model) filterMatches(terms []string) (reqs, folders map[string]bool) {
	reqs, folders = map[string]bool{}, map[string]bool{}
	showWithParents := func(id string) {
		for i := m.ws.FindFolder(id); i >= 0 && !folders[m.ws.Folders[i].ID]; i = m.ws.FindFolder(m.ws.Folders[i].Parent) {
			folders[m.ws.Folders[i].ID] = true
		}
	}
	for _, f := range m.ws.Folders {
		if containsAll(strings.ToLower(m.ws.FolderPath(f.ID)), terms) {
			showWithParents(f.ID)
		}
	}
	for _, r := range m.ws.Requests {
		hay := strings.ToLower(strings.Join([]string{r.DisplayName(), r.Method, r.URL, m.ws.FolderPath(r.Folder)}, " "))
		if containsAll(hay, terms) {
			reqs[r.ID] = true
			showWithParents(r.Folder)
		}
	}
	return reqs, folders
}

func (m *Model) startFilter() {
	m.sideFiltering = true
	m.sideFilter.Focus()
	m.sideFilter.CursorEnd()
}

func (m *Model) clearFilter() {
	m.sideFilter.SetValue("")
	m.sideFiltering = false
	m.sideFilter.Blur()
	m.sideSel, m.sideOffset = 0, 0
	m.revealInSidebar(m.cur().savedID)
	m.ensureSideVisible()
}

func (m *Model) stopEditingFilter() {
	m.sideFiltering = false
	m.sideFilter.Blur()
}

// selectFirstMatch puts the selection on the first matching request.
func (m *Model) selectFirstMatch() {
	m.sideSel, m.sideOffset = 0, 0
	for i, r := range m.sideRows() {
		if r.kind == rowRequest {
			m.sideSel = i
			break
		}
	}
	m.ensureSideVisible()
}

// updateFilter handles keys while the filter is being typed.
func (m *Model) updateFilter(msg tea.KeyMsg) {
	switch msg.String() {
	case "esc":
		m.clearFilter()
		return
	case "enter":
		m.stopEditingFilter()
		if row, ok := m.selectedRow(); ok && row.kind == rowRequest {
			m.activate(row)
		}
		return
	case "up", "ctrl+k":
		m.sideSel--
		m.ensureSideVisible()
		return
	case "down", "ctrl+j":
		m.sideSel++
		m.ensureSideVisible()
		return
	case "pgup":
		m.sideSel -= m.sidebarListHeight()
		m.ensureSideVisible()
		return
	case "pgdown":
		m.sideSel += m.sidebarListHeight()
		m.ensureSideVisible()
		return
	}
	before := m.sideFilter.Value()
	m.sideFilter, _ = m.sideFilter.Update(msg)
	if m.sideFilter.Value() != before {
		m.selectFirstMatch()
	}
}

// filterLine is the sidebar's "/ query" line, with the match count.
func (m Model) filterLine(inner int) string {
	slash := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("/ ")
	if !m.sideFiltering {
		q := m.sideFilter.Value()
		line := slash + lipgloss.NewStyle().Foreground(colorAccent).Render(q)
		if hint := mutedStyle.Render("  esc clears"); lipgloss.Width(line+hint) <= inner {
			line += hint
		}
		return ansi.Truncate(line, inner, "…")
	}
	m.sideFilter.Width = max(inner-3, 4)
	return slash + m.sideFilter.View()
}

func (m Model) sidebarTitle() string {
	n := len(m.ws.Requests)
	count := fmt.Sprintf(" · %d", n)
	if m.sideFilter.Value() != "" {
		reqs, _ := m.filterMatches(m.filterTerms())
		count = fmt.Sprintf(" · %d/%d", len(reqs), n)
	}
	return titleStyle.Render("Saved") + mutedStyle.Render(count)
}

var filterHit = lipgloss.NewStyle().Foreground(colorYellow).Bold(true).Underline(true)

// highlightTerms styles s with base, marking where the filter words occur.
func highlightTerms(s string, terms []string, base lipgloss.Style) string {
	lower := strings.ToLower(s)
	if len(terms) == 0 || len(lower) != len(s) {
		return base.Render(s)
	}
	hit := make([]bool, len(s))
	for _, t := range terms {
		for i := 0; ; {
			j := strings.Index(lower[i:], t)
			if j < 0 || t == "" {
				break
			}
			for k := i + j; k < i+j+len(t); k++ {
				hit[k] = true
			}
			i += j + len(t)
		}
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && hit[j] == hit[i] {
			j++
		}
		if hit[i] {
			b.WriteString(filterHit.Inherit(base).Render(s[i:j]))
		} else {
			b.WriteString(base.Render(s[i:j]))
		}
		i = j
	}
	return b.String()
}
