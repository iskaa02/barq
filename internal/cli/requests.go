package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/iskaa02/barq/internal/core"
)

// Request editing flags, shared by `new` and `set`.
type editFlags struct {
	method, url, name, body, bodyMode, curl              string
	headers, params, forms, captures                     multi
	unsetHeaders, unsetParams, unsetForms, unsetCaptures multi
}

func addEditFlags(fs *flag.FlagSet, forSet bool) *editFlags {
	e := &editFlags{}
	fs.StringVar(&e.method, "method", "", "HTTP method")
	fs.StringVar(&e.method, "X", "", "HTTP method")
	fs.StringVar(&e.url, "url", "", "URL; may use {{variables}}")
	fs.Var(&e.headers, "header", `header "Key: value" (repeatable; replaces a header with the same key)`)
	fs.Var(&e.headers, "H", "header (same as --header)")
	fs.Var(&e.params, "param", "query parameter key=value (repeatable)")
	fs.StringVar(&e.body, "body", "", "raw body: text, @file, or - for stdin")
	fs.Var(&e.forms, "form", "form-data field key=value, or key=@path for a file (repeatable)")
	fs.Var(&e.captures, "capture", "store part of each successful response: var=jq-filter (repeatable)")
	fs.StringVar(&e.curl, "curl", "", "take the request from a curl command")
	fs.StringVar(&e.bodyMode, "body-mode", "", "raw or form")
	if forSet {
		fs.StringVar(&e.name, "name", "", "new name")
		fs.Var(&e.unsetHeaders, "unset-header", "remove a header (repeatable)")
		fs.Var(&e.unsetParams, "unset-param", "remove a query parameter (repeatable)")
		fs.Var(&e.unsetForms, "unset-form", "remove a form field (repeatable)")
		fs.Var(&e.unsetCaptures, "unset-capture", "remove a capture by variable (repeatable)")
	}
	return e
}

// readBody resolves --body: text, @file, or - (stdin).
func (c *cli) readBody(v string) (string, error) {
	switch {
	case v == "-":
		b, err := io.ReadAll(c.in)
		return string(b), err
	case strings.HasPrefix(v, "@"):
		b, err := os.ReadFile(v[1:])
		return string(b), err
	}
	return v, nil
}

// setKV replaces the first field with key (case-insensitively for headers)
// or appends one. A redacted value keeps the old one.
func setKV(list []core.SavedHeader, key, value string, fold bool) ([]core.SavedHeader, error) {
	same := func(a string) bool {
		if fold {
			return strings.EqualFold(a, key)
		}
		return a == key
	}
	for i := range list {
		if same(list[i].Key) {
			v, err := core.UnredactField(value, list[i].Value, true)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			list[i].Value, list[i].Enabled = v, true
			return list, nil
		}
	}
	if _, err := core.UnredactField(value, "", false); err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	return append(list, core.SavedHeader{Key: key, Value: value, Enabled: true}), nil
}

func unsetKV(list []core.SavedHeader, key string, fold bool) []core.SavedHeader {
	return slices.DeleteFunc(list, func(h core.SavedHeader) bool {
		if fold {
			return strings.EqualFold(h.Key, key)
		}
		return h.Key == key
	})
}

func splitKV(s, sep, what string) (string, string, error) {
	k, v, ok := strings.Cut(s, sep)
	if k = strings.TrimSpace(k); !ok || k == "" {
		return "", "", usagef("%s %q: expected key%svalue", what, s, sep)
	}
	return k, strings.TrimSpace(v), nil
}

// apply changes r according to the flags that were given.
func (e *editFlags) apply(c *cli, fs *flag.FlagSet, r *core.Request) error {
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })

	if e.curl != "" {
		cr, warnings, err := core.ParseCurl(e.curl)
		if err != nil {
			return err
		}
		for _, w := range warnings {
			fmt.Fprintf(c.err, "barq: curl: %s\n", w)
		}
		r.Method, r.URL, r.Headers, r.Body = cr.Method, cr.URL, core.ToSavedHeaders(cr.Headers), cr.Body
		r.DisabledParams, r.Form, r.BodyMode = nil, nil, ""
		if len(cr.Form) > 0 {
			r.BodyMode, r.Form = core.BodyForm, core.ToSavedHeaders(cr.Form)
		}
	}
	if given["method"] || given["X"] {
		r.Method = strings.ToUpper(strings.TrimSpace(e.method))
	}
	if given["url"] {
		if core.IsRedacted(e.url) {
			return core.ErrRedactedWrite
		}
		r.URL = e.url
	}
	if given["name"] {
		r.Name = strings.TrimSpace(e.name)
	}
	for _, h := range e.headers {
		k, v, err := splitKV(h, ":", "--header")
		if err != nil {
			return err
		}
		if r.Headers, err = setKV(r.Headers, k, v, true); err != nil {
			return err
		}
	}
	for _, k := range e.unsetHeaders {
		r.Headers = unsetKV(r.Headers, k, true)
	}

	if len(e.params) > 0 || len(e.unsetParams) > 0 {
		rows := core.ToSavedHeaders(core.ParamRows(r.URL, core.FromSavedHeaders(r.DisabledParams)))
		for _, p := range e.params {
			k, v, err := splitKV(p, "=", "--param")
			if err != nil {
				return err
			}
			if rows, err = setKV(rows, k, v, false); err != nil {
				return err
			}
		}
		for _, k := range e.unsetParams {
			rows = unsetKV(rows, k, false)
		}
		r.URL = core.BuildURL(r.URL, core.FromSavedHeaders(rows))
		r.DisabledParams = core.ToSavedHeaders(core.DisabledRows(core.FromSavedHeaders(rows)))
	}

	if given["body"] {
		body, err := c.readBody(e.body)
		if err != nil {
			return err
		}
		if r.Body, err = core.UnredactBody(body, r.Body); err != nil {
			return err
		}
		r.BodyMode = ""
	}
	for _, f := range e.forms {
		k, v, err := splitKV(f, "=", "--form")
		if err != nil {
			return err
		}
		if r.Form, err = setKV(r.Form, k, v, false); err != nil {
			return err
		}
		r.BodyMode = core.BodyForm
	}
	for _, k := range e.unsetForms {
		r.Form = unsetKV(r.Form, k, false)
	}
	switch e.bodyMode {
	case "":
	case "raw":
		r.BodyMode = ""
	case "form", "form-data", "multipart":
		r.BodyMode = core.BodyForm
	default:
		return usagef("--body-mode must be raw or form")
	}

	for _, spec := range e.captures {
		cp, err := core.ParseCapture(strings.Replace(spec, "=", " = ", 1))
		if err != nil {
			return usagef("--capture %q: %v", spec, err)
		}
		r.Captures = slices.DeleteFunc(r.Captures, func(x core.Capture) bool { return x.Var == cp.Var })
		r.Captures = append(r.Captures, cp)
	}
	for _, v := range e.unsetCaptures {
		r.Captures = slices.DeleteFunc(r.Captures, func(x core.Capture) bool { return x.Var == v })
	}
	if r.Method == "" {
		r.Method = "GET"
	}
	return nil
}

// Commands ---------------------------------------------------------------------

func cmdLs(c *cli, args []string) error {
	fs := c.flags("ls")
	pos, err := c.parse(fs, args, 0, 1, "[folder]")
	if err != nil {
		return err
	}
	if err := c.open(); err != nil {
		return err
	}
	root := ""
	if len(pos) == 1 {
		r, err := c.ws.FindRef(pos[0])
		if err != nil {
			return err
		}
		if !r.Folder {
			return fmt.Errorf("%q is a request; use `barq show`", pos[0])
		}
		root = r.ID
	}
	in := c.ws.Subtree(root)

	type folderOut struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	}
	type reqOut struct {
		ID     string `json:"id"`
		Path   string `json:"path"`
		Method string `json:"method"`
		URL    string `json:"url"`
	}
	var folders []folderOut
	var reqs []reqOut
	for _, f := range c.ws.Folders {
		if in[f.ID] && f.ID != root {
			folders = append(folders, folderOut{f.ID, c.ws.SlashPath(core.Ref{Folder: true, ID: f.ID})})
		}
	}
	for _, r := range c.ws.Requests {
		if in[r.Folder] {
			reqs = append(reqs, reqOut{r.ID, c.ws.SlashPath(core.Ref{ID: r.ID}), r.Method, c.rd.URL(r.URL, true)})
		}
	}
	if c.asJSON {
		if folders == nil {
			folders = []folderOut{}
		}
		if reqs == nil {
			reqs = []reqOut{}
		}
		return c.printJSON(map[string]any{"folders": folders, "requests": reqs})
	}
	if len(folders)+len(reqs) == 0 {
		c.printf("No saved requests. Create one with `barq new <folder>/<name> --url …`.\n")
		return nil
	}
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		pad := strings.Repeat("  ", depth)
		for _, f := range c.ws.Folders {
			if f.Parent == parent {
				c.printf("%s%s/\n", pad, f.Name)
				walk(f.ID, depth+1)
			}
		}
		for _, r := range c.ws.Requests {
			if r.Folder == parent {
				c.printf("%s%-7s %s  (%s)\n", pad, r.Method, r.DisplayName(), r.ID)
			}
		}
	}
	walk(root, 0)
	return nil
}

func cmdShow(c *cli, args []string) error {
	fs := c.flags("show")
	fs.BoolVar(&c.reveal, "reveal", false, "show secret values (needs a person at a terminal)")
	pos, err := c.parse(fs, args, 1, 1, "<request>")
	if err != nil {
		return err
	}
	if err := c.open(); err != nil {
		return err
	}
	if err := c.checkReveal(); err != nil {
		return err
	}
	id, err := c.ws.FindRequestRef(pos[0])
	if err != nil {
		return err
	}
	r := c.ws.Requests[c.ws.Find(id)]
	if c.asJSON {
		return c.printJSON(c.requestOut(r))
	}
	c.printRequest(r)
	return nil
}

func (c *cli) checkReveal() error {
	if c.reveal {
		return c.confirm("Showing secret values")
	}
	return nil
}

func cmdNew(c *cli, args []string) error {
	fs := c.flags("new")
	e := addEditFlags(fs, false)
	from := fs.String("from", "", "start as a copy of this request")
	pos, err := c.parse(fs, args, 1, 1, "<folder/…/name> [--from request] [--method M] [--url U] [-H 'K: V']… [--body …] [--form k=v]… [--curl '…']")
	if err != nil {
		return err
	}
	if err := c.open(); err != nil {
		return err
	}
	parts := core.SplitPath(pos[0])
	if len(parts) == 0 {
		return usagef("give the request a name, e.g. Auth/Login")
	}
	r := core.Request{Method: "GET"}
	if *from != "" {
		id, err := c.ws.FindRequestRef(*from)
		if err != nil {
			return err
		}
		r = c.ws.Requests[c.ws.Find(id)]
		r.ID, r.Source = "", ""
		r.Captures = slices.Clone(r.Captures)
	}
	r.Name = parts[len(parts)-1]
	if err := e.apply(c, fs, &r); err != nil {
		return err
	}
	var saved core.Request
	if err := c.mutate(func(w *core.Workspace) error {
		r.Folder = w.MkdirAll(strings.Join(parts[:len(parts)-1], "/"))
		saved = w.AddRequest(r)
		return nil
	}); err != nil {
		return err
	}
	return c.done(saved, "created")
}

// done prints a request after a change.
func (c *cli) done(r core.Request, verb string) error {
	if c.asJSON {
		return c.printJSON(c.requestOut(r))
	}
	c.printf("%s %s (%s)\n", verb, c.ws.SlashPath(core.Ref{ID: r.ID}), r.ID)
	return nil
}

func cmdSet(c *cli, args []string) error {
	fs := c.flags("set")
	e := addEditFlags(fs, true)
	pos, err := c.parse(fs, args, 1, 1, "<request> [--url U] [-H 'K: V']… [--body …] [--name N] …")
	if err != nil {
		return err
	}
	if fs.NFlag() == 0 {
		return usagef("nothing to change; see `barq ai` for the flags")
	}
	if err := c.open(); err != nil {
		return err
	}
	id, err := c.ws.FindRequestRef(pos[0])
	if err != nil {
		return err
	}
	var saved core.Request
	if err := c.mutate(func(w *core.Workspace) error {
		var applyErr error
		if err := w.UpdateRequest(id, func(r *core.Request) {
			edited := *r
			if applyErr = e.apply(c, fs, &edited); applyErr == nil {
				*r = edited
			}
			saved = *r
		}); err != nil {
			return err
		}
		return applyErr
	}); err != nil {
		return err
	}
	return c.done(saved, "updated")
}

func cmdMkdir(c *cli, args []string) error {
	fs := c.flags("mkdir")
	pos, err := c.parse(fs, args, 1, 1, "<folder/…>")
	if err != nil {
		return err
	}
	if err := c.open(); err != nil {
		return err
	}
	var id string
	if err := c.mutate(func(w *core.Workspace) error { id = w.MkdirAll(pos[0]); return nil }); err != nil {
		return err
	}
	if c.asJSON {
		return c.printJSON(map[string]string{"id": id, "path": c.ws.SlashPath(core.Ref{Folder: true, ID: id})})
	}
	c.printf("%s/ (%s)\n", c.ws.SlashPath(core.Ref{Folder: true, ID: id}), id)
	return nil
}

func cmdMv(c *cli, args []string) error {
	fs := c.flags("mv")
	pos, err := c.parse(fs, args, 2, 2, "<request|folder> <destination folder, or / for the top level>")
	if err != nil {
		return err
	}
	if err := c.open(); err != nil {
		return err
	}
	r, err := c.ws.FindRef(pos[0])
	if err != nil {
		return err
	}
	if err := c.mutate(func(w *core.Workspace) error {
		dest := w.MkdirAll(pos[1])
		if r.Folder {
			return w.MoveFolder(r.ID, dest)
		}
		return w.MoveRequest(r.ID, dest)
	}); err != nil {
		return err
	}
	if c.asJSON {
		return c.printJSON(map[string]string{"id": r.ID, "path": c.ws.SlashPath(r)})
	}
	c.printf("moved to %s\n", c.ws.SlashPath(r))
	return nil
}

func cmdRename(c *cli, args []string) error {
	fs := c.flags("rename")
	pos, err := c.parse(fs, args, 2, 2, "<request|folder> <new name>")
	if err != nil {
		return err
	}
	if err := c.open(); err != nil {
		return err
	}
	r, err := c.ws.FindRef(pos[0])
	if err != nil {
		return err
	}
	name := strings.TrimSpace(pos[1])
	if name == "" || strings.Contains(name, "/") {
		return usagef("names can't be empty or contain /")
	}
	if err := c.mutate(func(w *core.Workspace) error {
		if r.Folder {
			return w.RenameFolder(r.ID, name)
		}
		return w.UpdateRequest(r.ID, func(x *core.Request) { x.Name = name })
	}); err != nil {
		return err
	}
	if c.asJSON {
		return c.printJSON(map[string]string{"id": r.ID, "path": c.ws.SlashPath(r)})
	}
	c.printf("renamed to %s\n", c.ws.SlashPath(r))
	return nil
}

func cmdRm(c *cli, args []string) error {
	fs := c.flags("rm")
	recursive := fs.Bool("r", false, "delete a folder with everything in it")
	pos, err := c.parse(fs, args, 1, 1, "<request|folder> [-r]")
	if err != nil {
		return err
	}
	if err := c.open(); err != nil {
		return err
	}
	r, err := c.ws.FindRef(pos[0])
	if err != nil {
		return err
	}
	path := c.ws.SlashPath(r)
	if r.Folder && !*recursive && (c.ws.CountIn(r.ID) > 0 || len(c.ws.Subtree(r.ID)) > 1) {
		return errors.New(path + " isn't empty; use -r to delete it with everything in it")
	}
	removed := 1
	if err := c.mutate(func(w *core.Workspace) error {
		if r.Folder {
			gone, err := w.DeleteFolder(r.ID)
			removed = len(gone)
			return err
		}
		return w.DeleteRequest(r.ID)
	}); err != nil {
		return err
	}
	if c.asJSON {
		return c.printJSON(map[string]any{"deleted": path, "requests_deleted": removed})
	}
	c.printf("deleted %s\n", path)
	return nil
}

// cmdCurl prints a request as curl with non-secret variables filled in.
// Secret variables stay as {{name}} unless revealed.
func cmdCurl(c *cli, args []string) error {
	fs := c.flags("curl")
	envName := fs.String("env", "", "environment (default: the active one)")
	fs.BoolVar(&c.reveal, "reveal", false, "fill in secrets too (needs a person at a terminal)")
	pos, err := c.parse(fs, args, 1, 1, "<request> [--env E]")
	if err != nil {
		return err
	}
	if err := c.open(); err != nil {
		return err
	}
	if err := c.checkReveal(); err != nil {
		return err
	}
	id, err := c.ws.FindRequestRef(pos[0])
	if err != nil {
		return err
	}
	envID, err := c.envID(*envName)
	if err != nil {
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
	r, _ := core.ResolveVars(c.ws.Requests[c.ws.Find(id)], vars)
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
