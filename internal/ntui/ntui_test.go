package ntui

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iskaa02/barq/internal/core"
	"github.com/iskaa02/barq/internal/httpfile"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanFiles(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.http"), "### login\nPOST http://x/login\n\n### \nhttp://x/me\n")
	write(t, filepath.Join(root, "api", "v1", "b.http"), "GET http://x/b\n")
	write(t, filepath.Join(root, ".git", "c.http"), "GET http://x\n")
	write(t, filepath.Join(root, "node_modules", "d.http"), "GET http://x\n")
	write(t, filepath.Join(root, "vendor", "e.http"), "GET http://x\n")
	write(t, filepath.Join(root, "notes.txt"), "no")

	var got []string
	for _, e := range scanFiles(root) {
		got = append(got, strings.Repeat(" ", e.Depth)+e.Label)
	}
	want := []string{
		"a.http", " login", " /me",
		"api/", " v1/", "  b.http", "   /b",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestRequestKey(t *testing.T) {
	reqs := httpfile.Parse("### one\nGET http://x\n### \nGET http://y\n")
	a := &App{cwd: "/p"}
	if k := a.requestKey("/p/d/a.http", reqs, 0); k != "d/a.http#one" {
		t.Errorf("named: %q", k)
	}
	if k := a.requestKey("/p/d/a.http", reqs, 1); k != "d/a.http#2" {
		t.Errorf("unnamed: %q", k)
	}
}

func TestCurlFor(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "p.json"), `{"a":1}`)
	reqs := httpfile.Parse("POST {{base}}/x\nContent-Type: application/json\n# X-Off: 1\n\n< p.json\n")
	resolve := func(r core.Request) (core.Request, []string) {
		return core.ResolveVars(r, map[string]string{"base": "http://h"})
	}
	got, err := curlFor(reqs[0], dir, resolve)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"curl -X POST 'http://h/x'", "Content-Type: application/json", `{"a":1}`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "X-Off") {
		t.Errorf("disabled header in\n%s", got)
	}
	// Undefined variables: fall back to the raw request.
	got, _ = curlFor(reqs[0], dir, func(r core.Request) (core.Request, []string) { return r, []string{"base"} })
	if !strings.Contains(got, "{{base}}") {
		t.Errorf("want unresolved, got\n%s", got)
	}
}

func TestBodyLines(t *testing.T) {
	res := &core.Response{Headers: http.Header{"Content-Type": {"application/json"}}, Body: []byte(`{"a":1}`), Size: 7}
	lines, ft := bodyLines(res)
	if ft != "json" || strings.Join(lines, "\n") != "{\n  \"a\": 1\n}" {
		t.Errorf("json: %q %q", ft, lines)
	}
	res = &core.Response{Headers: http.Header{"Content-Type": {"text/html; charset=utf-8"}}, Body: []byte("<p>"), Size: 3}
	if _, ft = bodyLines(res); ft != "html" {
		t.Errorf("html: %q", ft)
	}
	res = &core.Response{Headers: http.Header{}, Body: []byte("plain"), Size: 5}
	if _, ft = bodyLines(res); ft != "text" {
		t.Errorf("text: %q", ft)
	}
}
