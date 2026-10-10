package ntui

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/iskaa02/barq/internal/core"
	"github.com/iskaa02/barq/internal/httpfile"
	"github.com/iskaa02/barq/internal/nvimpane"
	"github.com/iskaa02/barq/internal/runner"
)

// Messages posted by nvim handlers.
type (
	sendMsg    struct{}
	curlMsg    struct{}
	importMsg  struct{}
	quitMsg    struct{}
	envMsg     struct{ ref string }
	bufsMsg    struct{ tabs []tab }
	savedMsg   struct{ path string }
	changedMsg struct{ path string }
	cursorMsg  struct {
		path string
		line int // 0-based
	}
)

// editorLua installs the autocmds and :Barq* commands. Argument: our channel.
const editorLua = `
local chan = ...
local g = vim.api.nvim_create_augroup('barq', { clear = true })
local function notify(...) vim.rpcnotify(chan, ...) end
local function bufs()
  local out, cur = {}, vim.api.nvim_get_current_buf()
  for _, b in ipairs(vim.api.nvim_list_bufs()) do
    local name = vim.api.nvim_buf_get_name(b)
    if vim.api.nvim_buf_is_valid(b) and vim.bo[b].buflisted and name:match('%.http$') then
      out[#out + 1] = { path = name, cur = b == cur, mod = vim.bo[b].modified }
    end
  end
  return vim.json.encode(out)
end
vim.api.nvim_create_autocmd(
  { 'BufEnter', 'BufAdd', 'BufDelete', 'BufWipeout', 'BufFilePost', 'BufModifiedSet', 'BufWritePost' },
  { group = g, callback = function() vim.schedule(function() notify('barq_bufs', bufs()) end) end })
vim.api.nvim_create_autocmd('BufWritePost', {
  group = g, pattern = '*.http',
  callback = function(a) notify('barq_saved', vim.api.nvim_buf_get_name(a.buf)) end })
vim.api.nvim_create_autocmd({ 'TextChanged', 'TextChangedI', 'BufEnter' }, {
  group = g, pattern = '*.http',
  callback = function(a) notify('barq_changed', vim.api.nvim_buf_get_name(a.buf)) end })

local timer = vim.uv.new_timer()
vim.api.nvim_create_autocmd({ 'CursorMoved', 'CursorMovedI', 'BufEnter' }, {
  group = g, pattern = '*.http',
  callback = function(a)
    timer:stop()
    timer:start(60, 0, vim.schedule_wrap(function()
      if vim.api.nvim_get_current_buf() ~= a.buf or not vim.api.nvim_buf_is_valid(a.buf) then return end
      notify('barq_cursor', vim.api.nvim_buf_get_name(a.buf), vim.api.nvim_win_get_cursor(0)[1] - 1)
    end))
  end })

local function cmd(name, fn, opts) vim.api.nvim_create_user_command(name, fn, opts or {}) end
cmd('BarqSend', function() notify('barq_send') end)
cmd('BarqEnv', function(o) notify('barq_env', o.args) end, {
  nargs = '?',
  complete = function(lead)
    return vim.tbl_filter(function(n) return n:find(lead, 1, true) == 1 end, vim.rpcrequest(chan, 'barq_envs'))
  end })
cmd('BarqSave', function() vim.cmd('write') end)
cmd('BarqCurl', function() notify('barq_curl') end)
cmd('BarqImportSaved', function() notify('barq_import') end)
cmd('BarqQuit', function() notify('barq_quit') end)
vim.o.hidden = true
notify('barq_bufs', bufs())
`

// startEditor starts the editor nvim, opening files (the first one is shown).
func (a *App) startEditor(files []string) error {
	_, ew, _, bh := a.layout()
	w, h := 80, 24
	if a.w > 0 {
		w, h = ew-2, bh-3
	}
	p, err := nvimpane.New(w, h, nvimpane.Options{})
	if err != nil {
		return err
	}
	a.ed = p
	handlers := map[string]any{
		"barq_send":    func() { a.post(sendMsg{}) },
		"barq_curl":    func() { a.post(curlMsg{}) },
		"barq_import":  func() { a.post(importMsg{}) },
		"barq_quit":    func() { a.post(quitMsg{}) },
		"barq_env":     func(ref string) { a.post(envMsg{strings.TrimSpace(ref)}) },
		"barq_saved":   func(path string) { a.post(savedMsg{path}) },
		"barq_changed": func(path string) { a.post(changedMsg{path}) },
		"barq_cursor":  func(path string, line int) { a.post(cursorMsg{path, line}) },
		"barq_bufs": func(js string) {
			var tabs []tab
			_ = json.Unmarshal([]byte(js), &tabs) // an empty Lua table may encode as {}
			a.post(bufsMsg{tabs})
		},
		"barq_envs": func() ([]string, error) { return a.envNameList(), nil },
	}
	for name, fn := range handlers {
		if err := p.Handle(name, fn); err != nil {
			p.Close()
			return err
		}
	}
	if err := p.ExecLua(editorLua, nil, p.Channel()); err != nil {
		p.Close()
		return err
	}
	if err := a.setupAssist(); err != nil {
		a.flashErr("assist: " + err.Error())
	}
	if err := a.setupEnvEdit(); err != nil {
		a.flashErr("env edit: " + err.Error())
	}
	if err := a.setupPicker(); err != nil {
		a.flashErr("picker: " + err.Error())
	}
	// Last, and asynchronously: files never go on nvim's command line, so a
	// prompt while loading one can't block the setup above.
	if err := p.OpenAsync(files...); err != nil {
		a.flashErr("open: " + err.Error())
	}
	return nil
}

// restartEditor replaces an editor whose nvim quit, reopening its files.
func (a *App) restartEditor() tea.Cmd {
	var files []string
	for _, t := range a.tabs {
		files = append(files, t.Path)
	}
	a.ed.Close()
	if err := a.startEditor(files); err != nil {
		a.flashErr("restarting nvim: " + err.Error())
		return tea.Quit
	}
	a.ed.SetFocused(a.focus == focusEditor)
	a.flash("nvim restarted")
	a.assistCurrent()
	return a.ed.Wait()
}

// syncFromDisk picks up workspace changes made by someone else (the CLI):
// reloads the content, refreshes the environment snapshots and re-runs the
// assist on the current buffer. Our own writes update the stamp, so they
// don't count as changes.
func (a *App) syncFromDisk() {
	if !a.ws.ChangedOnDisk() {
		return
	}
	if err := core.WithWriteLock(a.ws.WriteLockPath(), a.ws.ReloadContent); err != nil {
		a.flashErr("couldn't reload workspace: " + err.Error())
		return
	}
	a.refreshEnv()
	a.snapshotEnvs()
	a.assistCurrent()
}

// assistCurrent runs the assist for the editor's current buffer, if it is an
// .http file.
func (a *App) assistCurrent() {
	if _, path, _, _, err := a.ed.Current(); err == nil && strings.HasSuffix(path, ".http") {
		a.assistBufferChanged(path)
	}
}

// startResponse starts the response nvim.
func (a *App) startResponse() error {
	_, _, rw, bh := a.layout()
	w, h := 80, 24
	if a.w > 0 {
		w, h = rw-2, bh-2
	}
	p, err := nvimpane.New(w, h, nvimpane.Options{})
	if err != nil {
		return err
	}
	a.rp = p
	if err := a.setupHighlights(p); err != nil {
		p.Close()
		return err
	}
	if a.shown != nil {
		a.showView()
	} else if _, err := p.SetScratch(viewBufs[viewBody], "text", []string{"alt+enter sends the request under the cursor"}); err != nil {
		p.Close()
		return err
	}
	return a.setupResponseActions()
}

// restartResponse replaces a response nvim that quit.
func (a *App) restartResponse() tea.Cmd {
	a.rp.Close()
	if err := a.startResponse(); err != nil {
		a.flashErr("restarting nvim: " + err.Error())
		return tea.Quit
	}
	a.rp.SetFocused(a.focus == focusResp)
	return a.rp.Wait()
}

// CurrentRequest reads the editor's current buffer and returns its path, the
// request under the cursor and that request's key. It calls nvim.
func (a *App) CurrentRequest() (path string, req httpfile.Request, key string, err error) {
	buf, path, line, _, err := a.ed.Current()
	if err != nil {
		return
	}
	if !strings.HasSuffix(path, ".http") {
		return path, req, "", errors.New("not an .http buffer")
	}
	lines, err := a.ed.BufLines(buf)
	if err != nil {
		return
	}
	reqs := httpfile.Parse(strings.Join(lines, "\n"))
	for i, r := range reqs {
		if line >= r.Start && line < r.End {
			return path, r, a.requestKey(path, reqs, i), nil
		}
	}
	return path, req, "", errors.New("no request under the cursor")
}

// requestKey identifies request i of a file as the CLI does: the runner
// Ref key, "refpath#name" or "refpath#n" (1-based); see runner.Roots.
func (a *App) requestKey(path string, reqs []httpfile.Request, i int) string {
	return runner.Ref{Path: filepath.ToSlash(a.rel(path)), Name: reqs[i].Name, Index: i + 1}.Key()
}

// openAt shows path in the editor, with the cursor on line when line >= 0.
func (a *App) openAt(path string, line int) error {
	if _, err := a.ed.Open(path); err != nil {
		return err
	}
	if line < 0 {
		return nil
	}
	nv := a.ed.Nvim()
	win, err := nv.CurrentWindow()
	if err != nil {
		return err
	}
	return nv.SetWindowCursor(win, [2]int{line + 1, 0})
}

func (a *App) rescan() {
	a.side.set(scanFiles(a.roots()))
	a.snapshotPickerFiles()
}

// createFile makes a new .http file from name (relative to the prompt's
// directory, may include subdirectories) and opens it.
func (a *App) createFile(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if !strings.HasSuffix(name, ".http") {
		name += ".http"
	}
	p := filepath.Join(a.promptDir, name)
	if _, ok := a.roots().RefPath(p); !ok {
		return errors.New("the name must be inside the project or .barq")
	}
	if err := writeNew(p, newFileTemplate); err != nil {
		return err
	}
	a.rescan()
	if err := a.openAt(p, 1); err != nil {
		return err
	}
	a.setFocus(focusEditor)
	return nil
}

// Environments --------------------------------------------------------------

// refreshEnv updates the snapshots handlers read.
func (a *App) refreshEnv() {
	names := []string{"none"}
	for _, e := range a.ws.Environments {
		names = append(names, e.Name)
	}
	name := "no env"
	if e := a.ws.CurrentEnv(); e != nil {
		name = e.Name
	}
	vars := a.ws.EnvVars(a.ws.ActiveEnv)
	infos, secret := a.envInfos()
	a.mu.Lock()
	a.envName, a.envNames, a.envVars, a.envSecrets = name, names, vars, secret
	a.mu.Unlock()
	assist.mu.Lock()
	assist.envs = infos
	assist.mu.Unlock()
}

// EnvSecrets is a copy of the names of the active environment's secret
// variables, from the same snapshot as EnvVars.
func (a *App) EnvSecrets() map[string]bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]bool, len(a.envSecrets))
	for k := range a.envSecrets {
		out[k] = true
	}
	return out
}

// EnvVarsMasked is EnvVars with secret values replaced by the mask. Vars and
// secret flags are read under one lock, so a value never appears without
// its flag.
func (a *App) EnvVarsMasked() map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return maskVars(a.envVars, a.envSecrets)
}

func maskVars(vars map[string]string, secret map[string]bool) map[string]string {
	out := make(map[string]string, len(vars))
	for k, v := range vars {
		if secret[k] {
			v = masked
		}
		out[k] = v
	}
	return out
}

func (a *App) envNameList() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.envNames...)
}

// EnvName is the active environment's name ("no env" for none).
func (a *App) EnvName() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.envName
}

// EnvVars is a copy of the active environment's enabled variables.
func (a *App) EnvVars() map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]string, len(a.envVars))
	for k, v := range a.envVars {
		out[k] = v
	}
	return out
}

// useEnv switches to the environment with this ID ("" for none).
func (a *App) useEnv(id string) {
	if err := a.ws.Mutate(func(w *core.Workspace) error { return w.UseEnv(id) }); err != nil {
		a.flashErr(err.Error())
		return
	}
	a.refreshEnv()
	a.flash("environment: " + a.EnvName())
}

func (a *App) cycleEnv() {
	ids := []string{""}
	for _, e := range a.ws.Environments {
		ids = append(ids, e.ID)
	}
	for i, id := range ids {
		if id == a.ws.ActiveEnv {
			a.useEnv(ids[(i+1)%len(ids)])
			return
		}
	}
	a.useEnv("")
}

// setEnv handles :BarqEnv [name].
func (a *App) setEnv(ref string) {
	switch strings.ToLower(ref) {
	case "":
		a.flash("environment: " + a.EnvName())
	case "none", "-":
		a.useEnv("")
	default:
		env, err := a.ws.EnvByRef(ref)
		if err != nil {
			a.flashErr(err.Error())
			return
		}
		a.useEnv(env.ID)
	}
}
