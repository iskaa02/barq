package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Workspaces live in ~/.barq/workspaces, one JSON file per working
// directory, so every project gets its own saved requests and open tabs
// without writing anything into the project itself.

type savedHeader struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
}

type request struct {
	ID      string        `json:"id,omitempty"`
	Name    string        `json:"name"`
	Folder  string        `json:"folder,omitempty"` // folder ID, "" for top level
	Method  string        `json:"method"`
	URL     string        `json:"url"`
	Headers []savedHeader `json:"headers,omitempty"`
	Body    string        `json:"body,omitempty"`
	// BodyMode is "" for a raw body, or bodyForm for multipart form-data,
	// whose fields are in Form. Both are kept so switching loses nothing.
	BodyMode string        `json:"body_mode,omitempty"`
	Form     []savedHeader `json:"form,omitempty"`
	// Where an imported request came from, e.g. "openapi:<title>:GET /users",
	// so re-importing skips it.
	Source string `json:"source,omitempty"`
	// Params switched off in the Params tab; enabled ones live in URL.
	DisabledParams []savedHeader `json:"disabled_params,omitempty"`
}

// sameContent reports whether two requests would send the same thing.
func (r request) sameContent(o request) bool {
	return r.Method == o.Method && r.URL == o.URL && r.Body == o.Body &&
		slices.Equal(r.Headers, o.Headers) && slices.Equal(r.DisabledParams, o.DisabledParams) &&
		r.BodyMode == o.BodyMode && slices.Equal(r.Form, o.Form)
}

func (r request) displayName() string {
	if r.Name != "" {
		return r.Name
	}
	return "Untitled"
}

// suggestedName derives a name from the URL: its path, or the host when
// there's no path.
func (r request) suggestedName() string {
	u := normalizeURL(r.URL)
	if _, rest, ok := strings.Cut(u, "://"); ok {
		u = rest
	}
	u, _, _ = strings.Cut(u, "?")
	host, path, _ := strings.Cut(u, "/")
	if path = strings.TrimSuffix(path, "/"); path != "" {
		return "/" + path
	}
	return host
}

type folder struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Parent    string `json:"parent,omitempty"` // "" for top level
	Collapsed bool   `json:"collapsed,omitempty"`
}

type savedTab struct {
	SavedID  string  `json:"saved_id,omitempty"`
	DraftKey string  `json:"draft_key,omitempty"` // history key while unsaved
	Request  request `json:"request"`
}

type workspace struct {
	path string

	CWD       string     `json:"cwd"`
	Folders   []folder   `json:"folders,omitempty"`
	Requests  []request  `json:"requests"`
	Tabs      []savedTab `json:"tabs"`
	ActiveTab int        `json:"active_tab"`
	Recent    []string   `json:"recent,omitempty"` // palette items, most recent first

	Environments []environment `json:"environments,omitempty"`
	ActiveEnv    string        `json:"active_env,omitempty"`
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func workspacePath(cwd string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	slug := strings.Trim(unsafeChars.ReplaceAllString(cwd, "-"), "-")
	if slug == "" {
		slug = "root"
	}
	if len(slug) > 60 {
		slug = slug[len(slug)-60:]
	}
	sum := sha256.Sum256([]byte(cwd))
	name := slug + "-" + hex.EncodeToString(sum[:4]) + ".json"
	return filepath.Join(home, ".barq", "workspaces", name), nil
}

func loadWorkspace(cwd string) (*workspace, error) {
	path, err := workspacePath(cwd)
	if err != nil {
		return nil, err
	}
	ws := &workspace{path: path, CWD: cwd}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ws, nil
	}
	if err != nil {
		return ws, err
	}
	if err := json.Unmarshal(data, ws); err != nil {
		return &workspace{path: path, CWD: cwd}, err
	}
	ws.path, ws.CWD = path, cwd
	ws.repair()
	return ws, nil
}

// repair moves anything whose folder no longer exists, or whose parents
// form a cycle, to the top level so nothing becomes unreachable.
func (w *workspace) repair() {
	for i := range w.Folders {
		if w.findFolder(w.Folders[i].Parent) < 0 {
			w.Folders[i].Parent = ""
		}
	}
	for i := range w.Folders {
		seen := map[string]bool{}
		for p := w.Folders[i].Parent; p != ""; p = w.Folders[w.findFolder(p)].Parent {
			if seen[p] || p == w.Folders[i].ID {
				w.Folders[i].Parent = ""
				break
			}
			seen[p] = true
		}
	}
	for i := range w.Requests {
		if w.findFolder(w.Requests[i].Folder) < 0 {
			w.Requests[i].Folder = ""
		}
	}
}

// findFolder returns the index of folder id, or -1. The top level ("") is
// never found.
func (w *workspace) findFolder(id string) int {
	if id == "" {
		return -1
	}
	return slices.IndexFunc(w.Folders, func(f folder) bool { return f.ID == id })
}

// folderPath is a folder's breadcrumb, e.g. "Users / Admin".
func (w *workspace) folderPath(id string) string {
	var parts []string
	for i := w.findFolder(id); i >= 0 && len(parts) <= len(w.Folders); i = w.findFolder(w.Folders[i].Parent) {
		parts = append([]string{w.Folders[i].Name}, parts...)
	}
	return strings.Join(parts, " / ")
}

// subtree returns id and all folders nested under it.
func (w *workspace) subtree(id string) map[string]bool {
	set := map[string]bool{id: true}
	for changed := true; changed; {
		changed = false
		for _, f := range w.Folders {
			if set[f.Parent] && !set[f.ID] {
				set[f.ID] = true
				changed = true
			}
		}
	}
	return set
}

// countIn counts the requests in a folder, including nested folders.
func (w *workspace) countIn(id string) int {
	set, n := w.subtree(id), 0
	for _, r := range w.Requests {
		if set[r.Folder] {
			n++
		}
	}
	return n
}

// save writes the workspace atomically.
func (w *workspace) save() error {
	if err := os.MkdirAll(filepath.Dir(w.path), 0o700); err != nil {
		return err
	}
	if w.Requests == nil {
		w.Requests = []request{}
	}
	data, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(w.path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), w.path)
}

func (w *workspace) find(id string) int {
	return slices.IndexFunc(w.Requests, func(r request) bool { return r.ID == id })
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
