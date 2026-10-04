package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// runImport implements `barq import`: it adds an OpenAPI spec to the
// workspace of a directory (the current one by default) without opening
// the UI.
func runImport(args []string) int {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	dir := fs.String("dir", ".", "project directory whose workspace receives the import")
	dryRun := fs.Bool("dry-run", false, "show what would be imported without saving")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }

	// Allow flags before or after the spec path.
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var src string
	if fs.NArg() > 0 {
		src = fs.Arg(0)
		if err := fs.Parse(fs.Args()[1:]); err != nil {
			return 2
		}
	}
	if src == "" || fs.NArg() > 0 {
		fs.Usage()
		return 2
	}

	fail := func(format string, a ...any) int {
		fmt.Fprintf(os.Stderr, "barq import: "+format+"\n", a...)
		return 1
	}
	target, err := filepath.Abs(*dir)
	if err != nil {
		return fail("%v", err)
	}
	if st, err := os.Stat(target); err != nil || !st.IsDir() {
		return fail("%s is not a directory", target)
	}

	spec, err := loadSpec(src)
	if err != nil {
		return fail("%v", err)
	}
	ws, err := loadWorkspace(target)
	if err != nil {
		return fail("reading the workspace: %v", err)
	}
	var res importResult
	if *dryRun {
		res, err = importOpenAPI(ws, spec) // in memory only
	} else {
		// Safe while barq is open in that directory: the TUI picks the
		// import up within a second.
		err = ws.mutate(func(w *workspace) (err error) { res, err = importOpenAPI(w, spec); return err })
	}
	if err != nil {
		return fail("%v", err)
	}

	verb := "Imported"
	if *dryRun {
		verb = "Would import"
	}
	fmt.Printf("%s %q into the workspace for %s\n", verb, res.Title, target)
	fmt.Printf("  %s, in %d new folder(s)\n", res.summary(), res.Folders)
	for _, line := range folderCounts(ws, res.FolderID) {
		fmt.Println("    " + line)
	}
	fmt.Printf("  environments: %s\n", strings.Join(res.Envs, ", "))
	if n := len(res.Multipart); n > 0 {
		fmt.Printf("  %d request(s) upload files (form-data); fill in the @ file paths before sending:\n", n)
		for _, k := range res.Multipart {
			fmt.Println("    " + k)
		}
	}
	if *dryRun {
		fmt.Println("Nothing was saved (--dry-run).")
	} else {
		fmt.Printf("Open it with: cd %s && barq\n", shellQuoteIfNeeded(target))
	}
	return 0
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
