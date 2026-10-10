package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iskaa02/barq/internal/core"
	"github.com/iskaa02/barq/internal/httpfile"
)

func write(t *testing.T, root, rel, text string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFind(t *testing.T) {
	root := t.TempDir()
	write(t, root, "api.http", "### login\nPOST http://x/login\n\n### \nGET http://x/me\n")
	write(t, root, "sub/b.http", "### login\nGET http://x/other\n\n### only-b\nGET http://x/b\n")
	write(t, root, "node_modules/z.http", "### hidden\nGET http://x\n")
	write(t, root, ".git/z.http", "### hidden2\nGET http://x\n")

	for ref, want := range map[string]string{
		"api.http#login": "api.http#login",
		"api.http#2":     "api.http#2",
		"api.http":       "api.http#login",
		"only-b":         "sub/b.http#only-b",
		"ONLY-B":         "sub/b.http#only-b",
	} {
		got, _, err := Find(Roots{Project: root}, ref)
		if err != nil || got.Key() != want {
			t.Errorf("Find(%q) = %v, %v; want %s", ref, got.Key(), err, want)
		}
	}
	for _, ref := range []string{"login", "nope", "hidden", "api.http#9", "api.http#zzz", "missing.http"} {
		if _, _, err := Find(Roots{Project: root}, ref); err == nil {
			t.Errorf("Find(%q) should fail", ref)
		}
	}
	all, err := ListAll(Roots{Project: root})
	if err != nil || len(all) != 4 {
		t.Errorf("ListAll: %d %v", len(all), err)
	}
}

func TestSend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			w.WriteHeader(500)
			return
		}
		w.Write([]byte(`{"token":"s3cret-token-value","n":` + r.Header.Get("X-N") + `}`))
	}))
	defer srv.Close()
	root, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	write(t, root, "api.http", "### go\n# @capture token = .token\n# @expect status 200\n# @expect jq .n == 2\nGET {{base}}/ok\nX-N: {{n}}\n\n### bad\n# @capture token = .token\nGET {{base}}/bad\n")
	ws, err := core.LoadWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	err = ws.Mutate(func(w *core.Workspace) error {
		id = w.AddEnv("dev", []core.SavedHeader{{Key: "base", Value: srv.URL, Enabled: true}, {Key: "n", Value: "2", Enabled: true}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ref, req, err := Find(Roots{Project: root}, "api.http#go")
	if err != nil {
		t.Fatal(err)
	}
	if NeedsConfirm(ws, id, req) {
		t.Error("no confirm expected")
	}
	res, err := Send(context.Background(), ws, id, root, ref, req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Resp.Close()
	if res.Resp.StatusCode != 200 || len(res.Fails) != 0 || res.Expects != 2 || res.Sent.URL != srv.URL+"/ok" {
		t.Errorf("result: %+v fails %v", res.Resp, res.Fails)
	}
	if len(res.Captured) != 1 || res.Captured[0].Value != "" || !res.Captured[0].Secret {
		t.Errorf("captured: %+v", res.Captured)
	}
	if ws.EnvVars(id)["token"] != "s3cret-token-value" {
		t.Error("capture not stored")
	}
	hist, _ := core.OpenHistory(ws)
	if runs := hist.ForKey("api.http#go"); len(runs) != 1 || runs[0].Code != 200 {
		t.Errorf("history: %+v", runs)
	}

	// Failed status: no capture; failing expectation reported.
	ref, req, _ = Find(Roots{Project: root}, "api.http#bad")
	req.Expects = append(req.Expects, req.Expects...) // none
	res2, err := Send(context.Background(), ws, id, root, ref, req, Opts{Vars: map[string]string{"n": "9"}})
	if err != nil || len(res2.Captured) != 0 {
		t.Errorf("bad: %v %+v", err, res2.Captured)
	}
	res2.Resp.Close()

	// Undefined variable.
	_, req, _ = Find(Roots{Project: root}, "api.http#go")
	req.URL = "{{nope}}/x"
	if _, err := Send(context.Background(), ws, id, root, ref, req); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("missing var: %v", err)
	}
}

func TestNeedsConfirm(t *testing.T) {
	ws := &core.Workspace{Environments: []core.Environment{{ID: "p", Name: "prod", Protected: true}}}
	_, req, _ := Find(Roots{Project: writeTmp(t, "### a\nPOST http://x\n")}, "a")
	if !NeedsConfirm(ws, "p", req) || NeedsConfirm(ws, "", req) {
		t.Error("protected env should confirm writes only there")
	}
	req.Method, req.Confirm = "GET", true
	if !NeedsConfirm(ws, "", req) || EnvNeedsConfirm(ws, "p", req) {
		t.Error("@confirm should confirm anywhere")
	}
}

func writeTmp(t *testing.T, text string) string {
	root := t.TempDir()
	write(t, root, "a.http", text)
	return root
}

func TestAppendBlocks(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "x.http")
	reqs := []httpfile.Request{{Name: "one", Method: "GET", URL: "http://x/1"}, {Name: "two", Method: "POST", URL: "http://x/2", Body: "{}"}}
	if a, s, err := AppendBlocks(p, reqs); err != nil || a != 2 || s != 0 {
		t.Fatal(a, s, err)
	}
	if a, s, _ := AppendBlocks(p, append(reqs, httpfile.Request{Name: "three", URL: "http://x/3"})); a != 1 || s != 2 {
		t.Errorf("second: %d %d", a, s)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "### three\nGET") && !strings.Contains(string(b), "### three\n") {
		t.Errorf("file:\n%s", b)
	}
	if es, _ := readFile(Roots{Project: root}, "x.http"); len(es) != 3 || es[1].Req.Body != "{}" {
		t.Errorf("parsed: %+v", es)
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Log in!":       "log-in",
		"/users/{id}":   "users-id",
		"  --  ":        "request",
		"Ünïcode  Name": "n-code-name",
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestImportSaved(t *testing.T) {
	root := t.TempDir()
	ws := &core.Workspace{
		Folders: []core.Folder{{ID: "f1", Name: "Auth Stuff"}},
		Requests: []core.Request{
			{ID: "1", Name: "Log in", Folder: "f1", Method: "POST", URL: "http://x/login", Body: `{"u":1}`},
			{ID: "2", Method: "GET", URL: "http://x/users"},
		},
	}
	n, skipped, err := ImportSaved(root, ws)
	if err != nil || n != 2 || skipped != 0 {
		t.Fatalf("got %d %d %v", n, skipped, err)
	}
	b, err := os.ReadFile(filepath.Join(root, "auth-stuff", "log-in.http"))
	if err != nil {
		t.Fatal(err)
	}
	if r := httpfile.Parse(string(b)); len(r) != 1 || r[0].Method != "POST" || r[0].Body != `{"u":1}` {
		t.Errorf("round trip: %q", b)
	}
	if _, err := os.Stat(filepath.Join(root, "users.http")); err != nil {
		t.Error(err)
	}
	if n, skipped, _ = ImportSaved(root, ws); n != 0 || skipped != 2 {
		t.Errorf("second run: %d %d", n, skipped)
	}
}

func TestImportSavedCollisions(t *testing.T) {
	root := t.TempDir()
	ws := &core.Workspace{Requests: []core.Request{
		{ID: "1", Method: "GET", URL: "http://a/users"},
		{ID: "2", Method: "POST", URL: "http://a/users"},
		{ID: "3", Name: "Get User", Method: "GET", URL: "http://a/1"},
		{ID: "4", Name: "get-user", Method: "GET", URL: "http://a/2"},
		{ID: "5", Name: "Up", Method: "POST", URL: "http://a/up", BodyMode: "form",
			Form: []core.SavedHeader{{Key: "f", Value: "@x", Enabled: true}}},
	}}
	n, sk, warns, err := ImportSavedWarn(root, ws)
	if err != nil || n != 5 || sk != 0 || len(warns) != 0 {
		t.Fatalf("%d %d %v %v", n, sk, warns, err)
	}
	for _, f := range []string{"users.http", "users-2.http", "get-user.http", "get-user-2.http"} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Error(err)
		}
	}
	if n, sk, _, _ = ImportSavedWarn(root, ws); n != 0 || sk != 5 {
		t.Errorf("rerun: %d %d", n, sk)
	}
}

func TestSendNoEnvCaptureStillRecorded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"token":"abc"}`))
	}))
	defer srv.Close()
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	write(t, root, "api.http", "### go\n# @capture token = .token\n# @expect status 200\nGET "+srv.URL+"/\n")
	ws, err := core.LoadWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	ref, req, _ := Find(Roots{Project: root}, "api.http#go")
	res, err := Send(context.Background(), ws, "", root, ref, req)
	if err != nil || res.Resp == nil {
		t.Fatalf("send: %v", err)
	}
	defer res.Resp.Close()
	if res.CaptureErr == nil || res.Expects != 1 || len(res.Fails) != 0 {
		t.Errorf("CaptureErr %v expects %d fails %v", res.CaptureErr, res.Expects, res.Fails)
	}
	hist, _ := core.OpenHistory(ws)
	if len(hist.ForKey("api.http#go")) != 1 {
		t.Error("run not recorded")
	}
}

// Do must not touch the workspace: reload it concurrently (run with -race).
func TestDoIndependentOfWorkspace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.Write([]byte(`{"token":"abc"}`))
	}))
	defer srv.Close()
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	write(t, root, "api.http", "### go\n# @capture token = .token\nGET {{base}}/\n")
	ws, err := core.LoadWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	_ = ws.Mutate(func(w *core.Workspace) error {
		id = w.AddEnv("dev", []core.SavedHeader{{Key: "base", Value: srv.URL, Enabled: true}})
		return nil
	})
	ref, req, _ := Find(Roots{Project: root}, "api.http#go")
	p, err := Prepare(ws, id, root, ref, req)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan Raw)
	go func() { done <- p.Do(context.Background()) }()
	for i := 0; i < 20; i++ {
		_ = ws.ReloadContent()
		_ = ws.EnvVars(id)
	}
	raw := <-done
	res, err := p.Finish(ws, raw)
	if err != nil || res.CaptureErr != nil {
		t.Fatalf("finish: %v %v", err, res.CaptureErr)
	}
	res.Resp.Close()
	if ws.EnvVars(id)["token"] != "abc" {
		t.Error("capture not stored")
	}
}

func TestSendMultipart(t *testing.T) {
	var got struct{ name, file, fname string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		got.name = r.FormValue("name")
		f, h, err := r.FormFile("doc")
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		buf := make([]byte, 100)
		n, _ := f.Read(buf)
		got.file, got.fname = string(buf[:n]), h.Filename
		if r.FormValue("off") != "" {
			t.Error("disabled field sent")
		}
	}))
	defer srv.Close()
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	write(t, root, "files/a.txt", "hello file")
	// The .http file lives in a subdirectory: paths are still relative to root.
	write(t, root, "api/up.http", "### up\nPOST "+srv.URL+"/up\nContent-Type: multipart/form-data\n\nname: {{user}}\ndoc: @files/a.txt\n# off: 1\n")
	ws, err := core.LoadWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	ref, req, err := Find(Roots{Project: root}, "api/up.http#up")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Send(context.Background(), ws, "", root, ref, req, Opts{Vars: map[string]string{"user": "ann"}}); err != nil {
		t.Fatal(err)
	}
	if got.name != "ann" || got.file != "hello file" || got.fname != "a.txt" {
		t.Errorf("%+v", got)
	}
}

func TestBodyFileRelativeToRoot(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 100)
		n, _ := r.Body.Read(b)
		body = string(b[:n])
	}))
	defer srv.Close()
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	write(t, root, "data/p.txt", "payload")
	write(t, root, "api/x.http", "### x\nPOST "+srv.URL+"\n\n< data/p.txt\n")
	ws, _ := core.LoadWorkspace(root)
	ref, req, err := Find(Roots{Project: root}, "api/x.http#x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Send(context.Background(), ws, "", root, ref, req); err != nil || body != "payload" {
		t.Errorf("%q %v", body, err)
	}
}

func TestRoots(t *testing.T) {
	proj, store := t.TempDir(), t.TempDir()
	rt := Roots{Project: proj, Store: store}
	for _, p := range []string{"api.http", "sub/b.http", ".barq/x.http", ".barq/d/y.http"} {
		abs := rt.Abs(p)
		if got, ok := rt.RefPath(abs); !ok || got != p {
			t.Errorf("round trip %q -> %q -> %q %v", p, abs, got, ok)
		}
		if rt.IsStore(p) != strings.HasPrefix(p, ".barq/") {
			t.Errorf("IsStore(%q)", p)
		}
	}
	if rt.Abs(".barq/d/y.http") != filepath.Join(store, "d", "y.http") || rt.Abs("sub/b.http") != filepath.Join(proj, "sub", "b.http") {
		t.Error("Abs")
	}
	if _, ok := rt.RefPath(filepath.Join(t.TempDir(), "o.http")); ok {
		t.Error("outside both roots")
	}
	if _, ok := rt.RefPath(store); ok {
		t.Error("the root itself is not a file")
	}
	// A store inside the project (project = HOME) is still the store.
	nested := Roots{Project: proj, Store: filepath.Join(proj, ".barq", "requests", "p")}
	if got, _ := nested.RefPath(filepath.Join(nested.Store, "a.http")); got != ".barq/a.http" {
		t.Errorf("nested store: %q", got)
	}
}

func TestTwoRoots(t *testing.T) {
	proj, store := t.TempDir(), t.TempDir()
	rt := Roots{Project: proj, Store: store}
	write(t, proj, "api.http", "### login\nGET http://x/p\n\n### only-proj\nGET http://x/1\n")
	write(t, store, "api.http", "### login\nGET http://x/s\n\n### only-store\nGET http://x/2\n")
	write(t, store, "d/z.http", "GET http://x/z\n")
	write(t, proj, ".barq/hidden.http", "GET http://x\n") // dot-dir: never scanned

	all, err := ListAll(rt)
	if err != nil || len(all) != 5 {
		t.Fatalf("ListAll: %d %v", len(all), err)
	}
	keys := map[string]bool{}
	for _, e := range all {
		keys[e.Ref.Key()] = true
	}
	for _, k := range []string{"api.http#login", ".barq/api.http#login", "api.http#only-proj", ".barq/api.http#only-store", ".barq/d/z.http#1"} {
		if !keys[k] {
			t.Errorf("missing key %s in %v", k, keys)
		}
	}
	ref, req, err := Find(rt, ".barq/api.http#login")
	if err != nil || req.URL != "http://x/s" || ref.File(rt) != filepath.Join(store, "api.http") {
		t.Errorf("store find: %v %v %v", ref, req.URL, err)
	}
	if ref, req, err = Find(rt, "api.http#login"); err != nil || req.URL != "http://x/p" || ref.Key() != "api.http#login" {
		t.Errorf("project find: %v %v %v", ref, req.URL, err)
	}
	if _, req, err = Find(rt, filepath.Join(store, "d", "z.http")); err != nil || req.URL != "http://x/z" {
		t.Errorf("abs find: %v %v", req.URL, err)
	}
	if _, req, err = Find(rt, "only-store"); err != nil || req.URL != "http://x/2" {
		t.Errorf("bare store name: %v %v", req.URL, err)
	}
	_, _, err = Find(rt, "login")
	if err == nil || !strings.Contains(err.Error(), "api.http#login") || !strings.Contains(err.Error(), ".barq/api.http#login") {
		t.Errorf("ambiguous bare name: %v", err)
	}
}

func TestMissingStore(t *testing.T) {
	proj := t.TempDir()
	store := filepath.Join(t.TempDir(), "nope")
	write(t, proj, "a.http", "GET http://x\n")
	all, err := ListAll(Roots{Project: proj, Store: store})
	if err != nil || len(all) != 1 {
		t.Fatalf("%d %v", len(all), err)
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Error("scan must not create the store")
	}
}
