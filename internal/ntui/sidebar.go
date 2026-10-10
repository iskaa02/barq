package ntui

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iskaa02/barq/internal/httpfile"
)

type entryKind int

const (
	dirEntry entryKind = iota
	fileEntry
	reqEntry
)

// entry is one sidebar row: a directory, an .http file, or a request in it.
type entry struct {
	Kind  entryKind
	Label string
	Path  string // full path of the file (empty for directories)
	Line  int    // 0-based request line, for requests
	Depth int
}

// skipDir reports whether the scan stays out of a directory.
func skipDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor"
}

// scanFiles lists the .http files under root, with their requests.
func scanFiles(root string) []entry {
	var files []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return nil
		case d.IsDir():
			if p != root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
		case strings.HasSuffix(d.Name(), ".http"):
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)

	var out []entry
	var shown []string // directories on the path of the last file
	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		dirs := parts[:len(parts)-1]
		keep := 0
		for keep < len(shown) && keep < len(dirs) && shown[keep] == dirs[keep] {
			keep++
		}
		shown = dirs
		for i := keep; i < len(dirs); i++ {
			out = append(out, entry{Kind: dirEntry, Label: dirs[i] + "/", Depth: i})
		}
		out = append(out, entry{Kind: fileEntry, Label: parts[len(parts)-1], Path: f, Depth: len(dirs)})
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, r := range httpfile.Parse(string(data)) {
			out = append(out, entry{Kind: reqEntry, Label: requestLabel(r), Path: f, Line: r.Line, Depth: len(dirs) + 1})
		}
	}
	return out
}

// requestLabel is the request's name, or "METHOD URL".
func requestLabel(r httpfile.Request) string {
	if r.Name != "" {
		return r.Name
	}
	return r.Method + " " + r.URL
}

// sidebar is the file tree. Directories are not selectable targets, but the
// cursor can rest on them.
type sidebar struct {
	entries []entry
	cur     int
	top     int
}

func (s *sidebar) set(entries []entry) {
	var path string
	var line int
	if e, ok := s.selected(); ok {
		path, line = e.Path, e.Line
	}
	s.entries = entries
	s.cur = min(s.cur, max(len(entries)-1, 0))
	for i, e := range entries { // stay on the same row if it still exists
		if path != "" && e.Path == path && e.Line == line {
			s.cur = i
			break
		}
	}
}

func (s *sidebar) selected() (entry, bool) {
	if s.cur < 0 || s.cur >= len(s.entries) {
		return entry{}, false
	}
	return s.entries[s.cur], true
}

func (s *sidebar) move(d int) {
	s.cur = min(max(s.cur+d, 0), max(len(s.entries)-1, 0))
}

// render draws h rows w cells wide. open is the path of the file in the editor.
func (s *sidebar) render(w, h int, focused bool, open string) []string {
	if len(s.entries) == 0 {
		return []string{muted.Render("no .http files"), muted.Render("press n to create one")}
	}
	if s.cur < s.top {
		s.top = s.cur
	}
	if s.cur >= s.top+h {
		s.top = s.cur - h + 1
	}
	var rows []string
	for i := s.top; i < len(s.entries) && i < s.top+h; i++ {
		e := s.entries[i]
		l := strings.Repeat("  ", e.Depth) + e.Label
		switch {
		case i == s.cur && focused:
			l = selStyle.Render(fit(l, w))
		case i == s.cur:
			l = boldStyle.Render(l)
		case e.Kind == dirEntry, e.Kind == reqEntry:
			l = muted.Render(l)
		case e.Path == open:
			l = okStyle.Render(l)
		}
		rows = append(rows, l)
	}
	return rows
}
