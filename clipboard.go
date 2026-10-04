package main

import (
	"os"
	"strings"

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

// shellQuote wraps s in single quotes for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// toCurl renders a request as a curl command: method and URL on the first
// line, then one option per line.
func toCurl(r request) string {
	form := r.BodyMode == bodyForm
	first := "curl"
	if r.Method != "GET" || (!form && r.Body != "") || (form && len(r.Form) > 0) {
		first += " -X " + r.Method
	}
	parts := []string{first + " " + shellQuote(r.URL)}
	for _, h := range r.Headers {
		if !h.Enabled || strings.TrimSpace(h.Key) == "" {
			continue
		}
		// curl writes its own multipart Content-Type, with the boundary.
		if form && strings.EqualFold(strings.TrimSpace(h.Key), "Content-Type") {
			continue
		}
		parts = append(parts, "-H "+shellQuote(h.Key+": "+h.Value))
	}
	switch {
	case form:
		for _, f := range r.Form {
			if !f.Enabled || strings.TrimSpace(f.Key) == "" {
				continue
			}
			// --form-string sends text as-is; -F would treat ; @ and < specially.
			opt := "--form-string "
			if _, isFile := fileValue(f.Value); isFile {
				opt = "-F "
			}
			parts = append(parts, opt+shellQuote(f.Key+"="+f.Value))
		}
	case r.Body != "":
		parts = append(parts, "--data-raw "+shellQuote(r.Body))
	}
	return strings.Join(parts, " \\\n  ")
}
