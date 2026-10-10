package ntui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func sideTree(t *testing.T) []entry {
	root := t.TempDir()
	write(t, filepath.Join(root, "api.http"), "GET http://x/ping\n")
	write(t, filepath.Join(root, "requests", "auth", "login.http"), "### login\nPOST http://x/login\n\n### refresh\nPOST http://x/refresh\n")
	write(t, filepath.Join(root, "requests", "users.http"), "GET http://x/users\n\n###\nGET http://x/users/1\n\n###\nDELETE http://x/users/1\n")
	return scanFiles(root)
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
	want := map[string]int{"api.http": 1, "requests": 5, "auth": 2, "login.http": 2, "users.http": 3}
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
	if got := labels(rows); got != "api.http| /ping|requests" || !rows[2].Collapsed {
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
	if got := labels(rows); got != "requests| auth|  login.http|   refresh" {
		t.Fatalf("got %q", got)
	}
	// Method and path words work too, and all words must match.
	if n := len(buildRows(es, nil, filterTerms("delete users"))); n != 3 {
		t.Fatalf("rows %d", n)
	}
	if n := len(buildRows(es, nil, filterTerms("zzz"))); n != 0 {
		t.Fatalf("rows %d", n)
	}
}

func TestSidebarNavigation(t *testing.T) {
	var s sidebar
	s.set(sideTree(t))
	s.cur = 2 // requests/
	s.left()
	if r, _ := s.selected(); !r.Collapsed || len(s.rows) != 3 {
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
	if s.filter != "" || len(s.rows) != 11 {
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
	if got := ansi.Strip(lines[1]); !strings.HasPrefix(got, "  GET    /ping") {
		t.Errorf("request row %q", got)
	}
	if got := ansi.Strip(lines[0]); !strings.HasPrefix(got, "▾ api.http 1") {
		t.Errorf("file row %q", got)
	}
	// The cursor row stays visible when it scrolls past the window.
	s.cur = len(s.rows) - 1
	lines = s.render(24, 4, true, nil)
	if !strings.Contains(ansi.Strip(lines[3]), "DELETE") {
		t.Errorf("last row not visible: %q", ansi.Strip(lines[3]))
	}
}

func TestSidebarTruncateAndMarker(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.http"), "### a very long request name indeed\nPOST http://x\n")
	var s sidebar
	s.set(scanFiles(root))
	path := s.rows[0].Path
	out := ansi.Strip(s.render(20, 3, false, map[string]bool{path: true})[1])
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
