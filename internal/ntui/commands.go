package ntui

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/atotto/clipboard"
	osc52 "github.com/aymanbagabas/go-osc52/v2"
	"github.com/iskaa02/barq/internal/core"
	"github.com/iskaa02/barq/internal/httpfile"
)

const newFileTemplate = `### new request
GET https://example.com
Accept: application/json
`

// curlFor renders a request as a curl command. resolve fills in {{variables}};
// when it reports any missing, the unresolved request is used.
func curlFor(r httpfile.Request, dir string, resolve func(core.Request) (core.Request, []string)) (string, error) {
	body, err := r.ResolveBody(dir)
	if err != nil {
		return "", err
	}
	cr := r.Core()
	cr.Body = body
	if resolve != nil {
		if res, missing := resolve(cr); len(missing) == 0 {
			cr = res
		}
	}
	return core.ToCurl(cr), nil
}

// copyText puts text on the system clipboard, or falls back to OSC 52.
func copyText(text, what string) string {
	if err := clipboard.WriteAll(text); err == nil {
		return "copied " + what
	}
	if _, err := osc52.New(text).WriteTo(os.Stderr); err == nil {
		return "copied " + what + " via terminal (OSC 52)"
	}
	return fmt.Sprintf("could not copy %s", what)
}

// writeNew creates path (and its directories) with content; it fails if the
// file exists.
func writeNew(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = f.WriteString(content)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
