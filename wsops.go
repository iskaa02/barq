package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Operations on a workspace's content, shared by the TUI and the CLI. They
// look everything up by ID, so they work on content freshly reloaded inside
// workspace.mutate.

var errNotFound = errors.New("not found")

// applyEdits copies what the request editors change onto r, leaving its
// identity (ID, name, folder, source) alone.
func (r *request) applyEdits(e request) {
	r.Method, r.URL, r.Headers, r.Body = e.Method, e.URL, e.Headers, e.Body
	r.BodyMode, r.Form, r.DisabledParams = e.BodyMode, e.Form, e.DisabledParams
}

// Requests ------------------------------------------------------------------

// addRequest saves a new request, giving it an ID if it has none. A folder
// that no longer exists puts it at the top level.
func (w *workspace) addRequest(r request) request {
	if r.ID == "" {
		r.ID = newID()
	}
	if w.findFolder(r.Folder) < 0 {
		r.Folder = ""
	}
	w.Requests = append(w.Requests, r)
	w.unfold(r.Folder)
	return r
}

func (w *workspace) updateRequest(id string, fn func(*request)) error {
	i := w.find(id)
	if i < 0 {
		return fmt.Errorf("request %s: %w", id, errNotFound)
	}
	fn(&w.Requests[i])
	return nil
}

func (w *workspace) deleteRequest(id string) error {
	i := w.find(id)
	if i < 0 {
		return fmt.Errorf("request %s: %w", id, errNotFound)
	}
	w.Requests = slices.Delete(w.Requests, i, i+1)
	return nil
}

func (w *workspace) moveRequest(id, folderID string) error {
	if folderID != "" && w.findFolder(folderID) < 0 {
		return fmt.Errorf("folder %s: %w", folderID, errNotFound)
	}
	if err := w.updateRequest(id, func(r *request) { r.Folder = folderID }); err != nil {
		return err
	}
	w.unfold(folderID)
	return nil
}

// Folders -------------------------------------------------------------------

func (w *workspace) createFolder(parent, name string) (string, error) {
	if parent != "" && w.findFolder(parent) < 0 {
		return "", fmt.Errorf("folder %s: %w", parent, errNotFound)
	}
	f := folder{ID: newID(), Name: name, Parent: parent}
	w.Folders = append(w.Folders, f)
	w.unfold(parent)
	return f.ID, nil
}

func (w *workspace) renameFolder(id, name string) error {
	i := w.findFolder(id)
	if i < 0 {
		return fmt.Errorf("folder %s: %w", id, errNotFound)
	}
	w.Folders[i].Name = name
	return nil
}

func (w *workspace) moveFolder(id, parent string) error {
	i := w.findFolder(id)
	if i < 0 {
		return fmt.Errorf("folder %s: %w", id, errNotFound)
	}
	if parent != "" && w.findFolder(parent) < 0 {
		return fmt.Errorf("folder %s: %w", parent, errNotFound)
	}
	if w.subtree(id)[parent] {
		return errors.New("can't move a folder into itself")
	}
	w.Folders[i].Parent = parent
	w.unfold(parent)
	return nil
}

// deleteFolder removes a folder with everything in it and returns the IDs
// of the requests that went with it.
func (w *workspace) deleteFolder(id string) ([]string, error) {
	if w.findFolder(id) < 0 {
		return nil, fmt.Errorf("folder %s: %w", id, errNotFound)
	}
	gone := w.subtree(id)
	var deleted []string
	w.Requests = slices.DeleteFunc(w.Requests, func(r request) bool {
		if gone[r.Folder] {
			deleted = append(deleted, r.ID)
			return true
		}
		return false
	})
	w.Folders = slices.DeleteFunc(w.Folders, func(f folder) bool { return gone[f.ID] })
	return deleted, nil
}

func (w *workspace) setCollapsed(id string, collapsed bool) {
	if i := w.findFolder(id); i >= 0 {
		w.Folders[i].Collapsed = collapsed
	}
}

func (w *workspace) setAllCollapsed(collapsed bool) {
	for i := range w.Folders {
		w.Folders[i].Collapsed = collapsed
	}
}

// unfold expands a folder so something just put in it is visible.
func (w *workspace) unfold(id string) { w.setCollapsed(id, false) }

// Environments ----------------------------------------------------------------

func (w *workspace) addEnv(name string, vars []savedHeader) string {
	env := environment{ID: newID(), Name: name, Vars: vars}
	w.Environments = append(w.Environments, env)
	return env.ID
}

func (w *workspace) env(id string) (*environment, error) {
	i := w.findEnv(id)
	if i < 0 {
		return nil, fmt.Errorf("environment %s: %w", id, errNotFound)
	}
	return &w.Environments[i], nil
}

func (w *workspace) renameEnv(id, name string) error {
	env, err := w.env(id)
	if err == nil {
		env.Name = name
	}
	return err
}

func (w *workspace) deleteEnv(id string) error {
	i := w.findEnv(id)
	if i < 0 {
		return fmt.Errorf("environment %s: %w", id, errNotFound)
	}
	w.Environments = slices.Delete(w.Environments, i, i+1)
	if w.ActiveEnv == id {
		w.ActiveEnv = ""
	}
	return nil
}

func (w *workspace) useEnv(id string) error {
	if id != "" && w.findEnv(id) < 0 {
		return fmt.Errorf("environment %s: %w", id, errNotFound)
	}
	w.ActiveEnv = id
	return nil
}

// setEnvVar sets one variable, adding it if needed.
func (w *workspace) setEnvVar(envID, key, value string) error {
	env, err := w.env(envID)
	if err != nil {
		return err
	}
	for i := range env.Vars {
		if env.Vars[i].Key == key {
			env.Vars[i].Value, env.Vars[i].Enabled = value, true
			return nil
		}
	}
	env.Vars = append(env.Vars, savedHeader{Key: key, Value: value, Enabled: true})
	return nil
}

func (w *workspace) setEnvVars(envID string, vars []savedHeader) error {
	env, err := w.env(envID)
	if err == nil {
		env.Vars = vars
	}
	return err
}

// Paths -------------------------------------------------------------------------

// requestPath is a request's full path, e.g. "Auth / Logs in a user".
func (w *workspace) requestPath(r request) string {
	if p := w.folderPath(r.Folder); p != "" {
		return p + " / " + r.displayName()
	}
	return r.displayName()
}

// splitPath splits "A/B/C" or "A / B / C" into trimmed parts.
func splitPath(p string) []string {
	var parts []string
	for _, s := range strings.Split(p, "/") {
		if s = strings.TrimSpace(s); s != "" {
			parts = append(parts, s)
		}
	}
	return parts
}

// Refs -------------------------------------------------------------------------

// ref is a request or folder found by findRef.
type ref struct {
	folder bool
	id     string
}

// slashPath is a request's or folder's path joined with "/", the form the
// CLI prints and accepts.
func (w *workspace) slashPath(r ref) string {
	var p string
	if r.folder {
		p = w.folderPath(r.id)
	} else if i := w.find(r.id); i >= 0 {
		p = w.requestPath(w.Requests[i])
	}
	return strings.Join(splitPath(p), "/")
}

// findRef finds a request or folder by ID, by exact path ("Auth/Login",
// case-insensitive), or by a unique part of its path.
func (w *workspace) findRef(s string) (ref, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return ref{}, errors.New("empty reference")
	}
	if w.find(s) >= 0 {
		return ref{id: s}, nil
	}
	if w.findFolder(s) >= 0 {
		return ref{folder: true, id: s}, nil
	}

	var all []ref
	for _, r := range w.Requests {
		all = append(all, ref{id: r.ID})
	}
	for _, f := range w.Folders {
		all = append(all, ref{folder: true, id: f.ID})
	}
	want := strings.ToLower(strings.Join(splitPath(s), "/"))
	var partial []ref
	for _, r := range all {
		p := strings.ToLower(w.slashPath(r))
		if p == want {
			return r, nil
		}
		if strings.Contains(p, want) {
			partial = append(partial, r)
		}
	}
	switch len(partial) {
	case 0:
		return ref{}, fmt.Errorf("nothing matches %q (see `barq ls`)", s)
	case 1:
		return partial[0], nil
	}
	var names []string
	for i, r := range partial {
		if i == 8 {
			names = append(names, fmt.Sprintf("… and %d more", len(partial)-8))
			break
		}
		names = append(names, fmt.Sprintf("  %s  %s", r.id, w.slashPath(r)))
	}
	return ref{}, fmt.Errorf("%q matches %d items; use a longer path or an ID:\n%s", s, len(partial), strings.Join(names, "\n"))
}

func (w *workspace) findRequestRef(s string) (string, error) {
	r, err := w.findRef(s)
	if err == nil && r.folder {
		err = fmt.Errorf("%q is a folder, not a request", s)
	}
	return r.id, err
}

// mkdirAll returns the folder at a slash path, creating missing parts.
func (w *workspace) mkdirAll(path string) string {
	parent := ""
	for _, part := range splitPath(path) {
		parent = ensureFolder(w, parent, part)
	}
	return parent
}

// envByRef finds an environment by ID or name (case-insensitive).
func (w *workspace) envByRef(s string) (*environment, error) {
	for i := range w.Environments {
		if w.Environments[i].ID == s || strings.EqualFold(w.Environments[i].Name, s) {
			return &w.Environments[i], nil
		}
	}
	return nil, fmt.Errorf("no environment %q (see `barq env ls`)", s)
}
