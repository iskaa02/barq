// Package nvimpane runs Neovim embedded (nvim --embed) and draws its screen
// inside a Bubble Tea view. Nvim does all the editing; the pane only keeps a
// copy of its grid from redraw events, renders it, and forwards keys.
package nvimpane

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/neovim/go-client/nvim"
)

// RedrawMsg says the pane has a new frame to show.
type RedrawMsg struct{ Pane *Pane }

// ExitedMsg says nvim quit (e.g. :q).
type ExitedMsg struct{ Pane *Pane }

type cell struct {
	text string // "" for the right half of a wide character
	hl   int
}

type attr struct {
	fg, bg, sp                       int // -1 is the default color
	reverse, bold, italic, underline bool
	undercurl, strikethrough         bool
}

// Options configures New.
type Options struct {
	Args []string // extra nvim args, e.g. "--clean"
}

// ErrBlocked is returned by the synchronous helpers while nvim sits in a
// blocking prompt (swap-file ATTENTION, hit-enter, confirm) and would not
// answer the request. Keys and pastes still get through, so the user can
// answer the prompt in the UI.
var ErrBlocked = errors.New("nvim is waiting for input")

type Pane struct {
	v   *nvim.Nvim
	cmd *exec.Cmd

	closeOnce sync.Once

	scratchMu sync.Mutex
	scratch   map[string]nvim.Buffer

	mu         sync.Mutex
	grid       [][]cell
	w, h       int
	hl         map[int]attr
	sgr        map[int]string // rendered escape sequence per highlight id
	defFg      int
	defBg      int
	curRow     int
	curCol     int
	modeShapes []string // cursor shape per mode index
	shape      string
	focused    bool
	exited     bool

	events chan tea.Msg
}

// New starts nvim with a screen of w×h cells.
func New(w, h int, opts Options) (*Pane, error) {
	p := &Pane{
		w: max(w, 1), h: max(h, 1),
		hl: map[int]attr{}, sgr: map[int]string{},
		defFg: -1, defBg: -1, shape: "block",
		events:  make(chan tea.Msg, 1),
		scratch: map[string]nvim.Buffer{},
	}
	p.resizeGrid(p.w, p.h)

	// shortmess+=A: never show the swap-file ATTENTION prompt, which would
	// block nvim (and every request to it) until answered. Swap files stay.
	args := append([]string{"--embed", "--cmd", "set shortmess+=A"}, opts.Args...)
	cmd := exec.Command("nvim", args...)
	cmd.SysProcAttr = procAttr()
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	v, err := nvim.New(out, in, in, log.New(io.Discard, "", 0).Printf)
	if err != nil {
		killProc(cmd)
		return nil, err
	}
	go v.Serve()
	p.v, p.cmd = v, cmd
	fail := func(err error) (*Pane, error) {
		p.Close()
		return nil, err
	}
	// With --embed nvim waits for the UI to attach before running its startup,
	// but API calls are served meanwhile: do all setup first, so a prompt
	// during startup can't get in the way.
	if err := v.RegisterHandler("redraw", p.redraw); err != nil {
		return fail(err)
	}
	if err := v.RegisterHandler("barq_exit", func() { p.notify(ExitedMsg{p}) }); err != nil {
		return fail(err)
	}
	if err := v.Command(fmt.Sprintf("autocmd VimLeavePre * call rpcnotify(%d, 'barq_exit')", v.ChannelID())); err != nil {
		return fail(err)
	}
	if err := v.AttachUI(p.w, p.h, map[string]any{"rgb": true, "ext_linegrid": true}); err != nil {
		return fail(fmt.Errorf("attach to nvim: %w", err))
	}
	return p, nil
}

// Wait returns a command that delivers the pane's next RedrawMsg or ExitedMsg.
// Call it again after each one.
func (p *Pane) Wait() tea.Cmd {
	return func() tea.Msg { return <-p.events }
}

func (p *Pane) notify(msg tea.Msg) {
	if _, ok := msg.(ExitedMsg); ok {
		p.mu.Lock()
		p.exited = true
		p.mu.Unlock()
		// An exit must not be dropped behind a pending redraw.
		select {
		case <-p.events:
		default:
		}
		p.events <- msg
		return
	}
	select {
	case p.events <- msg:
	default: // a redraw is already pending; it'll show the latest grid
	}
}

// Close shuts nvim down: it closes the connection (nvim quits on EOF), gives
// it a moment, then kills its whole process group and reaps it.
func (p *Pane) Close() { p.closeOnce.Do(p.close) }

func (p *Pane) close() {
	p.v.Close()
	done := make(chan struct{})
	go func() { p.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
	}
	killProc(p.cmd) // also its children (a shim's real nvim, LSPs)
	<-done
}

// Blocking reports whether nvim is in a blocking prompt. nvim_get_mode is
// answered even then. A prompt may still start right after the check, so the
// helpers below can occasionally block anyway; this only covers prompts that
// are already up.
func (p *Pane) Blocking() bool {
	m, err := p.v.Mode()
	return err == nil && m.Blocking
}

func (p *Pane) check() error {
	if p.Blocking() {
		return ErrBlocked
	}
	return nil
}

// Lines returns the current buffer as it is now, saved or not.
func (p *Pane) Lines() ([]string, error) {
	if err := p.check(); err != nil {
		return nil, err
	}
	buf, err := p.v.CurrentBuffer()
	if err != nil {
		return nil, err
	}
	return p.BufLines(buf)
}

// BufLines returns all lines of buf.
func (p *Pane) BufLines(buf nvim.Buffer) ([]string, error) {
	if err := p.check(); err != nil {
		return nil, err
	}
	raw, err := p.v.BufferLines(buf, 0, -1, false)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(raw))
	for i, l := range raw {
		out[i] = string(l)
	}
	return out, nil
}

// OpenAsync edits the first file and adds the others as buffers, without
// waiting: the work is scheduled inside nvim, so a prompt while loading (or a
// slow file) can't block the caller. Use it at startup instead of passing files
// on the command line.
func (p *Pane) OpenAsync(files ...string) error {
	if len(files) == 0 {
		return nil
	}
	return p.ExecLua(`local files = ...
vim.schedule(function()
  vim.cmd.edit(vim.fn.fnameescape(files[1]))
  for i = 2, #files do vim.cmd.badd(vim.fn.fnameescape(files[i])) end
end)`, nil, files)
}

// Channel is nvim's RPC channel id, for vim.rpcnotify/rpcrequest back to us.
func (p *Pane) Channel() int { return p.v.ChannelID() }

// Nvim is the underlying client, for calls the pane does not wrap.
func (p *Pane) Nvim() *nvim.Nvim { return p.v }

// Handle registers fn for RPC requests and notifications named method.
// It runs on the RPC goroutine: calling back into this nvim instance
// synchronously from inside fn can deadlock, so reply from Go data or hand
// the work to another goroutine.
func (p *Pane) Handle(method string, fn any) error { return p.v.RegisterHandler(method, fn) }

// ExecLua runs Lua code with args (available as ...) and stores its return
// value in result, which may be nil.
func (p *Pane) ExecLua(code string, result any, args ...any) error {
	if err := p.check(); err != nil {
		return err
	}
	return p.v.ExecLua(code, result, args...)
}

// Command runs an Ex command.
func (p *Pane) Command(cmd string) error {
	if err := p.check(); err != nil {
		return err
	}
	return p.v.Command(cmd)
}

// Open edits path in the current window and returns its buffer.
func (p *Pane) Open(path string) (nvim.Buffer, error) {
	if err := p.check(); err != nil {
		return 0, err
	}
	var esc string
	if err := p.v.Call("fnameescape", &esc, path); err != nil {
		return 0, err
	}
	if err := p.v.Command("edit " + esc); err != nil {
		return 0, err
	}
	return p.v.CurrentBuffer()
}

// Current returns the current buffer, its full path ("" if unnamed) and the
// 0-based cursor line and column (in bytes).
func (p *Pane) Current() (buf nvim.Buffer, path string, line, col int, err error) {
	if err = p.check(); err != nil {
		return
	}
	if buf, err = p.v.CurrentBuffer(); err != nil {
		return
	}
	var name string
	if name, err = p.v.BufferName(buf); err != nil {
		return
	}
	if name != "" {
		if err = p.v.Call("fnamemodify", &path, name, ":p"); err != nil {
			return
		}
	}
	win, err := p.v.CurrentWindow()
	if err != nil {
		return
	}
	pos, err := p.v.WindowCursor(win)
	if err != nil {
		return
	}
	return buf, path, pos[0] - 1, pos[1], nil
}

// SetScratch shows lines in the scratch buffer called name (created on first
// use, then reused), read-only, with the given filetype.
func (p *Pane) SetScratch(name, filetype string, lines []string) (nvim.Buffer, error) {
	if err := p.check(); err != nil {
		return 0, err
	}
	p.scratchMu.Lock()
	defer p.scratchMu.Unlock()
	buf, ok := p.scratch[name]
	if ok {
		if valid, err := p.v.IsBufferValid(buf); err != nil {
			return 0, err
		} else if !valid {
			ok = false
		}
	}
	if !ok {
		var err error
		if buf, err = p.v.CreateBuffer(false, true); err != nil {
			return 0, err
		}
		for k, v := range map[string]any{"buftype": "nofile", "bufhidden": "hide", "swapfile": false} {
			if err := p.v.SetBufferOption(buf, k, v); err != nil {
				return 0, err
			}
		}
		if err := p.v.SetBufferName(buf, name); err != nil {
			return 0, err
		}
		p.scratch[name] = buf
	}
	data := make([][]byte, len(lines))
	for i, l := range lines {
		data[i] = []byte(l)
	}
	if err := p.v.SetBufferOption(buf, "modifiable", true); err != nil {
		return 0, err
	}
	if err := p.v.SetBufferLines(buf, 0, -1, false, data); err != nil {
		return 0, err
	}
	if err := p.v.SetBufferOption(buf, "modifiable", false); err != nil {
		return 0, err
	}
	if err := p.v.SetBufferOption(buf, "filetype", filetype); err != nil {
		return 0, err
	}
	return buf, p.v.SetCurrentBuffer(buf)
}

func (p *Pane) Resize(w, h int) {
	w, h = max(w, 1), max(h, 1)
	p.mu.Lock()
	same := w == p.w && h == p.h
	p.mu.Unlock()
	if !same {
		_ = p.v.TryResizeUI(w, h)
	}
}

func (p *Pane) SetFocused(f bool) {
	p.mu.Lock()
	p.focused = f
	p.mu.Unlock()
}

// Cursor is the cursor's cell inside the pane and its shape ("block",
// "vertical" or "horizontal"). ok is false when the pane is not focused or
// has exited.
func (p *Pane) Cursor() (x, y int, shape string, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.focused || p.exited {
		return 0, 0, "", false
	}
	return p.curCol, p.curRow, p.shape, true
}

// Key sends a key press to nvim.
func (p *Pane) Key(msg tea.KeyPressMsg) {
	if k := keyNotation(msg); k != "" {
		_, _ = p.v.Input(k)
	}
}

// Paste sends pasted text to nvim.
func (p *Pane) Paste(s string) { _, _ = p.v.Paste(s, true, -1) }

// Redraw events ------------------------------------------------------------

func (p *Pane) redraw(updates ...[]any) {
	p.mu.Lock()
	flushed := false
	for _, u := range updates {
		if len(u) == 0 {
			continue
		}
		name, _ := u[0].(string)
		for _, a := range u[1:] {
			args, _ := a.([]any)
			p.event(name, args)
		}
		flushed = flushed || name == "flush"
	}
	p.mu.Unlock()
	if flushed {
		p.notify(RedrawMsg{p})
	}
}

func (p *Pane) event(name string, a []any) {
	switch name {
	case "grid_resize":
		if len(a) >= 3 {
			p.resizeGrid(toInt(a[1]), toInt(a[2]))
		}
	case "grid_clear":
		for _, row := range p.grid {
			for x := range row {
				row[x] = cell{text: " "}
			}
		}
	case "grid_cursor_goto":
		if len(a) >= 3 {
			p.curRow, p.curCol = toInt(a[1]), toInt(a[2])
		}
	case "grid_line":
		if len(a) >= 4 {
			p.gridLine(toInt(a[1]), toInt(a[2]), a[3])
		}
	case "grid_scroll":
		if len(a) >= 7 {
			p.scroll(toInt(a[1]), toInt(a[2]), toInt(a[3]), toInt(a[4]), toInt(a[5]))
		}
	case "default_colors_set":
		if len(a) >= 2 {
			p.defFg, p.defBg = toInt(a[0]), toInt(a[1])
			p.sgr = map[int]string{}
		}
	case "hl_attr_define":
		if len(a) >= 2 {
			m, _ := a[1].(map[string]any)
			p.hl[toInt(a[0])] = parseAttr(m)
			delete(p.sgr, toInt(a[0]))
		}
	case "mode_info_set":
		if len(a) >= 2 {
			infos, _ := a[1].([]any)
			p.modeShapes = p.modeShapes[:0]
			for _, i := range infos {
				m, _ := i.(map[string]any)
				s, _ := m["cursor_shape"].(string)
				p.modeShapes = append(p.modeShapes, s)
			}
		}
	case "mode_change":
		if len(a) >= 2 {
			if i := toInt(a[1]); i >= 0 && i < len(p.modeShapes) {
				p.shape = p.modeShapes[i]
			}
		}
	}
}

func (p *Pane) resizeGrid(w, h int) {
	g := make([][]cell, h)
	for y := range g {
		g[y] = make([]cell, w)
		for x := range g[y] {
			g[y][x] = cell{text: " "}
			if y < len(p.grid) && x < len(p.grid[y]) {
				g[y][x] = p.grid[y][x]
			}
		}
	}
	p.grid, p.w, p.h = g, w, h
}

func (p *Pane) gridLine(row, col int, cells any) {
	if row < 0 || row >= len(p.grid) {
		return
	}
	line := p.grid[row]
	hl := 0
	list, _ := cells.([]any)
	for _, c := range list {
		parts, _ := c.([]any)
		if len(parts) == 0 {
			continue
		}
		text, _ := parts[0].(string)
		if len(parts) > 1 {
			hl = toInt(parts[1]) // otherwise the previous cell's
		}
		repeat := 1
		if len(parts) > 2 {
			repeat = toInt(parts[2])
		}
		for range repeat {
			if col >= 0 && col < len(line) {
				line[col] = cell{text: text, hl: hl}
			}
			col++
		}
	}
}

func (p *Pane) scroll(top, bot, left, right, rows int) {
	copyRow := func(dst, src int) {
		if dst < 0 || src < 0 || dst >= len(p.grid) || src >= len(p.grid) {
			return
		}
		copy(p.grid[dst][left:min(right, len(p.grid[dst]))], p.grid[src][left:min(right, len(p.grid[src]))])
	}
	if rows > 0 {
		for y := top; y < bot-rows; y++ {
			copyRow(y, y+rows)
		}
	} else {
		for y := bot - 1; y >= top-rows; y-- {
			copyRow(y, y+rows)
		}
	}
}

func parseAttr(m map[string]any) attr {
	a := attr{fg: -1, bg: -1, sp: -1}
	for k, v := range m {
		switch k {
		case "foreground":
			a.fg = toInt(v)
		case "background":
			a.bg = toInt(v)
		case "special":
			a.sp = toInt(v)
		case "reverse":
			a.reverse = v == true
		case "bold":
			a.bold = v == true
		case "italic":
			a.italic = v == true
		case "underline", "underdouble", "underdotted", "underdashed":
			a.underline = a.underline || v == true
		case "undercurl":
			a.undercurl = v == true
		case "strikethrough":
			a.strikethrough = v == true
		}
	}
	return a
}

func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int8:
		return int(n)
	case int16:
		return int(n)
	case int32:
		return int(n)
	case int64:
		return int(n)
	case uint:
		return int(n)
	case uint8:
		return int(n)
	case uint16:
		return int(n)
	case uint32:
		return int(n)
	case uint64:
		return int(n)
	}
	return 0
}

// Rendering ----------------------------------------------------------------

// View renders the grid with 24-bit color escapes.
func (p *Pane) View() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.exited {
		return "nvim exited"
	}
	var b strings.Builder
	for y, row := range p.grid {
		last := ""
		for _, c := range row {
			if c.text == "" {
				continue // covered by the wide character before it
			}
			s := p.style(c.hl)
			if s != last {
				b.WriteString("\x1b[0m" + s)
				last = s
			}
			b.WriteString(c.text)
		}
		b.WriteString("\x1b[0m")
		if y < len(p.grid)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (p *Pane) style(id int) string {
	if s, ok := p.sgr[id]; ok {
		return s
	}
	a, ok := p.hl[id]
	if !ok { // id 0, the default colors, is never defined
		a = attr{fg: -1, bg: -1, sp: -1}
	}
	fg, bg := a.fg, a.bg
	if fg < 0 {
		fg = p.defFg
	}
	if bg < 0 {
		bg = p.defBg
	}
	if a.reverse {
		fg, bg = bg, fg
	}
	var b strings.Builder
	if fg >= 0 {
		b.WriteString(rgb(38, fg))
	}
	if bg >= 0 {
		b.WriteString(rgb(48, bg))
	}
	for _, f := range []struct {
		on   bool
		code string
	}{{a.bold, "1"}, {a.italic, "3"}, {a.underline, "4"}, {a.undercurl, "4:3"}, {a.strikethrough, "9"}} {
		if f.on {
			b.WriteString("\x1b[" + f.code + "m")
		}
	}
	p.sgr[id] = b.String()
	return p.sgr[id]
}

func rgb(kind, c int) string {
	return "\x1b[" + strconv.Itoa(kind) + ";2;" + strconv.Itoa(c>>16&0xff) + ";" +
		strconv.Itoa(c>>8&0xff) + ";" + strconv.Itoa(c&0xff) + "m"
}

// Keys ---------------------------------------------------------------------

var keyNames = map[rune]string{
	tea.KeyEnter: "CR", tea.KeyEscape: "Esc", tea.KeyBackspace: "BS", tea.KeyTab: "Tab",
	tea.KeyDelete: "Del", tea.KeyInsert: "Insert", tea.KeyUp: "Up", tea.KeyDown: "Down",
	tea.KeyLeft: "Left", tea.KeyRight: "Right", tea.KeyHome: "Home", tea.KeyEnd: "End",
	tea.KeyPgUp: "PageUp", tea.KeyPgDown: "PageDown", tea.KeySpace: "Space",
	tea.KeyKpEnter: "kEnter",
}

// keyNotation turns a Bubble Tea key into nvim's <...> notation.
func keyNotation(msg tea.KeyPressMsg) string {
	k := tea.Key(msg)
	mods := ""
	for _, m := range []struct {
		mod tea.KeyMod
		s   string
	}{{tea.ModCtrl, "C-"}, {tea.ModAlt, "M-"}, {tea.ModShift, "S-"}, {tea.ModSuper, "D-"}} {
		if k.Mod&m.mod != 0 {
			mods += m.s
		}
	}
	name, special := keyNames[k.Code]
	if !special && k.Code >= tea.KeyF1 && k.Code <= tea.KeyF20 {
		name, special = "F"+strconv.Itoa(int(k.Code-tea.KeyF1)+1), true
	}
	if special {
		if mods == "" && k.Code == tea.KeySpace {
			return " "
		}
		return "<" + mods + name + ">"
	}
	// Printable. Without ctrl/alt/super the typed text is already right.
	if k.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModSuper) == 0 {
		text := k.Text
		if text == "" && k.Code > 0 && k.Code != tea.KeyExtended && unicode.IsPrint(k.Code) {
			text = string(k.Code)
		}
		return strings.ReplaceAll(text, "<", "<lt>")
	}
	base := string(k.Code)
	if k.Text != "" && k.Mod&tea.ModShift != 0 { // shift is in the character
		base, mods = k.Text, strings.ReplaceAll(mods, "S-", "")
	}
	switch base {
	case "<":
		base = "lt"
	case "\\":
		base = "Bslash"
	}
	return "<" + mods + base + ">"
}

// Pid is nvim's process id.
func (p *Pane) Pid() int { return p.cmd.Process.Pid }
