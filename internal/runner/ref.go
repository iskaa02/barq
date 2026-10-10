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

	"github.com/iskaa02/barq/internal/httpfile"
)

// Ref names one request in a project.
type Ref struct {
	Path  string // slash-separated, relative to the project root
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
func (r Ref) File(root string) string { return filepath.Join(root, filepath.FromSlash(r.Path)) }

// Dir is the directory of the request's file, where "< file" bodies are read from.
func (r Ref) Dir(root string) string { return filepath.Dir(r.File(root)) }

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

// ListAll returns every request in every .http file under root.
func ListAll(root string) ([]Entry, error) {
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
		es, err := readFile(root, filepath.ToSlash(rel))
		if err == nil {
			out = append(out, es...)
		}
		return nil
	})
	return out, err
}

func readFile(root, rel string) ([]Entry, error) {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	reqs := httpfile.Parse(string(b))
	out := make([]Entry, len(reqs))
	for i, r := range reqs {
		out[i] = Entry{Ref{Path: rel, Name: r.Name, Index: i + 1}, r}
	}
	return out, nil
}

// Find resolves "file.http#name", "file.http#n", "file.http" (its first
// request) or a bare request name that is unique across the project.
func Find(root, ref string) (Ref, httpfile.Request, error) {
	path, sel := ParseRef(ref)
	if sel != "" || strings.HasSuffix(path, ".http") {
		return findInFile(root, filepath.ToSlash(filepath.Clean(path)), sel)
	}
	all, err := ListAll(root)
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

func findInFile(root, path, sel string) (Ref, httpfile.Request, error) {
	es, err := readFile(root, path)
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
