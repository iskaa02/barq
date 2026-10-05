package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testRedactor() Redactor {
	return Redactor{secrets: []secretValue{{"token", "tok-123456"}}}
}

func TestRedactRequestTemplate(t *testing.T) {
	r := Request{
		URL: "{{baseUrl}}/x?api_key=abc123&page=2&token={{token}}",
		Headers: []SavedHeader{
			{Key: "Authorization", Value: "Bearer {{token}}", Enabled: true}, // safe template
			{Key: "X-Api-Key", Value: "hardcoded-key", Enabled: true},
			{Key: "Cookie", Value: "sid={{sid}}; theme=dark", Enabled: true},
			{Key: "Accept", Value: "application/json", Enabled: true},
		},
		Body: `{"phone": "+218912345678", "password": "MySecureP@ssw0rd!", "otp": 9876, "nested": {"refreshToken": "r-1"}, "note": "{{token}}"}`,
	}
	got := testRedactor().Request(r, true)

	if got.URL != "{{baseUrl}}/x?api_key=«redacted»&page=2&token={{token}}" {
		t.Errorf("url: %s", got.URL)
	}
	want := []string{"Bearer {{token}}", Redacted, Redacted, "application/json"}
	for i, h := range got.Headers {
		if h.Value != want[i] {
			t.Errorf("header %s = %q, want %q", h.Key, h.Value, want[i])
		}
	}
	for _, leak := range []string{"MySecureP@ssw0rd!", "9876", "r-1"} {
		if strings.Contains(got.Body, leak) {
			t.Errorf("body leaks %q:\n%s", leak, got.Body)
		}
	}
	if !strings.Contains(got.Body, "+218912345678") || !strings.Contains(got.Body, `"note": "{{token}}"`) {
		t.Errorf("non-secret fields should stay:\n%s", got.Body)
	}
}

func TestRedactResponse(t *testing.T) {
	rd := testRedactor()
	h := rd.Headers(http.Header{"Set-Cookie": {"sid=abc"}, "X-Echo": {"got tok-123456"}, "Content-Type": {"application/json"}})
	if h.Get("Set-Cookie") != Redacted || h.Get("X-Echo") != "got «redacted:token»" || h.Get("Content-Type") != "application/json" {
		t.Errorf("headers: %v", h)
	}
	// Sent requests aren't templates: "Bearer <value>" is hidden.
	if v := rd.field("Authorization", "Bearer tok-123456", false); v != Redacted {
		t.Errorf("sent auth header: %q", v)
	}
	body := rd.Body(`{"data":{"accessToken":"eyJ.a.b","user":{"id":7}},"echo":"tok-123456"}`, "application/json", false)
	if strings.Contains(body, "eyJ.a.b") || strings.Contains(body, "tok-123456") || !strings.Contains(body, `"id": 7`) {
		t.Errorf("response body:\n%s", body)
	}
	form := rd.Body("user=a&password=hunter2", "application/x-www-form-urlencoded", false)
	if form != "user=a&password=«redacted»" {
		t.Errorf("form body: %q", form)
	}
	if strings.Contains(form, "hunter2") {
		t.Errorf("form body leaks: %q", form)
	}
}

func TestUnredactBody(t *testing.T) {
	old := `{"phone": "+218", "password": "real-secret", "list": [{"pin": "1111"}]}`
	shown := testRedactor().Body(old, "", true)
	edited := strings.Replace(shown, `"+218"`, `"+219"`, 1)

	got, err := UnredactBody(edited, old)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"+219"`) || !strings.Contains(got, "real-secret") || !strings.Contains(got, "1111") {
		t.Errorf("merge lost something:\n%s", got)
	}
	// A marker where the old body had nothing can't be restored.
	if _, err := UnredactBody(`{"apiKey": "«redacted»"}`, `{}`); err == nil {
		t.Error("expected an error for an unrestorable marker")
	}
	if got, _ := UnredactBody(`{"a": 1}`, old); got != `{"a": 1}` {
		t.Error("bodies without markers pass through untouched")
	}
}

func TestCaptures(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"accessToken":"eyJhbGciOi.eyJzdWIiOjF9.c2ln","user":{"id":7}}}`))
	}))
	defer srv.Close()

	ws, _ := LoadWorkspace("/proj")
	var envID string
	ws.Mutate(func(w *Workspace) error { envID = w.AddEnv("dev", nil); return nil })

	resp, err := RunRequest(context.Background(), Request{Method: "POST", URL: srv.URL}, "/")
	if err != nil {
		t.Fatal(err)
	}
	caps := []Capture{{"jwt", ".data.accessToken"}, {"userId", ".data.user.id"}, {"missing", ".nope"}}
	vals := EvalCaptures(caps, resp)
	if err := ws.Mutate(func(w *Workspace) error { return w.StoreCaptured(envID, vals) }); err != nil {
		t.Fatal(err)
	}
	if !vals[0].Secret || vals[0].Value != "" || vals[0].Length == 0 {
		t.Errorf("a JWT should be stored as secret, and not returned: %+v", vals[0])
	}
	if vals[1].Secret || vals[1].Value != "7" {
		t.Errorf("non-secret value: %+v", vals[1])
	}
	if vals[2].Error == "" {
		t.Error("missing field should report an error")
	}
	vars := ws.EnvVars(envID)
	if vars["jwt"] != "eyJhbGciOi.eyJzdWIiOjF9.c2ln" || vars["userId"] != "7" {
		t.Errorf("stored: %v", vars)
	}
	if _, err := ParseCapture("token .data"); err == nil {
		t.Error("parseCapture should require name = filter")
	}
}
