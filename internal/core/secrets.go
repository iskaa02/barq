package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/zalando/go-keyring"
)

// Secret environment variables are kept out of the workspace file: their
// values live in the OS keyring (Secret Service / KWallet on Linux,
// Keychain on macOS, Credential Manager on Windows) and only in memory
// while barq runs. When no keyring is available they stay in the file and
// barq warns.

const keyringService = "barq"

// BARQ_KEYRING=off keeps secrets out of the system keyring (they stay in
// the workspace file), e.g. on servers or CI.
func init() {
	if strings.EqualFold(os.Getenv("BARQ_KEYRING"), "off") {
		keyring.MockInitWithError(errors.New("disabled by BARQ_KEYRING=off"))
	}
}

// sensitiveName matches names that hold credentials: variables named like
// this are secret unless marked otherwise, and headers/fields named like
// this are redacted in CLI output.
var sensitiveName = regexp.MustCompile(`(?i)(token|secret|passw(or)?d|passwd|api[-_]?key|auth|cookie|session|credential|private[-_]?key|otp)`)

// IsSecret reports whether an environment variable is secret: as marked,
// or by its name when it isn't marked.
func IsSecret(v SavedHeader) bool {
	if v.Secret != nil {
		return *v.Secret
	}
	return sensitiveName.MatchString(v.Key)
}

func BoolPtr(b bool) *bool { return &b }

// keyringAccount names a variable's keyring entry.
func (w *Workspace) keyringAccount(envID, key string) string {
	return strings.TrimSuffix(filepath.Base(w.Path), ".json") + "/" + envID + "/" + key
}

// errNoKeyring is reported (once per process) when secrets have to stay in
// the workspace file.
var errNoKeyring = errors.New("no system keyring available: secret values are stored in the workspace file")

// storeSecrets moves secret values into the keyring and returns the
// environments as they should be written to disk. Values the keyring can't
// take stay in the file, and the returned error says so.
func (w *Workspace) storeSecrets() ([]Environment, error) {
	var warn error
	wanted := map[string]bool{}
	out := make([]Environment, len(w.Environments))
	for i, env := range w.Environments {
		out[i] = env
		out[i].Vars = make([]SavedHeader, len(env.Vars))
		for j, v := range env.Vars {
			out[i].Vars[j] = v
			if !IsSecret(v) || v.Value == "" {
				continue
			}
			acct := w.keyringAccount(env.ID, v.Key)
			wanted[acct] = true
			if w.keyringCache[acct] != v.Value {
				if err := keyring.Set(keyringService, acct, v.Value); err != nil {
					warn = errNoKeyring
					continue
				}
				w.cacheSecret(acct, v.Value)
			}
			out[i].Vars[j].Value = "" // lives in the keyring
		}
	}
	// Remove entries of variables that were deleted, renamed or emptied.
	for acct := range w.keyringCache {
		if !wanted[acct] {
			_ = keyring.Delete(keyringService, acct)
			delete(w.keyringCache, acct)
		}
	}
	return out, warn
}

// loadSecrets fills in secret values from the keyring after a read.
func (w *Workspace) loadSecrets() {
	for i := range w.Environments {
		env := &w.Environments[i]
		for j := range env.Vars {
			v := &env.Vars[j]
			if !IsSecret(*v) || v.Value != "" {
				continue
			}
			acct := w.keyringAccount(env.ID, v.Key)
			if val, err := keyring.Get(keyringService, acct); err == nil {
				v.Value = val
				w.cacheSecret(acct, val)
			}
		}
	}
}

func (w *Workspace) cacheSecret(acct, val string) {
	if w.keyringCache == nil {
		w.keyringCache = map[string]string{}
	}
	w.keyringCache[acct] = val
}

// secretValues maps every secret value in the workspace (all environments)
// to its variable name, longest first so overlapping values redact fully.
// Very short values are skipped: they'd match ordinary text.
func (w *Workspace) secretValues() []secretValue {
	seen := map[string]bool{}
	var out []secretValue
	for _, env := range w.Environments {
		for _, v := range env.Vars {
			if IsSecret(v) && len(v.Value) >= 4 && !seen[v.Value] {
				seen[v.Value] = true
				out = append(out, secretValue{v.Key, v.Value})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i].value) > len(out[j].value) })
	return out
}

type secretValue struct{ name, value string }

func secretMarker(name string) string { return "«secret:" + name + "»" }

// hideSecrets replaces secret values in s with markers.
func hideSecrets(s string, secrets []secretValue) string {
	for _, sv := range secrets {
		s = strings.ReplaceAll(s, sv.value, secretMarker(sv.name))
	}
	return s
}

func hideSecretsInRequest(r Request, secrets []secretValue) Request {
	r.URL = hideSecrets(r.URL, secrets)
	r.Body = hideSecrets(r.Body, secrets)
	hide := func(hs []SavedHeader) []SavedHeader {
		out := make([]SavedHeader, len(hs))
		for i, h := range hs {
			out[i] = SavedHeader{Key: h.Key, Value: hideSecrets(h.Value, secrets), Enabled: h.Enabled}
		}
		return out
	}
	r.Headers, r.Form, r.DisabledParams = hide(r.Headers), hide(r.Form), hide(r.DisabledParams)
	return r
}

// hideSecretsInRun scrubs a run before it's written to history: the request
// as sent and the response can both contain secret values.
func hideSecretsInRun(e *HistEntry, secrets []secretValue) {
	if len(secrets) == 0 {
		return
	}
	e.Sent = hideSecretsInRequest(e.Sent, secrets)
	e.Meta.URL = hideSecrets(e.Meta.URL, secrets)
	e.Meta.Error = hideSecrets(e.Meta.Error, secrets)
	// The headers are shared with the live response; don't change those.
	e.Headers = e.Headers.Clone()
	for k, vs := range e.Headers {
		for i := range vs {
			vs[i] = hideSecrets(vs[i], secrets)
		}
		e.Headers[k] = vs
	}
}

func (w *Workspace) secretWarning() string {
	if w.SecretErr == nil {
		return ""
	}
	return fmt.Sprint(w.SecretErr)
}
