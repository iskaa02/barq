package core

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

// A Capture stores part of a successful response in an environment
// variable, e.g. token = .data.accessToken after logging in, so later
// requests can use {{token}}.
type Capture struct {
	Var    string `json:"var"`
	Filter string `json:"filter"` // jq, "header Name" or "cookie name"
}

// Source splits a filter into its kind ("jq", "header" or "cookie") and, for
// the last two, the header or cookie name.
func (c Capture) Source() (kind, name string) {
	for _, k := range []string{"header", "cookie"} {
		if rest, ok := strings.CutPrefix(c.Filter, k+" "); ok {
			return k, strings.TrimSpace(rest)
		}
	}
	if c.Filter == "header" || c.Filter == "cookie" {
		return c.Filter, ""
	}
	return "jq", ""
}

func (c Capture) String() string { return c.Var + " = " + c.Filter }

// ParseCapture reads "name = jq filter".
func ParseCapture(spec string) (Capture, error) {
	name, filter, ok := strings.Cut(spec, "=")
	c := Capture{Var: strings.TrimSpace(name), Filter: strings.TrimSpace(filter)}
	if !ok || c.Var == "" || c.Filter == "" || strings.ContainsAny(c.Var, " {}") {
		return c, errors.New(`expected "name = jq filter", e.g. token = .data.accessToken`)
	}
	if kind, name := c.Source(); kind != "jq" {
		if name == "" || strings.ContainsAny(name, " \t") {
			return c, fmt.Errorf("expected %q followed by one name, e.g. %s = %s X-Request-Id", kind, c.Var, kind)
		}
		if kind == "header" && !validHeaderName(name) {
			return c, fmt.Errorf("%q is not a valid header name", name)
		}
	}
	return c, nil
}

func validHeaderName(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0) {
			return false
		}
	}
	return s != ""
}

type CapturedVar struct {
	Var    string `json:"var"`
	Secret bool   `json:"secret"`
	Length int    `json:"length"`
	Value  string `json:"value,omitempty"` // only for non-secret values
	Error  string `json:"error,omitempty"`
}

var jwtLike = regexp.MustCompile(`^eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*$`)

// EvalCaptures runs the filters against the whole response body (up to
// JQLimit). Strings are stored as-is; other values as compact JSON.
func EvalCaptures(caps []Capture, resp *Response) []CapturedVar {
	var body []byte
	var bodyErr error
	bodyRead := false
	out := make([]CapturedVar, 0, len(caps))
	for _, c := range caps {
		cv := CapturedVar{Var: c.Var}
		if kind, name := c.Source(); kind != "jq" {
			cv.Value = headerOrCookie(kind, name, resp)
			if cv.Value == "" {
				cv.Error = "no " + kind + " " + name
			}
			cv.Length = len(cv.Value)
			out = append(out, cv)
			continue
		}
		if !bodyRead {
			body, bodyErr = resp.FullBody(JQLimit)
			bodyRead = true
		}
		vals, err := []any(nil), bodyErr
		if err == nil {
			vals, err = RunJQ(c.Filter, body)
		}
		switch {
		case err != nil:
			cv.Error = err.Error()
		case len(vals) == 0 || vals[0] == nil:
			cv.Error = "filter returned nothing"
		default:
			s, ok := vals[0].(string)
			if !ok {
				s = MarshalJQ(vals[0], false)
			}
			cv.Value, cv.Length = s, len(s)
		}
		out = append(out, cv)
	}
	return out
}

func headerOrCookie(kind, name string, resp *Response) string {
	if kind == "header" {
		return resp.Headers.Get(name)
	}
	for _, ck := range (&http.Response{Header: resp.Headers}).Cookies() {
		if ck.Name == name {
			return ck.Value
		}
	}
	return ""
}

// StoreCaptured writes captured values into an environment. A new
// variable is secret if its name says so or its value looks like a token;
// an existing one keeps how it was marked. Values of secret variables are
// cleared from the result so callers can't print them.
func (w *Workspace) StoreCaptured(envID string, vals []CapturedVar) error {
	env, err := w.Env(envID)
	if err != nil {
		return errors.New("captures need an environment to store variables in")
	}
	for i := range vals {
		cv := &vals[i]
		if cv.Error != "" {
			continue
		}
		j := slices.IndexFunc(env.Vars, func(v SavedHeader) bool { return v.Key == cv.Var })
		if j < 0 {
			v := SavedHeader{Key: cv.Var, Value: cv.Value, Enabled: true}
			if !IsSecret(v) && jwtLike.MatchString(cv.Value) {
				v.Secret = BoolPtr(true)
			}
			env.Vars = append(env.Vars, v)
			j = len(env.Vars) - 1
		} else {
			env.Vars[j].Value, env.Vars[j].Enabled = cv.Value, true
		}
		if cv.Secret = IsSecret(env.Vars[j]); cv.Secret {
			cv.Value = ""
		}
	}
	return nil
}

func CapturedSummary(vals []CapturedVar) string {
	var parts []string
	for _, cv := range vals {
		switch {
		case cv.Error != "":
			parts = append(parts, fmt.Sprintf("{{%s}} ✗ %s", cv.Var, cv.Error))
		case cv.Secret:
			parts = append(parts, fmt.Sprintf("{{%s}} (secret)", cv.Var))
		default:
			parts = append(parts, fmt.Sprintf("{{%s}} = %s", cv.Var, truncate(cv.Value, 30)))
		}
	}
	return "captured " + strings.Join(parts, ", ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
