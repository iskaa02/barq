package ntui

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/iskaa02/barq/internal/core"
)

// Editing environments in a buffer `barq://env/<name>` of the editor nvim:
//
//	## protection: none            (none | writes | all)
//	base = http://localhost:8080
//	token = ••••  # secret          (•••• keeps the stored secret value)
//	#! old = x                      (a disabled variable)
//
// Secret values are never written into the buffer: they show as ••••.

const (
	envMask   = "••••"
	envPrefix = "barq://env/"
)

type (
	envEditMsg struct{ name string }
	envSaveMsg struct {
		name  string
		lines []string
	}
	envNewMsg    struct{ name string }
	envDeleteMsg struct{ name string }
)

// envVar is one variable line of the buffer.
type envVar struct {
	Key, Value       string
	Secret, Disabled bool
	Line             int // 0-based buffer line
}

// envLineErr is a problem on one buffer line.
type envLineErr struct {
	Line int
	Msg  string
}

func (e envLineErr) Error() string { return fmt.Sprintf("line %d: %s", e.Line+1, e.Msg) }

var (
	envKeyRe    = regexp.MustCompile(`^[A-Za-z0-9_.\-]+$`)
	envSecretRe = regexp.MustCompile(`\s+#\s*secret\s*$`)
	envProtRe   = regexp.MustCompile(`^##\s*protection:\s*(\S*)`)
)

// renderEnv makes the buffer text of an environment.
func renderEnv(e core.Environment) []string {
	prot := e.Protection()
	if prot == "" {
		prot = "none"
	}
	lines := []string{
		"## Environment: " + e.Name + "   (:w saves · secret values show as " + envMask + " — keep " + envMask + " to leave them unchanged)",
		"## protection: " + prot + "   (none | writes | all)",
	}
	for _, v := range e.Vars {
		val, sfx := v.Value, ""
		if core.IsSecret(v) {
			val, sfx = "", "  # secret"
			if v.Value != "" {
				val = envMask
			}
		}
		pre := ""
		if !v.Enabled {
			pre = "#! "
		}
		lines = append(lines, pre+v.Key+" = "+val+sfx)
	}
	return lines
}

// parseEnv parses the buffer text into the protection ("none", "writes" or
// "all") and the variables, reporting every bad line.
func parseEnv(lines []string) (prot string, vars []envVar, errs []envLineErr) {
	prot = "none"
	seen := map[string]bool{}
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		disabled := false
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "#!"):
			disabled, line = true, strings.TrimSpace(line[2:])
		case strings.HasPrefix(line, "#"):
			if m := envProtRe.FindStringSubmatch(line); m != nil {
				if !slices.Contains([]string{"none", "writes", "all"}, m[1]) {
					errs = append(errs, envLineErr{i, "protection must be none, writes or all"})
				} else {
					prot = m[1]
				}
			}
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !envKeyRe.MatchString(key) {
			errs = append(errs, envLineErr{i, "expected `key = value`"})
			continue
		}
		if seen[key] {
			errs = append(errs, envLineErr{i, "duplicate variable " + key})
			continue
		}
		seen[key] = true
		secret := false
		if loc := envSecretRe.FindStringIndex(val); loc != nil {
			secret, val = true, val[:loc[0]]
		}
		vars = append(vars, envVar{Key: key, Value: strings.TrimSpace(val), Secret: secret, Disabled: disabled, Line: i})
	}
	return
}

// applyEnv makes environment id match the parsed buffer. Unchanged secrets
// (••••) keep the value stored in the environment.
func applyEnv(w *core.Workspace, id, prot string, vars []envVar) error {
	e, err := w.Env(id)
	if err != nil {
		return err
	}
	old := map[string]core.SavedHeader{}
	for _, v := range e.Vars {
		old[v.Key] = v
	}
	out := make([]core.SavedHeader, 0, len(vars))
	for _, v := range vars {
		val := v.Value
		if val == envMask {
			o, ok := old[v.Key]
			switch {
			case !ok || o.Value == "":
				return envLineErr{v.Line, envMask + " but no value is stored: type the value"}
			case !v.Secret:
				return envLineErr{v.Line, "to make it not secret, type its value instead of " + envMask}
			}
			val = o.Value
		} else if core.IsRedacted(val) {
			return envLineErr{v.Line, core.ErrRedactedWrite.Error()}
		}
		h := core.SavedHeader{Key: v.Key, Value: val, Enabled: !v.Disabled}
		if v.Secret != core.IsSecret(core.SavedHeader{Key: v.Key}) {
			h.Secret = core.BoolPtr(v.Secret)
		}
		out = append(out, h)
	}
	e.Vars = out
	e.Protected, e.ProtectReads = prot != "none", prot == "all"
	return nil
}

// envEditLua opens (or refreshes) the env buffer. Arguments: channel, buffer name, lines.
const envEditLua = `
local chan, name, lines = ...
local buf = vim.fn.bufnr(name)
if buf < 0 then
  buf = vim.api.nvim_create_buf(true, false)
  vim.api.nvim_buf_set_name(buf, name)
  vim.bo[buf].buftype = 'acwrite'
  vim.bo[buf].swapfile = false
  vim.bo[buf].bufhidden = 'hide'
  vim.api.nvim_create_autocmd('BufWriteCmd', { buffer = buf, callback = function(a)
    vim.rpcnotify(chan, 'barq_envsave', name, vim.api.nvim_buf_get_lines(a.buf, 0, -1, false))
  end })
  vim.bo[buf].filetype = 'barqenv'
  vim.api.nvim_buf_call(buf, function()
    vim.cmd([[syntax match barqenvKey /^\s*[A-Za-z0-9_.-]\+\ze\s*=/]])
    vim.cmd([[syntax match barqenvEq /=/]])
    vim.cmd([[syntax match barqenvSecret /#\s*secret\s*$/]])
    vim.cmd([[syntax match barqenvMask /••••/]])
    vim.cmd([[syntax match barqenvOff /^\s*#!.*/]])
    vim.cmd([[syntax match barqenvComment /^\s*##.*/]])
    vim.cmd([[highlight default link barqenvKey Identifier]])
    vim.cmd([[highlight default link barqenvEq Operator]])
    vim.cmd([[highlight default link barqenvSecret WarningMsg]])
    vim.cmd([[highlight default link barqenvMask WarningMsg]])
    vim.cmd([[highlight default link barqenvOff Comment]])
    vim.cmd([[highlight default link barqenvComment Comment]])
  end)
end
vim.api.nvim_buf_set_lines(buf, 0, -1, false, lines)
vim.bo[buf].modified = false
vim.diagnostic.reset(vim.api.nvim_create_namespace('barq_env'), buf)
vim.api.nvim_set_current_buf(buf)
`

// envSavedLua reports a save. Arguments: buffer name, diagnostics ({line, msg}); no diagnostics means saved.
const envSavedLua = `
local name, errs = ...
local buf = vim.fn.bufnr(name)
if buf < 0 then return end
local ns = vim.api.nvim_create_namespace('barq_env')
local ds = {}
for _, e in ipairs(errs) do
  ds[#ds + 1] = { lnum = e.line, col = 0, severity = 1, message = e.msg, source = 'barq' }
end
vim.diagnostic.set(ns, buf, ds)
if #ds == 0 then vim.bo[buf].modified = false end
`

// envDeletedLua wipes the env buffer. Argument: buffer name.
const envDeletedLua = `
local buf = vim.fn.bufnr(...)
if buf >= 0 then vim.api.nvim_buf_delete(buf, { force = true }) end
`

// envCmdsLua registers the commands. Argument: our channel.
const envCmdsLua = `
local chan = ...
local function names()
  return vim.tbl_filter(function(n) return n ~= 'none' end, vim.rpcrequest(chan, 'barq_envs'))
end
local function complete(lead)
  return vim.tbl_filter(function(n) return n:find(lead, 1, true) == 1 end, names())
end
vim.api.nvim_create_user_command('BarqEnvEdit', function(o) vim.rpcnotify(chan, 'barq_envedit', o.args) end,
  { nargs = '?', complete = complete })
vim.api.nvim_create_user_command('BarqEnvNew', function(o) vim.rpcnotify(chan, 'barq_envnew', o.args) end,
  { nargs = 1 })
vim.api.nvim_create_user_command('BarqEnvDelete', function(o)
  if vim.fn.confirm('Delete environment ' .. o.args .. '?', '&Yes\n&No', 2) == 1 then
    vim.rpcnotify(chan, 'barq_envdelete', o.args)
  end
end, { nargs = 1, complete = complete })
`

// setupEnvEdit registers the env editing handlers and commands on the editor.
func (a *App) setupEnvEdit() error {
	handlers := map[string]any{
		"barq_envedit":   func(n string) { a.post(envEditMsg{strings.TrimSpace(n)}) },
		"barq_envnew":    func(n string) { a.post(envNewMsg{strings.TrimSpace(n)}) },
		"barq_envdelete": func(n string) { a.post(envDeleteMsg{strings.TrimSpace(n)}) },
		"barq_envsave": func(name string, lines []string) {
			a.post(envSaveMsg{strings.TrimPrefix(name, envPrefix), lines})
		},
	}
	for name, fn := range handlers {
		if err := a.ed.Handle(name, fn); err != nil {
			return err
		}
	}
	return a.ed.ExecLua(envCmdsLua, nil, a.ed.Channel())
}

// envEdit opens the buffer for the named environment (default: the active one).
func (a *App) envEdit(name string) {
	var env *core.Environment
	var err error
	if name == "" {
		if env = a.ws.CurrentEnv(); env == nil {
			a.flashErr("no active environment; :BarqEnvNew <name>")
			return
		}
	} else if env, err = a.ws.EnvByRef(name); err != nil {
		a.flashErr(err.Error())
		return
	}
	if err := a.ed.ExecLua(envEditLua, nil, a.ed.Channel(), envPrefix+env.Name, renderEnv(*env)); err != nil {
		a.flashErr(err.Error())
		return
	}
	a.setFocus(focusEditor)
}

// envSave handles :w in an env buffer.
func (a *App) envSave(name string, lines []string) {
	report := func(errs []envLineErr) {
		ds := make([]map[string]any, 0, len(errs))
		for _, e := range errs {
			ds = append(ds, map[string]any{"line": e.Line, "msg": e.Msg})
		}
		if err := a.ed.ExecLua(envSavedLua, nil, envPrefix+name, ds); err != nil {
			a.flashErr(err.Error())
		}
	}
	env, err := a.ws.EnvByRef(name)
	if err != nil {
		a.flashErr(err.Error())
		return
	}
	id := env.ID
	prot, vars, errs := parseEnv(lines)
	if len(errs) > 0 {
		report(errs)
		a.flashErr(errs[0].Error())
		return
	}
	err = a.ws.Mutate(func(w *core.Workspace) error { return applyEnv(w, id, prot, vars) })
	var le envLineErr
	if errors.As(err, &le) {
		report([]envLineErr{le})
		a.flashErr(le.Error())
		return
	}
	if err != nil {
		a.flashErr(err.Error())
		return
	}
	report(nil)
	a.refreshEnv()
	a.snapshotEnvs()
	a.assistCurrent()
	a.flash(fmt.Sprintf("saved %s (%d vars)", name, len(vars)))
}

// envNew handles :BarqEnvNew.
func (a *App) envNew(name string) {
	if name == "" {
		a.flashErr("usage: :BarqEnvNew <name>")
		return
	}
	if _, err := a.ws.EnvByRef(name); err == nil {
		a.flashErr(fmt.Sprintf("environment %q already exists", name))
		return
	}
	err := a.ws.Mutate(func(w *core.Workspace) error {
		id := w.AddEnv(name, nil)
		if w.ActiveEnv == "" {
			w.ActiveEnv = id
		}
		return nil
	})
	if err != nil {
		a.flashErr(err.Error())
		return
	}
	a.refreshEnv()
	a.envEdit(name)
}

// envDelete handles :BarqEnvDelete (already confirmed in nvim).
func (a *App) envDelete(name string) {
	env, err := a.ws.EnvByRef(name)
	if err != nil {
		a.flashErr(err.Error())
		return
	}
	id, nm := env.ID, env.Name
	if err := a.ws.Mutate(func(w *core.Workspace) error { return w.DeleteEnv(id) }); err != nil {
		a.flashErr(err.Error())
		return
	}
	_ = a.ed.ExecLua(envDeletedLua, nil, envPrefix+nm)
	a.refreshEnv()
	a.snapshotEnvs()
	a.assistCurrent()
	a.flash("deleted environment " + nm)
}
