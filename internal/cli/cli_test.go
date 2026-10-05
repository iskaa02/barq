package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iskaa02/barq/internal/core"
)

const (
	testToken    = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOjd9.c2lnbmF0dXJl"
	testPassword = "MySecureP@ssw0rd!"
)

// apiServer is a small API: login returns a token, /me requires it and
// echoes it back (as a careless server might).
func apiServer(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), testPassword) {
				w.WriteHeader(401)
				return
			}
			w.Header().Set("Set-Cookie", "session=abc123")
			w.Write([]byte(`{"data":{"accessToken":"` + testToken + `","user":{"id":7}}}`))
		case "/me":
			if r.Header.Get("Authorization") != "Bearer "+testToken {
				w.WriteHeader(401)
				w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}
			w.Write([]byte(`{"id":7,"echo":"` + r.Header.Get("Authorization") + `"}`))
		default:
			w.WriteHeader(404)
		}
	}))
}

type cliEnv struct {
	t      *testing.T
	dir    string
	output strings.Builder // everything printed, to check for leaks
}

func newCLIEnv(t *testing.T) *cliEnv {
	t.Setenv("HOME", t.TempDir())
	return &cliEnv{t: t, dir: t.TempDir()}
}

func (e *cliEnv) run(stdin string, args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := Main(append(args, "--dir", e.dir), strings.NewReader(stdin), &out, &errOut)
	e.output.WriteString(out.String() + errOut.String())
	return code, out.String(), errOut.String()
}

func (e *cliEnv) ok(args ...string) string {
	e.t.Helper()
	code, out, errOut := e.run("", args...)
	if code != 0 {
		e.t.Fatalf("barq %s: exit %d\n%s%s", strings.Join(args, " "), code, out, errOut)
	}
	return out
}

func TestCLIRequestsAndFolders(t *testing.T) {
	e := newCLIEnv(t)
	var created requestJSON
	json.Unmarshal([]byte(e.ok("new", "Users/Admin/List users", "--url", "{{baseUrl}}/users", "--param", "page=2",
		"-H", "Accept: application/json", "--json")), &created)
	if created.Path != "Users/Admin/List users" || created.URL != "{{baseUrl}}/users?page=2" || created.Method != "GET" {
		t.Fatalf("created: %+v", created)
	}

	e.ok("set", "list users", "--method", "post", "--param", "page=3", "-H", "Accept: text/plain", "--name", "Find users")
	var shown requestJSON
	json.Unmarshal([]byte(e.ok("show", created.ID, "--json")), &shown)
	if shown.Method != "POST" || shown.URL != "{{baseUrl}}/users?page=3" || shown.Headers[0].Value != "text/plain" || shown.Name != "Find users" {
		t.Errorf("after set: %+v", shown)
	}

	e.ok("mv", "Find users", "/")
	e.ok("rename", "Users/Admin", "Admins")
	if out := e.ok("ls"); !strings.Contains(out, "Users/\n  Admins/\nPOST    Find users") {
		t.Errorf("ls:\n%s", out)
	}
	if code, _, errOut := e.run("", "rm", "Users"); code != 1 || !strings.Contains(errOut, "-r") {
		t.Errorf("rm of a non-empty folder should need -r: %d %s", code, errOut)
	}
	e.ok("rm", "-r", "Users")
	e.ok("new", "A/One")
	e.ok("new", "A/Other")
	if code, _, errOut := e.run("", "show", "A/O"); code != 1 || !strings.Contains(errOut, "matches 2") {
		t.Errorf("ambiguous ref: %d %s", code, errOut)
	}
}

func TestCLISecretsNeverPrinted(t *testing.T) {
	srv := apiServer(t)
	defer srv.Close()
	e := newCLIEnv(t)

	e.ok("env", "new", "dev", "--use")
	e.ok("env", "set", "dev", "baseUrl", srv.URL)
	e.ok("env", "set", "dev", "password", "-", "--dir", e.dir) // value from stdin, below
	e.run(testPassword, "env", "set", "dev", "password", "-")

	e.ok("new", "Auth/Login", "--method", "POST", "--url", "{{baseUrl}}/login",
		"--body", `{"phone":"+218","password":"{{password}}"}`, "--capture", "token=.data.accessToken")
	e.ok("new", "Me", "--url", "{{baseUrl}}/me", "-H", "Authorization: Bearer {{token}}")

	out := e.ok("run", "Auth/Login", "-i")
	if !strings.Contains(out, "200 OK") || !strings.Contains(out, "captured {{token}} (secret)") {
		t.Errorf("login run:\n%s", out)
	}
	var me runJSON
	json.Unmarshal([]byte(e.ok("run", "Me", "--json", "-i")), &me)
	if me.Code != 200 {
		t.Fatalf("authenticated call failed: %+v", me)
	}
	if body, _ := json.Marshal(me.Body); !strings.Contains(string(body), "«redacted") {
		t.Errorf("echoed token should be redacted: %s", body)
	}

	// Everything an agent might look at.
	e.ok("show", "Auth/Login")
	e.ok("show", "Me", "--json")
	e.ok("env", "show", "dev")
	e.ok("env", "show", "dev", "--json")
	e.ok("curl", "Me")
	e.ok("history")
	var runs []struct {
		RunID string `json:"run_id"`
	}
	json.Unmarshal([]byte(e.ok("history", "--json")), &runs)
	for _, r := range runs {
		e.ok("history", "show", r.RunID)
	}

	for _, secret := range []string{testToken, testPassword, "abc123"} {
		if strings.Contains(e.output.String(), secret) {
			t.Errorf("secret %q appeared in CLI output", secret)
		}
	}
	// Nor on disk: workspace and history files.
	filepath.Walk(filepath.Join(os.Getenv("HOME"), ".barq"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			data, _ := os.ReadFile(p)
			for _, secret := range []string{testToken, testPassword} {
				if bytes.Contains(data, []byte(secret)) {
					t.Errorf("secret %q stored in %s", secret, filepath.Base(p))
				}
			}
		}
		return nil
	})
}

func TestCLIRefusesWithoutAPerson(t *testing.T) {
	srv := apiServer(t)
	defer srv.Close()
	e := newCLIEnv(t)
	e.ok("env", "new", "prod", "--protect-all", "--use")
	e.ok("env", "set", "prod", "baseUrl", srv.URL)
	e.ok("env", "set", "prod", "token", testToken)
	e.ok("new", "Me", "--url", "{{baseUrl}}/me")

	for _, args := range [][]string{
		{"run", "Me"},                                       // protected environment
		{"show", "Me", "--reveal"},                          // reveal
		{"env", "show", "prod", "--reveal"},                 //
		{"env", "unprotect", "prod"},                        // weaken protection
		{"env", "protect", "prod"},                          // only writes now
		{"env", "set", "prod", "token", "x", "--no-secret"}, // expose a secret
	} {
		code, _, errOut := e.run("y\n", args...)
		if code != 1 || !strings.Contains(errOut, "interactive terminal") {
			t.Errorf("%v should be refused: %d %s", args, code, errOut)
		}
	}

	// With a person at a terminal who presses y, it goes through, after
	// being shown the real target with the secret hidden.
	isInteractive = func() bool { return true }
	defer func() { isInteractive = stdioIsTerminal }()
	var prompt string
	answer := byte('y')
	askKey = func(p string, _ io.Reader, _ io.Writer) (byte, error) { prompt = p; return answer, nil }
	defer func() { askKey = ttyAskKey }()
	e.ok("set", "Me", "--url", "{{baseUrl}}/me?api_key={{token}}")
	if code, out, errOut := e.run("", "run", "Me"); code != 0 || !strings.Contains(out, "401") {
		t.Errorf("confirmed run: %d %s %s", code, out, errOut)
	}
	if !strings.Contains(prompt, "GET "+srv.URL+"/me?api_key=") || strings.Contains(prompt, testToken) {
		t.Errorf("prompt should show the real URL without the secret: %q", prompt)
	}
	answer = 'n'
	if code, _, _ := e.run("", "env", "unprotect", "prod"); code != 1 {
		t.Error("anything but y must cancel")
	}
}

func TestProtectedWritesOnly(t *testing.T) {
	srv := apiServer(t)
	defer srv.Close()
	e := newCLIEnv(t)
	e.ok("env", "new", "prod", "--protect", "--use")
	e.ok("env", "set", "prod", "baseUrl", srv.URL)
	e.ok("new", "Health", "--url", "{{baseUrl}}/health")
	e.ok("new", "Login", "--method", "POST", "--url", "{{baseUrl}}/login", "--body", `{"a":1}`)

	// Reads go through without a person; writes don't.
	if code, out, errOut := e.run("", "run", "Health"); code != 0 || !strings.Contains(out, "404") {
		t.Errorf("GET in a writes-protected environment: %d %s %s", code, out, errOut)
	}
	if code, _, errOut := e.run("", "run", "Login"); code != 1 || !strings.Contains(errOut, "interactive terminal") {
		t.Errorf("POST should be refused: %d %s", code, errOut)
	}

	isInteractive = func() bool { return true }
	defer func() { isInteractive = stdioIsTerminal }()
	var prompt string
	askKey = func(p string, _ io.Reader, _ io.Writer) (byte, error) { prompt = p; return 'y', nil }
	defer func() { askKey = ttyAskKey }()
	if code, _, errOut := e.run("", "run", "Login"); code != 0 {
		t.Errorf("confirmed POST: %d %s", code, errOut)
	}
	if !strings.Contains(prompt, "POST "+srv.URL+"/login") || !strings.Contains(prompt, "body: 7 B") {
		t.Errorf("prompt: %q", prompt)
	}

	var env envJSON
	json.Unmarshal([]byte(e.ok("env", "protect", "prod", "--all", "--json")), &env)
	if env.Protects != "all" {
		t.Errorf("protects: %+v", env)
	}
}

func TestCLIRedactedRoundTrip(t *testing.T) {
	e := newCLIEnv(t)
	e.ok("new", "Login", "--method", "POST", "--body", `{"phone":"+218","password":"`+testPassword+`"}`,
		"-H", "X-Api-Key: hardcoded-key")
	var shown requestJSON
	json.Unmarshal([]byte(e.ok("show", "Login", "--json")), &shown)
	if strings.Contains(shown.Body, testPassword) || shown.Headers[0].Value != core.Redacted {
		t.Fatalf("not redacted: %+v", shown)
	}
	// An agent edits the phone and writes back what it was shown.
	edited := strings.Replace(shown.Body, "+218", "+219", 1)
	e.ok("set", "Login", "--body", edited, "-H", "X-Api-Key: "+core.Redacted)

	ws, _ := core.LoadWorkspace(e.dir)
	r := ws.Requests[0]
	if !strings.Contains(r.Body, testPassword) || !strings.Contains(r.Body, "+219") || r.Headers[0].Value != "hardcoded-key" {
		t.Errorf("round trip lost real values: %s %+v", r.Body, r.Headers)
	}
}

func TestCLIFailExitCode(t *testing.T) {
	srv := apiServer(t)
	defer srv.Close()
	e := newCLIEnv(t)
	if code, _, _ := e.run("", "run", "--curl", "curl "+srv.URL+"/nope", "--fail"); code != 3 {
		t.Errorf("--fail on 404: exit %d", code)
	}
	if code, _, _ := e.run("", "run", "--curl", "curl "+srv.URL+"/nope"); code != 0 {
		t.Errorf("without --fail: exit %d", code)
	}
	if code, _, _ := e.run("", "frobnicate"); code != 2 {
		t.Errorf("unknown command: exit %d", code)
	}
}

func TestCLINewFromAndHistorySize(t *testing.T) {
	srv := apiServer(t)
	defer srv.Close()
	e := newCLIEnv(t)
	e.ok("new", "Me", "--url", srv.URL+"/me", "-H", "Authorization: Bearer {{token}}", "--capture", "x=.id")
	var cp requestJSON
	json.Unmarshal([]byte(e.ok("new", "Copies/Me 2", "--from", "Me", "--param", "full=1", "--json")), &cp)
	if cp.Headers[0].Value != "Bearer {{token}}" || !strings.HasSuffix(cp.URL, "/me?full=1") || len(cp.Captures) != 1 || cp.Path != "Copies/Me 2" {
		t.Errorf("copy: %+v", cp)
	}

	// history show reports the size that was received, not the stored one.
	e.ok("env", "new", "dev", "--use")
	e.ok("env", "set", "dev", "password", testPassword)
	var run runJSON
	json.Unmarshal([]byte(e.ok("run", "--curl", "curl -X POST "+srv.URL+"/login -d '{\"password\":\""+testPassword+"\"}'", "--json")), &run)
	var shown struct{ Run runJSON }
	json.Unmarshal([]byte(e.ok("history", "show", run.RunID, "--json")), &shown)
	if shown.Run.Size != run.Size || run.Size == 0 {
		t.Errorf("sizes: run %d, history %d", run.Size, shown.Run.Size)
	}
}

// bigBody is a JSON array of about n bytes, one item per line, with the
// token echoed and a credential field near the end.
func bigBody(n int) []byte {
	var b bytes.Buffer
	b.WriteString("[\n")
	for i := 0; b.Len() < n; i++ {
		fmt.Fprintf(&b, "{\"i\":%d,\"name\":\"item-%d\"},\n", i, i)
	}
	b.WriteString(`{"last":true,"echo":"` + testToken + `","password":"pw-42"}` + "\n]")
	return b.Bytes()
}

func TestCLILargeBodies(t *testing.T) {
	body := bigBody(3 << 20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()
	e := newCLIEnv(t)
	e.ok("env", "new", "dev", "--use")
	e.ok("env", "set", "dev", "token", testToken)
	e.ok("new", "Big", "--url", srv.URL)

	// run prints the start of it, says so, and points at the rest.
	var res runJSON
	json.Unmarshal([]byte(e.ok("run", "Big", "--json")), &res)
	if !res.Partial || res.Size != int64(len(body)) || res.BodyFile == "" || res.RunID == "" {
		t.Fatalf("run --json: partial=%v size=%d file=%q", res.Partial, res.Size, res.BodyFile)
	}
	if s, _ := res.Body.(string); len(s) > inlineLimit+1024 || !strings.HasPrefix(s, "[\n{\"i\":0") {
		t.Errorf("inline body: %d bytes", len(s))
	}
	if out := e.ok("run", "Big"); !strings.Contains(out, "… showing 1.0 MB of 3.0 MB. Read the rest with: barq history body ") {
		t.Errorf("text output should say it's partial:\n%s", out[len(out)-300:])
	}

	// jq reads all of it.
	id := res.RunID
	count := strings.Count(string(body), `"name"`) + 1
	if out := e.ok("run", "Big", "--jq", "length"); !strings.HasSuffix(out, "\n\n"+fmt.Sprint(count)+"\n") {
		t.Errorf("run --jq length: %q, want %d", out[max(len(out)-40, 0):], count)
	}
	if out := strings.TrimSpace(e.ok("history", "body", id, "--jq", "length")); out != fmt.Sprint(count) {
		t.Errorf("history body --jq length = %s, want %d", out, count)
	}

	// The whole body is in history, scrubbed.
	whole := e.ok("history", "body", id)
	if strings.TrimSpace(whole) == "" || !strings.Contains(whole, `"last":true`) || len(whole) < len(body)-1024 {
		t.Errorf("history body: %d bytes, want about %d", len(whole), len(body))
	}
	if out := e.ok("history", "body", id, "--grep", `"last"`); !strings.Contains(out, `"password":"«redacted»"`) {
		t.Errorf("grep: %q", out)
	}
	if out := e.ok("history", "body", id, "--lines", "2:3"); out != "{\"i\":0,\"name\":\"item-0\"},\n{\"i\":1,\"name\":\"item-1\"},\n" {
		t.Errorf("lines: %q", out)
	}
	if out := e.ok("history", "body", id, "--bytes", ":9"); out != "[\n{\"i\":0," {
		t.Errorf("bytes: %q", out)
	}
	path := strings.TrimSpace(e.ok("history", "body", id, "--path"))
	if data, err := os.ReadFile(path); err != nil || !bytes.Contains(data, []byte(`"last":true`)) {
		t.Errorf("--path %q: %v", path, err)
	}
	if code, _, _ := e.run("", "history", "body", id, "--grep", "no-such-thing"); code != 1 {
		t.Error("grep without matches should exit 1")
	}

	// -o saves all of it, redacted.
	out := filepath.Join(t.TempDir(), "big.json")
	e.ok("run", "Big", "-o", out)
	if data, _ := os.ReadFile(out); len(data) < len(body)-1024 || !bytes.Contains(data, []byte(`"last":true`)) {
		t.Errorf("-o wrote %d bytes, want about %d", len(data), len(body))
	}

	// --max-body stops reading, and says so.
	json.Unmarshal([]byte(e.ok("run", "Big", "--json", "--max-body", "1MB")), &res)
	if !res.CutAtCap || res.Size != 1<<20 {
		t.Errorf("--max-body: cut=%v size=%d", res.CutAtCap, res.Size)
	}

	// history show is cut the same way.
	var shown struct{ Run runJSON }
	json.Unmarshal([]byte(e.ok("history", "show", id, "--json")), &shown)
	if !shown.Run.Partial || shown.Run.Size != int64(len(body)) {
		t.Errorf("history show: partial=%v size=%d", shown.Run.Partial, shown.Run.Size)
	}

	// Nothing printed anywhere has the secrets.
	for _, leak := range []string{testToken, "pw-42"} {
		if strings.Contains(e.output.String(), leak) {
			t.Errorf("output leaks %q", leak)
		}
		if data, _ := os.ReadFile(out); bytes.Contains(data, []byte(leak)) {
			t.Errorf("-o file leaks %q", leak)
		}
	}
}
