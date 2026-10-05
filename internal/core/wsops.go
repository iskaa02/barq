package core

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Operations on a workspace's content, shared by the TUI and the CLI. They
// look everything up by ID, so they work on content freshly reloaded inside
// workspace.mutate.

var ErrNotFound = errors.New("not found")

// ApplyEdits copies what the request editors change onto r, leaving its
// identity (ID, name, folder, source) alone.
func (r *Request) ApplyEdits(e Request) {
	r.Method, r.URL, r.Headers, r.Body = e.Method, e.URL, e.Headers, e.Body
	r.BodyMode, r.Form, r.DisabledParams = e.BodyMode, e.Form, e.DisabledParams
}

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

func (w *Workspace) UpdateRequest(id string, fn func(*Request)) error {
	i := w.Find(id)
	if i < 0 {
		return fmt.Errorf("request %s: %w", id, ErrNotFound)
	}
	fn(&w.Requests[i])
	return nil
}

func (w *Workspace) DeleteRequest(id string) error {
	i := w.Find(id)
	if i < 0 {
		return fmt.Errorf("request %s: %w", id, ErrNotFound)
	}
	w.Requests = slices.Delete(w.Requests, i, i+1)
	return nil
}

func (w *Workspace) MoveRequest(id, folderID string) error {
	if folderID != "" && w.FindFolder(folderID) < 0 {
		return fmt.Errorf("folder %s: %w", folderID, ErrNotFound)
	}
	if err := w.UpdateRequest(id, func(r *Request) { r.Folder = folderID }); err != nil {
		return err
	}
	w.Unfold(folderID)
	return nil
}

// Folders -------------------------------------------------------------------

func (w *Workspace) CreateFolder(parent, name string) (string, error) {
	if parent != "" && w.FindFolder(parent) < 0 {
		return "", fmt.Errorf("folder %s: %w", parent, ErrNotFound)
	}
	f := Folder{ID: NewID(), Name: name, Parent: parent}
	w.Folders = append(w.Folders, f)
	w.Unfold(parent)
	return f.ID, nil
}

func (w *Workspace) RenameFolder(id, name string) error {
	i := w.FindFolder(id)
	if i < 0 {
		return fmt.Errorf("folder %s: %w", id, ErrNotFound)
	}
	w.Folders[i].Name = name
	return nil
}

func (w *Workspace) MoveFolder(id, parent string) error {
	i := w.FindFolder(id)
	if i < 0 {
		return fmt.Errorf("folder %s: %w", id, ErrNotFound)
	}
	if parent != "" && w.FindFolder(parent) < 0 {
		return fmt.Errorf("folder %s: %w", parent, ErrNotFound)
	}
	if w.Subtree(id)[parent] {
		return errors.New("can't move a folder into itself")
	}
	w.Folders[i].Parent = parent
	w.Unfold(parent)
	return nil
}

// DeleteFolder removes a folder with everything in it and returns the IDs
// of the requests that went with it.
func (w *Workspace) DeleteFolder(id string) ([]string, error) {
	if w.FindFolder(id) < 0 {
		return nil, fmt.Errorf("folder %s: %w", id, ErrNotFound)
	}
	gone := w.Subtree(id)
	var deleted []string
	w.Requests = slices.DeleteFunc(w.Requests, func(r Request) bool {
		if gone[r.Folder] {
			deleted = append(deleted, r.ID)
			return true
		}
		return false
	})
	w.Folders = slices.DeleteFunc(w.Folders, func(f Folder) bool { return gone[f.ID] })
	return deleted, nil
}

func (w *Workspace) SetCollapsed(id string, collapsed bool) {
	if i := w.FindFolder(id); i >= 0 {
		w.Folders[i].Collapsed = collapsed
	}
}

func (w *Workspace) SetAllCollapsed(collapsed bool) {
	for i := range w.Folders {
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

// requestPath is a request's full path, e.g. "Auth / Logs in a user".
func (w *Workspace) requestPath(r Request) string {
	if p := w.FolderPath(r.Folder); p != "" {
		return p + " / " + r.DisplayName()
	}
	return r.DisplayName()
}

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

// Refs -------------------------------------------------------------------------

// Ref is a request or folder found by findRef.
type Ref struct {
	Folder bool
	ID     string
}

// SlashPath is a request's or folder's path joined with "/", the form the
// CLI prints and accepts.
func (w *Workspace) SlashPath(r Ref) string {
	var p string
	if r.Folder {
		p = w.FolderPath(r.ID)
	} else if i := w.Find(r.ID); i >= 0 {
		p = w.requestPath(w.Requests[i])
	}
	return strings.Join(SplitPath(p), "/")
}

// FindRef finds a request or folder by ID, by exact path ("Auth/Login",
// case-insensitive), or by a unique part of its path.
func (w *Workspace) FindRef(s string) (Ref, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Ref{}, errors.New("empty reference")
	}
	if w.Find(s) >= 0 {
		return Ref{ID: s}, nil
	}
	if w.FindFolder(s) >= 0 {
		return Ref{Folder: true, ID: s}, nil
	}

	var all []Ref
	for _, r := range w.Requests {
		all = append(all, Ref{ID: r.ID})
	}
	for _, f := range w.Folders {
		all = append(all, Ref{Folder: true, ID: f.ID})
	}
	want := strings.ToLower(strings.Join(SplitPath(s), "/"))
	var partial []Ref
	for _, r := range all {
		p := strings.ToLower(w.SlashPath(r))
		if p == want {
			return r, nil
		}
		if strings.Contains(p, want) {
			partial = append(partial, r)
		}
	}
	switch len(partial) {
	case 0:
		return Ref{}, fmt.Errorf("nothing matches %q (see `barq ls`)", s)
	case 1:
		return partial[0], nil
	}
	var names []string
	for i, r := range partial {
		if i == 8 {
			names = append(names, fmt.Sprintf("… and %d more", len(partial)-8))
			break
		}
		names = append(names, fmt.Sprintf("  %s  %s", r.ID, w.SlashPath(r)))
	}
	return Ref{}, fmt.Errorf("%q matches %d items; use a longer path or an ID:\n%s", s, len(partial), strings.Join(names, "\n"))
}

func (w *Workspace) FindRequestRef(s string) (string, error) {
	r, err := w.FindRef(s)
	if err == nil && r.Folder {
		err = fmt.Errorf("%q is a folder, not a request", s)
	}
	return r.ID, err
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
