// Package runner is the logic shared by the CLI and the UI for sending one
// request from a .http file: finding it, resolving variables, running it,
// capturing values, checking expectations and recording history.
package runner

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/iskaa02/barq/internal/core"
	"github.com/iskaa02/barq/internal/httpfile"
)

// StorePrefix starts the ref path of every file in the store
// (~/.barq/requests/<project>/). Scans skip dot-dirs, so no project file's
// ref path can start with it.
const StorePrefix = ".barq/"

// Roots are the two places requests live: the project directory (where
// .http files may already be) and the per-project store under ~/.barq.
// It is the only thing that maps between ref paths and absolute paths.
type Roots struct {
	Project string // the project directory (cwd)
	Store   string // ~/.barq/requests/<project>; may not exist yet
}

// RootsOf returns the roots of a workspace.
func RootsOf(ws *core.Workspace) Roots { return Roots{Project: ws.CWD, Store: ws.RequestsDir()} }

// IsStore reports whether a ref path names a store file.
func (r Roots) IsStore(refPath string) bool { return strings.HasPrefix(refPath, StorePrefix) }

// Abs is the absolute path of a ref path.
func (r Roots) Abs(refPath string) string {
	if r.IsStore(refPath) {
		return filepath.Join(r.Store, filepath.FromSlash(strings.TrimPrefix(refPath, StorePrefix)))
	}
	return filepath.Join(r.Project, filepath.FromSlash(refPath))
}

// RefPath is the ref path of an absolute path; ok is false when the path is
// in neither root.
func (r Roots) RefPath(abs string) (string, bool) {
	if rel, ok := within(r.Store, abs); ok {
		return StorePrefix + rel, true
	}
	return within(r.Project, abs)
}

// within is abs relative to dir, slash-separated, when abs is inside dir.
func within(dir, abs string) (string, bool) {
	if dir == "" || abs == "" {
		return "", false
	}
	rel, err := filepath.Rel(dir, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// Ref names one request in a project or its store.
type Ref struct {
	Path  string // slash-separated: relative to the project root, or ".barq/" + relative to the store
	Name  string // may be empty
	Index int    // 1-based position among the file's requests
}

// Key identifies the request in history: "path#name", or "path#n" when unnamed.
func (r Ref) Key() string {
	if r.Name != "" {
		return r.Path + "#" + r.Name
	}
	return r.Path + "#" + strconv.Itoa(r.Index)
}

// File is the absolute path of the request's file.
func (r Ref) File(roots Roots) string { return roots.Abs(r.Path) }

// Entry is a request found in a project.
type Entry struct {
	Ref Ref
	Req httpfile.Request
}

// ParseRef splits "file.http#name" into its file and selector. Without a
// "#" the whole string is the path.
func ParseRef(s string) (path, sel string) {
	path, sel, _ = strings.Cut(s, "#")
	return path, sel
}

// skipDir reports whether a scan stays out of a directory.
func skipDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor"
}

// ListAll returns every request in every .http file in the project and in
// the store. A missing store is empty.
func ListAll(roots Roots) ([]Entry, error) {
	out, err := scan(roots, roots.Project, "")
	if err != nil {
		return out, err
	}
	more, _ := scan(roots, roots.Store, StorePrefix)
	return append(out, more...), nil
}

// scan lists the requests under root; prefix is added to their ref paths.
func scan(roots Roots, root, prefix string) ([]Entry, error) {
	if root == "" {
		return nil, nil
	}
	var out []Entry
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil
		}
		if d.IsDir() {
			if p != root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".http") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		es, err := readFile(roots, prefix+filepath.ToSlash(rel))
		if err == nil {
			out = append(out, es...)
		}
		return nil
	})
	return out, err
}

func readFile(roots Roots, refPath string) ([]Entry, error) {
	b, err := os.ReadFile(roots.Abs(refPath))
	if err != nil {
		return nil, err
	}
	reqs := httpfile.Parse(string(b))
	out := make([]Entry, len(reqs))
	for i, r := range reqs {
		out[i] = Entry{Ref{Path: refPath, Name: r.Name, Index: i + 1}, r}
	}
	return out, nil
}

// Find resolves "file.http#name", "file.http#n", "file.http" (its first
// request; ".barq/…" for store files, or an absolute path) or a bare request
// name that is unique across the project and the store.
func Find(roots Roots, ref string) (Ref, httpfile.Request, error) {
	path, sel := ParseRef(ref)
	if sel != "" || strings.HasSuffix(path, ".http") {
		if filepath.IsAbs(path) {
			if rp, ok := roots.RefPath(path); ok {
				path = rp
			}
		}
		return findInFile(roots, filepath.ToSlash(filepath.Clean(path)), sel)
	}
	all, err := ListAll(roots)
	if err != nil {
		return Ref{}, httpfile.Request{}, err
	}
	var hits []Entry
	for _, e := range all {
		if e.Ref.Name != "" && strings.EqualFold(e.Ref.Name, ref) {
			hits = append(hits, e)
		}
	}
	switch len(hits) {
	case 0:
		return Ref{}, httpfile.Request{}, fmt.Errorf("no request %q (see `barq ls`)", ref)
	case 1:
		return hits[0].Ref, hits[0].Req, nil
	}
	keys := make([]string, len(hits))
	for i, h := range hits {
		keys[i] = h.Ref.Key()
	}
	sort.Strings(keys)
	return Ref{}, httpfile.Request{}, fmt.Errorf("%q matches %d requests: %s", ref, len(hits), strings.Join(keys, ", "))
}

func findInFile(roots Roots, path, sel string) (Ref, httpfile.Request, error) {
	es, err := readFile(roots, path)
	if err != nil {
		return Ref{}, httpfile.Request{}, err
	}
	if len(es) == 0 {
		return Ref{}, httpfile.Request{}, fmt.Errorf("%s has no requests", path)
	}
	if sel == "" {
		return es[0].Ref, es[0].Req, nil
	}
	if n, err := strconv.Atoi(sel); err == nil {
		if n < 1 || n > len(es) {
			return Ref{}, httpfile.Request{}, fmt.Errorf("%s has %d request(s), no #%d", path, len(es), n)
		}
		return es[n-1].Ref, es[n-1].Req, nil
	}
	for _, e := range es {
		if e.Ref.Name == sel {
			return e.Ref, e.Req, nil
		}
	}
	for _, e := range es {
		if strings.EqualFold(e.Ref.Name, sel) {
			return e.Ref, e.Req, nil
		}
	}
	return Ref{}, httpfile.Request{}, fmt.Errorf("%s has no request named %q", path, sel)
}
