package main

import (
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Screen geometry, mirroring View(): a title row, then the sidebar beside
// the main area (tab bar, 3-row URL bar, panes), then the help row. Inside a
// bordered pane, content starts 2 columns in (border + padding), the tab row
// is the first inner row, and editors/viewports start two rows below it.
const (
	tabBarRow   = 1
	urlBarTop   = tabBarRow + 1
	panesTop    = urlBarTop + 3
	innerX      = 2
	contentRowY = 2
	urlTextX    = innerX + methodWidth + 2
)

// hitTab returns the index of the tab label at column x, given the labels as
// rendered by tabs(): names joined by a 5-column "  │  " separator.
func hitTab(names []string, x int) int {
	start := 0
	for i, n := range names {
		end := start + lipgloss.Width(n)
		if x >= start && x < end {
			return i
		}
		start = end + 5
	}
	return -1
}

type region int

const (
	regionNone region = iota
	regionSidebar
	regionTabBar
	regionURLBar
	regionRequest
	regionResponse
)

// hit reports which region (x, y) falls in, and the coordinates relative to
// that region's inner content area.
func (m model) hit(x, y int) (region, int, int) {
	if y < tabBarRow || y >= m.height-1 {
		return regionNone, 0, 0
	}
	if sw := m.sidebarW(); x < sw {
		return regionSidebar, x - innerX, y - tabBarRow - 1
	}
	x -= m.sidebarW()
	if y == tabBarRow {
		return regionTabBar, x, 0
	}
	if y >= urlBarTop && y < panesTop {
		return regionURLBar, x - innerX, y - urlBarTop - 1
	}
	reqW, reqH, _, _ := m.paneSizes()
	if y < panesTop {
		return regionNone, 0, 0
	}
	if m.stacked() {
		if y < panesTop+reqH {
			return regionRequest, x - innerX, y - panesTop - 1
		}
		return regionResponse, x - innerX, y - panesTop - reqH - 1
	}
	if x < reqW {
		return regionRequest, x - innerX, y - panesTop - 1
	}
	return regionResponse, x - reqW - innerX, y - panesTop - 1
}

func (m model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	// The environment label at the right of the title bar opens the switcher.
	if msg.Y == 0 && msg.Button == tea.MouseButtonLeft && msg.X >= m.width-lipgloss.Width(m.envIndicator()) {
		m.openPalette()
		m.pushStep(envStep)
		return m, nil
	}
	reg, px, py := m.hit(msg.X, msg.Y)

	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		up := msg.Button == tea.MouseButtonWheelUp
		switch reg {
		case regionSidebar:
			if up {
				m.sideSel--
			} else {
				m.sideSel++
			}
			m.ensureSideVisible()
		case regionTabBar:
			if up {
				m.switchTab(m.active - 1)
			} else {
				m.switchTab(m.active + 1)
			}
		case regionResponse:
			var cmd tea.Cmd
			m.resp, cmd = m.resp.Update(msg)
			return m, cmd
		case regionRequest:
			switch {
			case m.cur().reqTab == focusParams && up:
				m.params.Wheel(-1)
			case m.cur().reqTab == focusParams:
				m.params.Wheel(1)
			case m.cur().reqTab == focusHeaders && up:
				m.headers.Wheel(-1)
			case m.cur().reqTab == focusHeaders:
				m.headers.Wheel(1)
			case m.bodyMode == bodyForm && up:
				m.form.Wheel(-1)
			case m.bodyMode == bodyForm:
				m.form.Wheel(1)
			case up:
				m.body.CursorUp()
			default:
				m.body.CursorDown()
			}
		}
		return m, nil

	case tea.MouseButtonMiddle:
		if reg == regionTabBar {
			m.tabBarClick(px, true)
		}
		return m, nil

	case tea.MouseButtonLeft:
	default:
		return m, nil
	}

	switch reg {
	case regionSidebar:
		m.sidebarClick(py)

	case regionTabBar:
		m.tabBarClick(px, false)

	case regionURLBar:
		if px < methodWidth {
			if m.focus == focusMethod {
				m.methodIdx = (m.methodIdx + 1) % len(methods)
			}
			m.setFocus(focusMethod)
			return m, nil
		}
		m.setFocus(focusURL)
		// The input scrolls horizontally when the value overflows, so only
		// place the cursor when the whole value is visible.
		if v := []rune(m.url.Value()); len(v) < m.url.Width {
			m.url.SetCursor(min(max(msg.X-m.sidebarW()-urlTextX, 0), len(v)))
		}

	case regionRequest:
		if py == 0 {
			if i := hitTab(m.requestTabNames(), px); i >= 0 {
				m.setFocus(requestTabFocus[i])
			}
			return m, nil
		}
		m.setFocus(m.cur().reqTab)
		switch {
		case py < contentRowY:
		case m.cur().reqTab == focusHeaders:
			m.headers.Click(px, py-contentRowY)
		case m.cur().reqTab == focusParams:
			m.params.Click(px, py-contentRowY)
		case py == contentRowY: // body: the raw / form-data switch
			m.bodyModeClick(px)
		case m.bodyMode == bodyForm:
			m.form.Click(px, py-contentRowY-1)
		default:
			placeCursor(&m.body, py-contentRowY-1, px)
		}

	case regionResponse:
		m.setFocus(focusResponse)
		t := m.cur()
		if py == 0 {
			if i := hitTab(m.respTabNames(), px); i >= 0 && i != m.activeRespTab() {
				m.setRespTab(i)
			}
		} else if t.viewing == nil && t.respTab == respTabHistory && py >= contentRowY {
			// Click selects a run; clicking the selected one opens it.
			if i := m.resp.YOffset + py - contentRowY - histListTop; i >= 0 && i < len(m.runs()) {
				if i == t.histSel {
					m.viewRun(m.runs()[i].ID)
				} else {
					t.histSel = i
					m.refreshResponse()
				}
			}
		}
	}
	return m, nil
}

// placeCursor moves the editor cursor to a clicked (row, col) in its view.
// The textarea doesn't expose its scroll offset, so this only acts when the
// content fits on screen without scrolling or soft-wrapping.
func placeCursor(ta *textarea.Model, row, col int) {
	lines := strings.Split(ta.Value(), "\n")
	if len(lines) > ta.Height() {
		return
	}
	for _, l := range lines {
		if ansi.StringWidth(l) >= ta.Width() {
			return
		}
	}
	target := min(row, len(lines)-1)
	for i := 0; ta.Line() > target && i < len(lines); i++ {
		ta.CursorUp()
	}
	for i := 0; ta.Line() < target && i < len(lines); i++ {
		ta.CursorDown()
	}
	if ta.ShowLineNumbers {
		col -= lineNumberGutter
	}
	ta.SetCursor(max(col, 0))
}
