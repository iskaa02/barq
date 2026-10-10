package ntui

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iskaa02/barq/internal/core"
	"github.com/iskaa02/barq/internal/httpfile"
)

func TestCaptureEdit(t *testing.T) {
	text := "### a\n# @name a\nGET /x\n"
	lines := strings.Split(text, "\n")
	r := httpfile.Parse(text)[0]
	at, rep := captureEdit(lines, r, "id", ".id")
	if at != 2 || rep {
		t.Fatalf("insert: %d %v", at, rep)
	}
	text = "### a\n# @capture id = .old\n# @capture x = .x\nGET /x\n"
	lines = strings.Split(text, "\n")
	r = httpfile.Parse(text)[0]
	if at, rep = captureEdit(lines, r, "x", ".y"); at != 2 || !rep {
		t.Fatalf("replace: %d %v", at, rep)
	}
	if at, rep = captureEdit(lines, r, "z", ".z"); at != 3 || rep {
		t.Fatalf("new: %d %v", at, rep)
	}
}

func TestHeaderLines(t *testing.T) {
	res := &core.Response{Proto: "HTTP/1.1", Status: "200 OK",
		Headers: http.Header{"Content-Type": {"a"}, "Aa": {"1", "2"}}}
	want := []string{"HTTP/1.1 200 OK", "Aa: 1", "Aa: 2", "Content-Type: a"}
	if got := headerLines(res); !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestRequestLinesMasksSecrets(t *testing.T) {
	r := core.Request{Method: "POST", URL: "http://x/a", Body: "{\"k\":\"s3cr3t\"}",
		Headers: []core.SavedHeader{{Key: "Authorization", Value: "Bearer s3cr3t", Enabled: true}, {Key: "X-Off", Value: "1"}}}
	want := []string{"POST http://x/a", "Authorization: Bearer ••••", "", `{"k":"••••"}`}
	if got := requestLines(r, "", []string{"s3cr3t", ""}); !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestInfoLines(t *testing.T) {
	r := &Response{Res: &core.Response{Status: "200 OK", Size: 10}, Env: "dev",
		ReqLines: []string{"GET http://x/a"},
		Expects:  []expectResult{{true, "status 200"}, {false, "expected status 1, got 200"}},
		Caps:     []core.CapturedVar{{Var: "id", Value: "5"}, {Var: "token", Secret: true}}}
	got := strings.Join(infoLines(r), "\n")
	for _, w := range []string{"Status    200 OK", "Env       dev", "URL       http://x/a", "Expectations\n✓ status 200\n✗ expected", "Captured\nid = 5\ntoken = ••••"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in\n%s", w, got)
		}
	}
}

func TestViewTabs(t *testing.T) {
	if got := viewTabs(viewInfo, 20); strings.Contains(got, "Headers") || !strings.Contains(got, "I") {
		t.Fatalf("narrow: %q", got)
	}
	if got := viewTabs(viewInfo, 60); !strings.Contains(got, "Headers") {
		t.Fatalf("wide: %q", got)
	}
}

func TestHeaderCapture(t *testing.T) {
	for line, want := range map[string][2]string{
		"Content-Type: application/json": {"contentType", "header Content-Type"},
		"X-Request-Id: 42":               {"xRequestId", "header X-Request-Id"},
		"Set-Cookie: sid=abc; Path=/":    {"sid", "cookie sid"},
		"Set-Cookie: my.session=1":       {"my_session", "cookie my.session"},
	} {
		n, f, ok := headerCapture(line)
		if !ok || n != want[0] || f != want[1] {
			t.Errorf("%q: %q %q %v", line, n, f, ok)
		}
	}
	if _, _, ok := headerCapture("HTTP/1.1 200 OK"); ok {
		t.Error("status line accepted")
	}
}

func TestHistLines(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	runs := []core.HistMeta{
		{Time: at, Status: "200 OK", Duration: 12 * time.Millisecond, Size: 10, Env: "dev", ReqHash: "b"},
		{Time: at, Error: "boom", ReqHash: "a"},
	}
	got := histLines(runs)
	if len(got) != 2 || !strings.HasPrefix(got[0], " Jan  2 03:04:05  200 OK") || !strings.HasPrefix(got[1], "*") || !strings.Contains(got[1], "error") {
		t.Fatalf("got %q", got)
	}
	if got := histLines(nil); len(got) != 1 {
		t.Fatalf("empty: %q", got)
	}
}
