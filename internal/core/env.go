package core

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// An Environment is a named set of variables, e.g. "dev" or "prod".
// Requests reference them as {{name}} in the URL, headers and body.
type Environment struct {
	ID   string        `json:"id"`
	Name string        `json:"name"`
	Vars []SavedHeader `json:"vars,omitempty"`
	// Protected environments (e.g. production) can't be used from the CLI
	// without confirming in an interactive terminal. Only requests that can
	// change something need confirming, unless ProtectReads is set too.
	Protected    bool `json:"protected,omitempty"`
	ProtectReads bool `json:"protectReads,omitempty"`
}

// NeedsConfirm reports whether the CLI must confirm sending method here.
func (e *Environment) NeedsConfirm(method string) bool {
	return e != nil && e.Protected && (e.ProtectReads || !safeMethod(method))
}

// safeMethod reports whether method only reads (an empty method is GET).
func safeMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case "", "GET", "HEAD", "OPTIONS":
		return true
	}
	return false
}

// Protection names how an environment is protected, for display.
func (e Environment) Protection() string {
	switch {
	case !e.Protected:
		return ""
	case e.ProtectReads:
		return "all"
	}
	return "writes"
}

func (w *Workspace) FindEnv(id string) int {
	if id == "" {
		return -1
	}
	return slices.IndexFunc(w.Environments, func(e Environment) bool { return e.ID == id })
}

func (w *Workspace) CurrentEnv() *Environment {
	if i := w.FindEnv(w.ActiveEnv); i >= 0 {
		return &w.Environments[i]
	}
	return nil
}

var varPattern = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_.$-]+)\s*\}\}`)

// dynamicVars are built in and produce a fresh value on every send.
var dynamicVars = map[string]func() string{
	"$timestamp":    func() string { return strconv.FormatInt(time.Now().Unix(), 10) },
	"$isoTimestamp": func() string { return time.Now().UTC().Format(time.RFC3339) },
	"$uuid":         newUUID,
	"$randomInt": func() string {
		n, _ := rand.Int(rand.Reader, big.NewInt(1000))
		return n.String()
	},
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// EnvVars returns an environment's enabled variables ("" for none).
func (w *Workspace) EnvVars(envID string) map[string]string {
	vars := map[string]string{}
	if i := w.FindEnv(envID); i >= 0 {
		for _, v := range w.Environments[i].Vars {
			if k := strings.TrimSpace(v.Key); v.Enabled && k != "" {
				vars[k] = v.Value
			}
		}
	}
	return vars
}

// substitute replaces {{name}} references, recording names it can't resolve.
func substitute(s string, vars map[string]string, missing map[string]bool) string {
	return varPattern.ReplaceAllStringFunc(s, func(ref string) string {
		name := varPattern.FindStringSubmatch(ref)[1]
		if v, ok := vars[name]; ok {
			return v
		}
		if f, ok := dynamicVars[name]; ok {
			return f()
		}
		missing[name] = true
		return ref
	})
}

// Resolve fills in variables from an environment, with overrides taking
// precedence, and reports any that aren't defined.
func (w *Workspace) Resolve(envID string, r Request, overrides map[string]string) (Request, []string) {
	vars := w.EnvVars(envID)
	for k, v := range overrides {
		vars[k] = v
	}
	return ResolveVars(r, vars)
}

// ResolveVars fills in the given variables and lists the ones missing.
func ResolveVars(r Request, vars map[string]string) (Request, []string) {
	missing := map[string]bool{}
	r.URL = substitute(r.URL, vars, missing)
	r.Body = substitute(r.Body, vars, missing)
	hs := make([]SavedHeader, len(r.Headers))
	for i, h := range r.Headers {
		if !h.Enabled {
			hs[i] = h // not sent, so don't complain about its variables
			continue
		}
		hs[i] = SavedHeader{
			Key:     substitute(h.Key, vars, missing),
			Value:   substitute(h.Value, vars, missing),
			Enabled: h.Enabled,
		}
	}
	r.Headers = hs
	if len(r.Form) > 0 {
		fs := make([]SavedHeader, len(r.Form))
		for i, f := range r.Form {
			if !f.Enabled {
				fs[i] = f
				continue
			}
			fs[i] = SavedHeader{Key: substitute(f.Key, vars, missing), Value: substitute(f.Value, vars, missing), Enabled: true}
		}
		r.Form = fs
	}
	names := make([]string, 0, len(missing))
	for n := range missing {
		names = append(names, n)
	}
	sort.Strings(names)
	return r, names
}

func headersOf(r Request) http.Header {
	h := http.Header{}
	for _, sh := range r.Headers {
		if k := strings.TrimSpace(sh.Key); sh.Enabled && k != "" {
			h.Add(k, strings.TrimSpace(sh.Value))
		}
	}
	return h
}
