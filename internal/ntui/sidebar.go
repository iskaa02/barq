package ntui

import (
	"fmt"
	"image/color"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/iskaa02/barq/internal/core"
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
	Path  string // full path of the file or directory
	Line  int    // 0-based request line, for requests
	Depth int

	Rel        string // path relative to the project, slash-separated
	Method     string // requests only
	URL        string // requests only
	Start, End int    // requests: 0-based line range of the block, End exclusive
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
			out = append(out, entry{Kind: dirEntry, Label: dirs[i] + "/", Depth: i,
				Path: filepath.Join(root, filepath.FromSlash(strings.Join(dirs[:i+1], "/"))), Rel: strings.Join(dirs[:i+1], "/")})
		}
		out = append(out, entry{Kind: fileEntry, Label: parts[len(parts)-1], Path: f, Depth: len(dirs), Rel: filepath.ToSlash(rel)})
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, r := range httpfile.Parse(string(data)) {
			out = append(out, entry{Kind: reqEntry, Label: requestLabel(r), Path: f, Line: r.Line, Depth: len(dirs) + 1,
				Rel: filepath.ToSlash(rel), Method: r.Method, URL: r.URL, Start: r.Start, End: r.End})
		}
	}
	return out
}

// requestLabel is the request's name, else a short form of its URL (the
// path, or the host); the method is shown separately.
func requestLabel(r httpfile.Request) string {
	if r.Name != "" {
		return r.Name
	}
	if n := (core.Request{URL: r.URL}).SuggestedName(); n != "" {
		return n
	}
	return r.URL
}

// row is one visible sidebar line.
type row struct {
	entry
	Count     int  // requests inside (directories and files)
	Collapsed bool // directories and files
}

// sidebar is the file tree: directories, .http files and their requests.
// Directories and files fold; collapsed paths are remembered for the session.
type sidebar struct {
	entries   []entry
	collapsed map[string]bool
	filter    string
	filtering bool // the filter line is being typed into

	rows []row
	cur  int
	top  int

	// activePath/activeLine: where the editor's cursor is (0-based line).
	activePath string
	activeLine int
}

func filterTerms(q string) []string { return strings.Fields(strings.ToLower(q)) }

func containsAll(hay string, terms []string) bool {
	for _, t := range terms {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

// buildRows flattens entries into the visible rows. Collapsed directories
// and files hide what is inside them. With filter terms only matching
// requests and their parents are shown, all unfolded.
func buildRows(entries []entry, collapsed map[string]bool, terms []string) []row {
	n := len(entries)
	count := make([]int, n)
	for i, e := range entries {
		if e.Kind != reqEntry {
			continue
		}
		d := e.Depth
		for j := i - 1; j >= 0 && d > 0; j-- {
			if entries[j].Depth < d {
				count[j]++
				d = entries[j].Depth
			}
		}
	}
	// keep[i]: entry i survives the filter.
	keep := make([]bool, n)
	for i, e := range entries {
		if e.Kind != reqEntry {
			continue
		}
		if len(terms) > 0 {
			hay := strings.ToLower(strings.Join([]string{e.Label, e.Method, e.URL, e.Rel}, " "))
			if !containsAll(hay, terms) {
				continue
			}
		}
		keep[i] = true
		d := e.Depth
		for j := i - 1; j >= 0 && d > 0; j-- {
			if entries[j].Depth < d {
				keep[j] = true
				d = entries[j].Depth
			}
		}
	}
	var rows []row
	hideBelow := -1 // inside a collapsed entry of this depth
	for i, e := range entries {
		if hideBelow >= 0 {
			if e.Depth > hideBelow {
				continue
			}
			hideBelow = -1
		}
		if len(terms) > 0 && !keep[i] {
			continue
		}
		r := row{entry: e, Count: count[i]}
		if e.Kind != reqEntry && len(terms) == 0 && collapsed[e.Path] {
			r.Collapsed = true
			hideBelow = e.Depth
		}
		rows = append(rows, r)
	}
	return rows
}

func (s *sidebar) terms() []string { return filterTerms(s.filter) }

func (s *sidebar) key(r row) (entryKind, string, int) { return r.Kind, r.Path, r.Line }

// rebuild recomputes rows, keeping the cursor on the same item if it is
// still visible.
func (s *sidebar) rebuild() {
	var k entryKind
	var p string
	var l int
	had := false
	if r, ok := s.selected(); ok {
		k, p, l = s.key(r)
		had = true
	}
	s.rows = buildRows(s.entries, s.collapsed, s.terms())
	s.cur = min(s.cur, max(len(s.rows)-1, 0))
	if had {
		for i, r := range s.rows {
			if rk, rp, rl := s.key(r); rk == k && rp == p && rl == l {
				s.cur = i
				break
			}
		}
	}
}

func (s *sidebar) set(entries []entry) {
	s.entries = entries
	s.rebuild()
}

func (s *sidebar) selected() (row, bool) {
	if s.cur < 0 || s.cur >= len(s.rows) {
		return row{}, false
	}
	return s.rows[s.cur], true
}

func (s *sidebar) move(d int) {
	s.cur = min(max(s.cur+d, 0), max(len(s.rows)-1, 0))
}

// listTop is the number of rows above the list (the filter line).
func (s *sidebar) listTop() int {
	if s.filtering || s.filter != "" {
		return 1
	}
	return 0
}

// setCollapsed folds or unfolds the directory or file at the cursor.
func (s *sidebar) setCollapsed(r row, v bool) {
	if r.Kind == reqEntry || len(s.terms()) > 0 {
		return
	}
	if s.collapsed == nil {
		s.collapsed = map[string]bool{}
	}
	if v {
		s.collapsed[r.Path] = true
	} else {
		delete(s.collapsed, r.Path)
	}
	s.rebuild()
}

// toParent moves the cursor to the row's parent.
func (s *sidebar) toParent() {
	r, ok := s.selected()
	if !ok {
		return
	}
	for i := s.cur - 1; i >= 0; i-- {
		if s.rows[i].Depth < r.Depth {
			s.cur = i
			return
		}
	}
}

// left is h: fold an open directory or file, else go to the parent.
func (s *sidebar) left() {
	if r, ok := s.selected(); ok && r.Kind != reqEntry && !r.Collapsed && len(s.terms()) == 0 {
		s.setCollapsed(r, true)
		return
	}
	s.toParent()
}

// right is l: unfold, else step into the first child.
func (s *sidebar) right() {
	r, ok := s.selected()
	if !ok || r.Kind == reqEntry {
		return
	}
	if r.Collapsed {
		s.setCollapsed(r, false)
	} else if s.cur+1 < len(s.rows) && s.rows[s.cur+1].Depth > r.Depth {
		s.cur++
	}
}

// toggle is enter on a directory or file.
func (s *sidebar) toggle(r row) {
	s.setCollapsed(r, !r.Collapsed)
}

func (s *sidebar) setFilter(q string) {
	s.filter = q
	s.rebuild()
	s.top = 0
	s.cur = 0
	for i, r := range s.rows { // the first match
		if r.Kind == reqEntry {
			s.cur = i
			break
		}
	}
}

func (s *sidebar) clearFilter() {
	s.filter, s.filtering = "", false
	s.rebuild()
}

// filterKey handles a key while the filter is typed; it reports whether the
// key was consumed. enter is left to the caller.
func (s *sidebar) filterKey(name, text string) bool {
	switch name {
	case "esc":
		s.clearFilter()
	case "up", "ctrl+k":
		s.move(-1)
	case "down", "ctrl+j":
		s.move(1)
	case "backspace":
		if r := []rune(s.filter); len(r) > 0 {
			s.setFilter(string(r[:len(r)-1]))
		}
	case "ctrl+u":
		s.setFilter("")
	default:
		if text == "" || name == "enter" {
			return false
		}
		s.setFilter(s.filter + text)
	}
	return true
}

// setActive records the editor's cursor.
func (s *sidebar) setActive(path string, line int) {
	s.activePath, s.activeLine = path, line
}

func (s *sidebar) isActive(e entry) bool {
	return e.Kind == reqEntry && e.Path == s.activePath && s.activeLine >= e.Start && s.activeLine < e.End
}

// reveal moves the cursor to the active request, if it is visible.
func (s *sidebar) reveal() {
	for i, r := range s.rows {
		if s.isActive(r.entry) {
			s.cur = i
			return
		}
	}
}

// Rendering -------------------------------------------------------------------

func methodLabel(method string) string {
	if method == "OPTIONS" {
		return "OPT"
	}
	if len(method) > 6 {
		return method[:6]
	}
	return method
}

func methodColor(m string) color.Color {
	switch m {
	case "GET":
		return colGreen
	case "POST":
		return colYellow
	case "PUT", "PATCH":
		return colBlue
	case "DELETE":
		return colRed
	default:
		return colCyan
	}
}

var filterHit = lipgloss.NewStyle().Foreground(colYellow).Bold(true).Underline(true)

// highlightTerms styles s with base, marking where the filter words occur.
func highlightTerms(s string, terms []string, base lipgloss.Style) string {
	lower := strings.ToLower(s)
	if len(terms) == 0 || len(lower) != len(s) {
		return base.Render(s)
	}
	hit := make([]bool, len(s))
	for _, t := range terms {
		for i := 0; t != ""; {
			j := strings.Index(lower[i:], t)
			if j < 0 {
				break
			}
			for k := i + j; k < i+j+len(t); k++ {
				hit[k] = true
			}
			i += j + len(t)
		}
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && hit[j] == hit[i] {
			j++
		}
		if hit[i] {
			b.WriteString(filterHit.Render(s[i:j]))
		} else {
			b.WriteString(base.Render(s[i:j]))
		}
		i = j
	}
	return b.String()
}

// title is the box title: "files" and the request count.
func (s *sidebar) title() string {
	total, shown := 0, 0
	for _, e := range s.entries {
		if e.Kind == reqEntry {
			total++
		}
	}
	for _, r := range s.rows {
		if r.Kind == reqEntry {
			shown++
		}
	}
	c := fmt.Sprintf(" · %d", total)
	if s.filter != "" {
		c = fmt.Sprintf(" · %d/%d", shown, total)
	}
	return "files" + muted.Render(c)
}

func (s *sidebar) filterLine(w int) string {
	slash := accentBold.Render("/ ")
	q := accent.Render(s.filter)
	switch {
	case s.filtering:
		q += accent.Render("▏")
		if s.filter == "" {
			q = muted.Render("name, method, url, path…")
		}
	default:
		if hint := muted.Render("  esc clears"); ansi.StringWidth(slash+q+hint) <= w {
			q += hint
		}
	}
	return fit(slash+q, w)
}

// rowLine renders row r in w cells: the plain text in the selected style, or
// the coloured one. open marks requests whose file is in the editor.
func (s *sidebar) rowLine(r row, w int, selected, open bool) string {
	terms := s.terms()
	indent := strings.Repeat("  ", r.Depth)
	inner := w - 1 // the last cell holds the open marker
	var plain, styled string
	switch r.Kind {
	case dirEntry, fileEntry:
		arrow := "▾ "
		if r.Collapsed {
			arrow = "▸ "
		}
		name := strings.TrimSuffix(r.Label, "/")
		count := fmt.Sprintf(" %d", r.Count)
		name = ansi.Truncate(name, max(inner-len(indent)-len(arrow)-len(count), 4), "…")
		plain = indent + arrow + name + count
		styled = indent + muted.Render(arrow) + highlightTerms(name, terms, boldStyle) + muted.Render(count)
	default:
		method := fmt.Sprintf("%-6s", methodLabel(r.Method))
		name := ansi.Truncate(r.Label, max(inner-len(indent)-len(method)-1, 4), "…")
		plain = indent + method + " " + name
		ns := lipgloss.NewStyle()
		if s.isActive(r.entry) {
			ns = accentBold
		}
		styled = indent + lipgloss.NewStyle().Bold(true).Foreground(methodColor(r.Method)).Render(method) + " " + highlightTerms(name, terms, ns)
	}
	marker := " "
	if open && r.Kind == reqEntry {
		marker = "•"
	}
	pad := strings.Repeat(" ", max(inner-ansi.StringWidth(plain), 0))
	if selected {
		// Plain text, so the highlight is solid.
		return selStyle.Foreground(colAccent).Render(plain+pad) + muted.Render(marker)
	}
	return styled + pad + muted.Render(marker)
}

// render draws h rows w cells wide. open holds the paths of the files open
// in the editor.
func (s *sidebar) render(w, h int, focused bool, open map[string]bool) []string {
	var out []string
	if s.filtering || s.filter != "" {
		out = append(out, s.filterLine(w))
		h--
	}
	if len(s.rows) == 0 {
		msg := "No .http files here yet. n creates one."
		if len(s.entries) > 0 {
			msg = "No matches."
		}
		for _, l := range strings.Split(ansi.Wrap(msg, w, ""), "\n") {
			out = append(out, muted.Render(l))
		}
		return out
	}
	h = max(h, 1)
	s.cur = min(max(s.cur, 0), len(s.rows)-1)
	if s.cur < s.top {
		s.top = s.cur
	}
	if s.cur >= s.top+h {
		s.top = s.cur - h + 1
	}
	s.top = max(min(s.top, len(s.rows)-h), 0)
	for i := s.top; i < len(s.rows) && i < s.top+h; i++ {
		r := s.rows[i]
		out = append(out, s.rowLine(r, w, i == s.cur && focused, open[r.Path]))
	}
	return out
}
