package ntui

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/iskaa02/barq/internal/httpfile"
)

func words(items []completion) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Word)
	}
	return out
}

func TestComplete(t *testing.T) {
	vars := map[string]string{"base": "http://x", "token": strings.Repeat("a", 50)}
	tests := []struct {
		role, line string
		col        int
		start      int
		want       []string
	}{
		{"", "GET {{ba", 8, 6, []string{"base}}"}},
		{"", "GET {{}}", 6, 6, []string{"base", "token", "$timestamp", "$isoTimestamp", "$uuid", "$randomInt"}},
		{"", "X: {{$u", 7, 5, []string{"$uuid}}"}},
		{"", "GET {{base}} ", 13, 0, nil},
		{"", "# @c", 4, 3, []string{"capture", "confirm"}},
		{"", "# @", 3, 3, []string{"name", "capture", "expect", "confirm"}},
		{"", "# @capture t = ", 15, 15, []string{"header ", "cookie "}},
		{"", "# @capture t = co", 17, 15, []string{"cookie "}},
		{"", "# @capture t = .da", 18, 15, nil},
		{"", "# @capture t = header X-Req", 28, 22, []string{"X-Request-ID"}},
		{"", "# @capture t = cookie ", 23, 15, nil},
		{"", "Cont", 4, 0, []string{"Content-Type: "}},
		{"", "Content-Type: app", 17, 14, []string{"application/json", "application/x-www-form-urlencoded", "application/xml", "application/octet-stream"}},
		{"", "Authorization:", 14, 14, []string{"Bearer ", "Basic "}},
		{"", "PO", 2, 0, []string{"POST "}},
		{"request", "", 0, 0, []string{"GET ", "POST ", "PUT ", "PATCH ", "DELETE ", "HEAD ", "OPTIONS "}},
		{"request", "GET http", 8, 0, nil},
		{"header", "Acc", 3, 0, []string{"Accept: ", "Accept-Encoding: ", "Accept-Language: "}},
		{"header", "# X-Debug", 9, 0, nil},
		{"body", "Acc", 3, 0, nil},
	}
	for _, tc := range tests {
		start, items := completeIn(tc.role, tc.line, tc.col, vars)
		if got := words(items); !reflect.DeepEqual(got, tc.want) && !(len(got) == 0 && len(tc.want) == 0) {
			t.Errorf("%q %q: got %v want %v", tc.role, tc.line, got, tc.want)
		}
		if len(tc.want) > 0 && start != tc.start {
			t.Errorf("%q: start %d want %d", tc.line, start, tc.start)
		}
	}
	_, items := complete("{{to", 4, vars)
	if len(items) != 1 || len(items[0].Menu) > 33 || !strings.HasSuffix(items[0].Menu, "…") {
		t.Errorf("menu not truncated: %+v", items)
	}
}

func TestUndefinedVars(t *testing.T) {
	lines := []string{"GET {{base}}/{{missing}}", "# {{commented}}", "X: {{$uuid}} {{ $nope }}"}
	got := undefinedVars(lines, map[string]string{"base": "x"})
	if len(got) != 2 || got[0].Line != 0 || got[0].Col != 13 || got[0].EndCol != 24 || got[0].Severity != 2 || got[1].Line != 2 {
		t.Errorf("got %+v", got)
	}
}

func TestToDiagnostics(t *testing.T) {
	text := "GET /x\nbad header\n"
	got := toDiagnostics(strings.Split(text, "\n"), httpfile.Problems(text))
	if len(got) != 1 || got[0].Line != 1 || got[0].Severity != 1 || got[0].EndCol != len("bad header") {
		t.Errorf("got %+v", got)
	}
}

func TestVarHints(t *testing.T) {
	got := varHints([]string{"{{a}} {{s}} {{z}}"}, map[string]string{"a": "1", "s": "pw"}, map[string]bool{"s": true})
	if want := []string{" = 1", " = " + masked}; !reflect.DeepEqual(got[0], want) {
		t.Errorf("got %v", got[0])
	}
}

func TestHoverLines(t *testing.T) {
	envs := []envInfo{{"dev", true, map[string]string{"a": "1"}}, {"prod", false, map[string]string{"a": "2"}}}
	if got := hoverLines("a", envs); len(got) != 3 || got[1] != "* dev: 1" {
		t.Errorf("got %v", got)
	}
	if got := hoverLines("zz", envs); len(got) != 2 {
		t.Errorf("got %v", got)
	}
}

func TestMaskVars(t *testing.T) {
	vars := map[string]string{"base": "http://x", "token": "s3cret"}
	got := maskVars(vars, map[string]bool{"token": true})
	if got["token"] != masked || got["base"] != "http://x" {
		t.Errorf("got %v", got)
	}
	if vars["token"] != "s3cret" {
		t.Error("input modified")
	}
}

func TestEnvSnapshotAtomic(t *testing.T) {
	a := &App{}
	a.envVars = map[string]string{"token": "s3cret"}
	a.envSecrets = map[string]bool{"token": true}
	if v := a.EnvVarsMasked()["token"]; v != masked {
		t.Errorf("secret leaked: %q", v)
	}
}

func TestCompleteFile(t *testing.T) {
	cwd := t.TempDir()
	for _, f := range []string{"images/a.png", "images/b.txt", "images/.hidden", "notes.txt"} {
		p := filepath.Join(cwd, f)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, nil, 0o644)
	}
	start, items, ok := completeFile("body", "avatar: @./im", cwd)
	if !ok || start != len("avatar: @") || !reflect.DeepEqual(words(items), []string{"./images/"}) {
		t.Errorf("%d %v %v", start, words(items), ok)
	}
	start, items, ok = completeFile("body", "avatar: @im", cwd)
	if !ok || start != len("avatar: @") || !reflect.DeepEqual(words(items), []string{"images/"}) {
		t.Errorf("%d %v %v", start, words(items), ok)
	}
	start, items, _ = completeFile("body", "avatar: @images/", cwd)
	if start != len("avatar: @") || !reflect.DeepEqual(words(items), []string{"images/a.png", "images/b.txt"}) {
		t.Errorf("%d %v", start, words(items))
	}
	_, items, _ = completeFile("body", "# avatar: @./images/b", cwd)
	if !reflect.DeepEqual(words(items), []string{"./images/b.txt"}) {
		t.Errorf("%v", words(items))
	}
	_, items, _ = completeFile("body", "avatar: @", cwd)
	if !reflect.DeepEqual(words(items), []string{"images/", "notes.txt"}) {
		t.Errorf("%v", words(items))
	}
	if _, _, ok := completeFile("header", "Accept: @x", cwd); ok {
		t.Error("headers are not files")
	}
}

func TestMissingFiles(t *testing.T) {
	cwd := t.TempDir()
	os.WriteFile(filepath.Join(cwd, "ok.txt"), []byte("x"), 0o644)
	text := "POST /u\nContent-Type: multipart/form-data\n\na: @ok.txt\nb: @nope.txt\n# c: @gone\nd: @{{v}}\n"
	ds := missingFiles(strings.Split(text, "\n"), text, cwd)
	if len(ds) != 1 || ds[0].Line != 4 || ds[0].Col != 3 || ds[0].Severity != 2 || !strings.Contains(ds[0].Message, "nope.txt") {
		t.Errorf("%+v", ds)
	}
}
