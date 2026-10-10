package runner

import (
	"context"
	"fmt"
	"strings"

	"github.com/iskaa02/barq/internal/core"
	"github.com/iskaa02/barq/internal/httpfile"
)

// Opts are optional extras for Send.
type Opts struct {
	Vars     map[string]string // variable overrides for this send
	Captures []core.Capture    // captures on top of the request's own
}

// Result is the outcome of one send. The caller closes Resp.
type Result struct {
	Resp *core.Response // nil when Err is set
	Err  error          // the request could not be completed
	// CaptureErr is set when captures could not be stored (for example no
	// environment is active). The response is otherwise normal.
	CaptureErr error
	Sent       core.Request // after variable resolution
	Typed      core.Request // as typed, with {{vars}} and the body read
	Fails      []string     // failed expectations
	Expects    int          // how many expectations were checked
	Captured   []core.CapturedVar
	RunID      string
	Env        *core.Environment
	// Redactor knows every secret, including ones just captured.
	Redactor core.Redactor
}

// NeedsConfirm reports whether sending r should be confirmed first: the
// request says @confirm, or the environment is protected for its method.
func NeedsConfirm(ws *core.Workspace, envID string, r httpfile.Request) bool {
	return r.Confirm || EnvNeedsConfirm(ws, envID, r)
}

// EnvNeedsConfirm is NeedsConfirm for the environment's protection alone.
func EnvNeedsConfirm(ws *core.Workspace, envID string, r httpfile.Request) bool {
	env, _ := ws.Env(envID)
	return env.NeedsConfirm(r.Method)
}

// Prepared is a request resolved and ready to send. It is immutable after
// Prepare, so Do may run on any goroutine while the workspace changes.
type Prepared struct {
	ref   Ref
	req   httpfile.Request
	envID string
	root  string
	caps  []core.Capture
	typed core.Request
	sent  core.Request
	env   *core.Environment
	hist  *core.History
	run   *core.HistEntry
}

// Raw is what the network part of a send produced.
type Raw struct {
	Resp *core.Response
	Err  error
}

// Prepare resolves r for the environment: variables, "< file" body and the
// history entry. It reads ws, so call it on the goroutine that owns ws.
// root is the project directory that "< file" bodies and @file uploads are
// relative to.
func Prepare(ws *core.Workspace, envID, root string, ref Ref, r httpfile.Request, opts ...Opts) (*Prepared, error) {
	var o Opts
	if len(opts) > 0 {
		o = opts[0]
	}
	body, err := r.ResolveBody(root)
	if err != nil {
		return nil, err
	}
	typed := r.Core()
	typed.Body = body
	sent, missing := ws.Resolve(envID, typed, o.Vars)
	if len(missing) > 0 {
		return nil, fmt.Errorf("undefined variable(s): {{%s}}; set them with `barq env set` or --var", strings.Join(missing, "}}, {{"))
	}
	p := &Prepared{ref: ref, req: r, envID: envID, root: root, typed: typed, sent: sent}
	p.env, _ = ws.Env(envID)
	p.caps = append(append([]core.Capture(nil), r.Captures...), o.Captures...)
	p.hist, _ = core.OpenHistory(ws)
	name := ref.Name
	if name == "" {
		name = ref.Key()
	}
	p.run = ws.NewRun(ref.Key(), name, envID, typed, sent)
	return p, nil
}

// Do performs the HTTP call. It touches no workspace and is safe on any goroutine.
func (p *Prepared) Do(ctx context.Context) Raw {
	resp, err := core.RunRequest(ctx, p.sent, p.root)
	return Raw{resp, err}
}

// Finish stores captures, checks expectations and records the run. It
// touches ws, so call it on the goroutine that owns it. A failed request is
// still recorded; its error is in Result.Err and also returned. A failure
// to store captures is in Result.CaptureErr only: the response is still
// checked, recorded and returned.
func (p *Prepared) Finish(ws *core.Workspace, raw Raw) (Result, error) {
	res := Result{Sent: p.sent, Typed: p.typed, Env: p.env, RunID: p.run.Meta.ID, Resp: raw.Resp, Err: raw.Err}
	if raw.Err == nil {
		// Capture before recording, so a token taken from this response
		// is already a secret when the run is scrubbed.
		if len(p.caps) > 0 && raw.Resp.StatusCode < 400 {
			res.Captured = core.EvalCaptures(p.caps, raw.Resp)
			if err := ws.Mutate(func(w *core.Workspace) error { return w.StoreCaptured(p.envID, res.Captured) }); err != nil {
				res.CaptureErr = err
				res.Captured = nil
			}
		}
		res.Fails = httpfile.Check(p.req.Expects, raw.Resp)
		res.Expects = len(p.req.Expects)
	}
	_ = ws.RecordRun(p.hist, p.run, raw.Resp, raw.Err)
	res.Redactor = core.NewRedactor(ws)
	return res, raw.Err
}

// Send is Prepare, Do and Finish in sequence: it resolves r for the
// environment, runs it with root as the project directory (for "< file"
// bodies and @file uploads), stores its captures, checks its expectations
// and records the run in history under ref.Key(). Errors before the request ran return a zero Result.
func Send(ctx context.Context, ws *core.Workspace, envID, root string, ref Ref, r httpfile.Request, opts ...Opts) (Result, error) {
	p, err := Prepare(ws, envID, root, ref, r, opts...)
	if err != nil {
		return Result{}, err
	}
	return p.Finish(ws, p.Do(ctx))
}
