package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/iskaa02/barq/internal/cli"
	"github.com/iskaa02/barq/internal/core"
	"github.com/iskaa02/barq/internal/ntui"
)

// version is the module version Go recorded at build time: a tag like
// v0.1.0 or a pseudo-version with `go install …@latest`, and "(devel)"
// for local builds.
func version() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}

func main() {
	if len(os.Args) > 1 {
		switch arg := os.Args[1]; {
		case cli.IsCommand(arg):
			os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
		case arg == "-h" || arg == "--help" || arg == "help":
			fmt.Print(cli.Usage)
			return
		case arg == "-v" || arg == "--version" || arg == "version":
			fmt.Println("barq", version())
			return
		case removedCommands[arg]:
			fmt.Fprintf(os.Stderr, "barq %s was removed: requests are .http files now — edit them directly; see barq ai\n", arg)
			os.Exit(2)
		case !looksLikeRequest(arg):
			fmt.Fprintf(os.Stderr, "barq: unknown command %q\n\n%s", arg, cli.Usage)
			os.Exit(2)
		}
	}

	if err := checkNvim(); err != nil {
		fmt.Fprintln(os.Stderr, "barq:", err)
		os.Exit(1)
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	ws, loadErr := core.LoadWorkspace(cwd)
	if ws == nil {
		fmt.Fprintln(os.Stderr, "error:", loadErr)
		os.Exit(1)
	}

	lock, err := core.LockWorkspace(ws)
	if errors.Is(err, core.ErrLocked) {
		fmt.Fprintf(os.Stderr, "barq is already running in %s (%v).\n", cwd, err)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: locking workspace:", err)
		os.Exit(1)
	}
	defer lock.Close()

	opts := ntui.Options{}
	if len(os.Args) > 1 {
		for _, w := range ntui.ScratchWarnings(os.Args[1]) {
			fmt.Fprintln(os.Stderr, "warning:", w)
		}
		path, line, err := ntui.AppendScratch(ws.RequestsDir(), os.Args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "barq:", err)
			os.Exit(1)
		}
		opts = ntui.Options{File: path, Line: line}
	}
	if err := ntui.Run(ws, cwd, opts); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// removedCommands are old subcommands that now do nothing.
var removedCommands = map[string]bool{"rm": true, "new": true, "set": true, "mkdir": true, "mv": true, "rename": true}

// looksLikeRequest reports whether a first argument is a curl command or a
// URL rather than a mistyped command.
func looksLikeRequest(arg string) bool {
	if core.LooksLikeCurl(arg) || strings.Contains(arg, "://") || strings.HasPrefix(arg, "localhost") {
		return true
	}
	return strings.ContainsAny(arg, ".:/") && !strings.HasPrefix(arg, "-")
}

// nvimVersion matches the first line of `nvim --version`.
var nvimVersion = regexp.MustCompile(`^NVIM v(\d+)\.(\d+)`)

// checkNvim reports why the UI can't run, or nil if Neovim 0.10+ is found.
func checkNvim() error {
	if _, err := exec.LookPath("nvim"); err != nil {
		return errors.New("Neovim 0.10+ is required (nvim not found on PATH)")
	}
	out, err := exec.Command("nvim", "--version").Output()
	if err != nil {
		return fmt.Errorf("Neovim 0.10+ is required (nvim --version failed: %v)", err)
	}
	first, _, _ := strings.Cut(string(out), "\n")
	return checkNvimVersion(first)
}

// checkNvimVersion accepts the first line of `nvim --version` if it is 0.10+.
func checkNvimVersion(line string) error {
	m := nvimVersion.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return fmt.Errorf("Neovim 0.10+ is required (unrecognized version %q)", line)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major == 0 && minor < 10 {
		return fmt.Errorf("Neovim 0.10+ is required (found %s.%s)", m[1], m[2])
	}
	return nil
}
