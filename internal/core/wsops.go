package core

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Operations on a workspace's content (the legacy saved requests, folders and environments). They
// look everything up by ID, so they work on content freshly reloaded inside
// workspace.mutate.

var ErrNotFound = errors.New("not found")

// Requests ------------------------------------------------------------------

// AddRequest saves a new request, giving it an ID if it has none. A folder
// that no longer exists puts it at the top level.
func (w *Workspace) AddRequest(r Request) Request {
	if r.ID == "" {
		r.ID = NewID()
	}
	if w.FindFolder(r.Folder) < 0 {
		r.Folder = ""
	}
	w.Requests = append(w.Requests, r)
	w.Unfold(r.Folder)
	return r
}

// Folders -------------------------------------------------------------------

func (w *Workspace) SetCollapsed(id string, collapsed bool) {
	if i := w.FindFolder(id); i >= 0 {
		w.Folders[i].Collapsed = collapsed
	}
}

// Unfold expands a folder so something just put in it is visible.
func (w *Workspace) Unfold(id string) { w.SetCollapsed(id, false) }

// Environments ----------------------------------------------------------------

func (w *Workspace) AddEnv(name string, vars []SavedHeader) string {
	env := Environment{ID: NewID(), Name: name, Vars: vars}
	w.Environments = append(w.Environments, env)
	return env.ID
}

func (w *Workspace) Env(id string) (*Environment, error) {
	i := w.FindEnv(id)
	if i < 0 {
		return nil, fmt.Errorf("environment %s: %w", id, ErrNotFound)
	}
	return &w.Environments[i], nil
}

func (w *Workspace) RenameEnv(id, name string) error {
	env, err := w.Env(id)
	if err == nil {
		env.Name = name
	}
	return err
}

func (w *Workspace) DeleteEnv(id string) error {
	i := w.FindEnv(id)
	if i < 0 {
		return fmt.Errorf("environment %s: %w", id, ErrNotFound)
	}
	w.Environments = slices.Delete(w.Environments, i, i+1)
	if w.ActiveEnv == id {
		w.ActiveEnv = ""
	}
	return nil
}

func (w *Workspace) UseEnv(id string) error {
	if id != "" && w.FindEnv(id) < 0 {
		return fmt.Errorf("environment %s: %w", id, ErrNotFound)
	}
	w.ActiveEnv = id
	return nil
}

// SetEnvVar sets one variable, adding it if needed.
func (w *Workspace) SetEnvVar(envID, key, value string) error {
	env, err := w.Env(envID)
	if err != nil {
		return err
	}
	for i := range env.Vars {
		if env.Vars[i].Key == key {
			env.Vars[i].Value, env.Vars[i].Enabled = value, true
			return nil
		}
	}
	env.Vars = append(env.Vars, SavedHeader{Key: key, Value: value, Enabled: true})
	return nil
}

func (w *Workspace) SetEnvVars(envID string, vars []SavedHeader) error {
	env, err := w.Env(envID)
	if err == nil {
		env.Vars = vars
	}
	return err
}

// Paths -------------------------------------------------------------------------

// SplitPath splits "A/B/C" or "A / B / C" into trimmed parts.
func SplitPath(p string) []string {
	var parts []string
	for _, s := range strings.Split(p, "/") {
		if s = strings.TrimSpace(s); s != "" {
			parts = append(parts, s)
		}
	}
	return parts
}

// MkdirAll returns the folder at a slash path, creating missing parts.
func (w *Workspace) MkdirAll(path string) string {
	parent := ""
	for _, part := range SplitPath(path) {
		parent = ensureFolder(w, parent, part)
	}
	return parent
}

// EnvByRef finds an environment by ID or name (case-insensitive).
func (w *Workspace) EnvByRef(s string) (*Environment, error) {
	for i := range w.Environments {
		if w.Environments[i].ID == s || strings.EqualFold(w.Environments[i].Name, s) {
			return &w.Environments[i], nil
		}
	}
	return nil, fmt.Errorf("no environment %q (see `barq env ls`)", s)
}
