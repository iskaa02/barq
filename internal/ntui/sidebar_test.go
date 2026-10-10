package ntui

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/iskaa02/barq/internal/runner"
)

func sideTree(t *testing.T) []entry {
	root := filepath.Join(t.TempDir(), "proj")
	write(t, filepath.Join(root, "api.http"), "GET http://x/ping\n")
	write(t, filepath.Join(root, "requests", "auth", "login.http"), "### login\nPOST http://x/login\n\n### refresh\nPOST http://x/refresh\n")
	write(t, filepath.Join(root, "requests", "users.http"), "GET http://x/users\n\n###\nGET http://x/users/1\n\n###\nDELETE http://x/users/1\n")
	return scanFiles(runner.Roots{Project: root, Store: filepath.Join(t.TempDir(), "store")})
}

func labels(rows []row) string {
	var out []string
	for _, r := range rows {
		out = append(out, strings.Repeat(" ", r.Depth)+strings.TrimSuffix(r.Label, "/"))
	}
	return strings.Join(out, "|")
}

func TestBuildRowsCounts(t *testing.T) {
	rows := buildRows(sideTree(t), nil, nil)
	got := map[string]int{}
	for _, r := range rows {
		if r.Kind != reqEntry {
			got[strings.TrimSuffix(r.Label, "/")] = r.Count
		}
	}
	want := map[string]int{".barq": 0, "requests": 5, "auth": 2, "login.http": 2, "users.http": 3}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: count %d, want %d", k, got[k], v)
		}
	}
}

func TestBuildRowsCollapse(t *testing.T) {
	es := sideTree(t)
	var reqDir, users string
	for _, e := range es {
		if e.Label == "requests/" {
			reqDir = e.Path
		}
		if e.Label == "users.http" {
			users = e.Path
		}
	}
	rows := buildRows(es, map[string]bool{reqDir: true}, nil)
	if got := labels(rows); got != ".barq|proj| /ping| requests" || !rows[3].Collapsed {
		t.Fatalf("dir collapsed: %q", got)
	}
	rows = buildRows(es, map[string]bool{users: true}, nil)
	if !strings.Contains(labels(rows), "users.http|") && !strings.HasSuffix(labels(rows), "users.http") {
		t.Fatalf("file collapsed: %q", labels(rows))
	}
	if strings.Contains(labels(rows), "/users") {
		t.Fatalf("requests of a collapsed file shown: %q", labels(rows))
	}
}

func TestBuildRowsFilter(t *testing.T) {
	es := sideTree(t)
	rows := buildRows(es, map[string]bool{es[0].Path: true}, filterTerms("refresh"))
	if got := labels(rows); got != "proj| requests|  auth|   login.http|    refresh" {
		t.Fatalf("got %q", got)
	}
	// Method and path words work too, and all words must match.
	if n := len(buildRows(es, nil, filterTerms("delete users"))); n != 4 {
		t.Fatalf("rows %d", n)
	}
	if n := len(buildRows(es, nil, filterTerms("zzz"))); n != 0 {
		t.Fatalf("rows %d", n)
	}
}

func TestSidebarNavigation(t *testing.T) {
	var s sidebar
	s.set(sideTree(t))
	s.cur = 3 // requests/
	s.left()
	if r, _ := s.selected(); !r.Collapsed || len(s.rows) != 4 {
		t.Fatalf("not collapsed: %q", labels(s.rows))
	}
	s.right()
	if len(s.rows) < 6 {
		t.Fatalf("not expanded: %q", labels(s.rows))
	}
	s.right() // into the first child, auth/
	s.left()  // folds it
	s.left()  // to the parent
	if r, _ := s.selected(); r.Label != "requests/" {
		t.Fatalf("not on parent: %q", r.Label)
	}
}

func TestSidebarFilterKeys(t *testing.T) {
	var s sidebar
	s.set(sideTree(t))
	s.filtering = true
	for _, c := range "users" {
		s.filterKey(string(c), string(c))
	}
	if r, _ := s.selected(); r.Kind != reqEntry || s.filter != "users" {
		t.Fatalf("filter %q sel %+v", s.filter, r)
	}
	s.filterKey("backspace", "")
	if s.filter != "user" {
		t.Fatal(s.filter)
	}
	s.filterKey("esc", "")
	if s.filter != "" || len(s.rows) != 12 {
		t.Fatalf("not cleared: %d", len(s.rows))
	}
}

func TestSidebarActive(t *testing.T) {
	var s sidebar
	es := sideTree(t)
	s.set(es)
	for _, e := range es {
		if e.Label == "users.http" {
			s.setActive(e.Path, 4) // inside the second request
		}
	}
	n := 0
	for _, r := range s.rows {
		if s.isActive(r.entry) {
			n++
			if r.Label != "/users/1" || r.Method != "GET" {
				t.Errorf("active %+v", r.entry)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d active", n)
	}
}

func TestSidebarRender(t *testing.T) {
	var s sidebar
	s.set(sideTree(t))
	lines := s.render(24, 4, true, map[string]bool{})
	if len(lines) != 4 {
		t.Fatalf("%d lines", len(lines))
	}
	for _, l := range lines {
		if w := ansi.StringWidth(l); w != 24 {
			t.Errorf("width %d: %q", w, ansi.Strip(l))
		}
	}
	if got := ansi.Strip(lines[2]); !strings.HasPrefix(got, "  GET    /ping  api.h") {
		t.Errorf("single-request file row %q", got)
	}
	if got := ansi.Strip(lines[3]); !strings.HasPrefix(got, "  ▾ requests 5") {
		t.Errorf("dir row %q", got)
	}
	// The cursor row stays visible when it scrolls past the window.
	s.cur = len(s.rows) - 1
	lines = s.render(24, 4, true, nil)
	if !strings.Contains(ansi.Strip(lines[3]), "DELETE") {
		t.Errorf("last row not visible: %q", ansi.Strip(lines[3]))
	}
}

func TestSidebarTruncateAndMarker(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj")
	write(t, filepath.Join(root, "a.http"), "### a very long request name indeed\nPOST http://x\n")
	var s sidebar
	s.set(scanFiles(runner.Roots{Project: root, Store: filepath.Join(t.TempDir(), "store")}))
	path := s.rows[2].Path
	out := ansi.Strip(s.render(20, 3, false, map[string]bool{path: true})[2])
	if !strings.Contains(out, "…") || !strings.HasSuffix(out, "•") || ansi.StringWidth(out) != 20 {
		t.Errorf("row %q", out)
	}
}

func TestSidebarEmpty(t *testing.T) {
	var s sidebar
	s.set(nil)
	out := strings.Join(s.render(20, 5, false, nil), "\n")
	if !strings.Contains(ansi.Strip(out), "No .http files") {
		t.Errorf("%q", out)
	}
}

func TestSingleRequestFileIsOneRow(t *testing.T) {
	rows := buildRows(sideTree(t), nil, nil)
	r := rows[2]
	if r.Kind != reqEntry || r.Depth != 1 || r.File != "api.http" || r.Label != "/ping" {
		t.Fatalf("got %+v", r)
	}
	// Files with several requests still group them.
	if !strings.Contains(labels(rows), "users.http|   /users") {
		t.Fatalf("got %q", labels(rows))
	}
}

func TestSectionsAndStore(t *testing.T) {
	proj, store := filepath.Join(t.TempDir(), "proj"), filepath.Join(t.TempDir(), "store")
	rt := runner.Roots{Project: proj, Store: store}

	// Nothing anywhere: only the (missing) store section, and nothing is created.
	es := scanFiles(rt)
	if len(es) != 1 || es[0].Label != ".barq/" || es[0].Path != store || es[0].Rel != ".barq" {
		t.Fatalf("empty: %+v", es)
	}
	if exists(store) {
		t.Fatal("scan created the store")
	}

	write(t, filepath.Join(proj, "api.http"), "### login\nGET http://x/p\n\n### b\nGET http://x/p2\n")
	write(t, filepath.Join(store, "api.http"), "### login\nGET http://x/s\n\n### b\nGET http://x/s2\n")
	write(t, filepath.Join(store, "d", "z.http"), "GET http://x/z\n")
	es = scanFiles(rt)
	var s sidebar
	s.set(es)
	if got, want := labels(s.rows), ".barq| api.http|  login|  b| d|  /z|proj| api.http|  login|  b"; got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	// Same file name in both sections: distinct paths and ref paths.
	var rels []string
	for _, e := range es {
		if e.Kind == fileEntry {
			rels = append(rels, e.Rel)
		}
	}
	if got := strings.Join(rels, " "); got != ".barq/api.http .barq/d/z.http api.http" {
		t.Errorf("rels %q", got)
	}
	// Collapse state is per section.
	s.setCollapsed(s.rows[0], true)
	if got := labels(s.rows); got != ".barq|proj| api.http|  login|  b" {
		t.Errorf("collapsed store: %q", got)
	}
}

func TestSectionContext(t *testing.T) {
	proj, store := filepath.Join(t.TempDir(), "proj"), filepath.Join(t.TempDir(), "store")
	a := &App{cwd: proj, store: store}
	if d := a.ctxDir(); d != store {
		t.Errorf("nothing selected: %s", d)
	}
	write(t, filepath.Join(proj, "sub", "api.http"), "GET http://x\n")
	a.side.set(scanFiles(a.roots()))
	for i, r := range a.side.rows {
		a.side.cur = i
		want := map[string]string{".barq/": store, "proj/": proj, "api.http": filepath.Join(proj, "sub"), "sub/": filepath.Join(proj, "sub")}[r.Label]
		if r.File != "" {
			want = filepath.Join(proj, "sub")
		}
		if got := a.ctxDir(); got != want {
			t.Errorf("row %q: ctxDir %s, want %s", r.Label, got, want)
		}
	}
	if a.dirLabel(store) != ".barq/" || a.dirLabel(proj) != "./" || a.dirLabel(filepath.Join(proj, "sub")) != "sub/" || a.dirLabel(filepath.Join(store, "x")) != ".barq/x/" {
		t.Error("dirLabel")
	}
	if !a.isSection(store) || !a.isSection(proj) || a.isSection(filepath.Join(proj, "sub")) {
		t.Error("isSection")
	}
	a.tabs = []tab{{Path: filepath.Join(proj, "api.http")}, {Path: filepath.Join(store, "api.http")}, {Path: filepath.Join(store, "b.http")}}
	if a.tabLabel(a.tabs[0]) != "api.http" || a.tabLabel(a.tabs[1]) != ".barq/api.http" || a.tabLabel(a.tabs[2]) != "b.http" {
		t.Errorf("tab labels %q %q %q", a.tabLabel(a.tabs[0]), a.tabLabel(a.tabs[1]), a.tabLabel(a.tabs[2]))
	}
}

func TestMoveAcrossRootsKeys(t *testing.T) {
	proj, store := filepath.Join(t.TempDir(), "proj"), filepath.Join(t.TempDir(), "store")
	a := &App{cwd: proj, store: store}
	write(t, filepath.Join(proj, "api.http"), "### login\nGET http://x\n\n###\nGET http://y\n")
	got := a.fileKeys(filepath.Join(proj, "api.http"), filepath.Join(store, "api.http"))
	want := []keyPair{{"api.http#login", ".barq/api.http#login"}, {"api.http#2", ".barq/api.http#2"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
	// And back, with a sub directory.
	write(t, filepath.Join(store, "d", "b.http"), "### x\nGET http://x\n")
	got = a.fileKeys(filepath.Join(store, "d", "b.http"), filepath.Join(proj, "sub", "b.http"))
	if want = []keyPair{{".barq/d/b.http#x", "sub/b.http#x"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestMovePathAndLazyStore(t *testing.T) {
	proj, store := filepath.Join(t.TempDir(), "proj"), filepath.Join(t.TempDir(), "store")
	write(t, filepath.Join(proj, "d", "a.http"), "GET http://x\n")
	dst := filepath.Join(store, "d")
	if exists(store) {
		t.Fatal("store exists before the first write")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := movePath(filepath.Join(proj, "d"), dst); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(dst, "a.http")) || exists(filepath.Join(proj, "d")) {
		t.Error("not moved")
	}
	a := &App{cwd: proj, store: filepath.Join(t.TempDir(), "s2")}
	a.promptDir = a.store
	if err := a.createFolder("x/y"); err != nil || !exists(filepath.Join(a.store, "x", "y")) {
		t.Errorf("createFolder: %v", err)
	}
	if err := a.createFolder("../../escape"); err == nil {
		t.Error("a folder outside both roots must be refused")
	}
}
