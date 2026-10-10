package cli

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/iskaa02/barq/internal/core"
	"github.com/iskaa02/barq/internal/httpfile"
	"github.com/iskaa02/barq/internal/runner"
)

// cmdImport writes an OpenAPI spec's operations to .http files in the
// store (see `barq dir`), one file per tag, and adds its servers as environments.
// With --saved it writes the legacy saved requests of the workspace instead.
func cmdImport(c *cli, args []string) error {
	fs := c.flags("import")
	dryRun := fs.Bool("dry-run", false, "show what would be imported without writing")
	saved := fs.Bool("saved", false, "write the workspace's old saved requests out as .http files")
	pos, err := c.parse(fs, args, 0, 1, "<file|url> [--dry-run]  |  --saved")
	if err != nil {
		return err
	}
	if err := c.open(); err != nil {
		return err
	}
	if *saved {
		return c.importSaved(*dryRun)
	}
	if len(pos) != 1 {
		return usagef("usage: barq import <file|url> [--dry-run]  |  barq import --saved")
	}
	spec, err := core.LoadSpec(pos[0])
	if err != nil {
		return err
	}
	// The importer fills a scratch workspace; its requests become files and
	// its environments go to the real workspace.
	envs := make([]core.Environment, len(c.ws.Environments))
	for i, e := range c.ws.Environments {
		e.Vars = append([]core.SavedHeader(nil), e.Vars...) // deep copy: the importer edits these
		envs[i] = e
	}
	scratch := &core.Workspace{CWD: c.ws.CWD, Environments: envs, ActiveEnv: c.ws.ActiveEnv}
	res, err := core.ImportOpenAPI(scratch, spec)
	if err != nil {
		return err
	}
	files := groupByFile(scratch, res)
	var added, skipped int
	names := sortedKeys(files)
	for _, name := range names {
		var reqs []httpfile.Request
		for _, r := range files[name] {
			var files []string
			for i, f := range r.Form {
				if f.Value == "@" {
					r.Form[i].Value = "@./path/to/file"
					files = append(files, f.Key)
				}
			}
			hr := httpfile.FromCore(r)
			if len(files) > 0 {
				hr.Warnings = append(hr.Warnings, "set the file path of: "+strings.Join(files, ", "))
			}
			hr.Name = r.Name
			for _, w := range hr.Warnings {
				fmt.Fprintf(c.err, "warning: %s: %s\n", r.Name, w)
			}
			reqs = append(reqs, hr)
		}
		path := filepath.Join(c.ws.RequestsDir(), name+".http")
		if *dryRun {
			continue
		}
		a, s, err := runner.AppendBlocks(path, reqs)
		if err != nil {
			return err
		}
		added, skipped = added+a, skipped+s
	}
	if !*dryRun {
		err = c.mutate(func(w *core.Workspace) error {
			for _, e := range scratch.Environments {
				if i := w.FindEnv(e.ID); i < 0 {
					w.Environments = append(w.Environments, e)
				} else {
					w.Environments[i].Vars = e.Vars
				}
			}
			w.ActiveEnv = scratch.ActiveEnv
			return nil
		})
		if err != nil {
			return err
		}
	}
	if c.asJSON {
		return c.printJSON(map[string]any{"title": res.Title, "added": added, "skipped": skipped, "files": names,
			"environments": res.Envs, "saved": !*dryRun})
	}
	verb := "Imported"
	if *dryRun {
		verb = "Would import"
	}
	c.printf("%s %q into %s\n", verb, res.Title, c.ws.RequestsDir())
	if !*dryRun {
		c.printf("  %d request(s) written, %d already there\n", added, skipped)
	}
	for _, n := range names {
		c.printf("    %s%s.http  %3d\n", runner.StorePrefix, n, len(files[n]))
	}
	c.printf("  environments: %s\n", strings.Join(res.Envs, ", "))
	if *dryRun {
		c.printf("Nothing was written (--dry-run).\n")
	}
	return nil
}

// groupByFile sorts imported requests into files by tag (the importer's
// subfolder), else by the first segment of their path.
func groupByFile(ws *core.Workspace, res core.ImportResult) map[string][]core.Request {
	files := map[string][]core.Request{}
	for _, r := range ws.Requests {
		parts := core.SplitPath(ws.FolderPath(r.Folder))
		if res.FolderID != "" && len(parts) > 0 {
			parts = parts[1:] // the spec's own folder
		}
		name := ""
		if len(parts) > 0 {
			name = runner.Slug(parts[0])
		} else {
			name = runner.Slug(firstSegment(r.URL, res.Title))
		}
		files[name] = append(files[name], r)
	}
	return files
}

func firstSegment(rawURL, fallback string) string {
	u := strings.TrimPrefix(rawURL, "{{baseUrl}}")
	if p, err := url.Parse(u); err == nil {
		u = p.Path
	}
	if seg := strings.Split(strings.Trim(u, "/"), "/")[0]; seg != "" && !strings.Contains(seg, "{") {
		return seg
	}
	return fallback
}

// importSaved writes the legacy saved requests as .http files.
func (c *cli) importSaved(dryRun bool) error {
	if dryRun {
		c.printf("Would write %d saved request(s) to %s.\n", len(c.ws.Requests), c.ws.RequestsDir())
		return nil
	}
	written, skipped, warnings, err := runner.ImportSavedWarn(c.ws.RequestsDir(), c.ws)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintln(c.err, "warning:", w)
	}
	if c.asJSON {
		return c.printJSON(map[string]any{"written": written, "skipped": skipped})
	}
	c.printf("Wrote %d request(s) to %s (%d already there)\n", written, c.ws.RequestsDir(), skipped)
	return nil
}
