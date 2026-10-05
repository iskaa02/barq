package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/iskaa02/barq/internal/core"
)

type runJSON struct {
	RunID      string             `json:"run_id,omitempty"`
	Request    string             `json:"request"`
	Env        string             `json:"env,omitempty"`
	Status     string             `json:"status"`
	Code       int                `json:"code"`
	DurationMS int64              `json:"duration_ms"`
	Size       int64              `json:"size"` // of the whole body
	Headers    map[string]string  `json:"headers,omitempty"`
	Body       any                `json:"body"` // parsed when JSON, else a string
	Captured   []core.CapturedVar `json:"captured,omitempty"`
	Redacted   bool               `json:"redacted"`
	// Partial is set when body holds only the start of the whole body,
	// which `barq history body` reads (body_file is a scrubbed copy).
	Partial    bool   `json:"partial,omitempty"`
	BodyFile   string `json:"body_file,omitempty"`
	CutAtCap   bool   `json:"cut_at_cap,omitempty"`  // reading stopped at --max-body
	BodyPruned bool   `json:"body_pruned,omitempty"` // history no longer has the body
	Output     string `json:"output,omitempty"`      // where -o wrote the body

	duration time.Duration // for human output, finer than DurationMS
}

// cmdRun sends a saved request (or an ad-hoc one with --curl), records it
// in history, applies captures and prints the response, redacted.
func cmdRun(c *cli, args []string) error {
	fs := c.flags("run")
	envName := fs.String("env", "", "environment (default: the active one)")
	var vars, caps multi
	fs.Var(&vars, "var", "override a variable for this run: key=value (repeatable)")
	fs.Var(&caps, "capture", "also store part of the response: var=jq-filter (repeatable)")
	curl := fs.String("curl", "", "send a curl command instead of a saved request")
	filter := fs.String("jq", "", "show only this jq filter of the body")
	include := fs.Bool("i", false, "include response headers")
	fail := fs.Bool("fail", false, "exit with status 3 on HTTP 400 and above")
	timeout := fs.Duration("timeout", 30*time.Second, "give up after this long")
	output := fs.String("o", "", "write the whole body to this file (redacted unless --reveal) instead of printing it")
	maxBody := fs.String("max-body", "", "stop reading the body after this much, e.g. 50MB or 2GB; 0 for no limit (default 1GB)")
	fs.BoolVar(&c.reveal, "reveal", false, "show secret values (needs a person at a terminal)")
	pos, err := c.parse(fs, args, 0, 1, "<request> [--env E] [--var k=v]… [--capture var=filter]… [--jq F] [-i] [-o FILE] [--max-body SIZE] [--fail]")
	if err != nil {
		return err
	}
	if *maxBody != "" {
		if core.MaxBody, err = core.ParseSize(*maxBody); err != nil {
			return usagef("--max-body: %v", err)
		}
	}
	if *output != "" && *filter != "" {
		return usagef("-o writes the whole body; use --jq without it")
	}
	if (len(pos) == 1) == (*curl != "") {
		return usagef("give either a saved request or --curl '…'")
	}
	if err := c.open(); err != nil {
		return err
	}
	if err := c.checkReveal(); err != nil {
		return err
	}

	// What to send.
	var typed core.Request
	key, name := "cli", "curl"
	if *curl != "" {
		cr, _, err := core.ParseCurl(*curl)
		if err != nil {
			return err
		}
		typed = core.Request{Method: cr.Method, URL: cr.URL, Headers: core.ToSavedHeaders(cr.Headers), Body: cr.Body}
		if len(cr.Form) > 0 {
			typed.BodyMode, typed.Form = core.BodyForm, core.ToSavedHeaders(cr.Form)
		}
	} else {
		id, err := c.ws.FindRequestRef(pos[0])
		if err != nil {
			return err
		}
		typed = c.ws.Requests[c.ws.Find(id)]
		key, name = id, typed.DisplayName()
	}
	var allCaps []core.Capture
	allCaps = append(allCaps, typed.Captures...)
	for _, spec := range caps {
		cp, err := core.ParseCapture(strings.Replace(spec, "=", " = ", 1))
		if err != nil {
			return usagef("--capture %q: %v", spec, err)
		}
		allCaps = append(allCaps, cp)
	}

	// Where to send it.
	envID, err := c.envID(*envName)
	if err != nil {
		return err
	}
	env, _ := c.ws.Env(envID)
	overrides := map[string]string{}
	for _, v := range vars {
		k, val, err := splitKV(v, "=", "--var")
		if err != nil {
			return err
		}
		overrides[k] = val
	}
	sent, missing := c.ws.Resolve(envID, typed, overrides)
	if len(missing) > 0 {
		return fmt.Errorf("undefined variable(s): {{%s}}; set them with `barq env set` or --var", strings.Join(missing, "}}, {{"))
	}
	if env.NeedsConfirm(sent.Method) {
		if err := c.confirm(fmt.Sprintf("Sending %s in the protected environment %q", name, env.Name), c.sendSummary(sent)...); err != nil {
			return err
		}
	}

	// Send, record, capture.
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	hist, _ := core.OpenHistory(c.ws)
	run := c.ws.NewRun(key, name, envID, typed, sent)
	resp, sendErr := core.RunRequest(ctx, sent, c.ws.CWD)
	defer resp.Close()
	// Capture first, so a token taken from this response is already a
	// secret when the run is scrubbed and recorded.
	var captured []core.CapturedVar
	if sendErr == nil && len(allCaps) > 0 && resp.StatusCode < 400 {
		captured = core.EvalCaptures(allCaps, resp)
		if err := c.mutate(func(w *core.Workspace) error { return w.StoreCaptured(envID, captured) }); err != nil {
			return err
		}
	}
	if err := c.ws.RecordRun(hist, run, resp, sendErr); err != nil {
		fmt.Fprintf(c.err, "barq: warning: couldn't record history: %v\n", err)
	}
	if sendErr != nil {
		if c.reveal {
			return sendErr
		}
		return fmt.Errorf("%s", c.rd.Text(sendErr.Error()))
	}
	// Redact with the secrets known now, including ones just captured.
	c.rd = core.NewRedactor(c.ws)

	var out runJSON
	if *output != "" {
		out = c.runOut(run.Meta.ID, name, env, resp, "", *include, true)
		if err := c.writeBody(resp, *output); err != nil {
			return err
		}
		out.Body, out.Output = nil, *output
	} else {
		out = c.runOut(run.Meta.ID, name, env, resp, *filter, *include, false)
		if out.Partial && hist != nil {
			out.BodyFile = hist.BodyPath(run.Meta.ID)
		}
	}
	out.Captured = captured
	if c.asJSON {
		if err := c.printJSON(out); err != nil {
			return err
		}
	} else {
		c.printRun(out, *include)
	}
	if *fail && resp.StatusCode >= 400 {
		return exitCode(3)
	}
	return nil
}

// sendSummary describes a resolved request for a person to confirm:
// method, the real URL (secrets still hidden) and what goes in the body.
func (c *cli) sendSummary(r core.Request) []string {
	method := strings.ToUpper(r.Method)
	if method == "" {
		method = "GET"
	}
	lines := []string{method + " " + c.rd.URL(core.NormalizeURL(r.URL), false)}
	switch {
	case r.BodyMode == core.BodyForm:
		n := 0
		for _, f := range r.Form {
			if f.Enabled {
				n++
			}
		}
		lines = append(lines, fmt.Sprintf("body: form with %d field(s)", n))
	case r.Body != "":
		lines = append(lines, "body: "+core.HumanSize(len(r.Body)))
	}
	return lines
}

// inlineLimit is how much of a body barq run and barq history show print.
// Longer bodies are read with barq history body or saved with -o.
const inlineLimit = 1 << 20

// writeBody writes the whole body to path: redacted, or as received with
// --reveal.
func (c *cli) writeBody(resp *core.Response, path string) error {
	src, err := resp.OpenBody()
	if err != nil {
		return err
	}
	defer src.Close()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if c.reveal {
		_, err = io.Copy(f, src)
	} else {
		err = c.rd.BodyTo(f, src, resp.Headers.Get("Content-Type"))
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// runOut builds the printable result of a response. Bodies over
// inlineLimit are cut, and marked partial.
func (c *cli) runOut(runID, name string, env *core.Environment, resp *core.Response, filter string, include, noBody bool) runJSON {
	out := runJSON{RunID: runID, Request: name, Status: resp.Status, Code: resp.StatusCode,
		DurationMS: resp.Duration.Milliseconds(), Size: resp.Size, Redacted: !c.reveal, duration: resp.Duration,
		CutAtCap: resp.CutAtCap}
	if env != nil {
		out.Env = env.Name
	}
	if include {
		h := resp.Headers
		if !c.reveal {
			h = c.rd.Headers(h)
		}
		out.Headers = map[string]string{}
		for _, k := range sortedKeys(h) {
			out.Headers[k] = strings.Join(h[k], ", ")
		}
	}
	if noBody {
		return out
	}
	ct := resp.Headers.Get("Content-Type")
	var body string
	if filter != "" {
		full, err := resp.FullBody(core.JQLimit)
		var vals []any
		if err == nil {
			vals, err = core.RunJQ(filter, full)
		}
		if err != nil {
			body = "jq: " + err.Error()
		} else {
			parts := make([]string, len(vals))
			for i, v := range vals {
				parts[i] = core.MarshalJQ(v, true)
			}
			body = strings.Join(parts, "\n")
		}
		body = c.redactBody(body, ct)
	} else {
		raw := resp.Body
		if len(raw) > inlineLimit {
			raw = raw[:inlineLimit]
		}
		out.Partial = int64(len(raw)) < resp.Size
		body = c.redactBody(string(raw), ct)
		if !out.Partial {
			if pretty, isJSON := core.PrettyBody(&core.Response{Body: []byte(body), Headers: resp.Headers}); isJSON {
				body = pretty
			}
		}
	}
	out.Body = body
	if t := strings.TrimSpace(body); !out.Partial && t != "" && json.Valid([]byte(t)) {
		out.Body = json.RawMessage(t)
	}
	return out
}

func (c *cli) redactBody(body, contentType string) string {
	if c.reveal {
		return body
	}
	var b strings.Builder
	c.rd.BodyTo(&b, strings.NewReader(body), contentType)
	return b.String()
}

func (c *cli) printRun(out runJSON, include bool) {
	env := ""
	if out.Env != "" {
		env = "  env " + out.Env
	}
	c.printf("%s  %s  %s%s\n", out.Status, roundDuration(out.duration), core.HumanSize(out.Size), env)
	if len(out.Captured) > 0 {
		c.printf("%s\n", core.CapturedSummary(out.Captured))
	}
	if include {
		for _, k := range sortedKeys(out.Headers) {
			c.printf("%s: %s\n", k, out.Headers[k])
		}
	}
	switch b := out.Body.(type) {
	case json.RawMessage:
		c.printf("\n%s\n", strings.TrimSpace(string(b)))
	case string:
		if b != "" {
			c.printf("\n%s\n", strings.TrimRight(b, "\n"))
		}
	}
	switch {
	case out.Output != "":
		c.printf("wrote %s to %s\n", core.HumanSize(out.Size), out.Output)
	case out.BodyPruned:
		c.printf("\n(the body was pruned from history to stay under its size budget)\n")
	case out.Partial:
		c.printf("\n… showing %s of %s. Read the rest with: barq history body %s [--jq F | --grep RE | --lines A:B]\n",
			core.HumanSize(inlineLimit), core.HumanSize(out.Size), out.RunID)
	}
	if out.CutAtCap {
		c.printf("(reading stopped at %s; raise it with --max-body)\n", core.HumanSize(out.Size))
	}
}

// History -------------------------------------------------------------------------

func cmdHistory(c *cli, args []string) error {
	if len(args) > 0 && args[0] == "body" {
		c.command = "history body"
		return cmdHistoryBody(c, args[1:])
	}
	fs := c.flags("history")
	limit := fs.Int("n", 20, "how many runs to list")
	fs.BoolVar(&c.reveal, "reveal", false, "show secret values (needs a person at a terminal)")
	pos, err := c.parse(fs, args, 0, 2, "[request] | show <run-id>")
	if err != nil {
		return err
	}
	if err := c.open(); err != nil {
		return err
	}
	if err := c.checkReveal(); err != nil {
		return err
	}
	hist, err := core.OpenHistory(c.ws)
	if err != nil {
		return err
	}
	if len(pos) == 2 && pos[0] == "show" {
		return c.showRun(hist, pos[1])
	}
	if len(pos) == 2 {
		return usagef("usage: barq history [request] | barq history show <run-id> | barq history body <run-id>")
	}

	runs := hist.All()
	if len(pos) == 1 {
		id, err := c.ws.FindRequestRef(pos[0])
		if err != nil {
			return err
		}
		runs = hist.ForKey(id)
	}
	if len(runs) > *limit {
		runs = runs[:*limit]
	}
	type runRow struct {
		RunID      string    `json:"run_id"`
		Request    string    `json:"request"`
		Time       time.Time `json:"time"`
		Method     string    `json:"method"`
		Code       int       `json:"code"`
		Status     string    `json:"status,omitempty"`
		Error      string    `json:"error,omitempty"`
		DurationMS int64     `json:"duration_ms"`
		Env        string    `json:"env,omitempty"`
		Edited     bool      `json:"edited_since_previous"`
	}
	rows := []runRow{}
	for _, r := range runs {
		name := r.Name
		if i := c.ws.Find(r.Key); i >= 0 {
			name = c.ws.SlashPath(core.Ref{ID: r.Key})
		}
		prev, ok := hist.Previous(r.ID)
		rows = append(rows, runRow{r.ID, name, r.Time, r.Method, r.Code, r.Status, c.rd.Text(r.Error),
			r.Duration.Milliseconds(), r.Env, ok && prev.ReqHash != r.ReqHash})
	}
	if c.asJSON {
		return c.printJSON(rows)
	}
	if len(rows) == 0 {
		c.printf("No runs yet.\n")
	}
	for _, r := range rows {
		outcome := fmt.Sprintf("%d", r.Code)
		if r.Error != "" {
			outcome = "error: " + r.Error
		}
		edited := ""
		if r.Edited {
			edited = "  ✎ edited"
		}
		c.printf("%s  %s  %-6s %-30s %s  %dms  %s%s\n", r.RunID, r.Time.Format("2006-01-02 15:04:05"),
			r.Method, r.Request, outcome, r.DurationMS, r.Env, edited)
	}
	return nil
}

func (c *cli) showRun(hist *core.History, id string) error {
	e, err := hist.Load(id)
	if err != nil {
		return fmt.Errorf("no run %q (see `barq history`)", id)
	}
	typed, sent := e.Request, e.Sent
	if !c.reveal {
		typed, sent = c.rd.Request(typed, true), c.rd.Request(sent, false)
	}
	resp := e.Response()
	out := c.runOut(e.Meta.ID, e.Meta.Name, nil, resp, "", true, false)
	// History stores a scrubbed body; report the size as received.
	out.Env, out.DurationMS, out.duration, out.Size = e.Meta.Env, e.Meta.Duration.Milliseconds(), e.Meta.Duration, e.Meta.Size
	out.BodyPruned = e.Meta.BodyPruned
	if out.Partial {
		out.BodyFile = hist.BodyPath(id)
	}
	if e.BodyTruncated {
		out.Status += " (history kept the first 10 MB of this body)"
	}
	if e.Meta.Error != "" {
		out.Status = "error: " + c.rd.Text(e.Meta.Error)
	}
	if c.asJSON {
		return c.printJSON(map[string]any{
			"run": out, "time": e.Meta.Time,
			"request_as_typed": c.requestOutRaw(typed), "request_as_sent": c.requestOutRaw(sent),
		})
	}
	c.printf("run %s  %s\n\nas typed:\n%s\nas sent:\n%s\n", e.Meta.ID, e.Meta.Time.Format("2006-01-02 15:04:05"),
		indent(core.RequestText(typed), "  "), indent(core.RequestText(sent), "  "))
	c.printRun(out, true)
	return nil
}

// cmdHistoryBody prints a run's whole body, or the part asked for, with
// secret values replaced. Bodies are scrubbed when recorded, so this
// never needs to read more than a stream at a time (except for --jq).
func cmdHistoryBody(c *cli, args []string) error {
	fs := c.flags("history body")
	filter := fs.String("jq", "", "only this jq filter of the body")
	byteRange := fs.String("bytes", "", "only bytes START:END (from 0, END excluded; either may be left out)")
	lineRange := fs.String("lines", "", "only lines START:END (from 1, END included; either may be left out)")
	grep := fs.String("grep", "", "only lines matching this regular expression, with their numbers")
	path := fs.Bool("path", false, "print the path of the file holding the (scrubbed) body")
	pos, err := c.parse(fs, args, 1, 1, "<run-id> [--jq F | --bytes A:B | --lines A:B | --grep RE | --path]")
	if err != nil {
		return err
	}
	picked := 0
	for _, set := range []bool{*filter != "", *byteRange != "", *lineRange != "", *grep != "", *path} {
		if set {
			picked++
		}
	}
	if picked > 1 {
		return usagef("use one of --jq, --bytes, --lines, --grep and --path")
	}
	if err := c.open(); err != nil {
		return err
	}
	hist, err := core.OpenHistory(c.ws)
	if err != nil {
		return err
	}
	id := pos[0]
	e, err := hist.Load(id)
	if err != nil {
		return fmt.Errorf("no run %q (see `barq history`)", id)
	}
	if e.Meta.BodyPruned {
		return fmt.Errorf("the body of run %s was pruned to keep history under its size budget (BARQ_HISTORY_MB)", id)
	}
	resp := e.Response()

	switch {
	case *path:
		p := hist.BodyPath(id)
		if p == "" {
			return fmt.Errorf("run %s has no body file (it's empty, or was recorded before bodies had their own files)", id)
		}
		if c.asJSON {
			return c.printJSON(map[string]any{"path": p, "size": resp.Size})
		}
		c.printf("%s\n", p)
		return nil

	case *filter != "":
		full, err := resp.FullBody(core.JQLimit)
		if err != nil {
			return fmt.Errorf("%v; use --grep, --lines or --bytes instead", err)
		}
		vals, err := core.RunJQ(*filter, full)
		if err != nil {
			return fmt.Errorf("jq: %v", err)
		}
		for _, v := range vals {
			c.printf("%s\n", c.rd.Text(core.MarshalJQ(v, true)))
		}
		return nil
	}

	src, err := resp.OpenBody()
	if err != nil {
		return err
	}
	defer src.Close()
	switch {
	case *byteRange != "":
		start, end, err := parseRange(*byteRange, 0)
		if err != nil {
			return usagef("--bytes: %v", err)
		}
		if _, err := io.CopyN(io.Discard, src, start); err != nil && err != io.EOF {
			return err
		}
		var r io.Reader = src
		if end >= 0 {
			r = io.LimitReader(src, max(end-start, 0))
		}
		return c.rd.TextTo(c.out, r)

	case *lineRange != "":
		start, end, err := parseRange(*lineRange, 1)
		if err != nil {
			return usagef("--lines: %v", err)
		}
		return eachLine(src, func(n int64, line []byte) (bool, error) {
			if end >= 0 && n > end {
				return false, nil
			}
			if n >= start {
				c.printf("%s", c.rd.Text(string(line)))
			}
			return true, nil
		})

	case *grep != "":
		re, err := regexp.Compile(*grep)
		if err != nil {
			return usagef("--grep: %v", err)
		}
		found := false
		err = eachLine(src, func(n int64, line []byte) (bool, error) {
			if loc := re.FindIndex(line); loc != nil {
				found = true
				c.printf("%d: %s\n", n, c.rd.Text(clipAround(line, loc, 200)))
			}
			return true, nil
		})
		if err == nil && !found {
			return exitCode(1) // like grep
		}
		return err
	}
	if err := c.rd.TextTo(c.out, src); err != nil {
		return err
	}
	if resp.Size > 0 {
		c.printf("\n")
	}
	return nil
}

// parseRange reads START:END, either side optional. END is -1 when left
// out; START defaults to first.
func parseRange(s string, first int64) (start, end int64, err error) {
	a, b, ok := strings.Cut(s, ":")
	if !ok {
		return 0, 0, errors.New("expected START:END, e.g. 100:200")
	}
	start, end = first, -1
	if a = strings.TrimSpace(a); a != "" {
		if start, err = strconv.ParseInt(a, 10, 64); err != nil || start < first {
			return 0, 0, fmt.Errorf("bad start %q", a)
		}
	}
	if b = strings.TrimSpace(b); b != "" {
		if end, err = strconv.ParseInt(b, 10, 64); err != nil || end < start {
			return 0, 0, fmt.Errorf("bad end %q", b)
		}
	}
	return start, end, nil
}

// eachLine calls fn with each line (numbered from 1, newline included)
// until it returns false.
func eachLine(r io.Reader, fn func(n int64, line []byte) (bool, error)) error {
	br := bufio.NewReaderSize(r, 64<<10)
	for n := int64(1); ; n++ {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			more, ferr := fn(n, line)
			if ferr != nil || !more {
				return ferr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// clipAround shortens a long line (minified JSON is one line) to the
// match and up to ctx bytes either side.
func clipAround(line []byte, loc []int, ctx int) string {
	line = bytes.TrimRight(line, "\r\n")
	from, to := max(loc[0]-ctx, 0), min(loc[1]+ctx, len(line))
	s := string(line[from:to])
	if from > 0 {
		s = "…" + s
	}
	if to < len(line) {
		s += "…"
	}
	return s
}

// requestOutRaw shows a request that's already redacted (no saved identity).
func (c *cli) requestOutRaw(r core.Request) map[string]any {
	return map[string]any{"method": r.Method, "url": r.URL, "headers": fieldsJSON(r.Headers),
		"body": r.Body, "form": fieldsJSON(r.Form)}
}

// roundDuration keeps durations readable: 142ms, 1.4s, or 0.31ms for fast
// local servers.
func roundDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	}
	return d.Round(10 * time.Millisecond).String()
}
