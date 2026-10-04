package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
)

const usage = `barq — an API client for the terminal

Usage:
  barq [url | "curl …"]        open the TUI for this directory's workspace
  barq <command> [flags]       do the same from scripts and AI agents

Commands:
  ls, show, new, set, mkdir, mv, rename, rm     requests and folders
  run, history                                  send requests, past runs
  env                                           environments and variables
  curl                                          print a request as curl
  import                                        import an OpenAPI 3 spec
  ai                                            the full guide for AI agents

Workspaces are per directory and stored in ~/.barq/workspaces.
Secret values live in the OS keyring and are never printed.
`

func main() {
	if len(os.Args) > 1 {
		switch arg := os.Args[1]; {
		case isCLICommand(arg):
			os.Exit(cliMain(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
		case arg == "-h" || arg == "--help" || arg == "help":
			fmt.Print(usage)
			return
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	ws, loadErr := loadWorkspace(cwd)
	if ws == nil {
		fmt.Fprintln(os.Stderr, "error:", loadErr)
		os.Exit(1)
	}

	lock, err := lockWorkspace(ws)
	if errors.Is(err, errLocked) {
		fmt.Fprintf(os.Stderr, "barq is already running in %s (%v).\n", cwd, err)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: locking workspace:", err)
		os.Exit(1)
	}
	defer lock.Close()

	m := newModel(ws, loadErr)
	if len(os.Args) > 1 {
		if looksLikeCurl(os.Args[1]) {
			m.importCurl(os.Args[1])
		} else {
			m.openURL(os.Args[1])
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
	if fm, ok := final.(model); ok {
		fm.persist()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
