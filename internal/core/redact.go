package core

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

const Redacted = "«redacted»"

// IsRedacted reports whether s contains a redaction marker, including the
// «secret:name» markers history stores.
func IsRedacted(s string) bool {
	return strings.Contains(s, "«redacted") || strings.Contains(s, "«secret:")
}

type Redactor struct{ secrets []secretValue }

func NewRedactor(w *Workspace) Redactor { return Redactor{secrets: w.secretValues()} }

// Text replaces secret values.
func (rd Redactor) Text(s string) string {
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
func (rd Redactor) field(name, value string, template bool) string {
	if template && safeTemplate(value) {
		return value
	}
	if sensitiveName.MatchString(name) && value != "" {
		return Redacted
	}
	return rd.Text(value)
}

func (rd Redactor) fields(hs []SavedHeader, template bool) []SavedHeader {
	out := make([]SavedHeader, len(hs))
	for i, h := range hs {
		out[i] = h
		out[i].Value = rd.field(h.Key, h.Value, template)
	}
	return out
}

func (rd Redactor) Headers(h http.Header) http.Header {
	out := http.Header{}
	for k, vs := range h {
		for _, v := range vs {
			out.Add(k, rd.field(k, v, false))
		}
	}
	return out
}

// URL redacts credential-like query parameters.
func (rd Redactor) URL(u string, template bool) string {
	base, query, frag, ok := splitURL(u)
	if !ok {
		return rd.Text(u)
	}
	rows := parseParams(query)
	for i := range rows {
		if template && safeTemplate(rows[i].Value) {
			continue
		}
		if sensitiveName.MatchString(rows[i].Key) && rows[i].Value != "" {
			rows[i].Value = Redacted
		}
	}
	return rd.Text(readableMarkers(BuildURL(base+frag, rows)))
}

// readableMarkers undoes the query-escaping of markers, for display.
func readableMarkers(s string) string {
	return strings.ReplaceAll(s, url.QueryEscape(Redacted), Redacted)
}

// Body redacts a request or response body: credential-like JSON fields or
// form fields, then any secret values left.
func (rd Redactor) Body(b string, contentType string, template bool) string {
	trimmed := strings.TrimSpace(b)
	switch {
	case strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "["):
		if v, err := decodeOrderedJSON([]byte(trimmed)); err == nil {
			var sb strings.Builder
			writeJSON(&sb, rd.jsonValue(v, false, template), "")
			return rd.Text(sb.String())
		}
	case strings.Contains(contentType, "x-www-form-urlencoded"):
		rows := parseParams(trimmed)
		for i := range rows {
			rows[i].Value = rd.field(rows[i].Key, rows[i].Value, template)
		}
		return readableMarkers(strings.TrimPrefix(BuildURL("", rows), "?"))
	}
	return rd.Text(b)
}

// jsonValue redacts strings and numbers under credential-like keys.
func (rd Redactor) jsonValue(v any, sensitive, template bool) any {
	switch x := v.(type) {
	case *Omap:
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
			return Redacted
		}
		return x
	case nil, bool:
		return x
	}
	if sensitive {
		return Redacted // numbers: PINs, codes
	}
	return v
}

// Request redacts a request for display. template is true for saved
// requests (with {{variables}}), false for what was actually sent.
func (rd Redactor) Request(r Request, template bool) Request {
	ct := ""
	for _, h := range r.Headers {
		if strings.EqualFold(h.Key, "Content-Type") {
			ct = h.Value
		}
	}
	r.URL = rd.URL(r.URL, template)
	r.Headers = rd.fields(r.Headers, template)
	r.Form = rd.fields(r.Form, template)
	r.DisabledParams = rd.fields(r.DisabledParams, template)
	r.Body = rd.Body(r.Body, ct, template)
	return r
}

// Write-back ----------------------------------------------------------------------

// UnredactBody restores redacted JSON fields from the old body, so an agent
// that edits a body it was shown can't overwrite real values with markers.
// Non-JSON bodies with markers are rejected.
func UnredactBody(newBody, oldBody string) (string, error) {
	if !IsRedacted(newBody) {
		return newBody, nil
	}
	nv, err := decodeOrderedJSON([]byte(strings.TrimSpace(newBody)))
	if err != nil {
		return "", ErrRedactedWrite
	}
	ov, _ := decodeOrderedJSON([]byte(strings.TrimSpace(oldBody)))
	merged, ok := mergeRedacted(nv, ov)
	if !ok {
		return "", ErrRedactedWrite
	}
	var sb strings.Builder
	writeJSON(&sb, merged, "")
	return sb.String(), nil
}

var ErrRedactedWrite = errors.New("the new value contains «redacted» markers that don't match the saved request; " +
	"leave redacted fields out or set real values")

// mergeRedacted replaces marker strings in n with the value at the same
// place in o. It fails if there's nothing to restore from.
func mergeRedacted(n, o any) (any, bool) {
	switch x := n.(type) {
	case string:
		if IsRedacted(x) {
			if o == nil {
				return nil, false
			}
			return o, true
		}
		return x, true
	case *Omap:
		om, _ := o.(*Omap)
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

// UnredactField keeps the old value when the new one is a marker.
func UnredactField(newVal, oldVal string, hadOld bool) (string, error) {
	if !IsRedacted(newVal) {
		return newVal, nil
	}
	if !hadOld {
		return "", ErrRedactedWrite
	}
	return oldVal, nil
}
