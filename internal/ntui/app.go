// Package ntui is barq's Neovim-based TUI (`barq`): a sidebar of .http
// files, an embedded nvim editing them, and a second embedded nvim showing
// the response.
//
// Notes for code that extends App (assist.go, respactions.go):
//
//   - Threads. Bubble Tea's Update/View run on one goroutine; App's fields are
//     only touched there. nvim RPC handlers (Pane.Handle) run on the RPC
//     goroutine and must not touch App: they call a.post(msg), which queues
//     msg for Update (add a case to App.update to handle it). Handlers may
//     read the mutex-guarded snapshots below, and may reply from them, but
//     must never call back into the same nvim synchronously. Code reached
//     from update (including the hooks) may call nvim freely.
//   - Panes. a.ed is the editor nvim, a.rp the response nvim (a.ed.Nvim()
//     gives the go-client). Both are replaced when nvim exits, so re-read the
//     field instead of keeping it. Lua is run with pane.ExecLua; pass
//     pane.Channel() as an argument for rpcnotify/rpcrequest.
//   - Env variables. a.EnvVars() is a snapshot (a copy) of the active
//     environment's enabled variables and a.EnvName() its name; both are safe
//     from any goroutine and refreshed whenever the environment changes.
//     a.ws is the workspace itself (Update goroutine only).
//   - Current request. a.CurrentRequest() reads the editor's current buffer
//     (not the file on disk) and returns the path, the request under the
//     cursor and its key. It calls nvim, so use it from update/hooks only.
//   - Responses. The key of a request is "<file path>#<name or block index>"
//     (runner.Ref.Key(): a ref path, see runner.Roots). a.LastResponses(key) returns up to two responses sent this session,
//     oldest first. Past runs (also from earlier sessions) are in core history; a.shown may be one of them (Response.Hist).
package ntui

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/iskaa02/barq/internal/core"
	"github.com/iskaa02/barq/internal/nvimpane"
	"github.com/iskaa02/barq/internal/runner"
)

type focus int

const (
	focusSide focus = iota
	focusEditor
	focusResp
)

func (f focus) String() string { return [...]string{"files", "editor", "response"}[f] }

type modalKind int

const (
	noModal modalKind = iota
	confirmModal
	nameModal
)

// App is the Bubble Tea model.
type App struct {
	ws    *core.Workspace
	cwd   string       // the project directory: "< file" and @file paths are relative to it
	store string       // ~/.barq/requests/<project>: where new requests go
	in    chan tea.Msg // messages from RPC goroutines

	mu         sync.Mutex // guards the snapshots below
	envName    string
	envNames   []string
	envVars    map[string]string
	envSecrets map[string]bool // secret names of envVars, same snapshot
	resps      map[string][]*Response

	w, h  int
	focus focus
	side  sidebar
	ed    *nvimpane.Pane
	rp    *nvimpane.Pane
	tabs  []tab
	// ownTabline: the user's nvim draws its own tabline, so barq draws no tab bar.
	ownTabline bool

	sending bool
	spin    int
	shown   *Response

	notice    string
	noticeErr bool
	quitArmed bool

	modal   modalKind
	input   textinput.Model
	pending *pendingSend

	// Sidebar actions (sideops.go): the prompt's purpose, and what a
	// confirm modal runs on y.
	promptKind, promptLabel, promptDir string
	promptItem                         item
	confirmText                        string
	confirmFn                          func() error
}

type tab struct {
	Path string `json:"path"`
	Cur  bool   `json:"cur"`
	Mod  bool   `json:"mod"`
}

// inMsg carries a message posted from an RPC goroutine.
type inMsg struct{ msg tea.Msg }

// runMsg runs on the Bubble Tea loop; post one from an RPC handler to act
// on App there: a.post(runMsg(func(a *App) tea.Cmd { ... })).
type runMsg func(*App) tea.Cmd

// post queues msg for the Bubble Tea loop. Safe from any goroutine.
func (a *App) post(msg tea.Msg) {
	select {
	case a.in <- msg:
	default: // the loop is far behind; dropping beats blocking nvim
	}
}

func (a *App) wait() tea.Cmd {
	return func() tea.Msg { return inMsg{<-a.in} }
}

// Options tune how Run starts.
type Options struct {
	File string // open this file first (instead of the first one found)
	Line int    // with File: put the cursor on this 1-based line
}

// Run starts the UI and blocks until it quits.
func Run(ws *core.Workspace, cwd string, opts Options) error {
	a := &App{ws: ws, cwd: cwd, store: ws.RequestsDir(), in: make(chan tea.Msg, 256), resps: map[string][]*Response{}}
	a.input = textinput.New()
	a.refreshEnv()
	a.side.set(scanFiles(a.roots()))
	var files []string
	if len(a.side.entries) > 0 {
		for _, e := range a.side.entries {
			if e.Kind == fileEntry {
				files = append(files, e.Path)
				break
			}
		}
	}
	if opts.File != "" {
		files = []string{opts.File}
	}
	// Every exit path (quit, signal, panic, startup error) closes both nvims;
	// Close also kills their process groups. a.ed and a.rp are replaced on
	// restart, so read them when the function returns.
	defer func() {
		if a.ed != nil {
			a.ed.Close()
		}
		if a.rp != nil {
			a.rp.Close()
		}
	}()
	if err := a.startEditor(files); err != nil {
		return fmt.Errorf("start nvim: %w", err)
	}
	if err := a.startResponse(); err != nil {
		return fmt.Errorf("start nvim: %w", err)
	}
	if len(files) > 0 {
		a.focus = focusEditor
	}
	if opts.File != "" && opts.Line > 0 {
		// Scheduled after the file opens (OpenAsync schedules too).
		_ = a.ed.ExecLua(`local l = ...
vim.schedule(function() pcall(vim.api.nvim_win_set_cursor, 0, { l, 0 }); vim.cmd('normal! zz') end)`, nil, opts.Line)
	}
	a.ed.SetFocused(a.focus == focusEditor)

	p := tea.NewProgram(a)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sig)
	go func() {
		<-sig
		p.Quit()
	}()
	_, err := p.Run()
	return err
}

func (a *App) Init() tea.Cmd {
	return tea.Batch(a.wait(), a.ed.Wait(), a.rp.Wait(), syncTick())
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := a.update(msg)
	return a, cmd
}

// Notices -------------------------------------------------------------------

func (a *App) flash(s string)    { a.notice, a.noticeErr = s, false }
func (a *App) flashErr(s string) { a.notice, a.noticeErr = s, true }

// Layout --------------------------------------------------------------------

// layout returns the outer widths of the three boxes and the body height.
func (a *App) layout() (sw, ew, rw, bh int) {
	sw = min(max(a.w/5, 18), 30)
	rest := a.w - sw
	ew = rest * 55 / 100
	return sw, ew, rest - ew, a.h - 1
}

func (a *App) applySizes() {
	_, ew, rw, bh := a.layout()
	a.ed.Resize(ew-2, bh-2-a.tabRows())
	a.rp.Resize(rw-2, bh-3)
}

// tabRows is the number of rows barq's own tab bar takes above the editor.
func (a *App) tabRows() int {
	if a.ownTabline {
		return 0
	}
	return 1
}

func (a *App) setFocus(f focus) {
	a.focus = f
	a.ed.SetFocused(f == focusEditor)
	a.rp.SetFocused(f == focusResp)
}

func (a *App) focused() *nvimpane.Pane {
	switch a.focus {
	case focusEditor:
		return a.ed
	case focusResp:
		return a.rp
	}
	return nil
}

// View ----------------------------------------------------------------------

func (a *App) View() tea.View {
	v := tea.NewView(a.content())
	v.AltScreen = true
	v.KeyboardEnhancements = tea.KeyboardEnhancements{ReportAlternateKeys: true}
	v.Cursor = a.cursor()
	return v
}

func (a *App) cursor() *tea.Cursor {
	if a.modal != noModal || a.w == 0 {
		return nil
	}
	sw, ew, _, _ := a.layout()
	p, ox, oy := a.focused(), 0, 0
	switch a.focus {
	case focusEditor:
		ox, oy = sw+1, 1+a.tabRows()
	case focusResp:
		ox, oy = sw+ew+1, 2
	}
	if p == nil {
		return nil
	}
	x, y, shape, ok := p.Cursor()
	if !ok {
		return nil
	}
	c := tea.NewCursor(ox+x, oy+y)
	c.Blink = false
	switch shape {
	case "vertical":
		c.Shape = tea.CursorBar
	case "horizontal":
		c.Shape = tea.CursorUnderline
	}
	return c
}

func (a *App) content() string {
	if a.w < 40 || a.h < 6 {
		return ""
	}
	sw, ew, rw, bh := a.layout()
	open, openSet := "", map[string]bool{}
	for _, t := range a.tabs {
		openSet[t.Path] = true
		if t.Cur {
			open = t.Path
		}
	}
	side := box(a.side.title(), sw, bh, a.side.render(sw-2, bh-2, a.focus == focusSide, openSet), a.focus == focusSide)
	edLines := strings.Split(a.ed.View(), "\n")
	if !a.ownTabline {
		edLines = append([]string{a.tabBar(ew - 2)}, edLines...)
	}
	title := "editor"
	if open != "" {
		title = a.rel(open)
	}
	ed := box(title, ew, bh, edLines, a.focus == focusEditor)
	rpLines := append([]string{viewTabs(respState.view, rw-2)}, strings.Split(a.rp.View(), "\n")...)
	rp := box(a.respTitle(), rw, bh, rpLines, a.focus == focusResp)
	return lipgloss.JoinHorizontal(lipgloss.Top, side, ed, rp) + "\n" + fit(a.footer(), a.w)
}

// roots maps between request files and their ref paths.
func (a *App) roots() runner.Roots { return runner.Roots{Project: a.cwd, Store: a.store} }

// rel is the ref path of a file (what labels, history keys and the CLI
// call it), or p itself when it is in neither root.
func (a *App) rel(p string) string {
	if r, ok := a.roots().RefPath(p); ok {
		return r
	}
	return p
}

// tabLabel is a tab's file name, with its ref path when another open tab
// has the same name (api.http in the project and in the store).
func (a *App) tabLabel(t tab) string {
	name := filepath.Base(t.Path)
	for _, o := range a.tabs {
		if o.Path != t.Path && filepath.Base(o.Path) == name {
			return a.rel(t.Path)
		}
	}
	return name
}

func (a *App) tabBar(w int) string {
	var b strings.Builder
	for _, t := range a.tabs {
		name := a.tabLabel(t)
		if t.Mod {
			name += "+"
		}
		if t.Cur {
			b.WriteString(selStyle.Render(" " + name + " "))
		} else {
			b.WriteString(muted.Render(" " + name + " "))
		}
	}
	return fit(b.String(), w)
}

func (a *App) footer() string {
	switch a.modal {
	case confirmModal:
		text := a.confirmText
		if a.pending != nil {
			text = a.pending.confirmText()
		}
		return errStyle.Render(" "+text) + " (y/n)"
	case nameModal:
		return " " + a.promptLabel + ": " + a.input.View()
	}
	hints := "alt+enter send · alt+h/l focus · alt+e env · ctrl+q quit"
	if a.focus == focusSide {
		hints = "n new · f folder · r rename · d delete · m move · c copy · / filter"
		if c := a.side.carry; c != nil {
			verb, sym := "copy", "⧉"
			if c.move {
				verb, sym = "move", "✂"
			}
			what := filepath.Base(c.item.Path)
			if c.item.Kind == reqEntry {
				what = c.item.Name
				if what == "" {
					what = "request"
				}
			}
			hints = sym + " " + verb + " " + what + " · m/enter on a target to drop · esc cancels"
		}
	}
	parts := []string{" " + a.EnvName(), a.focus.String(), muted.Render(hints)}
	if a.focus == focusResp {
		parts = append(parts, muted.Render("tab views · gc capture · gd diff"))
	}
	if a.notice != "" {
		n := a.notice
		if a.noticeErr {
			n = errStyle.Render(n)
		}
		parts = append(parts, n)
	}
	return strings.Join(parts, " · ")
}
