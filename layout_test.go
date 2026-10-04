package main

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// A frame taller or wider than the terminal makes it scroll, pushing the
// title bar off the top so it flickers. Check the unclipped frame fits in
// a range of sizes and states.
func TestFrameFitsTerminal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	longURL := "https://example.com/a/very/long/path/that/keeps/going/and/going?with=params&and=more&x=1"

	for _, w := range []int{60, 79, 80, 89, 90, 100, 109, 110, 120, 150, 200} {
		for _, h := range []int{16, 24, 40} {
			ws, _ := loadWorkspace(t.TempDir())
			var tm tea.Model = newModel(ws, nil)
			check := func(state string) {
				t.Helper()
				m := tm.(model)
				lines := strings.Split(m.render(), "\n")
				if len(lines) != h {
					t.Errorf("%dx%d %s: frame is %d lines", w, h, state, len(lines))
				}
				for i, l := range lines {
					if lw := ansi.StringWidth(l); lw > w {
						t.Errorf("%dx%d %s: line %d is %d wide: %q", w, h, state, i, lw, ansi.Strip(l))
						break
					}
				}
			}
			send := func(msg tea.Msg) { tm, _ = tm.Update(msg) }
			key := func(k tea.KeyType) { send(tea.KeyMsg{Type: k}) }

			send(tea.WindowSizeMsg{Width: w, Height: h})
			check("start")
			for _, sidebar := range []bool{false, true} {
				if tm.(model).showSidebar != sidebar {
					key(tea.KeyCtrlB)
				}
				label := fmt.Sprintf("sidebar=%v", sidebar)
				// Long URL with the cursor at the end, start and middle.
				m := tm.(model)
				m.setFocus(focusURL)
				m.url.SetValue(longURL)
				m.url.CursorEnd()
				tm = m
				send(m.url.Cursor.BlinkCmd()) // blink frames
				check(label + " url cursor end")
				key(tea.KeyHome)
				check(label + " url cursor start")
				for i := 0; i < 30; i++ {
					key(tea.KeyRight)
				}
				check(label + " url cursor middle")
				for i := 0; i < 6; i++ {
					key(tea.KeyTab)
					check(fmt.Sprintf("%s focus %d", label, tm.(model).focus))
				}
				key(tea.KeyCtrlP)
				check(label + " palette")
				key(tea.KeyEsc)
				key(tea.KeyCtrlN)
				check(label + " new tab")
			}
		}
	}
}
