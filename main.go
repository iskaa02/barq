package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/iskaa02/barq/internal/cli"
	"github.com/iskaa02/barq/internal/core"
	"github.com/iskaa02/barq/internal/tui"
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
		}
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

	m := tui.New(ws, loadErr)
	if len(os.Args) > 1 {
		if core.LooksLikeCurl(os.Args[1]) {
			m.ImportCurl(os.Args[1])
		} else {
			m.OpenURL(os.Args[1])
		}
	}
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())

	// Closing the terminal sends SIGHUP; quit cleanly so the state below
	// still gets saved. (SIGINT and SIGTERM are handled by Bubble Tea.)
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		<-hup
		p.Quit()
	}()

	final, err := p.Run()
	if fm, ok := final.(tui.Model); ok {
		fm.Persist()
		fm.Close()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
