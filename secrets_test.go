package main

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
	ws, _ := loadWorkspace("/proj")
	var envID string
	ws.mutate(func(w *workspace) error {
		envID = w.addEnv("dev", []savedHeader{
			{Key: "baseUrl", Value: "https://dev.example", Enabled: true},
			{Key: "token", Value: "tok-123456", Enabled: true},                               // secret by name
			{Key: "region", Value: "eu-west-1-secret", Enabled: true, Secret: boolPtr(true)}, // marked
			{Key: "authorMode", Value: "plain-on-purpose", Enabled: true, Secret: boolPtr(false)},
		})
		return nil
	})

	file, _ := os.ReadFile(ws.path)
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
	other, _ := loadWorkspace("/proj")
	env, _ := other.env(envID)
	if env.Vars[1].Value != "tok-123456" || env.Vars[2].Value != "eu-west-1-secret" {
		t.Errorf("secrets not restored from the keyring: %+v", env.Vars)
	}

	// Deleting a variable removes its keyring entry.
	other.mutate(func(w *workspace) error {
		e, _ := w.env(envID)
		return w.setEnvVars(envID, e.Vars[:1])
	})
	if _, err := keyring.Get(keyringService, other.keyringAccount(envID, "token")); !errors.Is(err, keyring.ErrNotFound) {
		t.Errorf("keyring entry should be deleted: %v", err)
	}
}

func TestSecretsFallBackWithoutKeyring(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	keyring.MockInitWithError(errors.New("no secret service"))
	defer keyring.MockInit()

	ws, _ := loadWorkspace("/proj")
	ws.mutate(func(w *workspace) error {
		w.addEnv("dev", []savedHeader{{Key: "token", Value: "tok-123456", Enabled: true}})
		return nil
	})
	if ws.secretErr == nil {
		t.Error("should warn that secrets stay in the file")
	}
	again, _ := loadWorkspace("/proj")
	if again.Environments[0].Vars[0].Value != "tok-123456" {
		t.Error("value lost when the keyring is unavailable")
	}
}

func TestHistoryScrubsSecrets(t *testing.T) {
	secrets := []secretValue{{"token", "tok-123456"}}
	liveHeaders := http.Header{"Set-Cookie": {"s=tok-123456"}}
	e := &histEntry{
		Meta:    histMeta{URL: "https://x/y?t=tok-123456"},
		Sent:    request{URL: "https://x/y?t=tok-123456", Headers: []savedHeader{{Key: "Authorization", Value: "Bearer tok-123456", Enabled: true}}},
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
	spec, err := loadSpec(writeSpec(t, "s.yaml", `
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
	ws, _ := loadWorkspace("/p")
	importOpenAPI(ws, spec)
	if !ws.Environments[0].Protected || ws.Environments[1].Protected {
		t.Errorf("protected: %v %v", ws.Environments[0].Protected, ws.Environments[1].Protected)
	}
}
