package ntui

import (
	"fmt"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"github.com/iskaa02/barq/internal/core"
	"github.com/iskaa02/barq/internal/nvimpane"
	"github.com/iskaa02/barq/internal/runner"
)

// update is the message switch; handlers for posted messages go here.
func (a *App) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.w, a.h = msg.Width, msg.Height
		a.applySizes()
	case inMsg:
		return tea.Batch(a.wait(), a.update(msg.msg))
	case runMsg:
		return msg(a)
	case nvimpane.RedrawMsg:
		if msg.Pane == a.ed || msg.Pane == a.rp {
			return msg.Pane.Wait()
		}
	case nvimpane.ExitedMsg:
		switch msg.Pane {
		case a.ed:
			return a.restartEditor()
		case a.rp:
			return a.restartResponse()
		}
	case tea.PasteMsg:
		if a.modal == nameModal {
			var cmd tea.Cmd
			a.input, cmd = a.input.Update(msg)
			return cmd
		}
		if p := a.focused(); p != nil && a.modal == noModal {
			p.Paste(msg.Content)
		}
	case tea.KeyPressMsg:
		return a.key(msg)
	case tickMsg:
		if a.sending {
			a.spin++
			return tick()
		}
	case syncMsg:
		a.syncFromDisk()
		return syncTick()
	case respMsg:
		a.onResponse(msg)
	case sendMsg:
		return a.send()
	case envMsg:
		a.setEnv(msg.ref)
	case envEditMsg:
		a.envEdit(msg.name)
	case envSaveMsg:
		a.envSave(msg.name, msg.lines)
	case envNewMsg:
		a.envNew(msg.name)
	case envDeleteMsg:
		a.envDelete(msg.name)
	case quitMsg:
		return a.quit()
	case bufsMsg:
		a.tabs = msg.tabs
	case cursorMsg:
		a.side.setActive(msg.path, msg.line)
		if a.focus != focusSide {
			a.side.reveal()
		}
	case savedMsg:
		a.rescan()
	case changedMsg:
		a.assistBufferChanged(msg.path)
	case curlMsg:
		a.copyCurl()
	case importMsg:
		a.importSaved()
	}
	return nil
}

func (a *App) quit() tea.Cmd {
	if !a.quitArmed {
		for _, t := range a.tabs {
			if t.Mod {
				a.quitArmed = true
				a.flashErr("unsaved changes in " + filepath.Base(t.Path) + " — quit again to discard")
				return nil
			}
		}
	}
	return tea.Quit
}

func (a *App) key(msg tea.KeyPressMsg) tea.Cmd {
	if a.modal != noModal {
		return a.modalKey(msg)
	}
	s := msg.String()
	if s != "ctrl+q" {
		a.quitArmed = false
	}
	a.notice = ""
	switch s {
	case "ctrl+q":
		return a.quit()
	case "alt+h":
		a.setFocus(max(a.focus-1, focusSide))
		return nil
	case "alt+l":
		a.setFocus(min(a.focus+1, focusResp))
		return nil
	case "alt+e":
		a.cycleEnv()
		return nil
	case "alt+v":
		a.envEdit("")
		return nil
	case "ctrl+enter", "alt+enter":
		return a.send()
	}
	if p := a.focused(); p != nil {
		p.Key(msg)
		return nil
	}
	return a.sideKey(msg)
}

func (a *App) sideKey(msg tea.KeyPressMsg) tea.Cmd {
	s := msg.String()
	if a.side.filtering {
		if s == "enter" {
			a.side.filtering = false
			return a.openSelected()
		}
		if s == "esc" {
			a.side.filtering = false
			a.side.clearFilter()
			return nil
		}
		a.side.filterKey(s, msg.Text)
		return nil
	}
	switch s {
	case "j", "down":
		a.side.move(1)
	case "k", "up":
		a.side.move(-1)
	case "g", "home":
		a.side.cur = 0
	case "G", "end":
		a.side.cur = max(len(a.side.rows)-1, 0)
	case "h", "left":
		a.side.left()
	case "l", "right":
		a.side.right()
	case "/":
		a.side.filtering = true
	case "esc":
		if a.side.carry != nil {
			a.side.carry = nil
			break
		}
		a.side.clearFilter()
	case "R":
		a.rescan()
	case "n":
		return a.startNew(false)
	case "f":
		return a.startNew(true)
	case "r":
		return a.startRename()
	case "d", "delete":
		a.startDelete()
	case "m", "c":
		if a.side.carry == nil {
			a.startCarry(s == "m")
			break
		}
		a.dropCarried()
	case "space", " ":
		if a.side.carry != nil {
			a.dropCarried()
		}
	case "enter":
		if a.side.carry != nil {
			a.dropCarried()
			break
		}
		return a.openSelected()
	}
	return nil
}

func (a *App) dropCarried() {
	if err := a.drop(); err != nil {
		a.flashErr(err.Error())
	}
}

// openSelected is enter on the sidebar: fold a directory or file, or open a
// request at its line and focus the editor.
func (a *App) openSelected() tea.Cmd {
	r, ok := a.side.selected()
	if !ok {
		return nil
	}
	if r.Kind != reqEntry {
		a.side.toggle(r)
		return nil
	}
	if err := a.openAt(r.Path, r.Line); err != nil {
		a.flashErr(err.Error())
		return nil
	}
	a.setFocus(focusEditor)
	return nil
}

func (a *App) modalKey(msg tea.KeyPressMsg) tea.Cmd {
	s := msg.String()
	switch a.modal {
	case confirmModal:
		p, fn := a.pending, a.confirmFn
		a.modal, a.pending, a.confirmFn = noModal, nil, nil
		if s == "y" || s == "Y" || s == "enter" {
			if fn != nil {
				if err := fn(); err != nil {
					a.flashErr(err.Error())
				}
				return nil
			}
			if p != nil {
				return a.dispatch(p)
			}
		}
	case nameModal:
		switch s {
		case "esc", "ctrl+c":
			a.modal = noModal
		case "enter":
			a.modal = noModal
			if err := a.submitPrompt(a.input.Value()); err != nil {
				a.flashErr(err.Error())
			}
		default:
			var cmd tea.Cmd
			a.input, cmd = a.input.Update(msg)
			return cmd
		}
	}
	return nil
}

func (a *App) copyCurl() {
	_, req, _, err := a.CurrentRequest()
	if err != nil {
		a.flashErr(err.Error())
		return
	}
	env := a.ws.ActiveEnv
	text, err := curlFor(req, a.cwd, func(r core.Request) (core.Request, []string) {
		return a.ws.Resolve(env, r, nil)
	})
	if err != nil {
		a.flashErr(err.Error())
		return
	}
	a.flash(copyText(text, "curl"))
}

func (a *App) importSaved() {
	written, skipped, warnings, err := runner.ImportSavedWarn(a.cwd, a.ws)
	if err != nil {
		a.flashErr(err.Error())
		return
	}
	a.rescan()
	msg := fmt.Sprintf("imported %d requests into requests/ (%d already there)", written, skipped)
	if len(warnings) > 0 {
		// The details are kept as ## comments in the written files.
		msg += fmt.Sprintf(" · %d need attention (see ## comments): %s", len(warnings), warnings[0])
	}
	a.flash(msg)
}
