package main

import (
	"fmt"
	"sort"
	"strings"
)

// cmdImport adds an OpenAPI spec to a workspace. It's safe while the TUI
// is open there: the TUI picks the import up within a second.
func cmdImport(c *cli, args []string) error {
	fs := c.flags("import")
	dryRun := fs.Bool("dry-run", false, "show what would be imported without saving")
	pos, err := c.parse(fs, args, 1, 1, "<file|url> [--dir <dir>] [--dry-run]")
	if err != nil {
		return err
	}
	if err := c.open(); err != nil {
		return err
	}
	spec, err := loadSpec(pos[0])
	if err != nil {
		return err
	}
	var res importResult
	if *dryRun {
		res, err = importOpenAPI(c.ws, spec) // in memory only
	} else {
		err = c.mutate(func(w *workspace) (err error) { res, err = importOpenAPI(w, spec); return err })
	}
	if err != nil {
		return err
	}
	if c.asJSON {
		return c.printJSON(map[string]any{"title": res.Title, "added": res.Added, "skipped": res.Skipped,
			"new_folders": res.Folders, "environments": res.Envs, "uploads": res.Multipart, "saved": !*dryRun})
	}

	verb := "Imported"
	if *dryRun {
		verb = "Would import"
	}
	c.printf("%s %q into the workspace for %s\n", verb, res.Title, c.ws.CWD)
	c.printf("  %s, in %d new folder(s)\n", res.summary(), res.Folders)
	for _, line := range folderCounts(c.ws, res.FolderID) {
		c.printf("    %s\n", line)
	}
	c.printf("  environments: %s\n", strings.Join(res.Envs, ", "))
	if n := len(res.Multipart); n > 0 {
		c.printf("  %d request(s) upload files (form-data); fill in the @ file paths before sending:\n", n)
		for _, k := range res.Multipart {
			c.printf("    %s\n", k)
		}
	}
	if *dryRun {
		c.printf("Nothing was saved (--dry-run).\n")
	} else {
		c.printf("Open it with: cd %s && barq\n", shellQuoteIfNeeded(c.ws.CWD))
	}
	return nil
}

// folderCounts lists the direct subfolders of a folder with how many
// requests each holds.
func folderCounts(ws *workspace, parent string) []string {
	type row struct {
		name string
		n    int
	}
	var rows []row
	for _, f := range ws.Folders {
		if f.Parent == parent {
			rows = append(rows, row{f.Name, ws.countIn(f.ID)})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = fmt.Sprintf("%-32s %3d", r.name, r.n)
	}
	return out
}

func shellQuoteIfNeeded(s string) string {
	if strings.ContainsAny(s, " '\"$`\\") {
		return shellQuote(s)
	}
	return s
}
