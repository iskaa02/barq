package main

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
func parseParams(query string) []headerRow {
	var rows []headerRow
	for _, part := range strings.Split(query, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		rows = append(rows, headerRow{key: decodeParam(k), value: decodeParam(v), enabled: true})
	}
	return rows
}

// buildURL replaces the URL's query with the enabled rows.
func buildURL(raw string, rows []headerRow) string {
	base, _, frag, _ := splitURL(raw)
	var parts []string
	for _, r := range rows {
		if !r.enabled || r.key == "" {
			continue
		}
		p := encodeParam(r.key)
		if r.value != "" {
			p += "=" + encodeParam(r.value)
		}
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return base + frag
	}
	return base + "?" + strings.Join(parts, "&") + frag
}

func disabledRows(rows []headerRow) []headerRow {
	var out []headerRow
	for _, r := range rows {
		if !r.enabled {
			out = append(out, r)
		}
	}
	return out
}

// paramRows is the Params table for a URL plus its disabled params.
func paramRows(rawURL string, disabled []headerRow) []headerRow {
	_, query, _, _ := splitURL(rawURL)
	return append(parseParams(query), disabled...)
}

// syncParamsFromURL refreshes the table after the URL changed.
func (m *model) syncParamsFromURL() {
	m.params.SetRows(paramRows(m.url.Value(), disabledRows(m.params.Rows())))
}

// syncURLFromParams rewrites the URL's query after the table changed.
func (m *model) syncURLFromParams() {
	pos := m.url.Position()
	m.url.SetValue(buildURL(m.url.Value(), m.params.Rows()))
	m.url.SetCursor(pos)
}
