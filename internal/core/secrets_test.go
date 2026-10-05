package core

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestSecretsLiveInKeyring(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws, _ := LoadWorkspace("/proj")
	var envID string
	ws.Mutate(func(w *Workspace) error {
		envID = w.AddEnv("dev", []SavedHeader{
			{Key: "baseUrl", Value: "https://dev.example", Enabled: true},
			{Key: "token", Value: "tok-123456", Enabled: true},                               // secret by name
			{Key: "region", Value: "eu-west-1-secret", Enabled: true, Secret: BoolPtr(true)}, // marked
			{Key: "authorMode", Value: "plain-on-purpose", Enabled: true, Secret: BoolPtr(false)},
		})
		return nil
	})

	file, _ := os.ReadFile(ws.Path)
	for _, leak := range []string{"tok-123456", "eu-west-1-secret"} {
		if strings.Contains(string(file), leak) {
			t.Errorf("secret %q written to the workspace file", leak)
		}
	}
	for _, kept := range []string{"https://dev.example", "plain-on-purpose"} {
		if !strings.Contains(string(file), kept) {
			t.Errorf("non-secret %q should stay in the file", kept)
		}
	}

	// Another process sees the values.
	other, _ := LoadWorkspace("/proj")
	env, _ := other.Env(envID)
	if env.Vars[1].Value != "tok-123456" || env.Vars[2].Value != "eu-west-1-secret" {
		t.Errorf("secrets not restored from the keyring: %+v", env.Vars)
	}

	// Deleting a variable removes its keyring entry.
	other.Mutate(func(w *Workspace) error {
		e, _ := w.Env(envID)
		return w.SetEnvVars(envID, e.Vars[:1])
	})
	if _, err := keyring.Get(keyringService, other.keyringAccount(envID, "token")); !errors.Is(err, keyring.ErrNotFound) {
		t.Errorf("keyring entry should be deleted: %v", err)
	}
}

func TestSecretsFallBackWithoutKeyring(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	keyring.MockInitWithError(errors.New("no secret service"))
	defer keyring.MockInit()

	ws, _ := LoadWorkspace("/proj")
	ws.Mutate(func(w *Workspace) error {
		w.AddEnv("dev", []SavedHeader{{Key: "token", Value: "tok-123456", Enabled: true}})
		return nil
	})
	if ws.SecretErr == nil {
		t.Error("should warn that secrets stay in the file")
	}
	again, _ := LoadWorkspace("/proj")
	if again.Environments[0].Vars[0].Value != "tok-123456" {
		t.Error("value lost when the keyring is unavailable")
	}
}

func TestHistoryScrubsSecrets(t *testing.T) {
	secrets := []secretValue{{"token", "tok-123456"}}
	liveHeaders := http.Header{"Set-Cookie": {"s=tok-123456"}}
	e := &HistEntry{
		Meta:    HistMeta{URL: "https://x/y?t=tok-123456"},
		Sent:    Request{URL: "https://x/y?t=tok-123456", Headers: []SavedHeader{{Key: "Authorization", Value: "Bearer tok-123456", Enabled: true}}},
		Headers: liveHeaders,
		Body:    []byte(`{"accessToken":"tok-123456"}`),
	}
	hideSecretsInRun(e, secrets)
	all := e.Meta.URL + e.Sent.URL + e.Sent.Headers[0].Value + e.Headers.Get("Set-Cookie") + string(e.Body)
	if strings.Contains(all, "tok-123456") {
		t.Errorf("secret left in history: %s", all)
	}
	if !strings.Contains(e.Sent.Headers[0].Value, "«secret:token»") {
		t.Errorf("expected a marker: %q", e.Sent.Headers[0].Value)
	}
	if liveHeaders.Get("Set-Cookie") != "s=tok-123456" {
		t.Error("the live response's headers must not change")
	}
}

func TestImportProtectsProduction(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	spec, err := LoadSpec(writeSpec(t, "s.yaml", `
openapi: 3.0.0
info: {title: T}
servers:
  - {url: "https://api.example.com", description: Production Server}
  - {url: "http://localhost:3000", description: Local}
paths: {/a: {get: {}}}
`))
	if err != nil {
		t.Fatal(err)
	}
	ws, _ := LoadWorkspace("/p")
	ImportOpenAPI(ws, spec)
	if !ws.Environments[0].Protected || ws.Environments[1].Protected {
		t.Errorf("protected: %v %v", ws.Environments[0].Protected, ws.Environments[1].Protected)
	}
}
