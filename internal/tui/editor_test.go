package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/iskaa02/barq/internal/core"
)

func TestHeadersTextRoundTrip(t *testing.T) {
	rows := []core.HeaderRow{
		{Key: "Accept", Value: "*/*", Enabled: true},
		{Key: "X-Off", Value: "a: b", Enabled: false},
	}
	if got := textToHeaders(headersToText(rows)); !reflect.DeepEqual(got, rows) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, rows)
	}
}

func TestWrapLinesMatchesHardwrap(t *testing.T) {
	in := "short\n" + strings.Repeat("x", 25) + "\n\x1b[31m" + strings.Repeat("y", 12) + "\x1b[0m\n\nend"
	for _, w := range []int{5, 10, 30} {
		if got, want := wrapLines(in, w), ansi.Hardwrap(in, w, true); got != want {
			t.Errorf("width %d:\n got %q\nwant %q", w, got, want)
		}
	}
}

func TestTypedWordsThatAreKeyNames(t *testing.T) {
	h := newFormEditor("/")
	h.Focus()
	// How Bubble Tea delivers "label:Home sweet home" typed quickly.
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("label:Home")},
		{Type: tea.KeySpace, Runes: []rune(" ")},
		{Type: tea.KeyRunes, Runes: []rune("sweet")},
		{Type: tea.KeySpace, Runes: []rune(" ")},
		{Type: tea.KeyRunes, Runes: []rune("home")},
		{Type: tea.KeySpace, Runes: []rune(" ")},
		{Type: tea.KeyRunes, Runes: []rune("end")},
	} {
		h, _ = h.Update(literalText(k))
	}
	if got := h.Rows()[0].Value; got != "Home sweet home end" {
		t.Errorf("value = %q", got)
	}
	// A real Home key is still a key.
	if k := literalText(tea.KeyMsg{Type: tea.KeyHome}).(tea.KeyMsg); k.Paste {
		t.Error("the Home key must not become text")
	}
}

func TestRequestHTTPRoundTrip(t *testing.T) {
	for _, r := range []httpRequest{
		{Method: "POST", URL: "{{base}}/users?x=1", Body: "{\n  \"a\": 1\n}",
			Headers: []core.HeaderRow{
				{Key: "Content-Type", Value: "application/json", Enabled: true},
				{Key: "X-Off", Value: "a: b", Enabled: false},
			}},
		{Method: "GET"},
		{Method: "PUT", URL: "example.com", Body: "\nstarts with a blank line"},
		{Method: "POST", URL: "example.com/login", Body: "{}",
			Headers:  []core.HeaderRow{{Key: "Accept", Value: "*/*", Enabled: true}},
			Captures: []core.Capture{{Var: "token", Filter: ".data | {t: .token}"}}},
	} {
		got, ok := httpToRequest(requestToHTTP(r, false))
		if !ok || !reflect.DeepEqual(got, r) {
			t.Errorf("round trip:\n got %+v\nwant %+v", got, r)
		}
	}
}

func TestHTTPToRequest(t *testing.T) {
	got, ok := httpToRequest("\n## note\nexample.com/a\nAccept: */*\n## skipped\n# X-Off: 1\n\nbody\n")
	want := httpRequest{URL: "example.com/a", Body: "body", Headers: []core.HeaderRow{
		{Key: "Accept", Value: "*/*", Enabled: true},
		{Key: "X-Off", Value: "1", Enabled: false},
	}}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if _, ok := httpToRequest("## only comments\n\n"); ok {
		t.Error("a file without a request line must not parse")
	}
}

func TestHTTPCaptures(t *testing.T) {
	got, _ := httpToRequest("GET x\n@capture a = .a\n@capture a = .b\nAccept: */*\n")
	if want := []core.Capture{{Var: "a", Filter: ".b"}}; !reflect.DeepEqual(got.Captures, want) || len(got.Headers) != 1 {
		t.Errorf("captures %+v, headers %+v", got.Captures, got.Headers)
	}
	if got, _ := httpToRequest("GET x\n@capture nofilter\n"); got.CaptureErr == nil {
		t.Error("a bad @capture line must be reported")
	}
}
