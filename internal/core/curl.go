package core

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// ShellQuote wraps s in single quotes for a POSIX shell.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ToCurl renders a request as a curl command: method and URL on the first
// line, then one option per line.
func ToCurl(r Request) string {
	form := r.BodyMode == BodyForm
	first := "curl"
	if r.Method != "GET" || (!form && r.Body != "") || (form && len(r.Form) > 0) {
		first += " -X " + r.Method
	}
	parts := []string{first + " " + ShellQuote(r.URL)}
	for _, h := range r.Headers {
		if !h.Enabled || strings.TrimSpace(h.Key) == "" {
			continue
		}
		// curl writes its own multipart Content-Type, with the boundary.
		if form && strings.EqualFold(strings.TrimSpace(h.Key), "Content-Type") {
			continue
		}
		parts = append(parts, "-H "+ShellQuote(h.Key+": "+h.Value))
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
			parts = append(parts, opt+ShellQuote(f.Key+"="+f.Value))
		}
	case r.Body != "":
		parts = append(parts, "--data-raw "+ShellQuote(r.Body))
	}
	return strings.Join(parts, " \\\n  ")
}

type curlRequest struct {
	Method  string
	URL     string
	Headers []HeaderRow
	Body    string
	Form    []HeaderRow // multipart fields from -F; "@path" values are files
}

func LooksLikeCurl(s string) bool {
	s = strings.TrimSpace(s)
	return s == "curl" || strings.HasPrefix(s, "curl ") || strings.HasPrefix(s, "curl\t") ||
		strings.HasPrefix(s, "curl\\")
}

// ShellSplit splits a command line the way a POSIX shell would, supporting
// '...', "...", $'...' and backslash escapes. A backslash before whitespace
// (a line continuation, possibly with the newline already flattened to a
// space) is treated as plain whitespace.
func ShellSplit(s string) ([]string, error) {
	var (
		args    []string
		cur     strings.Builder
		inToken bool
	)
	flush := func() {
		if inToken {
			args = append(args, cur.String())
			cur.Reset()
			inToken = false
		}
	}
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		case c == '\\':
			if i+1 >= len(r) {
				continue
			}
			i++
			if n := r[i]; n == ' ' || n == '\t' || n == '\n' || n == '\r' {
				flush()
				continue
			}
			cur.WriteRune(r[i])
			inToken = true
		case c == '\'':
			inToken = true
			j := i + 1
			for j < len(r) && r[j] != '\'' {
				j++
			}
			if j >= len(r) {
				return nil, errors.New("unterminated ' quote")
			}
			cur.WriteString(string(r[i+1 : j]))
			i = j
		case c == '$' && i+1 < len(r) && r[i+1] == '\'':
			inToken = true
			j := i + 2
			for ; j < len(r) && r[j] != '\''; j++ {
				if r[j] == '\\' && j+1 < len(r) {
					j++
					switch r[j] {
					case 'n':
						cur.WriteRune('\n')
					case 't':
						cur.WriteRune('\t')
					case 'r':
						cur.WriteRune('\r')
					default:
						cur.WriteRune(r[j])
					}
					continue
				}
				cur.WriteRune(r[j])
			}
			if j >= len(r) {
				return nil, errors.New("unterminated $' quote")
			}
			i = j
		case c == '"':
			inToken = true
			j := i + 1
			for ; j < len(r) && r[j] != '"'; j++ {
				if r[j] == '\\' && j+1 < len(r) && strings.ContainsRune("\"\\$`\n", r[j+1]) {
					j++
					if r[j] != '\n' {
						cur.WriteRune(r[j])
					}
					continue
				}
				cur.WriteRune(r[j])
			}
			if j >= len(r) {
				return nil, errors.New(`unterminated " quote`)
			}
			i = j
		default:
			cur.WriteRune(c)
			inToken = true
		}
	}
	flush()
	return args, nil
}

// Short options mapped to the long names handled below.
var curlShort = map[byte]string{
	'X': "request", 'H': "header", 'd': "data", 'u': "user", 'A': "user-agent",
	'e': "referer", 'b': "cookie", 'F': "form", 'G': "get", 'I': "head",
}

// Short options that take an argument but don't affect the request.
const curlShortSkipArg = "omwxTrcEKCDYyzUPQt"

// Long options that take an argument. Anything not listed here is treated
// as a flag.
var curlLongArg = map[string]bool{
	"request": true, "url": true, "header": true, "data": true, "data-raw": true,
	"data-binary": true, "data-ascii": true, "data-urlencode": true, "json": true,
	"user": true, "user-agent": true, "referer": true, "cookie": true, "form": true,
	"form-string": true, "oauth2-bearer": true, "url-query": true,
	"output": true, "max-time": true, "connect-timeout": true, "write-out": true,
	"proxy": true, "proxy-user": true, "cacert": true, "capath": true, "cert": true,
	"key": true, "cert-type": true, "key-type": true, "retry": true,
	"retry-delay": true, "retry-max-time": true, "range": true, "cookie-jar": true,
	"limit-rate": true, "resolve": true, "connect-to": true, "interface": true,
	"dns-servers": true, "upload-file": true, "config": true, "max-redirs": true,
	"unix-socket": true, "abstract-unix-socket": true, "aws-sigv4": true,
	"trace": true, "trace-ascii": true, "stderr": true, "local-port": true,
	"keepalive-time": true, "expect100-timeout": true, "ciphers": true,
	"pinnedpubkey": true, "tls-max": true, "dump-header": true, "time-cond": true,
}

// ParseCurl turns a curl command line into a request. Warnings describe
// options that were understood but can't be represented.
func ParseCurl(cmd string) (curlRequest, []string, error) {
	var req curlRequest
	args, err := ShellSplit(cmd)
	if err != nil {
		return req, nil, err
	}
	if len(args) == 0 || args[0] != "curl" {
		return req, nil, errors.New("not a curl command")
	}

	type opt struct{ name, val string }
	var opts []opt
	for i := 1; i < len(args); i++ {
		a := args[i]
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch {
		case a == "--":
			for _, p := range args[i+1:] {
				opts = append(opts, opt{"url", p})
			}
			i = len(args)
		case strings.HasPrefix(a, "--"):
			name, val, hasVal := strings.Cut(a[2:], "=")
			if curlLongArg[name] && !hasVal {
				val = next()
			}
			opts = append(opts, opt{name, val})
		case strings.HasPrefix(a, "-") && len(a) > 1:
			// Bundled short flags, e.g. -sSL, -XPOST, -sX POST.
			for j := 1; j < len(a); j++ {
				c := a[j]
				long, known := curlShort[c]
				takesArg := (known && long != "get" && long != "head") ||
					strings.IndexByte(curlShortSkipArg, c) >= 0
				if !takesArg {
					if known {
						opts = append(opts, opt{long, ""})
					}
					continue
				}
				val := a[j+1:]
				if val == "" {
					val = next()
				}
				if known {
					opts = append(opts, opt{long, val})
				}
				break
			}
		default:
			opts = append(opts, opt{"url", a})
		}
	}

	var (
		data     []string
		getQuery bool
		isJSON   bool
		warnings []string
		query    []string
	)
	addHeader := func(k, v string) {
		req.Headers = append(req.Headers, HeaderRow{Key: k, Value: v, Enabled: true})
	}
	hasHeader := func(k string) bool {
		for _, h := range req.Headers {
			if strings.EqualFold(h.Key, k) {
				return true
			}
		}
		return false
	}
	readData := func(v string) string {
		if strings.HasPrefix(v, "@") {
			b, err := os.ReadFile(v[1:])
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("couldn't read %s", v[1:]))
				return ""
			}
			return string(b)
		}
		return v
	}

	for _, o := range opts {
		switch o.name {
		case "url":
			if req.URL == "" {
				req.URL = o.val
			}
		case "request":
			req.Method = strings.ToUpper(o.val)
		case "header":
			k, v, ok := strings.Cut(o.val, ":")
			if ok && strings.TrimSpace(k) != "" {
				addHeader(strings.TrimSpace(k), strings.TrimSpace(v))
			}
		case "data", "data-ascii", "data-binary":
			data = append(data, readData(o.val))
		case "data-raw":
			data = append(data, o.val)
		case "data-urlencode":
			if name, val, ok := strings.Cut(o.val, "="); ok {
				data = append(data, name+"="+url.QueryEscape(val))
			} else {
				data = append(data, url.QueryEscape(o.val))
			}
		case "json":
			data = append(data, readData(o.val))
			isJSON = true
		case "user":
			addHeader("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(o.val)))
		case "oauth2-bearer":
			addHeader("Authorization", "Bearer "+o.val)
		case "user-agent":
			addHeader("User-Agent", o.val)
		case "referer":
			addHeader("Referer", o.val)
		case "cookie":
			if strings.Contains(o.val, "=") {
				addHeader("Cookie", o.val)
			}
		case "url-query":
			query = append(query, o.val)
		case "get":
			getQuery = true
		case "head":
			req.Method = "HEAD"
		case "form", "form-string":
			name, val, ok := strings.Cut(o.val, "=")
			if !ok || name == "" {
				warnings = append(warnings, fmt.Sprintf("skipped -F %q: expected name=value", o.val))
				continue
			}
			switch {
			case o.name == "form-string" && strings.HasPrefix(val, "@"):
				warnings = append(warnings, fmt.Sprintf("--form-string %s: barq treats values starting with @ as files", name))
			case o.name == "form" && strings.HasPrefix(val, "@"):
				// Drop curl's ;type= and ;filename= options; barq picks
				// the type from the file.
				path, _, _ := strings.Cut(val[1:], ";")
				val = "@" + path
			case o.name == "form" && strings.HasPrefix(val, "<"):
				warnings = append(warnings, fmt.Sprintf("-F %s=<file (a file's content as text) isn't supported; kept as text", name))
			}
			req.Form = append(req.Form, HeaderRow{Key: name, Value: val, Enabled: true})
		}
	}

	if req.URL == "" {
		return req, warnings, errors.New("no URL in curl command")
	}
	if getQuery {
		query = append(query, data...)
		data = nil
	}
	if len(query) > 0 {
		sep := "?"
		if strings.Contains(req.URL, "?") {
			sep = "&"
		}
		req.URL += sep + strings.Join(query, "&")
	}

	if len(req.Form) > 0 && req.Method == "" {
		req.Method = "POST"
	}
	if len(data) > 0 {
		sepr := "&"
		if isJSON {
			sepr = ""
		}
		req.Body = strings.Join(data, sepr)
		if req.Method == "" {
			req.Method = "POST"
		}
		if isJSON {
			if !hasHeader("Content-Type") {
				addHeader("Content-Type", "application/json")
			}
			if !hasHeader("Accept") {
				addHeader("Accept", "application/json")
			}
		} else if !hasHeader("Content-Type") {
			addHeader("Content-Type", "application/x-www-form-urlencoded")
		}
	}
	if req.Method == "" {
		req.Method = "GET"
	}
	return req, warnings, nil
}
