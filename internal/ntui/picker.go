package ntui

import (
	"sync"

	"github.com/iskaa02/barq/internal/runner"
)

// pickerItem is one file the picker offers: its absolute path, the label to
// show (the ref path) and whether it is in the store.
type pickerItem struct {
	Path  string `json:"path"`
	Label string `json:"label"`
	Store bool   `json:"store"`
}

// pickerFiles is the mutex-guarded list of .http files (the project's and
// the store's) that barq's <C-p> picker offers; the rpcrequest handler
// answers from it.
var pickerFiles struct {
	sync.Mutex
	cwd  string
	list []pickerItem
}

// snapshotPickerFiles refreshes the picker's file list from disk. Call it
// wherever the sidebar rescans.
func (a *App) snapshotPickerFiles() {
	list := pickerList(a.roots())
	pickerFiles.Lock()
	pickerFiles.cwd = a.cwd
	pickerFiles.list = list
	pickerFiles.Unlock()
}

// pickerList returns the .http files of both roots.
func pickerList(rt runner.Roots) []pickerItem {
	list := []pickerItem{}
	for _, e := range scanFiles(rt) {
		if e.Kind == fileEntry {
			list = append(list, pickerItem{Path: e.Path, Label: e.Rel, Store: rt.IsStore(e.Rel)})
		}
	}
	return list
}

// pickerPayload is what the Lua side receives. It is plain maps, not
// structs: the nvim client encodes struct fields by their Go names, and the
// Lua reads lower-case keys.
func pickerPayload() (map[string]any, error) {
	pickerFiles.Lock()
	defer pickerFiles.Unlock()
	files := make([]map[string]any, 0, len(pickerFiles.list))
	for _, it := range pickerFiles.list {
		files = append(files, map[string]any{"path": it.Path, "label": it.Label, "store": it.Store})
	}
	return map[string]any{"cwd": pickerFiles.cwd, "files": files}, nil
}

// setupPicker installs <C-p> / :BarqFiles in the editor nvim, listing only
// barq's .http files.
func (a *App) setupPicker() error {
	err := a.ed.Handle("barq_http_files", pickerPayload)
	if err != nil {
		return err
	}
	a.snapshotPickerFiles()
	return a.ed.ExecLua(pickerLua, nil, a.ed.Channel())
}

const pickerLua = `
local chan = ...
-- Items are { path = absolute, label = ref path shown to the user, store = bool }.
-- Project files are shown relative to cwd; store files by their absolute path.
function _G.barq_files()
  local ok, res = pcall(vim.rpcrequest, chan, 'barq_http_files')
  if not ok or type(res) ~= 'table' then return end
  local list, cwd = res.files or {}, res.cwd
  if cwd == nil or cwd == '' then cwd = vim.fn.getcwd() end
  if #list == 0 then vim.notify('barq: no .http files', vim.log.levels.INFO) return end
  local function open(path)
    vim.cmd.edit(vim.fn.fnameescape(path))
  end
  local okf, fzf = pcall(require, 'fzf-lua')
  if okf then
    local names = {}
    for _, it in ipairs(list) do names[#names + 1] = it.store and it.path or it.label end
    fzf.fzf_exec(names, {
      prompt = 'http> ', cwd = cwd, previewer = 'builtin',
      actions = fzf.defaults.actions.files, file_icons = true, git_icons = false,
    })
    return
  end
  local oks, snacks = pcall(function() return Snacks.picker end)
  if oks and snacks then
    local items = {}
    for _, it in ipairs(list) do items[#items + 1] = { text = it.label, file = it.path } end
    snacks.pick({ title = 'http', items = items, format = 'file' })
    return
  end
  local okt, pickers = pcall(require, 'telescope.pickers')
  if okt then
    local finders = require('telescope.finders')
    local conf = require('telescope.config').values
    pickers.new({ prompt_title = 'http' }, {
      finder = finders.new_table({ results = list, entry_maker = function(it)
        return { value = it.path, display = it.label, ordinal = it.label, path = it.path, filename = it.path }
      end }),
      sorter = conf.generic_sorter({}),
      previewer = conf.file_previewer({}),
    }):find()
    return
  end
  local labels = {}
  for _, it in ipairs(list) do labels[#labels + 1] = it.label end
  vim.ui.select(labels, { prompt = 'http file' }, function(c, i) if c then open(list[i].path) end end)
end

vim.api.nvim_create_user_command('BarqFiles', function() _G.barq_files() end, {})
local function map(buf)
  vim.keymap.set('n', '<C-p>', _G.barq_files, { buffer = buf, desc = 'barq http files', nowait = true })
end
vim.api.nvim_create_autocmd('FileType', {
  group = vim.api.nvim_create_augroup('barq_picker', { clear = true }),
  pattern = 'http',
  callback = function(ev) map(ev.buf) end,
})
for _, b in ipairs(vim.api.nvim_list_bufs()) do
  if vim.api.nvim_buf_is_loaded(b) and vim.bo[b].filetype == 'http' then map(b) end
end
`
