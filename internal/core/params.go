package core

import (
	"net/url"
	"strings"
)

// The Params tab and the URL's query string are two views of the same data.
// Enabled params live in the URL; disabled ones are kept beside it in the
// request (DisabledParams) so they survive round trips.

// splitURL splits a URL into everything before "?", the query, and the
// "#fragment" (with its "#"). hasQuery reports whether there was a "?".
func splitURL(raw string) (base, query, frag string, hasQuery bool) {
	base = raw
	if i := strings.IndexByte(base, '#'); i >= 0 {
		base, frag = base[:i], base[i:]
	}
	base, query, hasQuery = strings.Cut(base, "?")
	return base, query, frag, hasQuery
}

func decodeParam(s string) string {
	if d, err := url.QueryUnescape(s); err == nil {
		return d
	}
	return s
}

// encodeParam query-escapes s but leaves {{variables}} intact, so they can
// still be substituted at send time.
func encodeParam(s string) string {
	var b strings.Builder
	last := 0
	for _, loc := range varPattern.FindAllStringIndex(s, -1) {
		b.WriteString(url.QueryEscape(s[last:loc[0]]))
		b.WriteString(s[loc[0]:loc[1]])
		last = loc[1]
	}
	b.WriteString(url.QueryEscape(s[last:]))
	return b.String()
}

// parseParams reads a query string into rows, in order, keeping repeated
// keys.
func parseParams(query string) []HeaderRow {
	var rows []HeaderRow
	for _, part := range strings.Split(query, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		rows = append(rows, HeaderRow{Key: decodeParam(k), Value: decodeParam(v), Enabled: true})
	}
	return rows
}

// BuildURL replaces the URL's query with the enabled rows.
func BuildURL(raw string, rows []HeaderRow) string {
	base, _, frag, _ := splitURL(raw)
	var parts []string
	for _, r := range rows {
		if !r.Enabled || r.Key == "" {
			continue
		}
		p := encodeParam(r.Key)
		if r.Value != "" {
			p += "=" + encodeParam(r.Value)
		}
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return base + frag
	}
	return base + "?" + strings.Join(parts, "&") + frag
}

func DisabledRows(rows []HeaderRow) []HeaderRow {
	var out []HeaderRow
	for _, r := range rows {
		if !r.Enabled {
			out = append(out, r)
		}
	}
	return out
}

// ParamRows is the Params table for a URL plus its disabled params.
func ParamRows(rawURL string, disabled []HeaderRow) []HeaderRow {
	_, query, _, _ := splitURL(rawURL)
	return append(parseParams(query), disabled...)
}
