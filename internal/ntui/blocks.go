package ntui

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/iskaa02/barq/internal/httpfile"
	"github.com/iskaa02/barq/internal/runner"
)

// Pure helpers for the sidebar's rename / delete / move / copy. Request
// edits work on a file's lines (no trailing newline element), the same lines
// an nvim buffer holds, so one implementation serves buffers and disk.

func splitLines(text string) []string {
	text = strings.TrimSuffix(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func joinLines(l []string) string {
	if len(l) == 0 {
		return ""
	}
	return strings.Join(l, "\n") + "\n"
}

func parseLines(lines []string) []httpfile.Request {
	return httpfile.Parse(strings.Join(lines, "\n"))
}

// findReq locates a request by name (preferring position idx, 1-based, when
// several share it), else by position when it is unnamed. -1 if absent.
func findReq(reqs []httpfile.Request, name string, idx int) int {
	if name != "" {
		if idx >= 1 && idx <= len(reqs) && reqs[idx-1].Name == name {
			return idx - 1
		}
		for i, r := range reqs {
			if r.Name == name {
				return i
			}
		}
		return -1
	}
	if idx >= 1 && idx <= len(reqs) && reqs[idx-1].Name == "" {
		return idx - 1
	}
	return -1
}

// renameBlock gives request r the name: it rewrites the "# @name" line if
// that is what names it, else the "###" line, else adds a "### name" line.
func renameBlock(lines []string, r httpfile.Request, name string) []string {
	out := slices.Clone(lines)
	for i := r.Start; i < r.Line; i++ {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(lines[i]), "# @name"); ok && (rest == "" || rest[0] == ' ') {
			out[i] = "# @name " + name
			return out
		}
	}
	if strings.HasPrefix(lines[r.Start], "###") {
		out[r.Start] = "### " + name
		return out
	}
	return slices.Insert(out, r.Start, "### "+name)
}

// removeBlock drops request r's lines.
func removeBlock(lines []string, r httpfile.Request) []string {
	out := slices.Delete(slices.Clone(lines), r.Start, r.End)
	if r.End >= len(lines) { // it was last: don't leave its separator behind
		for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
			out = out[:len(out)-1]
		}
	}
	return out
}

// extractBlock is request r's lines as a free-standing block: it starts with
// a ### line and has no trailing blank lines.
func extractBlock(lines []string, r httpfile.Request) []string {
	b := slices.Clone(lines[r.Start:r.End])
	for len(b) > 0 && strings.TrimSpace(b[len(b)-1]) == "" {
		b = b[:len(b)-1]
	}
	if !strings.HasPrefix(b[0], "###") {
		b = slices.Insert(b, 0, "###")
	}
	return b
}

// insertBlock puts block at line index at, keeping one blank line between
// blocks.
func insertBlock(lines []string, at int, block []string) []string {
	at = min(max(at, 0), len(lines))
	out := slices.Clone(lines[:at])
	if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
		out = append(out, "")
	}
	out = append(out, block...)
	if at < len(lines) {
		out = append(out, "")
		out = append(out, lines[at:]...)
	}
	return out
}

// blockWithName renames a free-standing block.
func blockWithName(block []string, name string) []string {
	if reqs := parseLines(block); len(reqs) > 0 {
		return renameBlock(block, reqs[0], name)
	}
	return block
}

// afterReq is where a block dropped on request i of reqs goes: the line
// index and the new block's position among the requests. i < 0 is the end.
func afterReq(lines []string, reqs []httpfile.Request, i int) (at, pos int) {
	if i < 0 || i >= len(reqs) {
		return len(lines), len(reqs)
	}
	return reqs[i].End, i + 1
}

// uniqueName is name, or "name copy", "name copy 2"... when taken.
func uniqueName(name string, taken func(string) bool) string {
	if !taken(name) {
		return name
	}
	c := name + " copy"
	for n := 2; taken(c); n++ {
		c = name + " copy " + strconv.Itoa(n)
	}
	return c
}

// copyBase is base, or "stem-copy.http", "stem-copy-2.http"... when exists
// reports it taken. Directories have no extension.
func copyBase(base string, exists func(string) bool) string {
	if !exists(base) {
		return base
	}
	ext := ""
	if strings.HasSuffix(base, ".http") {
		ext = ".http"
	}
	stem := strings.TrimSuffix(base, ext)
	c := stem + "-copy" + ext
	for n := 2; exists(c); n++ {
		c = fmt.Sprintf("%s-copy-%d%s", stem, n, ext)
	}
	return c
}

// within reports whether p is dir or inside it.
func within(dir, p string) bool {
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// spliceRange finds the lines that differ between old and new: replacing
// old[s:e] with repl turns old into new.
func spliceRange(old, nw []string) (s, e int, repl []string) {
	for s < len(old) && s < len(nw) && old[s] == nw[s] {
		s++
	}
	e = len(old)
	ne := len(nw)
	for e > s && ne > s && old[e-1] == nw[ne-1] {
		e--
		ne--
	}
	return s, e, append([]string{}, nw[s:ne]...)
}

// History keys ------------------------------------------------------------

// keyPair moves history from one request key to another.
type keyPair [2]string

func reqKey(rel string, r httpfile.Request, i int) string {
	return runner.Ref{Path: rel, Name: r.Name, Index: i + 1}.Key()
}

// keyPairs lists the key changes of a file's requests when it goes from old
// (at oldRel) to nw (at newRel); idx[i] is where old request i went in nw, or
// -1 if it is gone.
func keyPairs(oldRel string, old []httpfile.Request, newRel string, nw []httpfile.Request, idx []int) []keyPair {
	var out []keyPair
	for i, j := range idx {
		if j < 0 || j >= len(nw) {
			continue
		}
		if a, b := reqKey(oldRel, old[i], i), reqKey(newRel, nw[j], j); a != b {
			out = append(out, keyPair{a, b})
		}
	}
	return out
}

func identityIdx(n int) []int {
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	return idx
}

// deleteIdx maps requests after request k is removed.
func deleteIdx(n, k int) []int {
	idx := identityIdx(n)
	idx[k] = -1
	for i := k + 1; i < n; i++ {
		idx[i] = i - 1
	}
	return idx
}

// insertIdx maps requests after a new one is put at position p.
func insertIdx(n, p int) []int {
	idx := identityIdx(n)
	for i := p; i < n; i++ {
		idx[i] = i + 1
	}
	return idx
}

// moveIdx maps requests after request k is moved to follow request t
// (t < 0: to the end).
func moveIdx(n, k, t int) []int {
	var order []int
	for i := 0; i < n; i++ {
		if i != k {
			order = append(order, i)
		}
	}
	pos := len(order)
	if t >= 0 {
		pos = slices.Index(order, t) + 1
	}
	order = slices.Insert(order, pos, k)
	idx := make([]int, n)
	for j, i := range order {
		idx[i] = j
	}
	return idx
}

// File system ---------------------------------------------------------------

// httpFilesUnder lists the .http files at or under p.
func httpFilesUnder(p string) []string {
	var out []string
	_ = filepath.WalkDir(p, func(f string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), ".http") {
			out = append(out, f)
		}
		return nil
	})
	return out
}

// copyPath copies a file or a directory tree to dst (which must not exist).
func copyPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst, info.Mode().Perm())
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	ents, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if err := copyPath(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}
