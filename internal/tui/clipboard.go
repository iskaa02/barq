package tui

import (
	"os"

	"github.com/atotto/clipboard"
	osc52 "github.com/aymanbagabas/go-osc52/v2"
)

// copyText puts text on the system clipboard. Without a clipboard tool
// (xclip, xsel, wl-clipboard…) it falls back to the OSC 52 escape sequence,
// which many terminals honor, including over SSH. It returns a message
// describing what happened.
func copyText(text, what string) string {
	if err := clipboard.WriteAll(text); err == nil {
		return "copied " + what
	}
	if _, err := osc52.New(text).WriteTo(os.Stderr); err == nil {
		return "copied " + what + " via terminal (OSC 52)"
	}
	return ""
}

func pasteText() (string, error) {
	return clipboard.ReadAll()
}
