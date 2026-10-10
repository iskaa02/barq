// Package httpfile reads and writes requests in the .http file format:
// blocks separated by "###" lines, each with optional "# @directive" lines,
// a request line, headers, a blank line and a body.
package httpfile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/iskaa02/barq/internal/core"
)

// Expect is an assertion on the response: "status 200" or "jq <filter>".
type Expect struct {
	Kind string // "status" or "jq"
	Arg  string // "200" or the jq filter
	Line int    // 0-based line in the file
}

type Request struct {
	Name     string // "# @name x", else text after ###, else ""
	Method   string // upper case, "GET" when the line has only a URL
	URL      string
	Headers  []core.HeaderRow // incl. disabled ones (Enabled=false)
	Body     string
	BodyFile string // from a "< ./path" body; Body is "" then
	// Form holds the fields of a multipart/form-data request ("name: value",
	// "@path" values are files); Body is "" then.
	Form      []core.HeaderRow
	FormLines []int // 0-based file line of each Form row (parsed requests only)
	Captures  []core.Capture
	Expects   []Expect
	Confirm   bool
	// Warnings are data FromCore could not express in .http; Format writes
	// them as "##" comment lines so nothing is silently lost.
	Warnings []string

	Start, End int // 0-based line range of the block in the file, End exclusive
	Line       int // 0-based line of the request line
	BodyLine   int // 0-based first body line, -1 if no body
}

// Problem is something wrong with the file's format.
type Problem struct {
	Line, Col, EndCol int // 0-based; EndCol exclusive, -1 = end of line
	Message           string
	Warning           bool // false = error
}

// Parse splits text into requests (blocks without a request line are skipped).
func Parse(text string) []Request {
	reqs, _ := parse(text)
	return reqs
}

// Problems lists format errors and warnings.
func Problems(text string) []Problem {
	_, probs := parse(text)
	return probs
}

func parse(text string) ([]Request, []Problem) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var reqs []Request
	var probs []Problem
	start := 0
	for i := 1; i <= len(lines); i++ {
		if i < len(lines) && !strings.HasPrefix(lines[i], "###") {
			continue
		}
		if r, ok := parseBlock(lines, start, i, &probs); ok {
			reqs = append(reqs, r)
		}
		start = i
	}
	return reqs, probs
}

func isComment(s string) bool {
	return strings.HasPrefix(s, "#") || strings.HasPrefix(s, "//")
}

func problem(probs *[]Problem, line int, msg string, warning bool) {
	*probs = append(*probs, Problem{Line: line, EndCol: -1, Message: msg, Warning: warning})
}

// parseBlock reads lines[start:end].
func parseBlock(lines []string, start, end int, probs *[]Problem) (Request, bool) {
	r := Request{Start: start, End: end, BodyLine: -1}
	i := start
	if strings.HasPrefix(lines[i], "###") {
		r.Name = strings.TrimSpace(strings.TrimPrefix(lines[i], "###"))
		i++
	}
	for ; i < end; i++ {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		if !isComment(l) {
			break
		}
		if rest, ok := strings.CutPrefix(l, "# @"); ok {
			r.directive(i, rest, probs)
		}
	}
	if i >= end {
		return r, false
	}
	r.Line = i
	r.requestLine(strings.TrimSpace(lines[i]))
	for i++; i < end; i++ {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			break
		}
		if h, ok := headerRow(l); ok {
			r.Headers = append(r.Headers, h)
		} else if !isComment(l) {
			problem(probs, i, `header needs "Key: Value"`, false)
		}
	}
	r.body(lines, i+1, end)
	if multipartHeader(r.Headers) {
		r.formFields(lines, probs)
	}
	if r.Body != "" && !strings.Contains(r.Body, "{{") && jsonHeader(r.Headers) && !json.Valid([]byte(r.Body)) {
		problem(probs, r.BodyLine, "body is not valid JSON", true)
	}
	return r, true
}

// directive applies "# @word args".
func (r *Request) directive(line int, s string, probs *[]Problem) {
	word, args, _ := strings.Cut(s, " ")
	args = strings.TrimSpace(args)
	switch word {
	case "name":
		r.Name = args
	case "confirm":
		r.Confirm = true
	case "capture":
		c, err := core.ParseCapture(args)
		if err != nil {
			problem(probs, line, "@capture: "+err.Error(), false)
			break
		}
		r.Captures = append(r.Captures, c)
	case "expect":
		kind, arg, _ := strings.Cut(args, " ")
		arg = strings.TrimSpace(arg)
		switch {
		case kind != "status" && kind != "jq":
			problem(probs, line, `@expect: kind must be "status" or "jq"`, false)
		case kind == "status" && !isStatus(arg):
			problem(probs, line, "@expect status: need a 3-digit code", false)
		case kind == "jq" && arg == "":
			problem(probs, line, "@expect jq: need a filter", false)
		default:
			r.Expects = append(r.Expects, Expect{Kind: kind, Arg: arg, Line: line})
		}
	default:
		problem(probs, line, "unknown directive @"+word, false)
	}
}

func isStatus(s string) bool {
	_, err := strconv.ParseUint(s, 10, 16)
	return err == nil && len(s) == 3
}

func (r *Request) requestLine(l string) {
	method, url, ok := strings.Cut(l, " ")
	if !ok || !isMethod(method) {
		r.Method, r.URL = "GET", l
		return
	}
	r.Method, r.URL = strings.ToUpper(method), stripVersion(strings.TrimSpace(url))
}

// stripVersion drops a trailing HTTP version token from a request line URL.
func stripVersion(u string) string {
	i := strings.LastIndexAny(u, " \t")
	if i < 0 {
		return u
	}
	switch strings.ToUpper(u[i+1:]) {
	case "HTTP/1.0", "HTTP/1.1", "HTTP/2", "HTTP/2.0", "HTTP/3":
		return strings.TrimSpace(u[:i])
	}
	return u
}

func isMethod(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return !strings.Contains(s, "://")
}

// headerRow reads "Key: Value" or the disabled form "# Key: Value".
func headerRow(l string) (core.HeaderRow, bool) {
	enabled := true
	if strings.HasPrefix(l, "##") || strings.HasPrefix(l, "//") {
		return core.HeaderRow{}, false
	}
	if rest, ok := strings.CutPrefix(l, "#"); ok {
		l, enabled = strings.TrimSpace(rest), false
	}
	k, v, ok := strings.Cut(l, ":")
	k = strings.TrimSpace(k)
	if !ok || k == "" || (!enabled && strings.ContainsAny(k, " \t@")) {
		return core.HeaderRow{}, false
	}
	return core.HeaderRow{Key: k, Value: strings.TrimSpace(v), Enabled: enabled}, true
}

func jsonHeader(hs []core.HeaderRow) bool {
	for _, h := range hs {
		if h.Enabled && strings.EqualFold(h.Key, "Content-Type") && strings.Contains(strings.ToLower(h.Value), "json") {
			return true
		}
	}
	return false
}

// multipartHeader reports whether an enabled Content-Type asks for multipart/form-data.
func multipartHeader(hs []core.HeaderRow) bool {
	for _, h := range hs {
		if h.Enabled && strings.EqualFold(h.Key, "Content-Type") && strings.Contains(strings.ToLower(h.Value), "multipart/form-data") {
			return true
		}
	}
	return false
}

// formFields turns the body text into form rows. "# name: value" is a
// disabled field; other "#" and "//" lines are comments.
func (r *Request) formFields(lines []string, probs *[]Problem) {
	if r.BodyLine < 0 {
		return
	}
	n := strings.Count(r.Body, "\n") + 1
	if r.BodyFile != "" {
		n = 1
	}
	for i := r.BodyLine; i < r.BodyLine+n; i++ {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		if h, ok := headerRow(l); ok {
			if p, isFile := strings.CutPrefix(h.Value, "@"); isFile && h.Enabled && strings.TrimSpace(p) == "" {
				problem(probs, i, `form field "`+h.Key+`": add a file path after @`, false)
			}
			r.Form = append(r.Form, h)
			r.FormLines = append(r.FormLines, i)
		} else if !isComment(l) {
			problem(probs, i, `form field needs "name: value"`, false)
		}
	}
	r.Body, r.BodyFile = "", ""
}

// body reads lines[from:end] as the body, trimming blank edges.
func (r *Request) body(lines []string, from, end int) {
	for from < end && strings.TrimSpace(lines[from]) == "" {
		from++
	}
	for end > from && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	if from >= end {
		return
	}
	r.BodyLine = from
	if end-from == 1 {
		if p, ok := strings.CutPrefix(strings.TrimSpace(lines[from]), "< "); ok {
			r.BodyFile = strings.TrimSpace(p)
			return
		}
	}
	r.Body = strings.Join(lines[from:end], "\n")
}

// At returns the request whose block contains the 0-based line.
func At(reqs []Request, line int) (Request, bool) {
	for _, r := range reqs {
		if line >= r.Start && line < r.End {
			return r, true
		}
	}
	return Request{}, false
}

// Core converts to a core.Request. BodyFile contents are not read.
func (r Request) Core() core.Request {
	c := core.Request{
		Name: r.Name, Method: r.Method, URL: r.URL, Body: r.Body,
		Headers:  core.ToSavedHeaders(r.Headers),
		Captures: r.Captures,
	}
	if len(r.Form) > 0 || multipartHeader(r.Headers) {
		c.BodyMode, c.Form, c.Body = core.BodyForm, core.ToSavedHeaders(r.Form), ""
	}
	return c
}

// ResolveBody returns the body, reading BodyFile relative to root (the
// project directory) if set.
func (r Request) ResolveBody(root string) (string, error) {
	if r.BodyFile == "" {
		return r.Body, nil
	}
	p := r.BodyFile
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	b, err := os.ReadFile(p)
	return string(b), err
}

// FromCore builds a Request from a saved one.
func FromCore(c core.Request) Request {
	m := c.Method
	if m == "" {
		m = "GET"
	}
	r := Request{
		Name: c.Name, Method: m, URL: c.URL, Body: c.Body,
		Headers:  core.FromSavedHeaders(c.Headers),
		Captures: c.Captures, BodyLine: -1,
	}
	if c.BodyMode == core.BodyForm || len(c.Form) > 0 {
		r.Form = core.FromSavedHeaders(c.Form)
		r.Body = ""
		r.setMultipartHeader()
	}
	if len(c.DisabledParams) > 0 {
		var ps []string
		for _, f := range c.DisabledParams {
			ps = append(ps, f.Key+"="+f.Value)
		}
		r.Warnings = append(r.Warnings, "disabled params dropped from URL: "+strings.Join(ps, ", "))
	}
	return r
}

// setMultipartHeader makes sure an enabled Content-Type says multipart/form-data.
func (r *Request) setMultipartHeader() {
	for i, h := range r.Headers {
		if h.Enabled && strings.EqualFold(h.Key, "Content-Type") {
			r.Headers[i].Value = "multipart/form-data"
			return
		}
	}
	r.Headers = append(r.Headers, core.HeaderRow{Key: "Content-Type", Value: "multipart/form-data", Enabled: true})
}

// Format renders one request as a block, without the ### line.
func Format(r Request) string {
	var b strings.Builder
	if r.Name != "" {
		fmt.Fprintf(&b, "# @name %s\n", r.Name)
	}
	for _, c := range r.Captures {
		fmt.Fprintf(&b, "# @capture %s\n", c)
	}
	for _, e := range r.Expects {
		fmt.Fprintf(&b, "# @expect %s %s\n", e.Kind, e.Arg)
	}
	if r.Confirm {
		b.WriteString("# @confirm\n")
	}
	m := r.Method
	if m == "" {
		m = "GET"
	}
	for _, w := range r.Warnings {
		for _, l := range strings.Split(w, "\n") {
			fmt.Fprintf(&b, "## %s\n", l)
		}
	}
	fmt.Fprintf(&b, "%s %s\n", m, r.URL)
	for _, h := range r.Headers {
		if !h.Enabled {
			b.WriteString("# ")
		}
		fmt.Fprintf(&b, "%s: %s\n", h.Key, h.Value)
	}
	switch {
	case len(r.Form) > 0:
		b.WriteString("\n")
		for _, f := range r.Form {
			if !f.Enabled {
				b.WriteString("# ")
			}
			fmt.Fprintf(&b, "%s: %s\n", f.Key, f.Value)
		}
	case r.BodyFile != "":
		fmt.Fprintf(&b, "\n< %s\n", r.BodyFile)
	case r.Body != "":
		fmt.Fprintf(&b, "\n%s\n", r.Body)
	}
	return b.String()
}

// Check evaluates expectations against a response and returns one message
// per failed one.
func Check(expects []Expect, resp *core.Response) []string {
	var fails []string
	for _, e := range expects {
		switch e.Kind {
		case "status":
			if want, _ := strconv.Atoi(e.Arg); resp.StatusCode != want {
				fails = append(fails, fmt.Sprintf("expected status %s, got %d", e.Arg, resp.StatusCode))
			}
		case "jq":
			if msg := checkJQ(e.Arg, resp); msg != "" {
				fails = append(fails, msg)
			}
		}
	}
	return fails
}

func checkJQ(filter string, resp *core.Response) string {
	body, err := resp.FullBody(core.JQLimit)
	if err != nil {
		return fmt.Sprintf("jq %s: %v", filter, err)
	}
	vals, err := core.RunJQ(filter, body)
	switch {
	case err != nil:
		return fmt.Sprintf("jq %s: %v", filter, err)
	case len(vals) == 0:
		return fmt.Sprintf("jq %s: returned nothing", filter)
	case vals[0] == nil || vals[0] == false:
		return fmt.Sprintf("jq %s: got %s", filter, core.MarshalJQ(vals[0], false))
	}
	return ""
}
