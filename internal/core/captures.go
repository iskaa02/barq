package core

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// A Capture stores part of a successful response in an environment
// variable, e.g. token = .data.accessToken after logging in, so later
// requests can use {{token}}.
type Capture struct {
	Var    string `json:"var"`
	Filter string `json:"filter"` // jq
}

func (c Capture) String() string { return c.Var + " = " + c.Filter }

// ParseCapture reads "name = jq filter".
func ParseCapture(spec string) (Capture, error) {
	name, filter, ok := strings.Cut(spec, "=")
	c := Capture{Var: strings.TrimSpace(name), Filter: strings.TrimSpace(filter)}
	if !ok || c.Var == "" || c.Filter == "" || strings.ContainsAny(c.Var, " {}") {
		return c, errors.New(`expected "name = jq filter", e.g. token = .data.accessToken`)
	}
	return c, nil
}

type CapturedVar struct {
	Var    string `json:"var"`
	Secret bool   `json:"secret"`
	Length int    `json:"length"`
	Value  string `json:"value,omitempty"` // only for non-secret values
	Error  string `json:"error,omitempty"`
}

var jwtLike = regexp.MustCompile(`^eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*$`)

// EvalCaptures runs the filters against a response body. Strings are
// stored as-is; other values as compact JSON.
func EvalCaptures(caps []Capture, body []byte) []CapturedVar {
	out := make([]CapturedVar, 0, len(caps))
	for _, c := range caps {
		cv := CapturedVar{Var: c.Var}
		vals, err := RunJQ(c.Filter, body)
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
