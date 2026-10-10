package cli

import (
	"strings"

	"github.com/iskaa02/barq/internal/core"
	"github.com/iskaa02/barq/internal/httpfile"
	"github.com/iskaa02/barq/internal/runner"
)

func splitKV(s, sep, what string) (string, string, error) {
	k, v, ok := strings.Cut(s, sep)
	if k = strings.TrimSpace(k); !ok || k == "" {
		return "", "", usagef("%s %q: expected key%svalue", what, s, sep)
	}
	return k, strings.TrimSpace(v), nil
}

// root is the project directory: where the .http files are searched.
func (c *cli) root() string { return c.ws.CWD }

func (c *cli) checkReveal() error {
	if c.reveal {
		return c.confirm("Showing secret values")
	}
	return nil
}

// find resolves a request reference in the project.
func (c *cli) find(ref string) (runner.Ref, httpfile.Request, error) {
	return runner.Find(c.root(), ref)
}

// Commands ---------------------------------------------------------------------

func cmdLs(c *cli, args []string) error {
	fs := c.flags("ls")
	if _, err := c.parse(fs, args, 0, 0, ""); err != nil {
		return err
	}
	if err := c.open(); err != nil {
		return err
	}
	all, err := runner.ListAll(c.root())
	if err != nil {
		return err
	}
	type reqOut struct {
		Ref    string `json:"ref"`
		Path   string `json:"path"`
		Name   string `json:"name,omitempty"`
		Index  int    `json:"index"`
		Method string `json:"method"`
		URL    string `json:"url"`
	}
	rows := []reqOut{}
	for _, e := range all {
		rows = append(rows, reqOut{e.Ref.Key(), e.Ref.Path, e.Ref.Name, e.Ref.Index, e.Req.Method, c.rd.URL(e.Req.URL, true)})
	}
	if c.asJSON {
		return c.printJSON(rows)
	}
	if len(rows) == 0 {
		c.printf("No requests. Add some to a .http file, e.g. api.http:\n\n### list users\nGET {{baseUrl}}/users\n")
		return nil
	}
	w := 0
	for _, r := range rows {
		w = max(w, len(r.Ref))
	}
	for _, r := range rows {
		c.printf("%-*s  %-6s %s\n", w, r.Ref, r.Method, r.URL)
	}
	return nil
}

func cmdShow(c *cli, args []string) error {
	fs := c.flags("show")
	envName := fs.String("env", "", "environment (default: the active one)")
	pos, err := c.parse(fs, args, 1, 1, "<ref> [--env E]")
	if err != nil {
		return err
	}
	if err := c.open(); err != nil {
		return err
	}
	ref, req, err := c.find(pos[0])
	if err != nil {
		return err
	}
	envID, err := c.envID(*envName)
	if err != nil {
		return err
	}
	typed := req.Core()
	resolved, missing := c.ws.Resolve(envID, typed, nil)
	text := c.rd.Text(httpfile.Format(req))
	url := c.rd.URL(core.NormalizeURL(resolved.URL), false)
	if c.asJSON {
		return c.printJSON(map[string]any{"ref": ref.Key(), "file": ref.Path, "text": text,
			"resolved_url": url, "missing_vars": missing, "confirm": req.Confirm})
	}
	c.printf("# %s\n%s", ref.Key(), text)
	c.printf("\nresolved: %s %s\n", resolved.Method, url)
	if len(missing) > 0 {
		c.printf("undefined: {{%s}}\n", strings.Join(missing, "}}, {{"))
	}
	return nil
}

// cmdCurl prints a request as curl with non-secret variables filled in.
// Secret variables stay as {{name}} unless revealed.
func cmdCurl(c *cli, args []string) error {
	fs := c.flags("curl")
	envName := fs.String("env", "", "environment (default: the active one)")
	fs.BoolVar(&c.reveal, "reveal", false, "fill in secrets too (needs a person at a terminal)")
	pos, err := c.parse(fs, args, 1, 1, "<ref> [--env E]")
	if err != nil {
		return err
	}
	if err := c.open(); err != nil {
		return err
	}
	if err := c.checkReveal(); err != nil {
		return err
	}
	_, req, err := c.find(pos[0])
	if err != nil {
		return err
	}
	envID, err := c.envID(*envName)
	if err != nil {
		return err
	}
	r := req.Core()
	if r.Body, err = req.ResolveBody(c.root()); err != nil {
		return err
	}
	vars := c.ws.EnvVars(envID)
	if !c.reveal {
		for _, v := range c.envVarList(envID) {
			if core.IsSecret(v) {
				delete(vars, v.Key)
			}
		}
	}
	r, _ = core.ResolveVars(r, vars)
	if !c.reveal {
		r = c.rd.Request(r, true)
	}
	c.printf("%s\n", core.ToCurl(r))
	return nil
}

// envID resolves --env, defaulting to the active environment.
func (c *cli) envID(name string) (string, error) {
	if name == "" {
		return c.ws.ActiveEnv, nil
	}
	env, err := c.ws.EnvByRef(name)
	if err != nil {
		return "", err
	}
	return env.ID, nil
}

func (c *cli) envVarList(envID string) []core.SavedHeader {
	if env, err := c.ws.Env(envID); err == nil {
		return env.Vars
	}
	return nil
}
