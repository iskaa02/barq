package ntui

import (
	"strings"

	"github.com/iskaa02/barq/internal/nvimpane"
)

// span is a highlighted range of one line: 0-based line, byte columns,
// EndCol exclusive.
type span struct {
	Line, Col, EndCol int
	Group             string
}

type hlScan struct {
	lines []string
	out   []span
}

func (h *hlScan) add(line, col, end int, group string) {
	if end > col {
		h.out = append(h.out, span{line, col, end, group})
	}
}

// highlightSpans tokenizes an .http buffer using the same block structure as
// httpfile.Parse. {{var}} spans come last so they win over the surrounding token.
func highlightSpans(lines []string) []span {
	h := &hlScan{lines: lines}
	start := 0
	for i := 1; i <= len(lines); i++ {
		if i < len(lines) && !strings.HasPrefix(lines[i], "###") {
			continue
		}
		h.block(start, i)
		start = i
	}
	for i, l := range lines {
		for p := 0; ; {
			s := strings.Index(l[p:], "{{")
			if s < 0 {
				break
			}
			e := strings.Index(l[p+s:], "}}")
			if e < 0 {
				break
			}
			h.add(i, p+s, p+s+e+2, "BarqVar")
			p += s + e + 2
		}
	}
	return h.out
}

func indent(l string) int { return len(l) - len(strings.TrimLeft(l, " \t")) }

func (h *hlScan) block(start, end int) {
	i := start
	if strings.HasPrefix(h.lines[i], "###") {
		l := h.lines[i]
		h.add(i, 0, 3, "BarqSeparator")
		n := 3 + indent(l[3:])
		h.add(i, n, len(strings.TrimRight(l, " \t\r")), "BarqName")
		i++
	}
	for ; i < end; i++ {
		l := h.lines[i]
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		if !strings.HasPrefix(t, "#") && !strings.HasPrefix(t, "//") {
			break
		}
		h.comment(i)
	}
	if i >= end {
		return
	}
	h.requestLine(i)
	multipart := false
	for i++; i < end; i++ {
		l := h.lines[i]
		if strings.TrimSpace(l) == "" {
			break
		}
		h.header(i, false)
		if k, v, ok := strings.Cut(strings.TrimSpace(l), ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Content-Type") {
			multipart = strings.Contains(strings.ToLower(v), "multipart/form-data")
		}
	}
	if multipart {
		for i++; i < end; i++ {
			h.header(i, true)
		}
		return
	}
	h.body(i+1, end)
}

// comment handles a comment line in the preamble.
func (h *hlScan) comment(i int) {
	l := h.lines[i]
	st := indent(l)
	endc := len(strings.TrimRight(l, " \t\r"))
	rest, ok := strings.CutPrefix(l[st:], "# @")
	if !ok {
		h.add(i, st, endc, "BarqComment")
		return
	}
	wlen := len(rest)
	if k := strings.IndexAny(rest, " \t"); k >= 0 {
		wlen = k
	}
	dend := st + 3 + wlen
	h.add(i, st, dend, "BarqDirective")
	word := rest[:wlen]
	as := dend + indent(l[dend:])
	if as >= endc {
		return
	}
	h.add(i, as, endc, "BarqDirectiveArg")
	if word == "capture" {
		if eq := strings.Index(l[as:endc], "="); eq >= 0 {
			js := as + eq + 1
			js += indent(l[js:endc])
			src := l[js:endc]
			kw := ""
			for _, k := range []string{"header", "cookie"} {
				if strings.HasPrefix(src, k+" ") {
					kw = k
				}
			}
			if kw == "" {
				h.add(i, js, endc, "BarqJq")
				return
			}
			h.add(i, js, js+len(kw), "BarqDirective")
			ns := js + len(kw)
			ns += indent(l[ns:endc])
			if ns < endc {
				h.add(i, ns, endc, "BarqHeaderName")
			}
		}
	}
}

func (h *hlScan) requestLine(i int) {
	l := h.lines[i]
	st := indent(l)
	endc := len(strings.TrimRight(l, " \t\r"))
	t := l[st:endc]
	us := st
	if m, _, ok := strings.Cut(t, " "); ok && isAlpha(m) && !strings.Contains(m, "://") {
		h.add(i, st, st+len(m), "BarqMethod")
		us = st + len(m)
		us += indent(l[us:endc])
	}
	h.add(i, us, endc, "BarqURL")
	q := strings.Index(l[us:endc], "?")
	if q < 0 {
		return
	}
	q += us
	h.add(i, q, q+1, "BarqPunct")
	p := q + 1
	for p <= endc {
		e := strings.Index(l[p:endc], "&")
		segEnd := endc
		if e >= 0 {
			segEnd = p + e
		}
		if eq := strings.Index(l[p:segEnd], "="); eq >= 0 {
			h.add(i, p, p+eq, "BarqQueryKey")
			h.add(i, p+eq, p+eq+1, "BarqPunct")
		} else {
			h.add(i, p, segEnd, "BarqQueryKey")
		}
		if e < 0 {
			break
		}
		h.add(i, segEnd, segEnd+1, "BarqPunct")
		p = segEnd + 1
	}
}

func isAlpha(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return true
}

// header highlights a header line, or a form field line when form is set
// (a value starting with @ is a file path).
func (h *hlScan) header(i int, form bool) {
	l := h.lines[i]
	st := indent(l)
	endc := len(strings.TrimRight(l, " \t\r"))
	t := l[st:endc]
	if strings.HasPrefix(t, "//") || strings.HasPrefix(t, "##") {
		h.add(i, st, endc, "BarqComment")
		return
	}
	body, disabled := t, false
	if r, ok := strings.CutPrefix(t, "#"); ok {
		body, disabled = strings.TrimSpace(r), true
	}
	k, _, ok := strings.Cut(body, ":")
	k = strings.TrimSpace(k)
	if !ok || k == "" || (disabled && strings.ContainsAny(k, " \t@")) {
		if disabled {
			h.add(i, st, endc, "BarqComment")
		}
		return
	}
	if disabled {
		h.add(i, st, endc, "BarqDisabled")
		return
	}
	c := st + strings.Index(t, ":")
	h.add(i, st, st+len(strings.TrimRight(t[:c-st], " \t")), "BarqHeaderName")
	h.add(i, c, c+1, "BarqPunct")
	vs := c + 1 + indent(l[c+1:endc])
	if form && strings.HasPrefix(l[vs:endc], "@") {
		h.add(i, vs, endc, "BarqURL")
		return
	}
	h.add(i, vs, endc, "BarqHeaderValue")
}

func (h *hlScan) body(from, end int) {
	for from < end && strings.TrimSpace(h.lines[from]) == "" {
		from++
	}
	for end > from && strings.TrimSpace(h.lines[end-1]) == "" {
		end--
	}
	if from >= end {
		return
	}
	first := strings.TrimSpace(h.lines[from])
	if end-from == 1 {
		if _, ok := strings.CutPrefix(first, "< "); ok {
			l := h.lines[from]
			st := indent(l)
			h.add(from, st, st+1, "BarqDirective")
			ps := st + 1 + indent(l[st+1:])
			h.add(from, ps, len(strings.TrimRight(l, " \t\r")), "BarqURL")
			return
		}
	}
	if first[0] == '{' || first[0] == '[' {
		for i := from; i < end; i++ {
			h.jsonLine(i)
		}
	}
}

func (h *hlScan) jsonLine(i int) {
	l := h.lines[i]
	for p := 0; p < len(l); {
		c := l[p]
		switch {
		case c == '"':
			e := p + 1
			for e < len(l) && l[e] != '"' {
				if l[e] == '\\' {
					e++
				}
				e++
			}
			if e < len(l) {
				e++
			} else {
				e = len(l)
			}
			g := "BarqJsonString"
			if strings.HasPrefix(strings.TrimLeft(l[e:], " \t"), ":") {
				g = "BarqJsonKey"
			}
			h.add(i, p, e, g)
			p = e
		case strings.IndexByte("{}[],:", c) >= 0:
			h.add(i, p, p+1, "BarqPunct")
			p++
		case c == '-' || c >= '0' && c <= '9':
			e := p + 1
			for e < len(l) && strings.IndexByte("0123456789.eE+-", l[e]) >= 0 {
				e++
			}
			h.add(i, p, e, "BarqJsonNumber")
			p = e
		case c >= 'a' && c <= 'z':
			e := p
			for e < len(l) && l[e] >= 'a' && l[e] <= 'z' {
				e++
			}
			switch l[p:e] {
			case "true", "false":
				h.add(i, p, e, "BarqJsonBool")
			case "null":
				h.add(i, p, e, "BarqJsonNull")
			}
			p = e
		default:
			p++
		}
	}
}

var highlightLinks = [][2]string{
	{"BarqSeparator", "@markup.heading"}, {"BarqName", "@markup.heading"},
	{"BarqDirective", "@keyword.directive"}, {"BarqDirectiveArg", "@string"},
	{"BarqJq", "@string.special"}, {"BarqComment", "@comment"},
	{"BarqMethod", "@keyword"}, {"BarqURL", "@string.special.url"},
	{"BarqQueryKey", "@property"}, {"BarqPunct", "@punctuation.delimiter"},
	{"BarqHeaderName", "@property"}, {"BarqHeaderValue", "@string"},
	{"BarqDisabled", "@comment"}, {"BarqVar", "@variable.parameter"},
	{"BarqJsonKey", "@property"}, {"BarqJsonString", "@string"},
	{"BarqJsonNumber", "@number"}, {"BarqJsonBool", "@boolean"},
	{"BarqJsonNull", "@constant.builtin"},
}

const setupHighlightLua = `
for _, p in ipairs(...) do
  vim.api.nvim_set_hl(0, p[1], { link = p[2], default = true })
end
`

// setupHighlights defines the Barq* highlight groups (as overridable links).
func (a *App) setupHighlights(p *nvimpane.Pane) error {
	links := make([][]string, len(highlightLinks))
	for i, l := range highlightLinks {
		links[i] = []string{l[0], l[1]}
	}
	return p.ExecLua(setupHighlightLua, nil, links)
}

// highlightLua args: buffer, list of {line, col, endcol, group}.
const highlightLua = `
local buf, spans = ...
if not vim.api.nvim_buf_is_valid(buf) then return end
local ns = vim.api.nvim_create_namespace("barq_hl")
vim.api.nvim_buf_clear_namespace(buf, ns, 0, -1)
for _, s in ipairs(spans) do
  pcall(vim.api.nvim_buf_set_extmark, buf, ns, s[1], s[2], {
    end_col = s[3], hl_group = s[4],
    priority = s[4] == "BarqVar" and 120 or 110, strict = false,
  })
end
`

// applyHighlights replaces the barq_hl extmarks of buf with the spans of lines.
func (a *App) applyHighlights(p *nvimpane.Pane, buf int, lines []string) error {
	sp := highlightSpans(lines)
	flat := make([][]any, len(sp))
	for i, s := range sp {
		flat[i] = []any{s.Line, s.Col, s.EndCol, s.Group}
	}
	return p.ExecLua(highlightLua, nil, buf, flat)
}
