package ntui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/iskaa02/barq/internal/core"
	"github.com/iskaa02/barq/internal/httpfile"
	"github.com/iskaa02/barq/internal/runner"
)

// Response is one answer to a request, kept for display and diffing.
type Response struct {
	Key      string
	RunID    string         // the history run it was recorded as
	Hist     bool           // loaded from history, not just sent
	Res      *core.Response // nil when Err is set
	Err      error
	At       time.Time
	Env      string
	Pass     int      // passed expectations
	Fails    []string // failed expectation messages
	Expects  []expectResult
	Caps     []core.CapturedVar // @capture results (secret values cleared)
	ReqLines []string           // the request as sent, secrets masked
	Lines    []string           // the body as shown
	FT       string             // filetype of Lines: json, html, xml or text
}

// pendingSend is a request waiting for confirmation or for its answer.
type pendingSend struct {
	ref runner.Ref
	req httpfile.Request
	env *core.Environment
}

func (p *pendingSend) confirmText() string {
	where := ""
	if p.env != nil {
		where = " to " + p.env.Name
	}
	return fmt.Sprintf("send %s %s%s?", p.req.Method, p.req.URL, where)
}

type (
	respMsg struct {
		p   *pendingSend
		pr  *runner.Prepared
		raw runner.Raw
	}
	tickMsg struct{}
	syncMsg struct{}
)

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func tick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

// send starts sending the request under the editor's cursor.
func (a *App) send() tea.Cmd {
	if a.sending {
		return nil
	}
	_, req, key, err := a.CurrentRequest()
	if err != nil {
		a.flashErr(err.Error())
		return nil
	}
	p := &pendingSend{ref: refOf(key, req), req: req, env: a.ws.CurrentEnv()}
	if runner.NeedsConfirm(a.ws, a.ws.ActiveEnv, req) {
		a.pending, a.modal = p, confirmModal
		return nil
	}
	return a.dispatch(p)
}

// refOf rebuilds the Ref of a request from its key ("path#name" or "path#n").
func refOf(key string, req httpfile.Request) runner.Ref {
	path, sel := runner.ParseRef(key)
	ref := runner.Ref{Path: path, Name: req.Name}
	if req.Name == "" {
		ref.Index, _ = strconv.Atoi(sel)
	}
	return ref
}

// dispatch prepares the request here and runs only the network call in the
// background: the workspace is Update-goroutine state.
func (a *App) dispatch(p *pendingSend) tea.Cmd {
	pr, err := runner.Prepare(a.ws, a.ws.ActiveEnv, a.cwd, p.ref, p.req)
	if err != nil {
		a.flashErr(err.Error())
		return nil
	}
	a.sending = true
	run := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		return respMsg{p, pr, pr.Do(ctx)}
	}
	return tea.Batch(run, tick())
}

// onResponse finishes a request (captures, expectations, history) and shows it.
func (a *App) onResponse(m respMsg) {
	a.sending = false
	res, err := m.pr.Finish(a.ws, m.raw)
	r := &Response{Key: m.p.ref.Key(), RunID: res.RunID, Res: res.Resp, Err: err, At: time.Now(), Env: a.EnvName()}
	if err != nil {
		r.Lines, r.FT = []string{err.Error()}, "text"
	} else {
		r.Lines, r.FT = bodyLines(res.Resp)
		r.Fails = res.Fails
		r.Pass = res.Expects - len(res.Fails)
		for _, e := range m.p.req.Expects {
			er := expectResult{OK: true, Text: e.Kind + " " + e.Arg}
			if f := httpfile.Check([]httpfile.Expect{e}, res.Resp); len(f) > 0 {
				er = expectResult{Text: f[0]}
			}
			r.Expects = append(r.Expects, er)
		}
		r.Caps = res.Captured
		if len(res.Captured) > 0 {
			a.refreshEnv()
			a.flash("captured " + core.CapturedSummary(res.Captured))
		}
	}
	r.ReqLines = requestLines(res.Sent, a.cwd, a.secretValues())
	a.mu.Lock()
	hist := append(a.resps[r.Key], r)
	if len(hist) > 2 {
		if old := hist[0].Res; old != nil {
			_ = old.Close()
		}
		hist = hist[1:]
	}
	a.resps[r.Key] = hist
	a.mu.Unlock()
	a.show(r)
	if res.CaptureErr != nil {
		a.flashErr("captures not stored: " + res.CaptureErr.Error())
	}
}

// respFromRun builds a Response from a stored run.
func respFromRun(e *core.HistEntry, cwd string) *Response {
	m := e.Meta
	r := &Response{Key: m.Key, RunID: m.ID, Hist: true, At: m.Time, Env: m.Env,
		ReqLines: requestLines(e.Sent, cwd, nil)}
	if m.Error != "" {
		r.Err = errors.New(m.Error)
		r.Lines, r.FT = []string{m.Error}, "text"
		return r
	}
	r.Res = e.Response()
	r.Lines, r.FT = bodyLines(r.Res)
	return r
}

// secretValues lists the values of the active environment's secret variables.
func (a *App) secretValues() []string {
	env := a.ws.CurrentEnv()
	if env == nil {
		return nil
	}
	var out []string
	for _, v := range env.Vars {
		if v.Enabled && v.Value != "" && core.IsSecret(v) {
			out = append(out, v.Value)
		}
	}
	return out
}

// show puts r in the response pane.
func (a *App) show(r *Response) {
	a.shown = r
	a.responseShown(r.Key)
	a.showView()
}

// LastResponses returns up to two responses for key, oldest first.
func (a *App) LastResponses(key string) []*Response {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]*Response(nil), a.resps[key]...)
}

// bodyLines formats a response body for the response pane and picks its filetype.
func bodyLines(res *core.Response) ([]string, string) {
	ct := strings.ToLower(res.Headers.Get("Content-Type"))
	body := res.Body
	ft := "text"
	trimmed := bytes.TrimSpace(body)
	switch {
	case strings.Contains(ct, "json") || (!res.Partial() && json.Valid(trimmed) && len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')):
		ft = "json"
		var buf bytes.Buffer
		if !res.Partial() && json.Indent(&buf, trimmed, "", "  ") == nil {
			body = buf.Bytes()
		}
	case strings.Contains(ct, "html"):
		ft = "html"
	case strings.Contains(ct, "xml"):
		ft = "xml"
	}
	s := strings.ReplaceAll(string(body), "\r\n", "\n")
	return strings.Split(s, "\n"), ft
}

// respTitle is the response box's title: status, timing, expectations, env.
func (a *App) respTitle() string {
	if a.sending {
		return spinFrames[a.spin%len(spinFrames)] + " sending…"
	}
	r := a.shown
	if r == nil {
		return "response"
	}
	if r.Hist {
		return "history · " + r.At.Format("Jan 2 15:04:05") + " · " + a.respStatus(r)
	}
	return a.respStatus(r)
}

// respStatus is the status summary after the view tabs.
func (a *App) respStatus(r *Response) string {
	if r.Err != nil {
		return errStyle.Render("error") + muted.Render(" · "+r.Env)
	}
	st := okStyle
	if r.Res.StatusCode >= 400 {
		st = errStyle
	}
	parts := []string{st.Render(r.Res.Status), r.Res.Duration.Round(time.Millisecond).String(), core.HumanSize(r.Res.Size)}
	if r.Pass > 0 {
		parts = append(parts, okStyle.Render(fmt.Sprintf("✓ %d", r.Pass)))
	}
	if len(r.Fails) > 0 {
		parts = append(parts, errStyle.Render("✗ "+strings.Join(r.Fails, "; ")))
	}
	if r.Env != "" {
		parts = append(parts, r.Env)
	}
	return strings.Join(parts, " · ")
}

// syncTick schedules the next check for workspace changes made outside the app.
func syncTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return syncMsg{} })
}
