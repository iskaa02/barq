package ntui

import (
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/iskaa02/barq/internal/core"
	"github.com/iskaa02/barq/internal/httpfile"
	"github.com/iskaa02/barq/internal/jsonpos"
	"github.com/neovim/go-client/nvim"
)

// Response-pane state; only touched on the Bubble Tea loop.
var respState struct {
	key  string
	view view            // the selected view; kept across responses
	runs []core.HistMeta // the History view's lines, newest first
}

// respLua installs the buffer-local maps on barq://(body|headers|request|info|history). Argument: our channel.
const respLua = `
local chan = ...
local g = vim.api.nvim_create_augroup('barq_resp', { clear = true })
local function prevwin()
  local w = vim.g.barq_prev_win
  if w and vim.api.nvim_win_is_valid(w) then return w end
end
function _G.barq_diff_close()
  local w = prevwin()
  if not w then return end
  vim.cmd('diffoff!')
  pcall(vim.api.nvim_win_close, w, true)
  vim.g.barq_prev_win = nil
end
function _G.barq_diff_open(lines, ft)
  barq_diff_close()
  local main = vim.api.nvim_get_current_win()
  local buf = vim.fn.bufnr('barq://previous')
  if buf < 0 or not vim.api.nvim_buf_is_valid(buf) then
    buf = vim.api.nvim_create_buf(false, true)
    vim.bo[buf].buftype = 'nofile'
    vim.bo[buf].bufhidden = 'hide'
    vim.bo[buf].swapfile = false
    vim.api.nvim_buf_set_name(buf, 'barq://previous')
  end
  vim.bo[buf].modifiable = true
  vim.api.nvim_buf_set_lines(buf, 0, -1, false, lines)
  vim.bo[buf].modifiable = false
  vim.bo[buf].filetype = ft
  vim.cmd('leftabove vsplit')
  local w = vim.api.nvim_get_current_win()
  vim.api.nvim_win_set_buf(w, buf)
  vim.g.barq_prev_win = w
  vim.keymap.set('n', 'q', barq_diff_close, { buffer = buf, silent = true })
  vim.cmd('diffthis')
  vim.api.nvim_set_current_win(main)
  vim.cmd('diffthis')
end
local function attach(buf, name)
  if name == 'history' then
    vim.keymap.set('n', '<CR>', function() vim.rpcnotify(chan, 'barq_hist', 'show', vim.api.nvim_win_get_cursor(0)[1] - 1) end, { buffer = buf, silent = true, nowait = true })
    vim.keymap.set('n', 'd', function() vim.rpcnotify(chan, 'barq_hist', 'diff', vim.api.nvim_win_get_cursor(0)[1] - 1) end, { buffer = buf, silent = true, nowait = true })
  end
  local o = { buffer = buf, silent = true, nowait = true }
  vim.keymap.set('n', 'gc', function()
    local p = vim.api.nvim_win_get_cursor(0)
    vim.rpcnotify(chan, 'barq_capture', p[1] - 1, p[2])
  end, o)
  local function go(k, v) vim.keymap.set('n', k, function() vim.rpcnotify(chan, 'barq_view', v) end, o) end
  go('<Tab>', 'next') go('<S-Tab>', 'prev')
  go('gb', 'body') go('gh', 'headers') go('gr', 'request') go('gi', 'info') go('gp', 'history')
  vim.keymap.set('n', 'gd', function()
    if prevwin() then barq_diff_close() else vim.rpcnotify(chan, 'barq_diff') end
  end, o)
end
function _G.barq_resp_attach()
  local b = vim.api.nvim_get_current_buf()
  local n = vim.api.nvim_buf_get_name(b):match('barq://(%a+)$')
  if n == 'body' or n == 'headers' or n == 'request' or n == 'info' or n == 'history' then attach(b, n) end
end
vim.api.nvim_create_autocmd({ 'BufEnter', 'BufWinEnter' }, { group = g, callback = barq_resp_attach })
barq_resp_attach()
`

// setupResponseActions is called after the response nvim starts (and after a
// restart). It installs the buffer-local maps Tab, S-Tab, gb/gh/gr/gi/gp, gc and gd.
func (a *App) setupResponseActions() error {
	handlers := map[string]any{
		"barq_capture": func(line, col int) {
			a.post(runMsg(func(a *App) tea.Cmd { a.captureAt(line, col); return nil }))
		},
		"barq_diff": func() {
			a.post(runMsg(func(a *App) tea.Cmd { a.diffPrevious(); return nil }))
		},
		"barq_hist": func(act string, row int) {
			a.post(runMsg(func(a *App) tea.Cmd { a.histAction(act, row); return nil }))
		},
		"barq_view": func(to string) {
			a.post(runMsg(func(a *App) tea.Cmd { a.switchView(to); return nil }))
		},
	}
	for name, fn := range handlers {
		if err := a.rp.Handle(name, fn); err != nil {
			return err
		}
	}
	return a.rp.ExecLua(respLua, nil, a.rp.Channel())
}

// responseShown is called, on the Bubble Tea loop, after a response is
// displayed. key is the request key (see App.LastResponses).
func (a *App) responseShown(key string) {
	respState.key = key
	_ = a.rp.ExecLua(`barq_diff_close()`, nil)
	_ = a.rp.ExecLua(`barq_resp_attach()`, nil)
}

// captureAt adds an @capture for the value at the cursor: a body value in
// the Body view, a header or cookie in the Headers view.
func (a *App) captureAt(line, col int) {
	r := a.shown
	if r == nil || r.Res == nil {
		a.flashErr("no response to capture from")
		return
	}
	var name, filter string
	switch respState.view {
	case viewHeaders:
		hl := headerLines(r.Res)
		if line < 1 || line >= len(hl) {
			a.flashErr("put the cursor on a header line")
			return
		}
		var ok bool
		if name, filter, ok = headerCapture(hl[line]); !ok {
			a.flashErr("put the cursor on a header line")
			return
		}
	case viewBody:
		path, ok := "", false
		if r.FT == "json" {
			path, ok = jsonpos.PathAt(strings.Join(r.Lines, "\n"), line, col)
		}
		if !ok {
			a.flashErr("capture needs a JSON response")
			return
		}
		name, filter = jsonpos.VarName(path), path
	default:
		a.flashErr("capture works on the Body (gb) and Headers (gh) views")
		return
	}
	if err := a.addCapture(r.Key, name, filter); err != nil {
		a.flashErr(err.Error())
		return
	}
	a.flash("@capture " + name + " = " + filter)
}

// headerCapture turns a "Name: value" header line into a capture variable and
// filter: "header Name", or for Set-Cookie "cookie <cookie-name>".
func headerCapture(line string) (name, filter string, ok bool) {
	h, v, found := strings.Cut(line, ":")
	h = strings.TrimSpace(h)
	if !found || h == "" || strings.ContainsAny(h, " \t") {
		return "", "", false
	}
	if strings.EqualFold(h, "Set-Cookie") {
		c, _, _ := strings.Cut(strings.TrimSpace(v), "=")
		c = strings.TrimSpace(c)
		if c == "" {
			return "", "", false
		}
		return identOf(c), "cookie " + c, true
	}
	parts := strings.Split(strings.ToLower(h), "-")
	for i := 1; i < len(parts); i++ {
		parts[i] = strings.ToUpper(parts[i][:min(1, len(parts[i]))]) + parts[i][min(1, len(parts[i])):]
	}
	return identOf(strings.Join(parts, "")), "header " + h, true
}

// identOf replaces anything that is not a letter, digit or _ with _.
func identOf(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return r
		}
		return '_'
	}, s)
}

// addCapture writes the directive into the editor buffer of the key's file.
func (a *App) addCapture(key, name, filter string) error {
	i := strings.LastIndex(key, "#")
	if i < 0 {
		return errors.New("bad request key")
	}
	path := a.roots().Abs(key[:i])
	nv := a.ed.Nvim()
	var n int
	if err := a.ed.ExecLua(`local b = vim.fn.bufnr(...); if b > 0 and vim.api.nvim_buf_is_loaded(b) then return b end; return -1`, &n, path); err != nil {
		return err
	}
	if n < 0 {
		return errors.New("open " + a.rel(path) + " in the editor first")
	}
	buf := nvim.Buffer(n)
	lines, err := a.ed.BufLines(buf)
	if err != nil {
		return err
	}
	reqs := httpfile.Parse(strings.Join(lines, "\n"))
	for j, r := range reqs {
		if a.requestKey(path, reqs, j) != key {
			continue
		}
		at, replace := captureEdit(lines, r, name, filter)
		end := at
		if replace {
			end++
		}
		return nv.SetBufferLines(buf, at, end, false, [][]byte{[]byte(captureLine(name, filter))})
	}
	return errors.New("request not found in the editor buffer")
}

func captureLine(name, filter string) string { return "# @capture " + name + " = " + filter }

// captureEdit says where the capture directive for name goes in lines: the
// index of an existing directive to replace, else the request line (insert).
func captureEdit(lines []string, r httpfile.Request, name, filter string) (at int, replace bool) {
	for i := r.Start; i < r.Line && i < len(lines); i++ {
		s := strings.TrimSpace(lines[i])
		rest, ok := strings.CutPrefix(s, "# @capture ")
		if !ok {
			continue
		}
		if v, _, _ := strings.Cut(rest, "="); strings.TrimSpace(v) == name {
			return i, true
		}
	}
	return r.Line, false
}

// diffPrevious opens the run before the shown one (from history) beside it.
func (a *App) diffPrevious() {
	r := a.shown
	if r == nil || r.RunID == "" {
		a.flash("no response to diff")
		return
	}
	h, err := core.OpenHistory(a.ws)
	if err != nil {
		a.flashErr(err.Error())
		return
	}
	m, ok := h.Previous(r.RunID)
	if !ok {
		a.flash("no previous response to diff")
		return
	}
	a.diffWith(h, m.ID, r)
}

// diffWith shows r's body and opens run id's body beside it.
func (a *App) diffWith(h *core.History, id string, r *Response) {
	e, err := h.Load(id)
	if err != nil {
		a.flashErr(err.Error())
		return
	}
	prev := respFromRun(e, a.cwd)
	a.shown = r
	respState.view = viewBody
	a.showView()
	if err := a.rp.ExecLua(`barq_diff_open(...)`, nil, prev.Lines, prev.FT); err != nil {
		a.flashErr(err.Error())
	}
}

// switchView selects a view: "next", "prev", or a name's key (body, headers…).
func (a *App) switchView(to string) {
	v := respState.view
	switch to {
	case "next":
		v = (v + 1) % numViews
	case "prev":
		v = (v + numViews - 1) % numViews
	case "body":
		v = viewBody
	case "headers":
		v = viewHeaders
	case "request":
		v = viewRequest
	case "info":
		v = viewInfo
	case "history":
		v = viewHistory
		if _, _, key, err := a.CurrentRequest(); err == nil {
			respState.key = key
		}
	}
	if a.shown == nil && v != viewHistory {
		a.flash("no response yet")
		return
	}
	respState.view = v
	_ = a.rp.ExecLua(`barq_diff_close()`, nil)
	a.showView()
}

// showView puts the selected view of the shown response in the response pane.
func (a *App) showView() {
	r := a.shown
	if respState.view == viewHistory {
		a.showHistory()
		return
	}
	if r == nil {
		return
	}
	lines, ft := viewContent(r, respState.view)
	buf, err := a.rp.SetScratch(viewBufs[respState.view], ft, lines)
	if err != nil {
		a.flashErr(err.Error())
		return
	}
	if respState.view == viewRequest {
		_ = a.applyHighlights(a.rp, int(buf), lines)
	}
}
