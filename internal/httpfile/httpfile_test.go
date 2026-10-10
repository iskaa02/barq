package httpfile

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/iskaa02/barq/internal/core"
)

const sample = `// file header
## nothing here

### login
# @capture token = .data.accessToken
# @expect status 200
# @expect jq .data.ok
# @confirm
# just a comment
post {{base}}/login
Content-Type: application/json
# X-Debug: 1
## note
// other

{"user": "x",

 "n": 1}


### list
https://example.com/items

###
# @name third
DELETE /x
`

func TestParse(t *testing.T) {
	reqs := Parse(sample)
	if len(reqs) != 3 {
		t.Fatalf("got %d requests", len(reqs))
	}
	a, b, c := reqs[0], reqs[1], reqs[2]
	if a.Name != "login" || a.Method != "POST" || a.URL != "{{base}}/login" || !a.Confirm {
		t.Errorf("a = %+v", a)
	}
	wantH := []core.HeaderRow{{Key: "Content-Type", Value: "application/json", Enabled: true}, {Key: "X-Debug", Value: "1"}}
	if !reflect.DeepEqual(a.Headers, wantH) {
		t.Errorf("headers = %+v", a.Headers)
	}
	if a.Body != "{\"user\": \"x\",\n\n \"n\": 1}" {
		t.Errorf("body = %q", a.Body)
	}
	if !reflect.DeepEqual(a.Captures, []core.Capture{{Var: "token", Filter: ".data.accessToken"}}) {
		t.Errorf("captures = %+v", a.Captures)
	}
	if !reflect.DeepEqual(a.Expects, []Expect{{"status", "200", 5}, {"jq", ".data.ok", 6}}) {
		t.Errorf("expects = %+v", a.Expects)
	}
	if a.Start != 3 || a.Line != 9 || a.BodyLine != 15 || a.End != 20 {
		t.Errorf("lines = %d %d %d %d", a.Start, a.Line, a.BodyLine, a.End)
	}
	if b.Name != "list" || b.Method != "GET" || b.URL != "https://example.com/items" || b.BodyLine != -1 || b.Body != "" {
		t.Errorf("b = %+v", b)
	}
	if c.Name != "third" || c.Method != "DELETE" {
		t.Errorf("c = %+v", c)
	}
}

func TestBodyFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "p.json"), []byte(`{"a":1}`), 0o644)
	reqs := Parse("POST /x\n\n< ./p.json\n\n")
	if len(reqs) != 1 || reqs[0].BodyFile != "./p.json" || reqs[0].Body != "" {
		t.Fatalf("%+v", reqs)
	}
	body, err := reqs[0].ResolveBody(dir)
	if err != nil || body != `{"a":1}` {
		t.Errorf("body %q err %v", body, err)
	}
	if _, err := (Request{BodyFile: "nope"}).ResolveBody(dir); err == nil {
		t.Error("want error")
	}
}

func TestRoundTrip(t *testing.T) {
	cases := []Request{
		{Name: "a", Method: "POST", URL: "/x", Headers: []core.HeaderRow{{Key: "A", Value: "b", Enabled: true}, {Key: "C", Value: "d"}},
			Body: "{\n\n}", Captures: []core.Capture{{Var: "t", Filter: ".a"}},
			Expects: []Expect{{Kind: "status", Arg: "201"}, {Kind: "jq", Arg: ".ok"}}, Confirm: true},
		{Method: "GET", URL: "/y"},
		{Method: "PUT", URL: "/z", BodyFile: "./b.json"},
	}
	for _, in := range cases {
		out := Parse(Format(in))
		if len(out) != 1 {
			t.Fatalf("%q: %d requests", Format(in), len(out))
		}
		got := out[0]
		if got.Name != in.Name || got.Method != in.Method || got.URL != in.URL || got.Body != in.Body ||
			got.BodyFile != in.BodyFile || got.Confirm != in.Confirm ||
			!reflect.DeepEqual(got.Headers, in.Headers) || !reflect.DeepEqual(got.Captures, in.Captures) {
			t.Errorf("round trip:\n in  %+v\n out %+v", in, got)
		}
		for i, e := range in.Expects {
			if got.Expects[i].Kind != e.Kind || got.Expects[i].Arg != e.Arg {
				t.Errorf("expects %+v", got.Expects)
			}
		}
	}
}

func TestFormatLayout(t *testing.T) {
	r := Request{Name: "n", Method: "POST", URL: "/u", Body: "x", Headers: []core.HeaderRow{{Key: "K", Value: "V"}}}
	want := "# @name n\nPOST /u\n# K: V\n\nx\n"
	if got := Format(r); got != want {
		t.Errorf("got %q", got)
	}
}

func TestCoreAndFromCore(t *testing.T) {
	c := core.Request{Name: "n", Method: "POST", URL: "/u", Body: "b",
		Headers:  []core.SavedHeader{{Key: "K", Value: "V", Enabled: true}},
		Captures: []core.Capture{{Var: "a", Filter: ".b"}}}
	r := FromCore(c)
	if !reflect.DeepEqual(r.Core(), c) {
		t.Errorf("%+v != %+v", r.Core(), c)
	}
	if FromCore(core.Request{URL: "/"}).Method != "GET" {
		t.Error("default method")
	}
}

func TestAt(t *testing.T) {
	reqs := Parse(sample)
	for line, want := range map[int]string{3: "login", 15: "login", 19: "login", 20: "list", 23: "third"} {
		r, ok := At(reqs, line)
		if !ok || r.Name != want {
			t.Errorf("line %d: %q %v", line, r.Name, ok)
		}
	}
	if _, ok := At(reqs, 1); ok {
		t.Error("header comment has no request")
	}
	if _, ok := At(reqs, 100); ok {
		t.Error("past end")
	}
}

func TestProblems(t *testing.T) {
	cases := []struct {
		name, text string
		line       int // -1: none expected
		warning    bool
	}{
		{"ok", "GET /x\nA: b\n# C: d\n## n\n\n{}", -1, false},
		{"header no colon", "GET /x\nbogus\n", 1, false},
		{"unknown directive", "# @nope\nGET /x", 0, false},
		{"bad capture", "# @capture nope\nGET /x", 0, false},
		{"header capture no name", "# @capture a = header\nGET /x", 0, false},
		{"header capture bad name", "# @capture a = header Bad Name\nGET /x", 0, false},
		{"cookie capture no name", "# @capture a = cookie \nGET /x", 0, false},
		{"bad status", "# @expect status 20\nGET /x", 0, false},
		{"bad kind", "# @expect body x\nGET /x", 0, false},
		{"empty jq", "# @expect jq\nGET /x", 0, false},
		{"bad json", "POST /x\nContent-Type: application/json\n\n{nope", 3, true},
		{"template json ok", "POST /x\nContent-Type: application/json\n\n{\"a\": {{n}}}", -1, false},
		{"disabled json header ok", "POST /x\n# Content-Type: application/json\n\n{nope", -1, false},
	}
	for _, c := range cases {
		ps := Problems(c.text)
		if c.line < 0 {
			if len(ps) != 0 {
				t.Errorf("%s: %+v", c.name, ps)
			}
			continue
		}
		if len(ps) != 1 || ps[0].Line != c.line || ps[0].Warning != c.warning || ps[0].EndCol != -1 {
			t.Errorf("%s: %+v", c.name, ps)
		}
	}
}

func TestCheck(t *testing.T) {
	resp := &core.Response{StatusCode: 200, Headers: http.Header{}, Body: []byte(`{"ok":true,"no":false,"n":null}`)}
	cases := []struct {
		e    Expect
		fail bool
	}{
		{Expect{Kind: "status", Arg: "200"}, false},
		{Expect{Kind: "status", Arg: "404"}, true},
		{Expect{Kind: "jq", Arg: ".ok"}, false},
		{Expect{Kind: "jq", Arg: ".no"}, true},
		{Expect{Kind: "jq", Arg: ".n"}, true},
		{Expect{Kind: "jq", Arg: ".missing"}, true},
		{Expect{Kind: "jq", Arg: "empty"}, true},
		{Expect{Kind: "jq", Arg: "((("}, true},
	}
	for _, c := range cases {
		if got := Check([]Expect{c.e}, resp); (len(got) > 0) != c.fail {
			t.Errorf("%+v: %v", c.e, got)
		}
	}
}

func TestRequestLineVersion(t *testing.T) {
	for in, want := range map[string]string{
		"GET https://x/api HTTP/1.1": "https://x/api",
		"GET https://x/api http/2":   "https://x/api",
		"GET https://x/api HTTP/3":   "https://x/api",
		"GET https://x/api HTTP/1.0": "https://x/api",
		"GET https://x/a b":          "https://x/a b",
	} {
		rs := Parse(in + "\n")
		if len(rs) != 1 || rs[0].URL != want {
			t.Errorf("%q: %+v", in, rs)
		}
	}
}

func TestFromCoreWarnings(t *testing.T) {
	r := FromCore(core.Request{Method: "POST", URL: "/u",
		DisabledParams: []core.SavedHeader{{Key: "q", Value: "2"}}})
	if len(r.Warnings) != 1 || !strings.Contains(Format(r), "## disabled params dropped from URL: q=2") {
		t.Fatalf("%q", r.Warnings)
	}
	if len(FromCore(core.Request{URL: "/"}).Warnings) != 0 {
		t.Error("spurious warning")
	}
}

const formText = `### upload
POST {{base}}/upload
Content-Type: multipart/form-data

name: {{user}}
avatar: @./images/me.png
# note: off
## a comment
// another
`

func TestMultipart(t *testing.T) {
	reqs := Parse(formText)
	if len(reqs) != 1 {
		t.Fatalf("%+v", reqs)
	}
	r := reqs[0]
	if r.Body != "" || len(r.Form) != 3 || r.Form[0].Key != "name" || r.Form[0].Value != "{{user}}" ||
		r.Form[1].Value != "@./images/me.png" || r.Form[2].Enabled || r.Form[2].Key != "note" {
		t.Fatalf("%+v", r)
	}
	if !reflect.DeepEqual(r.FormLines, []int{4, 5, 6}) {
		t.Errorf("lines %v", r.FormLines)
	}
	c := r.Core()
	if c.BodyMode != core.BodyForm || len(c.Form) != 3 || c.Body != "" {
		t.Errorf("core %+v", c)
	}
	if probs := Problems(formText); len(probs) != 0 {
		t.Errorf("%+v", probs)
	}
	// Round trip.
	back := Parse("### upload\n" + Format(r))
	if len(back) != 1 || !reflect.DeepEqual(back[0].Form, r.Form) || back[0].URL != r.URL {
		t.Errorf("round trip %+v\n%s", back, Format(r))
	}
	// A disabled multipart header is a plain body.
	plain := Parse("POST /x\n# Content-Type: multipart/form-data\n\na: 1\n")
	if len(plain[0].Form) != 0 || plain[0].Body != "a: 1" || plain[0].Core().BodyMode != "" {
		t.Errorf("%+v", plain[0])
	}
}

func TestMultipartProblems(t *testing.T) {
	probs := Problems("POST /x\nContent-Type: multipart/form-data\n\nnocolon\nf: @\n# f2: @\n# just a note\n")
	if len(probs) != 2 || probs[0].Line != 3 || probs[1].Line != 4 || probs[0].Warning || probs[1].Warning {
		t.Errorf("%+v", probs)
	}
}

func TestFromCoreForm(t *testing.T) {
	r := FromCore(core.Request{Method: "POST", URL: "/u", BodyMode: core.BodyForm,
		Headers: []core.SavedHeader{{Key: "X-A", Value: "1", Enabled: true}},
		Form:    []core.SavedHeader{{Key: "a", Value: "1", Enabled: true}, {Key: "f", Value: "@x.png"}}})
	if len(r.Warnings) != 0 {
		t.Errorf("%q", r.Warnings)
	}
	out := Format(r)
	if !strings.Contains(out, "Content-Type: multipart/form-data\n\na: 1\n# f: @x.png\n") {
		t.Errorf("%q", out)
	}
	back := Parse(out)[0]
	if back.Core().BodyMode != core.BodyForm || len(back.Form) != 2 || back.Form[1].Enabled {
		t.Errorf("%+v", back)
	}
	// An existing Content-Type is made multipart.
	r = FromCore(core.Request{BodyMode: core.BodyForm,
		Headers: []core.SavedHeader{{Key: "content-type", Value: "text/plain", Enabled: true}}})
	if len(r.Headers) != 1 || r.Headers[0].Value != "multipart/form-data" {
		t.Errorf("%+v", r.Headers)
	}
}
