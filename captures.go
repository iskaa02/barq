package main

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// A capture stores part of a successful response in an environment
// variable, e.g. token = .data.accessToken after logging in, so later
// requests can use {{token}}.
type capture struct {
	Var    string `json:"var"`
	Filter string `json:"filter"` // jq
}

func (c capture) String() string { return c.Var + " = " + c.Filter }

// parseCapture reads "name = jq filter".
func parseCapture(spec string) (capture, error) {
	name, filter, ok := strings.Cut(spec, "=")
	c := capture{Var: strings.TrimSpace(name), Filter: strings.TrimSpace(filter)}
	if !ok || c.Var == "" || c.Filter == "" || strings.ContainsAny(c.Var, " {}") {
		return c, errors.New(`expected "name = jq filter", e.g. token = .data.accessToken`)
	}
	return c, nil
}

type capturedVar struct {
	Var    string `json:"var"`
	Secret bool   `json:"secret"`
	Length int    `json:"length"`
	Value  string `json:"value,omitempty"` // only for non-secret values
	Error  string `json:"error,omitempty"`
}

var jwtLike = regexp.MustCompile(`^eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*$`)

// evalCaptures runs the filters against a response body. Strings are
// stored as-is; other values as compact JSON.
func evalCaptures(caps []capture, body []byte) []capturedVar {
	out := make([]capturedVar, 0, len(caps))
	for _, c := range caps {
		cv := capturedVar{Var: c.Var}
		vals, err := runJQ(c.Filter, body)
		switch {
		case err != nil:
			cv.Error = err.Error()
		case len(vals) == 0 || vals[0] == nil:
			cv.Error = "filter returned nothing"
		default:
			s, ok := vals[0].(string)
			if !ok {
				s = marshalJQ(vals[0], false)
			}
			cv.Value, cv.Length = s, len(s)
		}
		out = append(out, cv)
	}
	return out
}

// storeCaptured writes captured values into an environment. A new
// variable is secret if its name says so or its value looks like a token;
// an existing one keeps how it was marked. Values of secret variables are
// cleared from the result so callers can't print them.
func (w *workspace) storeCaptured(envID string, vals []capturedVar) error {
	env, err := w.env(envID)
	if err != nil {
		return errors.New("captures need an environment to store variables in")
	}
	for i := range vals {
		cv := &vals[i]
		if cv.Error != "" {
			continue
		}
		j := slices.IndexFunc(env.Vars, func(v savedHeader) bool { return v.Key == cv.Var })
		if j < 0 {
			v := savedHeader{Key: cv.Var, Value: cv.Value, Enabled: true}
			if !isSecret(v) && jwtLike.MatchString(cv.Value) {
				v.Secret = boolPtr(true)
			}
			env.Vars = append(env.Vars, v)
			j = len(env.Vars) - 1
		} else {
			env.Vars[j].Value, env.Vars[j].Enabled = cv.Value, true
		}
		if cv.Secret = isSecret(env.Vars[j]); cv.Secret {
			cv.Value = ""
		}
	}
	return nil
}

func capturedSummary(vals []capturedVar) string {
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

// TUI -------------------------------------------------------------------------

// runCaptures applies a saved request's captures after a successful send.
func (m *model) runCaptures(t *tab, resp *response) {
	if t.savedID == "" || resp == nil || resp.StatusCode >= 400 {
		return
	}
	j := m.ws.find(t.savedID)
	if j < 0 || len(m.ws.Requests[j].Captures) == 0 {
		return
	}
	vals := evalCaptures(m.ws.Requests[j].Captures, resp.Body)
	envID := m.ws.ActiveEnv
	if m.mutate(func(w *workspace) error { return w.storeCaptured(envID, vals) }) {
		m.flash(capturedSummary(vals))
	}
}

func (m *model) addCapture(spec string) {
	c, err := parseCapture(spec)
	if err != nil {
		m.notice = errorStyle.Render(err.Error())
		return
	}
	id := m.cur().savedID
	if m.mutate(func(w *workspace) error {
		return w.updateRequest(id, func(r *request) {
			r.Captures = slices.DeleteFunc(r.Captures, func(x capture) bool { return x.Var == c.Var })
			r.Captures = append(r.Captures, c)
		})
	}) {
		m.flash("after each successful send: " + c.String())
	}
}

func captureItems(m model) []paletteItem {
	j := m.ws.find(m.cur().savedID)
	if j < 0 {
		return nil
	}
	id := m.cur().savedID
	var items []paletteItem
	for _, c := range m.ws.Requests[j].Captures {
		v := c.Var
		items = append(items, paletteItem{title: c.String(), run: func(m *model) tea.Cmd {
			m.mutate(func(w *workspace) error {
				return w.updateRequest(id, func(r *request) {
					r.Captures = slices.DeleteFunc(r.Captures, func(x capture) bool { return x.Var == v })
				})
			})
			return nil
		}})
	}
	return items
}
