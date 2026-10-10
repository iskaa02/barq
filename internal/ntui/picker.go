package ntui

import (
	"path/filepath"
	"sync"
)

// pickerFiles is the mutex-guarded list of .http files (relative to the
// cwd) that barq's <C-p> picker offers; the rpcrequest handler answers from it.
var pickerFiles struct {
	sync.Mutex
	cwd  string
	list []string
}

// snapshotPickerFiles refreshes the picker's file list from disk. Call it
// wherever the sidebar rescans.
func (a *App) snapshotPickerFiles() {
	list := pickerList(a.cwd)
	pickerFiles.Lock()
	pickerFiles.cwd = a.cwd
	pickerFiles.list = list
	pickerFiles.Unlock()
}

// pickerList returns the .http files under root, relative and slash-separated.
func pickerList(root string) []string {
	list := []string{}
	for _, e := range scanFiles(root) {
		if e.Kind != fileEntry {
			continue
		}
		rel, err := filepath.Rel(root, e.Path)
		if err != nil {
			rel = e.Path
		}
		list = append(list, filepath.ToSlash(rel))
	}
	return list
}

// setupPicker installs <C-p> / :BarqFiles in the editor nvim, listing only
// barq's .http files.
func (a *App) setupPicker() error {
	err := a.ed.Handle("barq_http_files", func() (map[string]any, error) {
		pickerFiles.Lock()
		defer pickerFiles.Unlock()
		return map[string]any{"cwd": pickerFiles.cwd, "files": append([]string{}, pickerFiles.list...)}, nil
	})
	if err != nil {
		return err
	}
	a.snapshotPickerFiles()
	return a.ed.ExecLua(pickerLua, nil, a.ed.Channel())
}

const pickerLua = `
local chan = ...
function _G.barq_files()
  local ok, res = pcall(vim.rpcrequest, chan, 'barq_http_files')
  if not ok or type(res) ~= 'table' then return end
  local list, cwd = res.files or {}, res.cwd
  if cwd == nil or cwd == '' then cwd = vim.fn.getcwd() end
  if #list == 0 then vim.notify('barq: no .http files', vim.log.levels.INFO) return end
  local function open(rel)
    vim.cmd.edit(vim.fn.fnameescape(vim.fs.joinpath(cwd, rel)))
  end
  local okf, fzf = pcall(require, 'fzf-lua')
  if okf then
    fzf.fzf_exec(list, {
      prompt = 'http> ', cwd = cwd, previewer = 'builtin',
      actions = fzf.defaults.actions.files, file_icons = true, git_icons = false,
    })
    return
  end
  local oks, snacks = pcall(function() return Snacks.picker end)
  if oks and snacks then
    local items = {}
    for _, f in ipairs(list) do items[#items + 1] = { text = f, file = f, cwd = cwd } end
    snacks.pick({ title = 'http', items = items, format = 'file', cwd = cwd })
    return
  end
  local okt, pickers = pcall(require, 'telescope.pickers')
  if okt then
    local finders = require('telescope.finders')
    local conf = require('telescope.config').values
    local mk = require('telescope.make_entry')
    pickers.new({ cwd = cwd, prompt_title = 'http' }, {
      finder = finders.new_table({ results = list, entry_maker = mk.gen_from_file({ cwd = cwd }) }),
      sorter = conf.generic_sorter({}),
      previewer = conf.file_previewer({ cwd = cwd }),
    }):find()
    return
  end
  vim.ui.select(list, { prompt = 'http file' }, function(c) if c then open(c) end end)
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
