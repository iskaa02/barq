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
	"github.com/iskaa02/barq/internal/httpfile"
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

// writeHTTP puts a .http file in the project.
func (e *cliEnv) writeHTTP(rel, text string) {
	e.t.Helper()
	p := filepath.Join(e.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

const authFile = `### login
# @capture token = .data.accessToken
POST {{baseUrl}}/login

{"phone":"+218","password":"{{password}}"}

### me
GET {{baseUrl}}/me
Authorization: Bearer {{token}}
`

func TestCLILsShowAndRefs(t *testing.T) {
	e := newCLIEnv(t)
	e.writeHTTP("auth.http", authFile)
	e.writeHTTP("users/list.http", "### list\nGET http://x/users\n\n###\nDELETE http://x/users/1\n")
	e.writeHTTP("node_modules/skip.http", "### skipped\nGET http://x\n")

	out := e.ok("ls")
	for _, want := range []string{"auth.http#login", "POST   {{baseUrl}}/login", "users/list.http#list", "users/list.http#2", "DELETE"} {
		if !strings.Contains(out, want) {
			t.Errorf("ls missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "skipped") {
		t.Errorf("ls should skip node_modules:\n%s", out)
	}
	var rows []struct{ Ref, Method string }
	json.Unmarshal([]byte(e.ok("ls", "--json")), &rows)
	if len(rows) != 4 {
		t.Errorf("ls --json: %+v", rows)
	}

	e.ok("env", "new", "dev", "--use")
	e.ok("env", "set", "dev", "baseUrl", "http://api.test")
	out = e.ok("show", "auth.http#me")
	if !strings.Contains(out, "GET {{baseUrl}}/me") || !strings.Contains(out, "resolved: GET http://api.test/me") ||
		!strings.Contains(out, "undefined: {{token}}") {
		t.Errorf("show:\n%s", out)
	}
	if out := e.ok("curl", "me"); !strings.Contains(out, "http://api.test/me") || !strings.Contains(out, "{{token}}") {
		t.Errorf("curl (bare name):\n%s", out)
	}
	if code, _, errOut := e.run("", "show", "nope"); code != 1 || !strings.Contains(errOut, "no request") {
		t.Errorf("unknown ref: %d %s", code, errOut)
	}
	if code, _, errOut := e.run("", "show", "users/list.http#7"); code != 1 || !strings.Contains(errOut, "no #7") {
		t.Errorf("bad index: %d %s", code, errOut)
	}
	if code, _, _ := e.run("", "new", "x"); code != 2 {
		t.Errorf("removed command: %d", code)
	}
}

func TestCLISecretsNeverPrinted(t *testing.T) {
	srv := apiServer(t)
	defer srv.Close()
	e := newCLIEnv(t)

	e.ok("env", "new", "dev", "--use")
	e.ok("env", "set", "dev", "baseUrl", srv.URL)
	e.run(testPassword, "env", "set", "dev", "password", "-")
	e.writeHTTP("auth.http", authFile)

	out := e.ok("run", "auth.http#login", "-i")
	if !strings.Contains(out, "200 OK") || !strings.Contains(out, "captured {{token}} (secret)") {
		t.Errorf("login run:\n%s", out)
	}
	var me runJSON
	json.Unmarshal([]byte(e.ok("run", "me", "--json", "-i")), &me)
	if me.Code != 200 || me.Request != "auth.http#me" {
		t.Fatalf("authenticated call failed: %+v", me)
	}
	if body, _ := json.Marshal(me.Body); !strings.Contains(string(body), "«redacted") {
		t.Errorf("echoed token should be redacted: %s", body)
	}

	// Everything an agent might look at.
	e.ok("show", "auth.http#login")
	e.ok("show", "me", "--json")
	e.ok("env", "show", "dev")
	e.ok("env", "show", "dev", "--json")
	e.ok("curl", "me")
	e.ok("history")
	e.ok("history", "auth.http#me")
	var runs []struct {
		RunID   string `json:"run_id"`
		Request string `json:"request"`
	}
	json.Unmarshal([]byte(e.ok("history", "--json")), &runs)
	if len(runs) != 2 {
		t.Errorf("history: %+v", runs)
	}
	var mine []struct {
		RunID string `json:"run_id"`
	}
	json.Unmarshal([]byte(e.ok("history", "me", "--json")), &mine)
	if len(mine) != 1 {
		t.Errorf("history for me: %+v", mine)
	}
	for _, r := range runs {
		e.ok("history", "show", r.RunID)
	}
	// history show reports the size that was received, not the stored one.
	var shown struct{ Run runJSON }
	json.Unmarshal([]byte(e.ok("history", "show", runs[0].RunID, "--json")), &shown)
	if shown.Run.Size == 0 {
		t.Errorf("history show size: %+v", shown.Run)
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
	e.writeHTTP("a.http", "### me\nGET {{baseUrl}}/me?api_key={{token}}\n")

	for _, args := range [][]string{
		{"run", "me"},                                       // protected environment
		{"run", "me", "--yes"},                              // --yes doesn't cover protection
		{"curl", "me", "--reveal"},                          // reveal
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
	if code, out, errOut := e.run("", "run", "me"); code != 0 || !strings.Contains(out, "401") {
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
	e.writeHTTP("a.http", "### health\nGET {{baseUrl}}/health\n\n### login\nPOST {{baseUrl}}/login\n\n{\"a\":1}\n")

	// Reads go through without a person; writes don't.
	if code, out, errOut := e.run("", "run", "health"); code != 0 || !strings.Contains(out, "404") {
		t.Errorf("GET in a writes-protected environment: %d %s %s", code, out, errOut)
	}
	if code, _, errOut := e.run("", "run", "login"); code != 1 || !strings.Contains(errOut, "interactive terminal") {
		t.Errorf("POST should be refused: %d %s", code, errOut)
	}

	isInteractive = func() bool { return true }
	defer func() { isInteractive = stdioIsTerminal }()
	var prompt string
	askKey = func(p string, _ io.Reader, _ io.Writer) (byte, error) { prompt = p; return 'y', nil }
	defer func() { askKey = ttyAskKey }()
	if code, _, errOut := e.run("", "run", "login"); code != 0 {
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

func TestCLIConfirmDirective(t *testing.T) {
	srv := apiServer(t)
	defer srv.Close()
	e := newCLIEnv(t)
	e.ok("env", "new", "dev", "--use")
	e.ok("env", "set", "dev", "baseUrl", srv.URL)
	e.writeHTTP("a.http", "### risky\n# @confirm\nGET {{baseUrl}}/health\n")
	if code, _, errOut := e.run("", "run", "risky"); code != 1 || !strings.Contains(errOut, "interactive terminal") {
		t.Errorf("@confirm without a person: %d %s", code, errOut)
	}
	if code, out, _ := e.run("", "run", "risky", "--yes"); code != 0 || !strings.Contains(out, "404") {
		t.Errorf("--yes: %d %s", code, out)
	}
}

func TestCLIExpectsAndFail(t *testing.T) {
	srv := apiServer(t)
	defer srv.Close()
	e := newCLIEnv(t)
	e.ok("env", "new", "dev", "--use")
	e.ok("env", "set", "dev", "baseUrl", srv.URL)
	e.writeHTTP("a.http", "### nope\nGET {{baseUrl}}/nope\n\n### wants200\n# @expect status 200\nGET {{baseUrl}}/nope\n\n### ok404\n# @expect status 404\nGET {{baseUrl}}/nope\n")
	if code, _, _ := e.run("", "run", "nope", "--fail"); code != 3 {
		t.Errorf("--fail on 404: exit %d", code)
	}
	if code, _, _ := e.run("", "run", "nope"); code != 0 {
		t.Errorf("without --fail: exit %d", code)
	}
	if code, _, errOut := e.run("", "run", "wants200"); code != 4 || !strings.Contains(errOut, "expected status 200, got 404") {
		t.Errorf("failed expect: exit %d %s", code, errOut)
	}
	if code, _, _ := e.run("", "run", "ok404"); code != 0 {
		t.Errorf("passing expect: exit %d", code)
	}
	if code, _, _ := e.run("", "run", "a.http#missing"); code != 1 {
		t.Errorf("unknown request: exit %d", code)
	}
	if code, _, _ := e.run("", "frobnicate"); code != 2 {
		t.Errorf("unknown command: exit %d", code)
	}
}

func TestCLIImport(t *testing.T) {
	e := newCLIEnv(t)
	spec := filepath.Join(t.TempDir(), "spec.json")
	os.WriteFile(spec, []byte(`{"openapi":"3.0.0","info":{"title":"Pets"},"servers":[{"url":"http://pets.test"}],
	  "paths":{"/pets":{"get":{"tags":["pets"],"summary":"List pets"},"post":{"tags":["pets"],"summary":"Add pet"}},
	           "/owners":{"get":{"summary":"List owners"}}}}`), 0o644)
	e.ok("import", spec)
	b, err := os.ReadFile(filepath.Join(e.dir, "requests", "pets.http"))
	if err != nil || !strings.Contains(string(b), "### List pets\nGET {{baseUrl}}/pets") || !strings.Contains(string(b), "### Add pet") {
		t.Fatalf("pets.http: %v\n%s", err, b)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "requests", "owners.http")); err != nil {
		t.Error(err)
	}
	if out := e.ok("env", "ls"); !strings.Contains(out, "pets.test") {
		t.Errorf("env ls:\n%s", out)
	}
	// Re-running writes nothing new.
	var res struct{ Added, Skipped int }
	json.Unmarshal([]byte(e.ok("import", spec, "--json")), &res)
	if res.Added != 0 || res.Skipped != 3 {
		t.Errorf("re-import: %+v", res)
	}
	if out := e.ok("ls"); !strings.Contains(out, "requests/pets.http#List pets") {
		t.Errorf("ls:\n%s", out)
	}
}

func TestCLIImportSaved(t *testing.T) {
	e := newCLIEnv(t)
	ws, err := core.LoadWorkspace(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.Mutate(func(w *core.Workspace) error {
		w.AddRequest(core.Request{Name: "Log in", Method: "POST", URL: "http://x/login", Body: "{}"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.ok("import", "--saved")
	if _, err := os.Stat(filepath.Join(e.dir, "requests", "log-in.http")); err != nil {
		t.Error(err)
	}
	if out := e.ok("import", "--saved"); !strings.Contains(out, "0 request(s)") || !strings.Contains(out, "1 already") {
		t.Errorf("second import:\n%s", out)
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
	e.writeHTTP("big.http", "### Big\nGET "+srv.URL+"\n")

	// run prints the start of it, says so, and points at the rest.
	var res runJSON
	json.Unmarshal([]byte(e.ok("run", "big.http#Big", "--json")), &res)
	if !res.Partial || res.Size != int64(len(body)) || res.BodyFile == "" || res.RunID == "" {
		t.Fatalf("run --json: partial=%v size=%d file=%q", res.Partial, res.Size, res.BodyFile)
	}
	if s, _ := res.Body.(string); len(s) > inlineLimit+1024 || !strings.HasPrefix(s, "[\n{\"i\":0") {
		t.Errorf("inline body: %d bytes", len(s))
	}
	if out := e.ok("run", "big.http#Big"); !strings.Contains(out, "… showing 1.0 MB of 3.0 MB. Read the rest with: barq history body ") {
		t.Errorf("text output should say it's partial:\n%s", out[len(out)-300:])
	}

	// jq reads all of it.
	id := res.RunID
	count := strings.Count(string(body), `"name"`) + 1
	if out := e.ok("run", "big.http#Big", "--jq", "length"); !strings.HasSuffix(out, "\n\n"+fmt.Sprint(count)+"\n") {
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
	e.ok("run", "big.http#Big", "-o", out)
	if data, _ := os.ReadFile(out); len(data) < len(body)-1024 || !bytes.Contains(data, []byte(`"last":true`)) {
		t.Errorf("-o wrote %d bytes, want about %d", len(data), len(body))
	}

	// --max-body stops reading, and says so.
	json.Unmarshal([]byte(e.ok("run", "big.http#Big", "--json", "--max-body", "1MB")), &res)
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

func TestSendSummaryUsesVarOverrides(t *testing.T) {
	ws := &core.Workspace{}
	c := &cli{ws: ws, rd: core.NewRedactor(ws)}
	req := httpfile.Request{Method: "DELETE", URL: "http://x.test/users/{{id}}"}
	got := strings.Join(c.sendSummary("", req, map[string]string{"id": "999"}), "\n")
	if !strings.Contains(got, "/users/999") {
		t.Errorf("summary ignores --var: %q", got)
	}
}

func TestCLIImportUpdatesEnv(t *testing.T) {
	e := newCLIEnv(t)
	spec := filepath.Join(t.TempDir(), "spec.json")
	os.WriteFile(spec, []byte(`{"openapi":"3.0.0","info":{"title":"Pets"},"servers":[{"url":"http://one.test","description":"main"}],"paths":{"/a":{"get":{"summary":"A"}}}}`), 0o644)
	e.ok("import", spec)
	os.WriteFile(spec, []byte(`{"openapi":"3.0.0","info":{"title":"Pets"},"servers":[{"url":"http://one.test/v2","description":"main"}],
	  "components":{"securitySchemes":{"k":{"type":"apiKey","in":"header","name":"X-Key"}}},"paths":{"/a":{"get":{"summary":"A"}}}}`), 0o644)
	e.ok("import", spec)
	ws, err := core.LoadWorkspace(e.dir)
	if ws == nil {
		t.Fatal(err)
	}
	if len(ws.Environments) != 1 {
		t.Fatalf("envs: %+v", ws.Environments)
	}
	vars := ws.Environments[0].Vars
	if vars[0].Value != "http://one.test/v2" || len(vars) < 2 {
		t.Errorf("env not updated: %+v", vars)
	}
}

func TestCLIImportMultipart(t *testing.T) {
	e := newCLIEnv(t)
	spec := filepath.Join(t.TempDir(), "spec.json")
	os.WriteFile(spec, []byte(`{"openapi":"3.0.0","info":{"title":"Up"},"servers":[{"url":"http://up.test"}],
	  "paths":{"/up":{"post":{"tags":["files"],"summary":"Upload","requestBody":{"content":{"multipart/form-data":{"schema":{
	    "type":"object","required":["file","title"],"properties":{"title":{"type":"string"},"file":{"type":"string","format":"binary"}}}}}}}}}}`), 0o644)
	e.ok("import", spec)
	b, err := os.ReadFile(filepath.Join(e.dir, "requests", "files.http"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Content-Type: multipart/form-data", "\nfile: @./path/to/file\n", "\ntitle: ", "## set the file path of: file"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %q in\n%s", want, b)
		}
	}
	if probs := httpfile.Problems(string(b)); len(probs) != 0 {
		t.Errorf("%+v", probs)
	}
}

func TestCLIRunAndCurlMultipart(t *testing.T) {
	var name, file string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			w.WriteHeader(400)
			return
		}
		name = r.FormValue("name")
		if f, _, err := r.FormFile("doc"); err == nil {
			b := make([]byte, 50)
			n, _ := f.Read(b)
			file = string(b[:n])
		}
	}))
	defer srv.Close()
	e := newCLIEnv(t)
	e.writeHTTP("docs/a.txt", "contents")
	e.writeHTTP("api/up.http", "### up\nPOST "+srv.URL+"/up\nContent-Type: multipart/form-data\n\nname: bob\ndoc: @docs/a.txt\n# off: 1\n")
	e.ok("run", "api/up.http#up")
	if name != "bob" || file != "contents" {
		t.Errorf("server got %q %q", name, file)
	}
	out := e.ok("curl", "api/up.http#up")
	if !strings.Contains(out, "--form-string 'name=bob'") || !strings.Contains(out, "-F 'doc=@docs/a.txt'") ||
		strings.Contains(out, "Content-Type") || strings.Contains(out, "off") {
		t.Errorf("curl:\n%s", out)
	}
}
