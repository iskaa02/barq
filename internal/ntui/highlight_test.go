package ntui

import (
	"strings"
	"testing"
)

// tokens renders spans as "line:text:Group" for easy comparison.
func tokens(lines []string) []string {
	var out []string
	for _, s := range highlightSpans(lines) {
		out = append(out, strings.Join([]string{itoa(s.Line), lines[s.Line][s.Col:s.EndCol], s.Group}, ":"))
	}
	return out
}

func itoa(n int) string { return string(rune('0' + n)) }

func has(t *testing.T, lines []string, want ...string) {
	t.Helper()
	got := tokens(lines)
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %q in %q", w, got)
		}
	}
}

func lacks(t *testing.T, lines []string, bad string) {
	t.Helper()
	for _, g := range tokens(lines) {
		if strings.HasSuffix(g, bad) {
			t.Errorf("unexpected %q", g)
		}
	}
}

func TestHighlightRequestAndQuery(t *testing.T) {
	has(t, []string{"GET https://x.io/a?id=1&flag&q=z"},
		"0:GET:BarqMethod", "0:https://x.io/a?id=1&flag&q=z:BarqURL",
		"0:?:BarqPunct", "0:id:BarqQueryKey", "0:=:BarqPunct", "0:&:BarqPunct",
		"0:flag:BarqQueryKey", "0:q:BarqQueryKey")
	has(t, []string{"https://x.io"}, "0:https://x.io:BarqURL")
	lacks(t, []string{"https://x.io"}, "BarqMethod")
}

func TestHighlightHeaders(t *testing.T) {
	l := []string{"GET /", "Accept:  a/b", "# X-Off: 1", "// note", "", "x: y"}
	has(t, l, "1:Accept:BarqHeaderName", "1:::BarqPunct", "1:a/b:BarqHeaderValue",
		"2:# X-Off: 1:BarqDisabled", "3:// note:BarqComment")
	lacks(t, []string{"GET /", "", "x: y"}, "BarqHeaderName")
}

func TestHighlightDirectivesAndSeparator(t *testing.T) {
	l := []string{"### Login", "# @capture tok = .data.token", "# @expect status 200", "# plain", "POST /"}
	has(t, l, "0:###:BarqSeparator", "0:Login:BarqName",
		"1:# @capture:BarqDirective", "1:tok = .data.token:BarqDirectiveArg", "1:.data.token:BarqJq",
		"2:# @expect:BarqDirective", "2:status 200:BarqDirectiveArg",
		"3:# plain:BarqComment")
}

func TestHighlightVars(t *testing.T) {
	l := []string{"GET {{base}}/a", "X: {{tok}}", "", `{"a": {{n}}}`}
	has(t, l, "0:{{base}}:BarqVar", "1:{{tok}}:BarqVar", "3:{{n}}:BarqVar")
	has(t, []string{"# @capture a = .x", "GET /"}, "0:a = .x:BarqDirectiveArg")
}

func TestHighlightJSONBody(t *testing.T) {
	l := []string{"POST /", "", "{", `  "a": [1, -2.5e3, true],`, `  "b": null, "c": "s\"q", "d": false`, "}"}
	has(t, l, "2:{:BarqPunct", `3:"a":BarqJsonKey`, "3:1:BarqJsonNumber", "3:-2.5e3:BarqJsonNumber",
		"3:true:BarqJsonBool", `4:"b":BarqJsonKey`, "4:null:BarqJsonNull",
		`4:"s\"q":BarqJsonString`, "4:false:BarqJsonBool", "5:}:BarqPunct")
}

func TestHighlightPartialJSON(t *testing.T) {
	l := []string{"POST /", "", `{"a": "unterminated`, `  "b": tru`}
	has(t, l, `2:"a":BarqJsonKey`, `2:"unterminated:BarqJsonString`)
	lacks(t, l[:3], "BarqJsonBool")
}

func TestHighlightBodyFileAndPlain(t *testing.T) {
	has(t, []string{"POST /", "", "< ./f.json"}, "2:<:BarqDirective", "2:./f.json:BarqURL")
	l := []string{"POST /", "", "hello \"x\" 12"}
	for _, g := range tokens(l) {
		if strings.HasPrefix(g, "2:") {
			t.Errorf("plain body highlighted: %s", g)
		}
	}
}

func TestHighlightCaptureSources(t *testing.T) {
	has(t, []string{"# @capture r = header X-Request-Id"},
		"0:header:BarqDirective", "0:X-Request-Id:BarqHeaderName")
	has(t, []string{"# @capture s = cookie sid"}, "0:cookie:BarqDirective", "0:sid:BarqHeaderName")
	has(t, []string{"# @capture t = .data.token"}, "0:.data.token:BarqJq")
}

func TestHighlightMultipart(t *testing.T) {
	lines := []string{"POST /up", "Content-Type: multipart/form-data", "", "name: {{u}}", "avatar: @./a.png", "# off: 1", "## c"}
	has(t, lines, "3:name:BarqHeaderName", "3:::BarqPunct", "4:avatar:BarqHeaderName", "4:@./a.png:BarqURL",
		"5:# off: 1:BarqDisabled", "6:## c:BarqComment")
	// Without the multipart header the body is not form fields.
	has(t, []string{"POST /up", "", "avatar: @./a.png"})
	lacks(t, []string{"POST /up", "", "avatar: @./a.png"}, "BarqHeaderName")
}
