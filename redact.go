package main

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Redaction for CLI output, which is meant to be safe to show an AI agent.
// Three layers: secret variable values wherever they appear, credential
// headers, and credential-like fields in bodies and query strings. The TUI
// shows real values: it's for the human.

const redacted = "«redacted»"

// isRedacted reports whether s contains a redaction marker, including the
// «secret:name» markers history stores.
func isRedacted(s string) bool {
	return strings.Contains(s, "«redacted") || strings.Contains(s, "«secret:")
}

type redactor struct{ secrets []secretValue }

func newRedactor(w *workspace) redactor { return redactor{secrets: w.secretValues()} }

// text replaces secret values.
func (rd redactor) text(s string) string {
	for _, sv := range rd.secrets {
		s = strings.ReplaceAll(s, sv.value, "«redacted:"+sv.name+"»")
	}
	return s
}

var authScheme = regexp.MustCompile(`(?i)^\s*(bearer|basic|token|digest)?\s*$`)

// safeTemplate reports whether a value only refers to variables, maybe
// after an auth scheme, as in "Bearer {{token}}": it reveals nothing.
func safeTemplate(v string) bool {
	return varPattern.MatchString(v) && authScheme.MatchString(varPattern.ReplaceAllString(v, ""))
}

// field redacts a header, form field or parameter value. In templates,
// values that only reference variables are kept.
func (rd redactor) field(name, value string, template bool) string {
	if template && safeTemplate(value) {
		return value
	}
	if sensitiveName.MatchString(name) && value != "" {
		return redacted
	}
	return rd.text(value)
}

func (rd redactor) fields(hs []savedHeader, template bool) []savedHeader {
	out := make([]savedHeader, len(hs))
	for i, h := range hs {
		out[i] = h
		out[i].Value = rd.field(h.Key, h.Value, template)
	}
	return out
}

func (rd redactor) headers(h http.Header) http.Header {
	out := http.Header{}
	for k, vs := range h {
		for _, v := range vs {
			out.Add(k, rd.field(k, v, false))
		}
	}
	return out
}

// url redacts credential-like query parameters.
func (rd redactor) url(u string, template bool) string {
	base, query, frag, ok := splitURL(u)
	if !ok {
		return rd.text(u)
	}
	rows := parseParams(query)
	for i := range rows {
		if template && safeTemplate(rows[i].value) {
			continue
		}
		if sensitiveName.MatchString(rows[i].key) && rows[i].value != "" {
			rows[i].value = redacted
		}
	}
	return rd.text(readableMarkers(buildURL(base+frag, rows)))
}

// readableMarkers undoes the query-escaping of markers, for display.
func readableMarkers(s string) string {
	return strings.ReplaceAll(s, url.QueryEscape(redacted), redacted)
}

// body redacts a request or response body: credential-like JSON fields or
// form fields, then any secret values left.
func (rd redactor) body(b string, contentType string, template bool) string {
	trimmed := strings.TrimSpace(b)
	switch {
	case strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "["):
		if v, err := decodeOrderedJSON([]byte(trimmed)); err == nil {
			var sb strings.Builder
			writeJSON(&sb, rd.jsonValue(v, false, template), "")
			return rd.text(sb.String())
		}
	case strings.Contains(contentType, "x-www-form-urlencoded"):
		rows := parseParams(trimmed)
		for i := range rows {
			rows[i].value = rd.field(rows[i].key, rows[i].value, template)
		}
		return readableMarkers(strings.TrimPrefix(buildURL("", rows), "?"))
	}
	return rd.text(b)
}

// jsonValue redacts strings and numbers under credential-like keys.
func (rd redactor) jsonValue(v any, sensitive, template bool) any {
	switch x := v.(type) {
	case *omap:
		out := newOmap()
		for _, k := range x.keys {
			out.set(k, rd.jsonValue(x.vals[k], sensitive || sensitiveName.MatchString(k), template))
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = rd.jsonValue(e, sensitive, template)
		}
		return out
	case string:
		if sensitive && x != "" && !(template && safeTemplate(x)) {
			return redacted
		}
		return x
	case nil, bool:
		return x
	}
	if sensitive {
		return redacted // numbers: PINs, codes
	}
	return v
}

// request redacts a request for display. template is true for saved
// requests (with {{variables}}), false for what was actually sent.
func (rd redactor) request(r request, template bool) request {
	ct := ""
	for _, h := range r.Headers {
		if strings.EqualFold(h.Key, "Content-Type") {
			ct = h.Value
		}
	}
	r.URL = rd.url(r.URL, template)
	r.Headers = rd.fields(r.Headers, template)
	r.Form = rd.fields(r.Form, template)
	r.DisabledParams = rd.fields(r.DisabledParams, template)
	r.Body = rd.body(r.Body, ct, template)
	return r
}

// Write-back ----------------------------------------------------------------------

// unredactBody restores redacted JSON fields from the old body, so an agent
// that edits a body it was shown can't overwrite real values with markers.
// Non-JSON bodies with markers are rejected.
func unredactBody(newBody, oldBody string) (string, error) {
	if !isRedacted(newBody) {
		return newBody, nil
	}
	nv, err := decodeOrderedJSON([]byte(strings.TrimSpace(newBody)))
	if err != nil {
		return "", errRedactedWrite
	}
	ov, _ := decodeOrderedJSON([]byte(strings.TrimSpace(oldBody)))
	merged, ok := mergeRedacted(nv, ov)
	if !ok {
		return "", errRedactedWrite
	}
	var sb strings.Builder
	writeJSON(&sb, merged, "")
	return sb.String(), nil
}

var errRedactedWrite = errors.New("the new value contains «redacted» markers that don't match the saved request; " +
	"leave redacted fields out or set real values")

// mergeRedacted replaces marker strings in n with the value at the same
// place in o. It fails if there's nothing to restore from.
func mergeRedacted(n, o any) (any, bool) {
	switch x := n.(type) {
	case string:
		if isRedacted(x) {
			if o == nil {
				return nil, false
			}
			return o, true
		}
		return x, true
	case *omap:
		om, _ := o.(*omap)
		out := newOmap()
		for _, k := range x.keys {
			v, ok := mergeRedacted(x.vals[k], om.get(k))
			if !ok {
				return nil, false
			}
			out.set(k, v)
		}
		return out, true
	case []any:
		oa, _ := o.([]any)
		out := make([]any, len(x))
		for i, e := range x {
			var oe any
			if i < len(oa) {
				oe = oa[i]
			}
			v, ok := mergeRedacted(e, oe)
			if !ok {
				return nil, false
			}
			out[i] = v
		}
		return out, true
	}
	return n, true
}

// unredactField keeps the old value when the new one is a marker.
func unredactField(newVal, oldVal string, hadOld bool) (string, error) {
	if !isRedacted(newVal) {
		return newVal, nil
	}
	if !hadOld {
		return "", errRedactedWrite
	}
	return oldVal, nil
}
