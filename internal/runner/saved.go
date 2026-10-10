package runner

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/iskaa02/barq/internal/core"
	"github.com/iskaa02/barq/internal/httpfile"
)

// Slug makes a file-system friendly name: lower case words joined by "-".
func Slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
	}
	if b.Len() == 0 {
		return "request"
	}
	return b.String()
}

// savedPath is where a legacy saved request goes: requests/<folders>/<name>.http.
func savedPath(root string, ws *core.Workspace, r core.Request) string {
	parts := []string{root, "requests"}
	for _, f := range core.SplitPath(ws.FolderPath(r.Folder)) {
		parts = append(parts, Slug(f))
	}
	name := r.Name
	if name == "" {
		name = r.SuggestedName()
	}
	return filepath.Join(append(parts, Slug(name)+".http")...)
}

// ImportSaved writes the workspace's legacy saved requests out as .http
// files, one per request, leaving existing files alone.
func ImportSaved(root string, ws *core.Workspace) (written, skipped int, err error) {
	written, skipped, _, err = ImportSavedWarn(root, ws)
	return
}

// ImportSavedWarn is ImportSaved that also returns a warning for every
// request whose data (form-data body, disabled params) .http cannot hold;
// that data is written into the block as "##" comments.
//
// Requests whose slug collides get a numeric suffix (users-2.http). A file
// counts as already imported only if it existed before the run and holds a
// block with that request's name.
func ImportSavedWarn(root string, ws *core.Workspace) (written, skipped int, warnings []string, err error) {
	wrote := map[string]bool{}
	for _, r := range ws.Requests {
		hr := httpfile.FromCore(r)
		if hr.Name == "" {
			hr.Name = r.SuggestedName()
		}
		base := savedPath(root, ws, r)
		p, skip := base, false
		for n := 2; ; n++ {
			if wrote[p] {
				// taken by an earlier request of this run
			} else if old, rerr := os.ReadFile(p); rerr != nil {
				break // free
			} else if hasName(string(old), hr.Name) {
				skip = true
				break
			}
			p = strings.TrimSuffix(base, ".http") + "-" + strconv.Itoa(n) + ".http"
		}
		if skip {
			skipped++
			continue
		}
		if err = os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return
		}
		if err = os.WriteFile(p, []byte(httpfile.Format(hr)), 0o644); err != nil {
			return
		}
		wrote[p] = true
		written++
		for _, w := range hr.Warnings {
			warnings = append(warnings, hr.Name+": "+w)
		}
	}
	return
}

func hasName(text, name string) bool {
	for _, q := range httpfile.Parse(text) {
		if q.Name == name {
			return true
		}
	}
	return false
}

// AppendBlocks adds requests to a .http file (created if needed) as
// "### name" blocks, skipping names the file already has.
func AppendBlocks(path string, reqs []httpfile.Request) (added, skipped int, err error) {
	old, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return 0, 0, err
	}
	have := map[string]bool{}
	for _, r := range httpfile.Parse(string(old)) {
		have[r.Name] = true
	}
	var b strings.Builder
	b.Write(old)
	if len(old) > 0 && !strings.HasSuffix(b.String(), "\n") {
		b.WriteString("\n")
	}
	for _, r := range reqs {
		if have[r.Name] {
			skipped++
			continue
		}
		have[r.Name] = true
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		name := r.Name
		r.Name = ""
		b.WriteString("### " + name + "\n" + httpfile.Format(r))
		added++
	}
	if added == 0 {
		return 0, skipped, nil
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	return added, skipped, os.WriteFile(path, []byte(b.String()), 0o644)
}
