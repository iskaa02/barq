package main

import (
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Screen geometry, mirroring View(): a title row, a 3-row URL bar, then the
// panes. Inside a bordered pane, content starts 2 columns in (border +
// padding), the tab row is the first inner row, and editors/viewports start
// two rows below it.
const (
	urlBarTop   = 1
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
	regionURLBar
	regionRequest
	regionResponse
)

// hit reports which region (x, y) falls in, and the coordinates relative to
// that region's inner content area.
func (m model) hit(x, y int) (region, int, int) {
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
	reg, px, py := m.hit(msg.X, msg.Y)

	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		switch reg {
		case regionResponse:
			var cmd tea.Cmd
			m.resp, cmd = m.resp.Update(msg)
			return m, cmd
		case regionRequest:
			up := msg.Button == tea.MouseButtonWheelUp
			switch {
			case m.reqTab == focusHeaders && up:
				m.headers.Wheel(-1)
			case m.reqTab == focusHeaders:
				m.headers.Wheel(1)
			case up:
				m.body.CursorUp()
			default:
				m.body.CursorDown()
			}
		}
		return m, nil

	case tea.MouseButtonLeft:
	default:
		return m, nil
	}

	switch reg {
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
			m.url.SetCursor(min(max(msg.X-urlTextX, 0), len(v)))
		}

	case regionRequest:
		if py == 0 {
			switch hitTab(m.requestTabNames(), px) {
			case 0:
				m.setFocus(focusHeaders)
			case 1:
				m.setFocus(focusBody)
			}
			return m, nil
		}
		m.setFocus(m.reqTab)
		switch {
		case py < contentRowY:
		case m.reqTab == focusHeaders:
			m.headers.Click(px, py-contentRowY)
		default:
			placeCursor(&m.body, py-contentRowY, px)
		}

	case regionResponse:
		m.setFocus(focusResponse)
		if py == 0 {
			if i := hitTab([]string{"Body", "Headers"}, px); i >= 0 && i != m.respTab {
				m.respTab = i
				m.resp.GotoTop()
				m.refreshResponse()
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
