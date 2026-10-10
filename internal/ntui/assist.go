package ntui

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/iskaa02/barq/internal/core"
	"github.com/iskaa02/barq/internal/httpfile"
)

var (
	headerNames = []string{
		"Accept", "Accept-Encoding", "Accept-Language", "Authorization",
		"Cache-Control", "Connection", "Content-Type", "Cookie", "If-Match",
		"If-Modified-Since", "If-None-Match", "Origin", "Referer", "User-Agent",
		"X-API-Key", "X-Forwarded-For", "X-Request-ID",
	}
	mediaTypes = []string{
		"application/json", "application/x-www-form-urlencoded",
		"multipart/form-data", "text/plain", "text/html", "application/xml",
		"application/octet-stream",
	}
	headerValues = map[string][]string{
		"Accept":          append([]string{"*/*"}, mediaTypes...),
		"Accept-Encoding": {"gzip", "deflate", "br", "identity", "gzip, deflate, br"},
		"Accept-Language": {"en-US,en;q=0.9", "en", "*"},
		"Authorization":   {"Bearer ", "Basic "},
		"Cache-Control":   {"no-cache", "no-store", "max-age=0"},
		"Connection":      {"keep-alive", "close"},
		"Content-Type":    mediaTypes,
	}
	httpMethods  = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}
	directives   = []string{"name", "capture", "expect", "confirm"}
	dynamicNames = []string{"$timestamp", "$isoTimestamp", "$uuid", "$randomInt"}
	varRef       = regexp.MustCompile(`\{\{\s*([^{}\s]+)\s*\}\}`)
)

const masked = "••••"

// completion is one entry of the omni popup.
type completion struct{ Word, Menu string }

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

func trunc(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func isUpper(s string) bool {
	for _, c := range s {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

// complete works out the completions at byte column col of line. start is the
// 0-based byte column the replaced text begins at. It guesses what kind of
// line this is; see completeIn.
func complete(line string, col int, vars map[string]string) (start int, items []completion) {
	return completeIn("", line, col, vars)
}

// completeIn is complete with the line's role known: "request" (before the
// request line is done), "header" or "body"; "" guesses from the text.
func completeIn(role, line string, col int, vars map[string]string) (int, []completion) {
	col = min(max(col, 0), len(line))
	before := line[:col]
	if i := strings.LastIndex(before, "{{"); i >= 0 && !strings.Contains(before[i:], "}}") {
		return completeVar(line, i+2, col, vars)
	}
	if rest, ok := strings.CutPrefix(before, "# @"); ok {
		if w, args, ok := strings.Cut(rest, " "); ok {
			if w == "capture" {
				if _, src, ok := strings.Cut(args, "="); ok {
					return completeCapture(before, len(before)-len(src), src)
				}
			}
			return 3, nil
		}
		return 3, filterWords(directives, rest, "")
	}
	if role == "" {
		role = guessRole(before)
	}
	if strings.HasPrefix(before, "#") || strings.HasPrefix(before, "//") {
		return 0, nil
	}
	switch role {
	case "request":
		if strings.ContainsAny(before, " \t") {
			return 0, nil
		}
		return 0, filterWords(httpMethods, before, " ")
	case "header":
		return completeHeader(before)
	}
	return 0, nil
}

var formFileRef = regexp.MustCompile(`^(?:#\s*)?[^:\s]+:\s*@(.*)$`)

// completeFile completes the path after "@" in a form field line: the
// entries of the typed directory (relative to cwd), skipping dot files
// unless asked for. Directories end in "/". ok is false when the line is
// not a file value.
func completeFile(role, before, cwd string) (start int, items []completion, ok bool) {
	m := formFileRef.FindStringSubmatch(before)
	if m == nil || role == "request" || role == "header" || strings.Contains(m[1], "{{") {
		return 0, nil, false
	}
	typed := m[1]
	start = len(before) - len(typed)
	dir, base := "", typed
	if i := strings.LastIndex(typed, "/"); i >= 0 {
		dir, base = typed[:i+1], typed[i+1:]
	}
	abs := dir
	switch {
	case dir == "":
		abs = cwd
	case !filepath.IsAbs(dir):
		abs = filepath.Join(cwd, dir)
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return start, nil, true
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".") || !strings.HasPrefix(name, base) {
			continue
		}
		isDir := e.IsDir()
		if e.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(filepath.Join(abs, name)); err == nil {
				isDir = st.IsDir()
			}
		}
		w, menu := dir+name, "file"
		if isDir {
			w, menu = w+"/", "dir"
		}
		items = append(items, completion{Word: w, Menu: menu})
		if len(items) >= 200 {
			break
		}
	}
	return start, items, true
}

// completeCapture completes the source of "# @capture name = src": the
// header/cookie keywords first, then header names. jq stays free text.
func completeCapture(before string, at int, src string) (int, []completion) {
	lead := len(src) - len(strings.TrimLeft(src, " \t"))
	start, src := at+lead, src[lead:]
	if name, ok := strings.CutPrefix(src, "header "); ok {
		if strings.ContainsAny(name, " \t") {
			return start, nil
		}
		return start + len("header "), filterWords(headerNames, name, "")
	}
	if strings.ContainsAny(src, " .|[(") {
		return start, nil
	}
	return start, filterWords([]string{"header", "cookie"}, src, " ")
}

func guessRole(before string) string {
	k := strings.Index(before, ":")
	switch {
	case k >= 0 && !strings.Contains(before[:k], " ") && !strings.HasPrefix(before[k:], "://"):
		return "header"
	case strings.ContainsAny(before, " \t"), strings.Contains(before, "://"):
		return "none"
	case before == "" || isUpper(before):
		return "request"
	}
	return "header"
}

func filterWords(words []string, prefix, suffix string) []completion {
	var out []completion
	for _, w := range words {
		if hasPrefixFold(w, prefix) {
			out = append(out, completion{Word: w + suffix})
		}
	}
	return out
}

func completeHeader(before string) (int, []completion) {
	k := strings.Index(before, ":")
	if k < 0 {
		if strings.ContainsAny(before, " \t") {
			return 0, nil
		}
		return 0, filterWords(headerNames, before, ": ")
	}
	start := k + 1
	for start < len(before) && before[start] == ' ' {
		start++
	}
	key := http.CanonicalHeaderKey(strings.TrimSpace(before[:k]))
	return start, filterWords(headerValues[key], before[start:], "")
}

// completeVar completes inside "{{": env variables, then dynamic ones.
func completeVar(line string, start, col int, vars map[string]string) (int, []completion) {
	for start < col && line[start] == ' ' {
		start++
	}
	prefix := line[start:col]
	if strings.Contains(prefix, " ") {
		return start, nil
	}
	closer := "}}"
	if strings.HasPrefix(line[col:], "}}") {
		closer = ""
	}
	names := make([]string, 0, len(vars))
	for k := range vars {
		names = append(names, k)
	}
	sort.Strings(names)
	var out []completion
	for _, n := range names {
		if hasPrefixFold(n, prefix) {
			out = append(out, completion{Word: n + closer, Menu: trunc(vars[n], 30)})
		}
	}
	for _, n := range dynamicNames {
		if hasPrefixFold(n, prefix) {
			out = append(out, completion{Word: n + closer, Menu: "dynamic"})
		}
	}
	return start, out
}

// Diagnostics and virtual text ------------------------------------------------

type diagnostic struct {
	Line, Col, EndCol int // 0-based, bytes
	Severity          int // vim.diagnostic.severity: 1 error, 2 warning
	Message           string
}

func isDynamic(name string) bool {
	for _, d := range dynamicNames {
		if d == name {
			return true
		}
	}
	return false
}

func isCommentLine(l string) bool {
	l = strings.TrimSpace(l)
	return strings.HasPrefix(l, "#") || strings.HasPrefix(l, "//")
}

// undefinedVars warns about {{name}} that neither the environment nor the
// dynamic variables define.
func undefinedVars(lines []string, vars map[string]string) []diagnostic {
	var out []diagnostic
	for i, l := range lines {
		if isCommentLine(l) {
			continue
		}
		for _, m := range varRef.FindAllStringSubmatchIndex(l, -1) {
			name := l[m[2]:m[3]]
			if _, ok := vars[name]; ok || isDynamic(name) {
				continue
			}
			out = append(out, diagnostic{Line: i, Col: m[0], EndCol: m[1], Severity: 2,
				Message: "undefined variable " + name})
		}
	}
	return out
}

// missingFiles warns about "@path" form values that name no file under cwd.
func missingFiles(lines []string, text, cwd string) []diagnostic {
	var out []diagnostic
	for _, r := range httpfile.Parse(text) {
		for i, f := range r.Form {
			if !f.Enabled {
				continue
			}
			isFile, exists := core.FormFileState(f.Value, cwd)
			if !isFile || exists || i >= len(r.FormLines) || strings.TrimSpace(f.Value) == "@" {
				continue
			}
			ln := r.FormLines[i]
			col := max(strings.Index(lines[ln], "@"), 0)
			out = append(out, diagnostic{Line: ln, Col: col, EndCol: len(strings.TrimRight(lines[ln], " \t\r")),
				Severity: 2, Message: "file not found: " + strings.TrimSpace(strings.TrimPrefix(f.Value, "@"))})
		}
	}
	return out
}

// toDiagnostics converts httpfile problems, clipping columns to the line.
func toDiagnostics(lines []string, probs []httpfile.Problem) []diagnostic {
	var out []diagnostic
	for _, p := range probs {
		if p.Line < 0 || p.Line >= len(lines) {
			continue
		}
		n := len(lines[p.Line])
		end := p.EndCol
		if end < 0 || end > n {
			end = n
		}
		sev := 1
		if p.Warning {
			sev = 2
		}
		out = append(out, diagnostic{Line: p.Line, Col: min(p.Col, end), EndCol: end, Severity: sev, Message: p.Message})
	}
	return out
}

// varHints returns, per line holding known variables, the virtual text
// chunks "= value" in order of appearance. Secrets show as masked.
func varHints(lines []string, vars map[string]string, secret map[string]bool) map[int][]string {
	out := map[int][]string{}
	for i, l := range lines {
		for _, m := range varRef.FindAllStringSubmatch(l, -1) {
			v, ok := vars[m[1]]
			if !ok {
				continue
			}
			if secret[m[1]] {
				v = masked
			}
			out[i] = append(out[i], " = "+trunc(strings.ReplaceAll(v, "\n", " "), 40))
		}
	}
	return out
}

// State -----------------------------------------------------------------------

// envInfo is one environment's variables as hover shows them.
type envInfo struct {
	Name   string
	Active bool
	Vars   map[string]string // secrets already masked
}

var assist struct {
	mu   sync.Mutex
	envs []envInfo
	last map[string]string // path -> fingerprint of what was last analysed
}

// envInfos describes every environment for hover, and returns the names of
// the active environment's secret variables. Loop only.
func (a *App) envInfos() ([]envInfo, map[string]bool) {
	var infos []envInfo
	secret := map[string]bool{}
	for _, e := range a.ws.Environments {
		info := envInfo{Name: e.Name, Active: e.ID == a.ws.ActiveEnv, Vars: map[string]string{}}
		for _, v := range e.Vars {
			k := strings.TrimSpace(v.Key)
			if !v.Enabled || k == "" {
				continue
			}
			if core.IsSecret(v) {
				info.Vars[k] = masked
				if info.Active {
					secret[k] = true
				}
			} else {
				info.Vars[k] = v.Value
			}
		}
		infos = append(infos, info)
	}
	return infos, secret
}

// snapshotEnvs refreshes every env snapshot (see refreshEnv) and returns
// the active environment's secret variable names. Loop only.
func (a *App) snapshotEnvs() map[string]bool {
	a.refreshEnv()
	return a.EnvSecrets()
}

// hoverLines describes variable name in every environment.
func hoverLines(name string, envs []envInfo) []string {
	if isDynamic(name) {
		return []string{name, "dynamic: generated when the request is sent"}
	}
	lines := []string{name}
	for _, e := range envs {
		v, ok := e.Vars[name]
		if !ok {
			continue
		}
		mark := "  "
		if e.Active {
			mark = "* "
		}
		lines = append(lines, mark+e.Name+": "+trunc(strings.ReplaceAll(v, "\n", " "), 80))
	}
	if len(lines) == 1 {
		lines = append(lines, "not defined in any environment")
	}
	return lines
}

func fingerprint(text, env string, vars map[string]string) string {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(env + "\x00")
	for _, k := range keys {
		b.WriteString(k + "=" + vars[k] + "\x00")
	}
	return b.String() + text
}

// Wiring ----------------------------------------------------------------------

// assistLua sets up filetype, omni completion, auto-popup and hover.
// Argument: our channel.
const assistLua = `
local chan = ...
local g = vim.api.nvim_create_augroup('barq_assist', { clear = true })

-- where in a request block line lnum (1-based) is: request, header or body
local function role(lnum)
  local lines = vim.api.nvim_buf_get_lines(0, 0, lnum, false)
  local first = 1
  for i = lnum - 1, 1, -1 do
    if lines[i]:match('^###') then first = i + 1 break end
  end
  local state = 'request'
  for i = first, lnum - 1 do
    local l = lines[i]
    if state == 'request' and l:match('%S') and not l:match('^%s*#') and not l:match('^%s*//') then
      state = 'header'
    elseif state == 'header' and not l:match('%S') then
      state = 'body'
    end
  end
  return state
end

local last
function BarqOmni(findstart, base)
  if findstart == 1 then
    local pos = vim.api.nvim_win_get_cursor(0)
    local ok, res = pcall(vim.rpcrequest, chan, 'barq_complete', vim.api.nvim_get_current_line(), pos[2], role(pos[1]))
    if not ok or not res or #res.items == 0 then last = nil return -2 end
    last = res.items
    return res.start
  end
  return last or {}
end

local function http_buf(b)
  vim.bo[b].omnifunc = 'v:lua.BarqOmni'
  vim.keymap.set('n', 'K', function()
    local line, col = vim.api.nvim_get_current_line(), vim.api.nvim_win_get_cursor(0)[2] + 1
    local init = 1
    while true do
      local s, e, name = line:find('{{%s*([^{}%s]+)%s*}}', init)
      if not s then break end
      if col >= s and col <= e then
        local lines = vim.rpcrequest(chan, 'barq_hover', name)
        vim.lsp.util.open_floating_preview(lines, 'plaintext', { border = 'rounded', focus_id = 'barq_hover' })
        return
      end
      init = e + 1
    end
    vim.cmd('normal! K')
  end, { buffer = b, desc = 'barq: variable values' })
end

vim.api.nvim_create_autocmd({ 'BufRead', 'BufNewFile' }, {
  group = g, pattern = '*.http', callback = function() vim.bo.filetype = 'http' end })
vim.api.nvim_create_autocmd('FileType', {
  group = g, pattern = 'http', callback = function(a) http_buf(a.buf) end })
for _, b in ipairs(vim.api.nvim_list_bufs()) do
  if vim.api.nvim_buf_is_loaded(b) and vim.api.nvim_buf_get_name(b):match('%.http$') then
    vim.bo[b].filetype = 'http'
    http_buf(b)
  end
end

-- completion engine: blink.cmp or nvim-cmp if present, else omni + auto popup
local has_blink = pcall(require, 'blink.cmp')
local has_cmp = not has_blink and pcall(require, 'cmp')

-- query returns start (0-based byte col), items, kind of the cursor line
local function query()
  local pos = vim.api.nvim_win_get_cursor(0)
  local line = vim.api.nvim_get_current_line()
  local r = role(pos[1])
  local ok, res = pcall(vim.rpcrequest, chan, 'barq_complete', line, pos[2], r)
  if not ok or not res then return nil end
  local before = line:sub(1, pos[2])
  local kind = 12 -- Value
  if before:match('{{[^}]*$') then kind = 6
  elseif before:match('^# @') or r == 'request' then kind = 14
  elseif res.start == 0 then kind = 10 end
  return res.start, res.items, kind, pos
end

-- a barq source in LSP CompletionItem form
local function lsp_items()
  local start, items, kind, pos = query()
  if not start then return {} end
  local row = pos[1] - 1
  local out = {}
  for _, it in ipairs(items) do
    out[#out + 1] = {
      label = it.word, kind = kind,
      labelDetails = it.menu ~= '' and { description = it.menu } or nil,
      textEdit = { newText = it.word, range = {
        start = { line = row, character = start }, ['end'] = { line = row, character = pos[2] } } },
      data = { reopen = kind == 10 },
    }
  end
  return out
end

if has_blink then
  local src = {}
  src.__index = src
  function src.new() return setmetatable({}, src) end
  function src:enabled() return vim.bo.filetype == 'http' end
  function src:get_trigger_characters() return { '{', ':', '@', ' ', '/' } end
  function src:get_completions(_, callback)
    local items = lsp_items()
    callback({ items = items, is_incomplete_forward = false, is_incomplete_backward = false })
    return function() end
  end
  function src:execute(_, item, callback)
    callback()
    if item.data and item.data.reopen then
      vim.schedule(function() require('blink.cmp').show({ providers = { 'barq' } }) end)
    end
  end
  package.preload['barq.blink'] = function() return src end
  local blink = require('blink.cmp')
  local sources = require('blink.cmp.config').sources
  if sources.providers.barq == nil then
    blink.add_provider('barq', { name = 'Barq', module = 'barq.blink', score_offset = 100 })
  end
  sources.per_filetype.http = { 'barq' }
  -- auto-pairs plugins insert "{{" without InsertCharPre, so blink never sees the trigger
  vim.api.nvim_create_autocmd('TextChangedI', {
    group = g, pattern = '*.http', callback = function()
      local pos = vim.api.nvim_win_get_cursor(0)
      if vim.api.nvim_get_current_line():sub(1, pos[2]):match('{{$') and not blink.is_visible() then
        vim.schedule(function() blink.show({ providers = { 'barq' } }) end)
      end
    end })
elseif has_cmp then
  local cmp = require('cmp')
  local src = {}
  function src:is_available() return vim.bo.filetype == 'http' end
  function src:get_trigger_characters() return { '{', ':', '@', ' ', '/' } end
  function src:complete(_, callback)
    local items = lsp_items()
    for _, i in ipairs(items) do i.data = nil end
    callback({ items = items })
  end
  function src:execute(item, callback)
    callback(item)
    if item.label and vim.api.nvim_get_current_line():sub(1, vim.api.nvim_win_get_cursor(0)[2]):match('^[%w-]+: $') then
      vim.schedule(function() cmp.complete({ config = { sources = { { name = 'barq' } } } }) end)
    end
  end
  cmp.register_source('barq', src)
  cmp.setup.filetype('http', { sources = { { name = 'barq' } } })
else
  vim.opt.completeopt:append('menuone')
  local function popup()
    vim.api.nvim_feedkeys(vim.api.nvim_replace_termcodes('<C-x><C-o>', true, false, true), 'n', false)
  end
  local typed
  vim.api.nvim_create_autocmd('InsertCharPre', {
    group = g, pattern = '*.http', callback = function() typed = vim.v.char end })
  vim.api.nvim_create_autocmd('TextChangedI', {
    group = g, pattern = '*.http', callback = function()
      local c = typed
      typed = nil
      if not c or vim.fn.pumvisible() == 1 then return end
      local pos = vim.api.nvim_win_get_cursor(0)
      local before = vim.api.nvim_get_current_line():sub(1, pos[2])
      local r = role(pos[1])
      if before:match('{{$') or before == '# @'
          or (r == 'header' and (c == ':' or before:match('^%a$')))
          or (r == 'body' and (c == '@' or c == '/') and before:match('^#?%s*[^:%s]+:%s*@')) 
          or (r == 'request' and before:match('^%u$')) then
        popup()
      end
    end })
  -- after a header name ("Accept: ") offer its values
  vim.api.nvim_create_autocmd('CompleteDone', {
    group = g, pattern = '*.http', callback = function()
      local pos = vim.api.nvim_win_get_cursor(0)
      local before = vim.api.nvim_get_current_line():sub(1, pos[2])
      if vim.v.completed_item.word and before:match('^[%w-]+: $') then vim.schedule(popup) end
    end })
end
`

// applyLua pushes diagnostics and variable hints. Arguments: buffer, diagnostics, hints.
const applyLua = `
local buf, diags, hints = ...
if not vim.api.nvim_buf_is_valid(buf) then return end
local ds = {}
for _, d in ipairs(diags) do
  ds[#ds + 1] = { lnum = d.line, col = d.col, end_lnum = d.line, end_col = d.endcol,
    severity = d.severity, message = d.message, source = 'barq' }
end
vim.diagnostic.set(vim.api.nvim_create_namespace('barq'), buf, ds)
local ns = vim.api.nvim_create_namespace('barq_vars')
vim.api.nvim_buf_clear_namespace(buf, ns, 0, -1)
for _, h in ipairs(hints) do
  local chunks = {}
  for _, t in ipairs(h.chunks) do chunks[#chunks + 1] = { t, 'Comment' } end
  pcall(vim.api.nvim_buf_set_extmark, buf, ns, h.line, 0, { virt_text = chunks, virt_text_pos = 'eol' })
end
`

// setupAssist is called once after the editor nvim starts (and again after a
// restart): filetype, completion, hover; diagnostics come from assistBufferChanged.
func (a *App) setupAssist() error {
	assist.mu.Lock()
	assist.last = map[string]string{}
	assist.mu.Unlock()
	a.snapshotEnvs()
	if err := a.setupHighlights(a.ed); err != nil {
		return err
	}
	p := a.ed
	err := p.Handle("barq_complete", func(line string, col int, role string) (map[string]any, error) {
		vars := a.EnvVarsMasked()
		before := line[:min(max(col, 0), len(line))]
		start, items, isFile := completeFile(role, before, a.cwd)
		if !isFile {
			start, items = completeIn(role, line, col, vars)
		}
		out := make([]map[string]string, 0, len(items))
		for _, it := range items {
			out = append(out, map[string]string{"word": it.Word, "menu": it.Menu})
		}
		return map[string]any{"start": start, "items": out}, nil
	})
	if err != nil {
		return err
	}
	err = p.Handle("barq_hover", func(name string) ([]string, error) {
		assist.mu.Lock()
		defer assist.mu.Unlock()
		return hoverLines(name, assist.envs), nil
	})
	if err != nil {
		return err
	}
	if err := p.ExecLua(assistLua, nil, p.Channel()); err != nil {
		return err
	}
	var own bool
	if err := p.ExecLua(`return vim.o.showtabline == 2`, &own); err != nil {
		return err
	}
	if own != a.ownTabline {
		a.ownTabline = own
		if a.w > 0 {
			a.applySizes()
		}
	}
	return nil
}

// assistBufferChanged is called, on the Bubble Tea loop, when an .http buffer
// changed (TextChanged, TextChangedI, BufEnter). path is the buffer's full path.
func (a *App) assistBufferChanged(path string) {
	buf, cur, _, _, err := a.ed.Current()
	if err != nil || filepath.Clean(cur) != filepath.Clean(path) {
		return
	}
	lines, err := a.ed.BufLines(buf)
	if err != nil {
		return
	}
	secret := a.snapshotEnvs()
	vars := a.EnvVars()
	text := strings.Join(lines, "\n")
	fp := fingerprint(text, a.EnvName(), vars)
	assist.mu.Lock()
	same := assist.last[path] == fp
	assist.last[path] = fp
	assist.mu.Unlock()
	if same {
		return
	}
	_ = a.applyHighlights(a.ed, int(buf), lines)
	diags := append(toDiagnostics(lines, httpfile.Problems(text)), undefinedVars(lines, vars)...)
	diags = append(diags, missingFiles(lines, text, a.cwd)...)
	ds := make([]map[string]any, 0, len(diags))
	for _, d := range diags {
		ds = append(ds, map[string]any{"line": d.Line, "col": d.Col, "endcol": d.EndCol,
			"severity": d.Severity, "message": d.Message})
	}
	var hs []map[string]any
	hints := varHints(lines, vars, secret)
	keys := make([]int, 0, len(hints))
	for l := range hints {
		keys = append(keys, l)
	}
	sort.Ints(keys)
	for _, l := range keys {
		hs = append(hs, map[string]any{"line": l, "chunks": hints[l]})
	}
	if hs == nil {
		hs = []map[string]any{}
	}
	_ = a.ed.ExecLua(applyLua, nil, int(buf), ds, hs)
}
