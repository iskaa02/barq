package ntui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/iskaa02/barq/internal/core"
	"github.com/iskaa02/barq/internal/httpfile"
	"github.com/neovim/go-client/nvim"
)

// Sidebar actions: new file / folder, rename, delete, move, copy.
//
// Request-level edits go through the editor's nvim buffer when the file is
// loaded there (so the user's undo works and an unsaved buffer isn't fought),
// else through the file on disk. File and directory operations refuse when
// an affected file has unsaved changes, then keep nvim's buffers in step.
// History follows renames and moves (Rekey). Deleting a request moves its
// runs aside, so the "#n" keys of those after it can shift down without
// merging into them.

// bufInfo is a loaded nvim buffer with a file name.
type bufInfo struct {
	Buf  int    `json:"buf"`
	Path string `json:"path"`
	Mod  bool   `json:"mod"`
}

const listBufsLua = `
local out = {}
for _, b in ipairs(vim.api.nvim_list_bufs()) do
  local n = vim.api.nvim_buf_get_name(b)
  if n ~= '' and vim.api.nvim_buf_is_loaded(b) then
    out[#out + 1] = { buf = b, path = n, mod = vim.bo[b].modified }
  end
end
return vim.json.encode(out)
`

func (a *App) openBufs() map[string]bufInfo {
	out := map[string]bufInfo{}
	var js string
	if err := a.ed.ExecLua(listBufsLua, &js); err != nil {
		return out
	}
	var l []bufInfo
	_ = json.Unmarshal([]byte(js), &l)
	for _, b := range l {
		out[b.Path] = b
	}
	return out
}

// readLines is the file's lines: its nvim buffer if loaded, else the disk.
func (a *App) readLines(path string) ([]string, error) {
	if b, ok := a.openBufs()[path]; ok {
		return a.ed.BufLines(nvimBuf(b.Buf))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return splitLines(string(data)), nil
}

// editFile rewrites a file's lines with fn: in its nvim buffer (set lines,
// then :write) if loaded there, else on disk. A nil result error with
// unchanged lines writes nothing.
func (a *App) editFile(path string, fn func([]string) ([]string, error)) error {
	b, loaded := a.openBufs()[path]
	var old []string
	var err error
	if loaded {
		old, err = a.ed.BufLines(nvimBuf(b.Buf))
	} else {
		var data []byte
		if data, err = os.ReadFile(path); err == nil {
			old = splitLines(string(data))
		}
	}
	if err != nil {
		return err
	}
	nw, err := fn(old)
	if err != nil {
		return err
	}
	s, e, repl := spliceRange(old, nw)
	if s == e && len(repl) == 0 {
		return nil
	}
	if !loaded {
		return os.WriteFile(path, []byte(joinLines(nw)), 0o644)
	}
	return a.ed.ExecLua(`local b, s, e, r = ...
vim.api.nvim_buf_set_lines(b, s, e, false, r)
vim.api.nvim_buf_call(b, function() vim.cmd('silent write') end)`, nil, b.Buf, s, e, repl)
}

// moveKeys applies history key changes (two phases, so swaps and shifts
// can't merge runs) and the in-session responses.
func (a *App) moveKeys(pairs []keyPair) {
	if len(pairs) == 0 {
		return
	}
	tmp := func(i int) string { return fmt.Sprintf("\x00rekey%d", i) }
	h, err := core.OpenHistory(a.ws)
	if err != nil {
		a.flashErr("history: " + err.Error())
		return
	}
	for i, p := range pairs {
		_ = h.Rekey(p[0], tmp(i))
	}
	for i, p := range pairs {
		if err := h.Rekey(tmp(i), p[1]); err != nil {
			a.flashErr("history: " + err.Error())
		}
	}
	a.mu.Lock()
	moved := map[string][]*Response{}
	for _, p := range pairs {
		if rs, ok := a.resps[p[0]]; ok {
			moved[p[1]] = rs
			delete(a.resps, p[0])
		}
	}
	for k, rs := range moved {
		for _, r := range rs {
			r.Key = k
		}
		a.resps[k] = rs
	}
	a.mu.Unlock()
}

// fileKeys is the history key changes of a whole file moving to newRel.
func (a *App) fileKeys(oldPath, newPath string) []keyPair {
	data, err := os.ReadFile(oldPath)
	if err != nil {
		return nil
	}
	reqs := httpfile.Parse(strings.Join(splitLines(string(data)), "\n"))
	return keyPairs(filepath.ToSlash(a.rel(oldPath)), reqs, filepath.ToSlash(a.rel(newPath)), reqs, identityIdx(len(reqs)))
}

// Prompts ---------------------------------------------------------------------

func (a *App) prompt(kind, label, value string, it item, dir string) tea.Cmd {
	a.modal, a.promptKind, a.promptLabel, a.promptItem, a.promptDir = nameModal, kind, label, it, dir
	a.input.Reset()
	a.input.Placeholder = ""
	a.input.SetValue(value)
	return a.input.Focus()
}

// ctxDir is the directory a new file or folder goes in: the selected
// directory, or the one holding the selected file or request.
func (a *App) ctxDir() string {
	r, ok := a.side.selected()
	switch {
	case !ok:
		return a.cwd
	case r.Kind == dirEntry:
		return r.Path
	}
	return filepath.Dir(r.Path)
}

func (a *App) dirLabel(dir string) string {
	if r := filepath.ToSlash(a.rel(dir)); r != "." {
		return r + "/"
	}
	return "./"
}

func (a *App) startNew(folder bool) tea.Cmd {
	dir := a.ctxDir()
	if folder {
		return a.prompt("folder", "new folder in "+a.dirLabel(dir), "", item{}, dir)
	}
	return a.prompt("file", "new file in "+a.dirLabel(dir), "", item{}, dir)
}

func (a *App) startRename() tea.Cmd {
	r, ok := a.side.selected()
	if !ok {
		return nil
	}
	it := a.side.itemOf(r)
	if r.File != "" {
		// The row shows the file's only request: rename what it shows.
		it = item{Kind: reqEntry, Path: r.Path, Rel: r.Rel, Name: r.Name, Idx: a.side.reqIdx(r.entry)}
	}
	if it.Kind == reqEntry {
		return a.prompt("rename", "rename request", it.Name, it, "")
	}
	return a.prompt("rename", "rename "+it.Rel, filepath.Base(it.Path), it, "")
}

// submitPrompt runs the prompt's action.
func (a *App) submitPrompt(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	switch a.promptKind {
	case "file":
		return a.createFile(value)
	case "folder":
		return a.createFolder(value)
	case "rename":
		return a.rename(a.promptItem, value)
	}
	return nil
}

// createFolder makes a directory (and parents) under the prompt's directory.
func (a *App) createFolder(name string) error {
	p := filepath.Join(a.promptDir, name)
	if !filepath.IsLocal(a.relTo(p)) {
		return errors.New("the name must be inside the project")
	}
	if exists(p) {
		return errors.New(a.rel(p) + " already exists")
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		return err
	}
	a.rescan()
	a.side.selectPath(dirEntry, p)
	return nil
}

func (a *App) relTo(p string) string {
	r, err := filepath.Rel(a.cwd, p)
	if err != nil {
		return ".."
	}
	return r
}

// Rename ------------------------------------------------------------------------

func (a *App) rename(it item, name string) error {
	if strings.ContainsAny(name, "/\\\n") {
		return errors.New("a name can't contain / (use m to move)")
	}
	switch it.Kind {
	case reqEntry:
		return a.renameRequest(it, name)
	case fileEntry:
		if !strings.HasSuffix(name, ".http") {
			name += ".http"
		}
	}
	dst := filepath.Join(filepath.Dir(it.Path), name)
	if dst == it.Path {
		return nil
	}
	if err := a.relocate(it, dst); err != nil {
		return err
	}
	a.rescan()
	a.side.selectPath(it.Kind, dst)
	return nil
}

func (a *App) renameRequest(it item, name string) error {
	var pairs []keyPair
	rel := filepath.ToSlash(a.rel(it.Path))
	err := a.editFile(it.Path, func(lines []string) ([]string, error) {
		reqs := parseLines(lines)
		k := findReq(reqs, it.Name, it.Idx)
		if k < 0 {
			return nil, errors.New("request not found (R rescans)")
		}
		for i, r := range reqs {
			if i != k && r.Name == name {
				return nil, fmt.Errorf("%s already has a request named %s", filepath.Base(it.Path), name)
			}
		}
		nl := renameBlock(lines, reqs[k], name)
		pairs = keyPairs(rel, reqs, rel, parseLines(nl), identityIdx(len(reqs)))
		return nl, nil
	})
	if err != nil {
		return err
	}
	a.moveKeys(pairs)
	a.rescan()
	a.side.selectReq(it.Path, name)
	return nil
}

// relocate renames or moves a file or directory on disk to dst, keeping nvim
// buffers and history. It refuses when dst exists or a file has unsaved changes.
func (a *App) relocate(it item, dst string) error {
	if exists(dst) {
		return errors.New(a.rel(dst) + " already exists")
	}
	files := httpFilesUnder(it.Path)
	bufs := a.openBufs()
	for _, f := range files {
		if b, ok := bufs[f]; ok && b.Mod {
			return fmt.Errorf("save %s first", filepath.Base(f))
		}
	}
	var pairs []keyPair
	for _, f := range files {
		pairs = append(pairs, a.fileKeys(f, filepath.Join(dst, strings.TrimPrefix(f, it.Path)))...)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(it.Path, dst); err != nil {
		return err
	}
	for _, f := range files {
		if b, ok := bufs[f]; ok {
			nf := filepath.Join(dst, strings.TrimPrefix(f, it.Path))
			if err := a.ed.ExecLua(`local b, new, old = ...
vim.api.nvim_buf_call(b, function()
  vim.cmd('keepalt silent file ' .. vim.fn.fnameescape(new))
  vim.cmd('silent! edit!')
end)
for _, o in ipairs(vim.api.nvim_list_bufs()) do
  if o ~= b and vim.api.nvim_buf_get_name(o) == old then pcall(vim.api.nvim_buf_delete, o, { force = true }) end
end`, nil, b.Buf, nf, f); err != nil {
				a.flashErr("editor: " + err.Error())
			}
		}
	}
	a.moveKeys(pairs)
	return nil
}

// Delete ------------------------------------------------------------------------

func plural(n int, one string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", one)
	}
	return fmt.Sprintf("%d %ss", n, one)
}

func (a *App) startDelete() {
	r, ok := a.side.selected()
	if !ok {
		return
	}
	it := a.side.itemOf(r)
	var text string
	switch it.Kind {
	case dirEntry:
		text = fmt.Sprintf("Delete folder %s/ and its %s?", it.Rel, plural(len(httpFilesUnder(it.Path)), "file"))
	case fileEntry:
		text = fmt.Sprintf("Delete %s (%s)?", it.Rel, plural(it.Count, "request"))
	default:
		n := it.Name
		if n == "" {
			n = r.Label
		}
		text = fmt.Sprintf("Delete request %s (%s)?", n, it.Rel)
	}
	a.modal, a.confirmText = confirmModal, text
	a.confirmFn = func() error { return a.delete(it) }
}

func (a *App) delete(it item) error {
	if it.Kind == reqEntry {
		var pairs []keyPair
		rel := filepath.ToSlash(a.rel(it.Path))
		err := a.editFile(it.Path, func(lines []string) ([]string, error) {
			reqs := parseLines(lines)
			k := findReq(reqs, it.Name, it.Idx)
			if k < 0 {
				return nil, errors.New("request not found (R rescans)")
			}
			nl := removeBlock(lines, reqs[k])
			gone := reqKey(rel, reqs[k], k)
			pairs = append([]keyPair{{gone, fmt.Sprintf("%s~deleted-%d", gone, time.Now().UnixNano())}},
				keyPairs(rel, reqs, rel, parseLines(nl), deleteIdx(len(reqs), k))...)
			return nl, nil
		})
		if err != nil {
			return err
		}
		a.moveKeys(pairs)
		a.rescan()
		return nil
	}
	bufs := a.openBufs()
	var open []bufInfo
	for _, f := range httpFilesUnder(it.Path) {
		if b, ok := bufs[f]; ok {
			if b.Mod {
				return fmt.Errorf("save %s first", filepath.Base(f))
			}
			open = append(open, b)
		}
	}
	if err := os.RemoveAll(it.Path); err != nil {
		return err
	}
	for _, b := range open {
		_ = a.ed.ExecLua(`pcall(vim.api.nvim_buf_delete, ..., { force = true })`, nil, b.Buf)
	}
	a.rescan()
	return nil
}

// Move and copy -------------------------------------------------------------------

func (a *App) startCarry(move bool) {
	r, ok := a.side.selected()
	if !ok {
		return
	}
	a.side.carry = &carry{move: move, item: a.side.itemOf(r)}
}

// drop puts the carried item on the selected row.
func (a *App) drop() error {
	c := a.side.carry
	t, ok := a.side.selected()
	if c == nil || !ok {
		return nil
	}
	var err error
	if c.item.Kind == reqEntry {
		err = a.dropRequest(c, t)
	} else {
		err = a.dropPath(c, t)
	}
	if err == nil {
		a.side.carry = nil
	}
	return err
}

// dropPath moves or copies a file or directory into the target's directory.
func (a *App) dropPath(c *carry, t row) error {
	it := c.item
	dir := t.Path
	if t.Kind != dirEntry {
		dir = filepath.Dir(t.Path)
	}
	if within(it.Path, dir) && (it.Kind == dirEntry) {
		return errors.New("can't put a folder inside itself")
	}
	dst := filepath.Join(dir, filepath.Base(it.Path))
	if c.move {
		if dst == it.Path {
			return errors.New(filepath.Base(it.Path) + " is already there")
		}
		if err := a.relocate(it, dst); err != nil {
			return err
		}
	} else {
		dst = filepath.Join(dir, copyBase(filepath.Base(it.Path), func(n string) bool { return exists(filepath.Join(dir, n)) }))
		if err := copyPath(it.Path, dst); err != nil {
			return err
		}
	}
	a.rescan()
	a.side.selectPath(it.Kind, dst)
	return nil
}

// dropRequest inserts the carried request's block into the target's file:
// after the target request, or at the end when dropped on a file.
func (a *App) dropRequest(c *carry, t row) error {
	if t.Kind == dirEntry {
		return errors.New("drop a request on a file or another request")
	}
	src := c.item
	tgt := a.side.itemOf(t)
	srcRel := filepath.ToSlash(a.rel(src.Path))
	dstRel := filepath.ToSlash(a.rel(t.Path))
	srcLines, err := a.readLines(src.Path)
	if err != nil {
		return err
	}
	srcReqs := parseLines(srcLines)
	k := findReq(srcReqs, src.Name, src.Idx)
	if k < 0 {
		return errors.New("request not found (R rescans)")
	}
	block := extractBlock(srcLines, srcReqs[k])
	// target request, -1 for the end (a file, or a one-request file's row)
	tIdx := func(reqs []httpfile.Request) int {
		if tgt.Kind == reqEntry {
			return findReq(reqs, tgt.Name, tgt.Idx)
		}
		return -1
	}
	var pairs []keyPair
	name := srcReqs[k].Name

	if src.Path == t.Path {
		err = a.editFile(src.Path, func(lines []string) ([]string, error) {
			reqs := parseLines(lines)
			k := findReq(reqs, src.Name, src.Idx)
			if k < 0 {
				return nil, errors.New("request not found (R rescans)")
			}
			ti := tIdx(reqs)
			if c.move {
				if ti == k {
					return lines, nil
				}
				without := removeBlock(lines, reqs[k])
				wreqs := parseLines(without)
				wt := ti
				if ti > k {
					wt--
				}
				at, _ := afterReq(without, wreqs, wt)
				nl := insertBlock(without, at, block)
				pairs = keyPairs(srcRel, reqs, srcRel, parseLines(nl), moveIdx(len(reqs), k, ti))
				return nl, nil
			}
			cname := name
			if name != "" {
				cname = uniqueName(name, func(n string) bool { return findReq(reqs, n, 0) >= 0 })
			}
			b := block
			if cname != name {
				b = blockWithName(block, cname)
			}
			at, pos := afterReq(lines, reqs, ti)
			nl := insertBlock(lines, at, b)
			pairs = keyPairs(srcRel, reqs, srcRel, parseLines(nl), insertIdx(len(reqs), pos))
			return nl, nil
		})
		if err != nil {
			return err
		}
		a.moveKeys(pairs)
		a.rescan()
		a.side.selectReq(t.Path, name) // best effort: the first one by that name
		return nil
	}

	newName := name
	var moved keyPair
	err = a.editFile(t.Path, func(lines []string) ([]string, error) {
		reqs := parseLines(lines)
		if name != "" {
			newName = uniqueName(name, func(n string) bool { return findReq(reqs, n, 0) >= 0 })
		}
		b := block
		if newName != name {
			b = blockWithName(block, newName)
		}
		at, pos := afterReq(lines, reqs, tIdx(reqs))
		nl := insertBlock(lines, at, b)
		nreqs := parseLines(nl)
		pairs = keyPairs(dstRel, reqs, dstRel, nreqs, insertIdx(len(reqs), pos))
		if pos < len(nreqs) {
			moved = keyPair{reqKey(srcRel, srcReqs[k], k), reqKey(dstRel, nreqs[pos], pos)}
		}
		return nl, nil
	})
	if err != nil {
		return err
	}
	if c.move {
		var srcPairs []keyPair
		err = a.editFile(src.Path, func(lines []string) ([]string, error) {
			reqs := parseLines(lines)
			k := findReq(reqs, src.Name, src.Idx)
			if k < 0 {
				return nil, errors.New("request not found (R rescans)")
			}
			nl := removeBlock(lines, reqs[k])
			srcPairs = keyPairs(srcRel, reqs, srcRel, parseLines(nl), deleteIdx(len(reqs), k))
			return nl, nil
		})
		if err != nil {
			a.rescan()
			return fmt.Errorf("copied to %s, but removing the original failed: %w", filepath.Base(t.Path), err)
		}
		pairs = append(pairs, moved)
		pairs = append(pairs, srcPairs...)
	}
	a.moveKeys(pairs)
	a.rescan()
	a.side.selectReq(t.Path, newName)
	return nil
}

// nvimBuf converts a buffer number from Lua.
func nvimBuf(n int) nvim.Buffer { return nvim.Buffer(n) }
