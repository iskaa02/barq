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
  barq [url | "curl …"]                    open the workspace for this directory
  barq import <file|url> [--dir <dir>] [--dry-run]
                                           import an OpenAPI 3 spec (JSON or YAML)

Workspaces are per directory and stored in ~/.barq/workspaces.
`

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "import":
			os.Exit(runImport(os.Args[2:]))
		case "-h", "--help", "help":
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
