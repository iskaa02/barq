package tui

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	maxRecent      = 15
	paletteMaxRows = 12
)

type paletteItem struct {
	key      string // identifies the item in the recent list; "" = not recorded
	category string // e.g. "Tab", shown before the title
	title    string
	method   string // shown as a colored badge, for requests
	hint     string // right-aligned: shortcut, folder path…
	run      func(m *Model) tea.Cmd
	next     *paletteStep // when set, choosing the item opens this list instead
	current  bool         // preselected when the list opens, e.g. the current method
}

func (it paletteItem) searchText() string {
	return it.category + " " + it.method + " " + it.title + " " + it.hint
}

// paletteStep is one list in the palette: the root command list, or a
// follow-up choice such as "pick a folder".
type paletteStep struct {
	title       string
	placeholder string
	items       func(m Model) []paletteItem
}

type palette struct {
	input    textinput.Model
	steps    []paletteStep
	matches  []paletteItem
	sel      int
	offset   int
	maxWidth int
}

func (m *Model) openPalette() {
	in := textinput.New()
	in.Prompt = ""
	in.PlaceholderStyle = mutedStyle
	in.Focus()
	m.palette = &palette{input: in}
	m.pushStep(paletteStep{
		placeholder: "Type a command, request or tab…",
		items:       func(m Model) []paletteItem { return m.rootItems() },
	})
}

func (m *Model) pushStep(s paletteStep) {
	p := m.palette
	p.steps = append(p.steps, s)
	p.input.SetValue("")
	p.input.Placeholder = s.placeholder
	m.filterPalette()
}

func (m *Model) filterPalette() {
	p := m.palette
	step := p.steps[len(p.steps)-1]
	items := step.items(*m)
	q := strings.TrimSpace(p.input.Value())

	if q == "" {
		if len(p.steps) == 1 {
			// Recently used first, then everything in its usual order.
			rank := map[string]int{}
			for i, k := range m.ws.Recent {
				rank[k] = i + 1
			}
			sort.SliceStable(items, func(i, j int) bool {
				ri, rj := rank[items[i].key], rank[items[j].key]
				if ri == 0 || rj == 0 {
					return ri > rj
				}
				return ri < rj
			})
		}
		p.matches = items
		p.sel, p.offset = 0, 0
		for i, it := range items {
			if it.current {
				p.sel = i
			}
		}
		p.clampSel()
		return
	} else {
		type scored struct {
			it    paletteItem
			score int
		}
		var ss []scored
		for _, it := range items {
			if s, ok := fuzzyScore(q, it.searchText()); ok {
				ss = append(ss, scored{it, s})
			}
		}
		sort.SliceStable(ss, func(i, j int) bool { return ss[i].score > ss[j].score })
		p.matches = p.matches[:0]
		for _, s := range ss {
			p.matches = append(p.matches, s.it)
		}
	}
	p.sel, p.offset = 0, 0
}

// fuzzyScore matches query as a subsequence of text, case-insensitively.
// Matches at word starts and runs of consecutive matches score higher.
func fuzzyScore(query, text string) (int, bool) {
	q := []rune(strings.ToLower(query))
	t := []rune(strings.ToLower(text))
	score, qi, prev := 0, 0, -2
	for ti := 0; ti < len(t) && qi < len(q); ti++ {
		if q[qi] == ' ' {
			qi++ // spaces in the query just separate words
			if qi == len(q) {
				break
			}
		}
		if t[ti] != q[qi] {
			continue
		}
		s := 1
		if ti == 0 || !unicode.IsLetter(t[ti-1]) && !unicode.IsDigit(t[ti-1]) {
			s += 8
		}
		if ti == prev+1 {
			s += 5
		}
		score += s
		prev = ti
		qi++
	}
	for qi < len(q) && q[qi] == ' ' {
		qi++
	}
	if qi < len(q) {
		return 0, false
	}
	return score - len(t)/10, true
}

func (m *Model) recordRecent(key string) {
	if key == "" {
		return
	}
	r := slices.DeleteFunc(slices.Clone(m.ws.Recent), func(k string) bool { return k == key })
	r = append([]string{key}, r...)
	if len(r) > maxRecent {
		r = r[:maxRecent]
	}
	m.ws.Recent = r
}

// choose runs the selected item, or opens its follow-up list.
func (m *Model) choose() tea.Cmd {
	p := m.palette
	if p.sel >= len(p.matches) {
		return nil
	}
	it := p.matches[p.sel]
	if it.next != nil {
		m.pushStep(*it.next)
		return nil
	}
	if len(p.steps) == 1 {
		m.recordRecent(it.key)
	}
	m.palette = nil
	m.notice = ""
	var cmd tea.Cmd
	if it.run != nil {
		cmd = it.run(m)
	}
	m.Persist()
	return cmd
}

func (m Model) updatePalette(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.palette
	switch msg.String() {
	case "esc", "ctrl+c":
		m.palette = nil
		return m, nil
	case "ctrl+p":
		if len(p.steps) == 1 {
			m.palette = nil
			return m, nil
		}
	case "enter":
		return m, m.choose()
	case "up", "ctrl+k":
		p.sel--
	case "down", "ctrl+j", "ctrl+n":
		p.sel++
	case "pgup":
		p.sel -= paletteMaxRows
	case "pgdown":
		p.sel += paletteMaxRows
	case "backspace":
		if p.input.Value() == "" && len(p.steps) > 1 {
			p.steps = p.steps[:len(p.steps)-1]
			p.input.Placeholder = p.steps[len(p.steps)-1].placeholder
			m.filterPalette()
			return m, nil
		}
		fallthrough
	default:
		before := p.input.Value()
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(msg)
		if p.input.Value() != before {
			m.filterPalette()
		}
		return m, cmd
	}
	p.clampSel()
	return m, nil
}

func (p *palette) clampSel() {
	p.sel = min(max(p.sel, 0), max(len(p.matches)-1, 0))
	rows := min(paletteMaxRows, len(p.matches))
	if p.sel < p.offset {
		p.offset = p.sel
	}
	if p.sel >= p.offset+rows {
		p.offset = p.sel - rows + 1
	}
}

// Rendering -----------------------------------------------------------------

func (m Model) paletteWidth() int { return min(80, m.width-4) }

// paletteGeometry returns the box's top-left corner and the screen row of
// the first list item.
func (m Model) paletteGeometry() (x, y, listY int) {
	x = (m.width - m.paletteWidth()) / 2
	y = 2
	listY = y + 1 + 2 // border, input, separator
	if len(m.palette.steps) > 1 {
		listY++ // step title
	}
	return x, y, listY
}

func (m Model) paletteView() string {
	p := m.palette
	w := m.paletteWidth()
	inner := w - 4

	var b strings.Builder
	if step := p.steps[len(p.steps)-1]; len(p.steps) > 1 {
		b.WriteString(titleStyle.Render(step.title) + mutedStyle.Render("  backspace to go back") + "\n")
	}
	p.input.Width = inner - 3
	b.WriteString(lipgloss.NewStyle().Foreground(colorAccent).Render("› ") + p.input.View() + "\n")
	b.WriteString(mutedStyle.Render(strings.Repeat("─", inner)))

	if len(p.matches) == 0 {
		b.WriteString("\n" + mutedStyle.Render("  No matches"))
	}
	end := min(p.offset+paletteMaxRows, len(p.matches))
	for i := p.offset; i < end; i++ {
		it := p.matches[i]
		b.WriteString("\n")

		selected := i == p.sel
		marker := "  "
		titleS := lipgloss.NewStyle()
		if selected {
			marker = lipgloss.NewStyle().Foreground(colorAccent).Render("▌ ")
			titleS = titleS.Bold(true).Foreground(colorAccent)
		}
		left := marker
		if it.category != "" {
			left += mutedStyle.Render(it.category + ": ")
		}
		if it.method != "" {
			label := methodLabel(it.method)
			if it.title == "" {
				label = it.method
			}
			left += lipgloss.NewStyle().Bold(true).Foreground(methodColor(it.method)).Render(label) + " "
		}
		hint := mutedStyle.Render(it.hint)
		room := inner - lipgloss.Width(left) - lipgloss.Width(hint) - 2
		if room < 10 {
			hint = ""
			room = inner - lipgloss.Width(left)
		}
		title := titleS.Render(ansi.Truncate(it.title, room, "…"))
		gap := max(inner-lipgloss.Width(left)-lipgloss.Width(title)-lipgloss.Width(hint), 1)
		b.WriteString(left + title + strings.Repeat(" ", gap) + hint)
	}
	if n := len(p.matches); n > paletteMaxRows {
		b.WriteString("\n" + mutedStyle.Render(fmt.Sprintf("%*s", inner, fmt.Sprintf("%d/%d", p.sel+1, n))))
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		Padding(0, 1).
		Width(w - 2).
		Render(b.String())
}

// overlay draws fg on top of bg with its top-left corner at (x, y).
func overlay(bg, fg string, x, y int) string {
	lines := strings.Split(bg, "\n")
	for i, fl := range strings.Split(fg, "\n") {
		row := y + i
		if row < 0 || row >= len(lines) {
			continue
		}
		bl := lines[row]
		left := ansi.Truncate(bl, x, "")
		left += strings.Repeat(" ", max(x-ansi.StringWidth(left), 0))
		right := ansi.TruncateLeft(bl, x+ansi.StringWidth(fl), "")
		lines[row] = left + "\x1b[0m" + fl + "\x1b[0m" + right
	}
	return strings.Join(lines, "\n")
}

// paletteMouse handles mouse input while the palette is open: clicking an
// item runs it, clicking outside closes the palette.
func (m Model) paletteMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	p := m.palette
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		p.sel--
		p.clampSel()
		return m, nil
	case tea.MouseButtonWheelDown:
		p.sel++
		p.clampSel()
		return m, nil
	case tea.MouseButtonLeft:
	default:
		return m, nil
	}
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	x, y, listY := m.paletteGeometry()
	h := lipgloss.Height(m.paletteView())
	if msg.X < x || msg.X >= x+m.paletteWidth() || msg.Y < y || msg.Y >= y+h {
		m.palette = nil
		return m, nil
	}
	if i := p.offset + msg.Y - listY; msg.Y >= listY && i < min(len(p.matches), p.offset+paletteMaxRows) {
		p.sel = i
		return m, m.choose()
	}
	return m, nil
}
