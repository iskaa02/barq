package ntui

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/iskaa02/barq/internal/core"
	"github.com/iskaa02/barq/internal/httpfile"
)

// ScratchFile is the file `barq <url>` appends new requests to.
const ScratchFile = "scratch.http"

// scratchBlock renders arg, a URL or a curl command, as a .http block and
// returns it with the 0-based line of its request line within the block.
func scratchBlock(arg string) (block string, reqLine int, err error) {
	r := httpfile.Request{Method: "GET", URL: arg, BodyLine: -1}
	if core.LooksLikeCurl(arg) {
		cr, _, err := core.ParseCurl(arg)
		if err != nil {
			return "", 0, fmt.Errorf("curl: %w", err)
		}
		r = fromCurl(cr.Method, cr.URL, cr.Body, cr.Headers, cr.Form)
	}
	name := shortName(r.Method, r.URL)
	body := httpfile.Format(r)
	for i, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, r.Method+" ") {
			reqLine = i + 1
			break
		}
	}
	return "### " + name + "\n" + body, reqLine, nil
}

// fromCurl converts a parsed curl command; -F fields make a multipart body.
func fromCurl(method, url, body string, headers, form []core.HeaderRow) httpfile.Request {
	c := core.Request{Method: method, URL: url, Body: body, Headers: core.ToSavedHeaders(headers)}
	if len(form) > 0 {
		c.BodyMode, c.Form, c.Body = core.BodyForm, core.ToSavedHeaders(form), ""
	}
	return httpfile.FromCore(c)
}

// ScratchWarnings returns what arg holds that a .http block cannot express
// (it is written into the block as "##" comments instead).
func ScratchWarnings(arg string) []string {
	if !core.LooksLikeCurl(arg) {
		return nil
	}
	cr, _, err := core.ParseCurl(arg)
	if err != nil {
		return nil
	}
	return fromCurl(cr.Method, cr.URL, cr.Body, cr.Headers, cr.Form).Warnings
}

// shortName is "METHOD host/path", cut to a reasonable length.
func shortName(method, raw string) string {
	s := raw
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		s = u.Host + u.Path
	}
	if len(s) > 40 {
		s = s[:40]
	}
	return strings.TrimSpace(method + " " + s)
}

// AppendScratch adds a block for arg to scratch.http in root (creating it)
// and returns the file's path and the 1-based line of the new request line.
func AppendScratch(root, arg string) (path string, line int, err error) {
	block, reqLine, err := scratchBlock(arg)
	if err != nil {
		return "", 0, err
	}
	path = filepath.Join(root, ScratchFile)
	old, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", 0, err
	}
	text := string(old)
	if text != "" {
		text = strings.TrimRight(text, "\n") + "\n\n"
	}
	line = strings.Count(text, "\n") + reqLine + 1
	if err := os.WriteFile(path, []byte(text+block), 0o644); err != nil {
		return "", 0, err
	}
	return path, line, nil
}
