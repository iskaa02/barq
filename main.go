package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	m := newModel()
	if len(os.Args) > 1 {
		if looksLikeCurl(os.Args[1]) {
			m.importCurl(os.Args[1])
		} else {
			m.url.SetValue(os.Args[1])
			m.url.CursorEnd()
		}
	}
	if _, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
