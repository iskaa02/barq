package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type runJSON struct {
	RunID      string            `json:"run_id,omitempty"`
	Request    string            `json:"request"`
	Env        string            `json:"env,omitempty"`
	Status     string            `json:"status"`
	Code       int               `json:"code"`
	DurationMS int64             `json:"duration_ms"`
	Size       int               `json:"size"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       any               `json:"body"` // parsed when JSON, else a string
	Captured   []capturedVar     `json:"captured,omitempty"`
	Redacted   bool              `json:"redacted"`

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
	fs.BoolVar(&c.reveal, "reveal", false, "show secret values (needs a person at a terminal)")
	pos, err := c.parse(fs, args, 0, 1, "<request> [--env E] [--var k=v]… [--capture var=filter]… [--jq F] [-i] [--fail]")
	if err != nil {
		return err
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
	var typed request
	key, name := "cli", "curl"
	if *curl != "" {
		cr, _, err := parseCurl(*curl)
		if err != nil {
			return err
		}
		typed = request{Method: cr.Method, URL: cr.URL, Headers: toSavedHeaders(cr.Headers), Body: cr.Body}
		if len(cr.Form) > 0 {
			typed.BodyMode, typed.Form = bodyForm, toSavedHeaders(cr.Form)
		}
	} else {
		id, err := c.ws.findRequestRef(pos[0])
		if err != nil {
			return err
		}
		typed = c.ws.Requests[c.ws.find(id)]
		key, name = id, typed.displayName()
	}
	var allCaps []capture
	allCaps = append(allCaps, typed.Captures...)
	for _, spec := range caps {
		cp, err := parseCapture(strings.Replace(spec, "=", " = ", 1))
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
	env, _ := c.ws.env(envID)
	if env != nil && env.Protected {
		if err := c.confirm(fmt.Sprintf("Sending %s %s in the protected environment %q", typed.Method, name, env.Name), "yes"); err != nil {
			return err
		}
	}
	overrides := map[string]string{}
	for _, v := range vars {
		k, val, err := splitKV(v, "=", "--var")
		if err != nil {
			return err
		}
		overrides[k] = val
	}
	sent, missing := c.ws.resolve(envID, typed, overrides)
	if len(missing) > 0 {
		return fmt.Errorf("undefined variable(s): {{%s}}; set them with `barq env set` or --var", strings.Join(missing, "}}, {{"))
	}

	// Send, record, capture.
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	hist, _ := openHistory(c.ws)
	run := c.ws.newRun(key, name, envID, typed, sent)
	resp, sendErr := runRequest(ctx, sent, c.ws.CWD)
	// Capture first, so a token taken from this response is already a
	// secret when the run is scrubbed and recorded.
	var captured []capturedVar
	if sendErr == nil && len(allCaps) > 0 && resp.StatusCode < 400 {
		captured = evalCaptures(allCaps, resp.Body)
		if err := c.mutate(func(w *workspace) error { return w.storeCaptured(envID, captured) }); err != nil {
			return err
		}
	}
	if err := c.ws.recordRun(hist, run, resp, sendErr); err != nil {
		fmt.Fprintf(c.err, "barq: warning: couldn't record history: %v\n", err)
	}
	if sendErr != nil {
		if c.reveal {
			return sendErr
		}
		return fmt.Errorf("%s", c.rd.text(sendErr.Error()))
	}
	// Redact with the secrets known now, including ones just captured.
	c.rd = newRedactor(c.ws)

	out := c.runOut(run.Meta.ID, name, env, resp, *filter, *include)
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

// runOut builds the printable result of a response.
func (c *cli) runOut(runID, name string, env *environment, resp *response, filter string, include bool) runJSON {
	out := runJSON{RunID: runID, Request: name, Status: resp.Status, Code: resp.StatusCode,
		DurationMS: resp.Duration.Milliseconds(), Size: len(resp.Body), Redacted: !c.reveal, duration: resp.Duration}
	if env != nil {
		out.Env = env.Name
	}
	if include {
		h := resp.Headers
		if !c.reveal {
			h = c.rd.headers(h)
		}
		out.Headers = map[string]string{}
		for _, k := range sortedKeys(h) {
			out.Headers[k] = strings.Join(h[k], ", ")
		}
	}
	body := string(resp.Body)
	if filter != "" {
		vals, err := runJQ(filter, resp.Body)
		if err != nil {
			body = "jq: " + err.Error()
		} else {
			parts := make([]string, len(vals))
			for i, v := range vals {
				parts[i] = marshalJQ(v, true)
			}
			body = strings.Join(parts, "\n")
		}
	} else if pretty, isJSON := prettyBody(resp); isJSON {
		body = pretty
	}
	if !c.reveal {
		body = c.rd.body(body, resp.Headers.Get("Content-Type"), false)
	}
	out.Body = body
	if t := strings.TrimSpace(body); json.Valid([]byte(t)) && t != "" {
		out.Body = json.RawMessage(t)
	}
	if truncated := resp.Truncated; truncated {
		out.Status += " (body truncated)"
	}
	return out
}

func (c *cli) printRun(out runJSON, include bool) {
	env := ""
	if out.Env != "" {
		env = "  env " + out.Env
	}
	c.printf("%s  %s  %s%s\n", out.Status, roundDuration(out.duration), humanSize(out.Size), env)
	if len(out.Captured) > 0 {
		c.printf("%s\n", capturedSummary(out.Captured))
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
}

// History -------------------------------------------------------------------------

func cmdHistory(c *cli, args []string) error {
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
	hist, err := openHistory(c.ws)
	if err != nil {
		return err
	}
	if len(pos) == 2 && pos[0] == "show" {
		return c.showRun(hist, pos[1])
	}
	if len(pos) == 2 {
		return usagef("usage: barq history [request] | barq history show <run-id>")
	}

	runs := hist.all()
	if len(pos) == 1 {
		id, err := c.ws.findRequestRef(pos[0])
		if err != nil {
			return err
		}
		runs = hist.forKey(id)
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
		if i := c.ws.find(r.Key); i >= 0 {
			name = c.ws.slashPath(ref{id: r.Key})
		}
		prev, ok := hist.previous(r.ID)
		rows = append(rows, runRow{r.ID, name, r.Time, r.Method, r.Code, r.Status, c.rd.text(r.Error),
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

func (c *cli) showRun(hist *history, id string) error {
	e, err := hist.load(id)
	if err != nil {
		return fmt.Errorf("no run %q (see `barq history`)", id)
	}
	typed, sent := e.Request, e.Sent
	if !c.reveal {
		typed, sent = c.rd.request(typed, true), c.rd.request(sent, false)
	}
	resp := e.response()
	out := c.runOut(e.Meta.ID, e.Meta.Name, nil, resp, "", true)
	// History stores a redacted, reformatted body; report the real size.
	out.Env, out.DurationMS, out.duration, out.Size = e.Meta.Env, e.Meta.Duration.Milliseconds(), e.Meta.Duration, e.Meta.Size
	if e.Meta.Error != "" {
		out.Status = "error: " + c.rd.text(e.Meta.Error)
	}
	if c.asJSON {
		return c.printJSON(map[string]any{
			"run": out, "time": e.Meta.Time,
			"request_as_typed": c.requestOutRaw(typed), "request_as_sent": c.requestOutRaw(sent),
		})
	}
	c.printf("run %s  %s\n\nas typed:\n%s\nas sent:\n%s\n", e.Meta.ID, e.Meta.Time.Format("2006-01-02 15:04:05"),
		indent(requestText(typed), "  "), indent(requestText(sent), "  "))
	c.printRun(out, true)
	return nil
}

// requestOutRaw shows a request that's already redacted (no saved identity).
func (c *cli) requestOutRaw(r request) map[string]any {
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
