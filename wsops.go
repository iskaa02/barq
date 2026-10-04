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
