package main

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestSubstitute(t *testing.T) {
	vars := map[string]string{"base": "https://x.com", "token": "abc"}
	missing := map[string]bool{}
	got := substitute("{{base}}/u?t={{ token }}&n={{nope}}", vars, missing)
	if got != "https://x.com/u?t=abc&n={{nope}}" {
		t.Errorf("got %q", got)
	}
	if !missing["nope"] || len(missing) != 1 {
		t.Errorf("missing = %v", missing)
	}
	if id := substitute("{{$uuid}}", nil, map[string]bool{}); len(id) != 36 || strings.Contains(id, "{") {
		t.Errorf("$uuid gave %q", id)
	}
}

func TestHeadersTextRoundTrip(t *testing.T) {
	rows := []headerRow{
		{key: "Accept", value: "*/*", enabled: true},
		{key: "X-Off", value: "a: b", enabled: false},
	}
	if got := textToHeaders(headersToText(rows)); !reflect.DeepEqual(got, rows) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, rows)
	}
}

func TestRunJQ(t *testing.T) {
	body := []byte(`{"data":[{"id":12345678901234567890,"name":"a"},{"id":2,"name":"b"}],"token":"xyz"}`)

	out, err := runJQ(".data[].name", body)
	if err != nil || !reflect.DeepEqual(out, []any{"a", "b"}) {
		t.Errorf("names: %v %v", out, err)
	}
	out, err = runJQ(".data[0].id", body)
	if err != nil || marshalJQ(out[0], false) != "12345678901234567890" {
		t.Errorf("big int should stay exact: %v %v", out, err)
	}
	if _, err := runJQ(".data[", body); err == nil {
		t.Error("expected a parse error")
	}
	if _, err := runJQ(".", []byte("<html>")); err == nil {
		t.Error("expected an error for non-JSON")
	}
	if _, err := runJQ("def f: f; f", body); err == nil {
		t.Error("expected an endless filter to be stopped")
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
	if got := h.Rows()[0].value; got != "Home sweet home end" {
		t.Errorf("value = %q", got)
	}
	// A real Home key is still a key.
	if k := literalText(tea.KeyMsg{Type: tea.KeyHome}).(tea.KeyMsg); k.Paste {
		t.Error("the Home key must not become text")
	}
}
