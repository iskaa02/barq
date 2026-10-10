package ntui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/iskaa02/barq/internal/core"
)

// view is one of the response pane's five views.
type view int

const (
	viewBody view = iota
	viewHeaders
	viewRequest
	viewInfo
	viewHistory
	numViews
)

var viewNames = [numViews]string{"Body", "Headers", "Request", "Info", "History"}

// viewBufs are the scratch buffer names in the response nvim, per view.
var viewBufs = [numViews]string{"barq://body", "barq://headers", "barq://request", "barq://info", "barq://history"}

// expectResult is one @expect and whether it held.
type expectResult struct {
	OK   bool
	Text string // "status 200", or the failure message
}

// maskSecrets replaces every secret value found in s with "••••".
func maskSecrets(s string, secrets []string) string {
	for _, v := range secrets {
		if v != "" {
			s = strings.ReplaceAll(s, v, masked)
		}
	}
	return s
}

// headerLines is the status line and the sorted headers, one per value.
func headerLines(res *core.Response) []string {
	out := []string{fmt.Sprintf("%s %s", res.Proto, res.Status)}
	keys := make([]string, 0, len(res.Headers))
	for k := range res.Headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range res.Headers[k] {
			out = append(out, k+": "+v)
		}
	}
	return out
}

// requestLines is the request as sent (variables resolved): "METHOD URL",
// the enabled headers, a blank line and the body. Secret values are masked.
func requestLines(r core.Request, cwd string, secrets []string) []string {
	m := r.Method
	if m == "" {
		m = "GET"
	}
	out := []string{m + " " + r.URL}
	for _, h := range r.Headers {
		if h.Enabled && strings.TrimSpace(h.Key) != "" {
			out = append(out, h.Key+": "+h.Value)
		}
	}
	if r.BodyMode == core.BodyForm {
		out = append(out, "")
		out = append(out, formLines(r.Form, cwd)...)
	} else if r.Body != "" {
		out = append(out, "", strings.ReplaceAll(r.Body, "\r\n", "\n"))
	}
	return strings.Split(maskSecrets(strings.Join(out, "\n"), secrets), "\n")
}

// formLines lists the enabled form fields; a file shows as "name: @path (12.3 KB)".
func formLines(form []core.SavedHeader, cwd string) []string {
	var out []string
	for _, f := range form {
		name := strings.TrimSpace(f.Key)
		if !f.Enabled || name == "" {
			continue
		}
		l := name + ": " + f.Value
		if p, ok := strings.CutPrefix(f.Value, "@"); ok {
			p = strings.TrimSpace(p)
			if !filepath.IsAbs(p) {
				p = filepath.Join(cwd, p)
			}
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				l += " (" + core.HumanSize(st.Size()) + ")"
			} else {
				l += " (missing)"
			}
		}
		out = append(out, l)
	}
	return out
}

// infoLines summarises a response: status, timing, expectations, captures.
func infoLines(r *Response) []string {
	row := func(k, v string) string { return fmt.Sprintf("%-9s %s", k, v) }
	var out []string
	if r.Err != nil {
		out = append(out, row("Status", "failed"))
	} else {
		out = append(out, row("Status", r.Res.Status),
			row("Time", r.Res.Duration.Round(time.Millisecond).String()),
			row("Size", core.HumanSize(r.Res.Size)))
	}
	out = append(out, row("Sent", r.At.Format("15:04:05")), row("Env", r.Env))
	if len(r.ReqLines) > 0 {
		_, u, _ := strings.Cut(r.ReqLines[0], " ")
		out = append(out, row("URL", u))
	}
	if len(r.Expects) > 0 {
		out = append(out, "", "Expectations")
		for _, e := range r.Expects {
			mark := "✓ "
			if !e.OK {
				mark = "✗ "
			}
			out = append(out, mark+e.Text)
		}
	}
	if len(r.Caps) > 0 {
		out = append(out, "", "Captured")
		for _, c := range r.Caps {
			switch {
			case c.Error != "":
				out = append(out, c.Var+" ✗ "+c.Error)
			case c.Secret:
				out = append(out, c.Var+" = "+masked)
			default:
				v := c.Value
				if len(v) > 60 {
					v = v[:60] + "…"
				}
				out = append(out, c.Var+" = "+v)
			}
		}
	}
	if r.Err != nil {
		out = append(out, "", "Error", r.Err.Error())
	}
	return out
}

// viewContent is the lines and filetype of view v of r.
func viewContent(r *Response, v view) ([]string, string) {
	switch v {
	case viewHeaders:
		if r.Res == nil {
			return []string{"no response"}, "text"
		}
		return headerLines(r.Res), "yaml"
	case viewRequest:
		return r.ReqLines, "http"
	case viewInfo:
		return infoLines(r), "text"
	}
	return r.Lines, r.FT
}

// viewTabs renders the view names on one line of w cells, the active one
// highlighted; names shrink to their initials when w is small.
func viewTabs(active view, w int) string {
	names := viewNames
	if w < 36 {
		names = [numViews]string{"B", "H", "R", "I", "P"}
	}
	parts := make([]string, numViews)
	for i, n := range names {
		if view(i) == active {
			parts[i] = activeBorder.Bold(true).Render(n)
		} else {
			parts[i] = muted.Render(n)
		}
	}
	return fit(" "+strings.Join(parts, "  "), w)
}

// histLines lists runs (newest first), one per line: time, status, duration,
// size and env. A "*" marks runs whose request text differs from the newest
// run's, so a changed request is easy to spot.
func histLines(runs []core.HistMeta) []string {
	if len(runs) == 0 {
		return []string{"no runs yet"}
	}
	out := make([]string, len(runs))
	for i, m := range runs {
		st := m.Status
		if m.Error != "" {
			st = "error"
		}
		mark := " "
		if m.ReqHash != runs[0].ReqHash {
			mark = "*"
		}
		out[i] = fmt.Sprintf("%s%s  %-14s %8s %9s  %s", mark, m.Time.Format("Jan _2 15:04:05"), st,
			m.Duration.Round(time.Millisecond), core.HumanSize(m.Size), m.Env)
	}
	return out
}
