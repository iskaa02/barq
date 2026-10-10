package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Workspaces live in ~/.barq/workspaces, one JSON file per working
// directory, so every project gets its own saved requests and open tabs
// without writing anything into the project itself.

type SavedHeader struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
	// Secret marks an environment variable whose value must never be shown
	// to the CLI or written in plain text; nil means "decide by its name".
	Secret *bool `json:"secret,omitempty"`
}

type Request struct {
	ID      string        `json:"id,omitempty"`
	Name    string        `json:"name"`
	Folder  string        `json:"folder,omitempty"` // folder ID, "" for top level
	Method  string        `json:"method"`
	URL     string        `json:"url"`
	Headers []SavedHeader `json:"headers,omitempty"`
	Body    string        `json:"body,omitempty"`
	// BodyMode is "" for a raw body, or bodyForm for multipart form-data,
	// whose fields are in Form. Both are kept so switching loses nothing.
	BodyMode string        `json:"body_mode,omitempty"`
	Form     []SavedHeader `json:"form,omitempty"`
	// Where an imported request came from, e.g. "openapi:<title>:GET /users",
	// so re-importing skips it.
	Source string `json:"source,omitempty"`
	// Captures store parts of each successful response in variables.
	Captures []Capture `json:"captures,omitempty"`
	// Params switched off in the Params tab; enabled ones live in URL.
	DisabledParams []SavedHeader `json:"disabled_params,omitempty"`
}

// SameContent reports whether two requests would send the same thing.
func (r Request) SameContent(o Request) bool {
	return r.Method == o.Method && r.URL == o.URL && r.Body == o.Body &&
		slices.Equal(r.Headers, o.Headers) && slices.Equal(r.DisabledParams, o.DisabledParams) &&
		r.BodyMode == o.BodyMode && slices.Equal(r.Form, o.Form)
}

func (r Request) DisplayName() string {
	if r.Name != "" {
		return r.Name
	}
	return "Untitled"
}

// SuggestedName derives a name from the URL: its path, or the host when
// there's no path.
func (r Request) SuggestedName() string {
	u := NormalizeURL(r.URL)
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

type Folder struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Parent    string `json:"parent,omitempty"` // "" for top level
	Collapsed bool   `json:"collapsed,omitempty"`
}

type SavedTab struct {
	SavedID  string  `json:"saved_id,omitempty"`
	DraftKey string  `json:"draft_key,omitempty"` // history key while unsaved
	Request  Request `json:"request"`
}

// Workspace is one directory's saved requests, folders and environments
// (the content, shared with the CLI) plus the TUI's open tabs (the session).
// They live in separate files so the CLI can edit content while the TUI is
// open without either overwriting the other.
type Workspace struct {
	Path  string    // content file
	stamp fileStamp // content file as last read or written

	keyringCache map[string]string // secret values as last read from or written to the keyring
	SecretErr    error             // set when secrets couldn't go to the keyring

	CWD          string
	Folders      []Folder
	Requests     []Request
	Environments []Environment
	ActiveEnv    string

	Tabs      []SavedTab
	ActiveTab int
	Recent    []string // palette items, most recent first
}

// contentFile is the on-disk shape of the shared content. The session
// fields are only read, to migrate workspaces saved before the split.
type contentFile struct {
	CWD          string        `json:"cwd"`
	Folders      []Folder      `json:"folders,omitempty"`
	Requests     []Request     `json:"requests"`
	Environments []Environment `json:"environments,omitempty"`
	ActiveEnv    string        `json:"active_env,omitempty"`

	LegacyTabs      []SavedTab `json:"tabs,omitempty"`
	LegacyActiveTab int        `json:"active_tab,omitempty"`
	LegacyRecent    []string   `json:"recent,omitempty"`
}

type sessionFile struct {
	Tabs      []SavedTab `json:"tabs"`
	ActiveTab int        `json:"active_tab"`
	Recent    []string   `json:"recent,omitempty"`
}

type fileStamp struct {
	mod  time.Time
	size int64
}

func stampOf(path string) fileStamp {
	st, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{st.ModTime(), st.Size()}
}

func (w *Workspace) sessionPath() string {
	return strings.TrimSuffix(w.Path, ".json") + ".session.json"
}

func (w *Workspace) WriteLockPath() string {
	return strings.TrimSuffix(w.Path, ".json") + ".write.lock"
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// projectName is the "<slug>-<hash>" name that identifies a project
// directory under ~/.barq (its workspace file and its requests folder).
func projectName(cwd string) string {
	slug := strings.Trim(unsafeChars.ReplaceAllString(cwd, "-"), "-")
	if slug == "" {
		slug = "root"
	}
	if len(slug) > 60 {
		slug = slug[len(slug)-60:]
	}
	sum := sha256.Sum256([]byte(cwd))
	return slug + "-" + hex.EncodeToString(sum[:4])
}

func barqDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".barq"), nil
}

func workspacePath(cwd string) (string, error) {
	d, err := barqDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "workspaces", projectName(cwd)+".json"), nil
}

// RequestsDir is where this project's own .http files live:
// ~/.barq/requests/<slug>-<hash>/. It may not exist yet; nothing creates it
// until a request is written.
func (w *Workspace) RequestsDir() string {
	d, err := barqDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "requests", projectName(w.CWD))
}

func LoadWorkspace(cwd string) (*Workspace, error) {
	path, err := workspacePath(cwd)
	if err != nil {
		return nil, err
	}
	ws := &Workspace{Path: path, CWD: cwd}
	legacy, err := ws.readContent()
	if err != nil {
		return ws, err
	}
	data, err := os.ReadFile(ws.sessionPath())
	switch {
	case err == nil:
		var sf sessionFile
		if err := json.Unmarshal(data, &sf); err != nil {
			return ws, err
		}
		ws.Tabs, ws.ActiveTab, ws.Recent = sf.Tabs, sf.ActiveTab, sf.Recent
	case errors.Is(err, os.ErrNotExist):
		// Saved before the content/session split: the session was inside
		// the content file.
		ws.Tabs, ws.ActiveTab, ws.Recent = legacy.LegacyTabs, legacy.LegacyActiveTab, legacy.LegacyRecent
	default:
		return ws, err
	}
	return ws, nil
}

// readContent loads the content file into w, leaving the session alone.
// A missing file means an empty workspace.
func (w *Workspace) readContent() (contentFile, error) {
	var cf contentFile
	data, err := os.ReadFile(w.Path)
	if errors.Is(err, os.ErrNotExist) {
		w.Folders, w.Requests, w.Environments, w.ActiveEnv = nil, nil, nil, ""
		w.stamp = fileStamp{}
		return cf, nil
	}
	if err != nil {
		return cf, err
	}
	if err := json.Unmarshal(data, &cf); err != nil {
		return cf, fmt.Errorf("reading %s: %w", w.Path, err)
	}
	w.Folders, w.Requests, w.Environments, w.ActiveEnv = cf.Folders, cf.Requests, cf.Environments, cf.ActiveEnv
	w.stamp = stampOf(w.Path)
	w.repair()
	w.loadSecrets()
	return cf, nil
}

// ChangedOnDisk reports whether someone else (the CLI) wrote the content
// since w last read or wrote it.
func (w *Workspace) ChangedOnDisk() bool {
	return stampOf(w.Path) != w.stamp
}

// ReloadContent re-reads the content written by someone else.
func (w *Workspace) ReloadContent() error {
	_, err := w.readContent()
	return err
}

// Mutate applies a change to the content safely against other writers: it
// takes the write lock, reloads the latest content, applies fn and writes
// it back. fn must find things by ID, as positions can change on reload.
func (w *Workspace) Mutate(fn func(*Workspace) error) error {
	return WithWriteLock(w.WriteLockPath(), func() error {
		if err := w.ReloadContent(); err != nil {
			return err
		}
		if err := fn(w); err != nil {
			return err
		}
		return w.saveContent()
	})
}

// repair moves anything whose folder no longer exists, or whose parents
// form a cycle, to the top level so nothing becomes unreachable.
func (w *Workspace) repair() {
	for i := range w.Folders {
		if w.FindFolder(w.Folders[i].Parent) < 0 {
			w.Folders[i].Parent = ""
		}
	}
	for i := range w.Folders {
		seen := map[string]bool{}
		for p := w.Folders[i].Parent; p != ""; p = w.Folders[w.FindFolder(p)].Parent {
			if seen[p] || p == w.Folders[i].ID {
				w.Folders[i].Parent = ""
				break
			}
			seen[p] = true
		}
	}
	for i := range w.Requests {
		if w.FindFolder(w.Requests[i].Folder) < 0 {
			w.Requests[i].Folder = ""
		}
	}
}

// FindFolder returns the index of folder id, or -1. The top level ("") is
// never found.
func (w *Workspace) FindFolder(id string) int {
	if id == "" {
		return -1
	}
	return slices.IndexFunc(w.Folders, func(f Folder) bool { return f.ID == id })
}

// FolderPath is a folder's breadcrumb, e.g. "Users / Admin".
func (w *Workspace) FolderPath(id string) string {
	var parts []string
	for i := w.FindFolder(id); i >= 0 && len(parts) <= len(w.Folders); i = w.FindFolder(w.Folders[i].Parent) {
		parts = append([]string{w.Folders[i].Name}, parts...)
	}
	return strings.Join(parts, " / ")
}

// Subtree returns id and all folders nested under it.
func (w *Workspace) Subtree(id string) map[string]bool {
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

// CountIn counts the requests in a folder, including nested folders.
func (w *Workspace) CountIn(id string) int {
	set, n := w.Subtree(id), 0
	for _, r := range w.Requests {
		if set[r.Folder] {
			n++
		}
	}
	return n
}

// save writes both the content and the session. Content writes from a
// running program should go through mutate instead.
func (w *Workspace) save() error {
	if err := w.saveContent(); err != nil {
		return err
	}
	return w.SaveSession()
}

func (w *Workspace) saveContent() error {
	if w.Requests == nil {
		w.Requests = []Request{}
	}
	envs, err := w.storeSecrets()
	w.SecretErr = err
	cf := contentFile{CWD: w.CWD, Folders: w.Folders, Requests: w.Requests,
		Environments: envs, ActiveEnv: w.ActiveEnv}
	if err := writeJSONFile(w.Path, cf); err != nil {
		return err
	}
	w.stamp = stampOf(w.Path)
	return nil
}

// SaveSession writes the TUI's tabs and recents.
func (w *Workspace) SaveSession() error {
	return writeJSONFile(w.sessionPath(), sessionFile{Tabs: w.Tabs, ActiveTab: w.ActiveTab, Recent: w.Recent})
}

// writeJSONFile writes v atomically, readable only by the user.
func writeJSONFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
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
	return os.Rename(tmp.Name(), path)
}

func (w *Workspace) Find(id string) int {
	return slices.IndexFunc(w.Requests, func(r Request) bool { return r.ID == id })
}

func NewID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
