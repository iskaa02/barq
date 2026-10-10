package ntui_test

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSmoke runs `barq` in tmux against a local server and sends a request.
func TestSmoke(t *testing.T) {
	for _, tool := range []string{"tmux", "nvim", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "barq")
	if out, err := exec.Command("go", "build", "-o", bin, "../..").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	www := filepath.Join(tmp, "www")
	os.MkdirAll(www, 0o755)
	os.WriteFile(filepath.Join(www, "data.json"), []byte(`{"smoke":"works"}`), 0o644)
	srv := exec.Command("python3", "-m", "http.server", fmt.Sprint(port), "--bind", "127.0.0.1")
	srv.Dir = www
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { srv.Process.Kill(); srv.Wait() }()

	proj := filepath.Join(tmp, "proj")
	os.MkdirAll(proj, 0o755)
	os.WriteFile(filepath.Join(proj, "a.http"),
		[]byte(fmt.Sprintf("### data\n# @expect status 200\nGET http://127.0.0.1:%d/data.json\n", port)), 0o644)
	home := filepath.Join(tmp, "home")
	os.MkdirAll(home, 0o755)

	sess := fmt.Sprintf("barqsmoke%d", os.Getpid())
	tm := func(args ...string) string {
		out, _ := exec.Command("tmux", args...).CombinedOutput()
		return string(out)
	}
	defer tm("kill-session", "-t", sess)
	tm("new-session", "-d", "-s", sess, "-x", "140", "-y", "30",
		fmt.Sprintf("cd %s && HOME=%s %s", proj, home, bin))

	waitFor := func(what string) string {
		var screen string
		for range 50 {
			screen = tm("capture-pane", "-p", "-t", sess)
			if strings.Contains(screen, what) {
				return screen
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("never saw %q:\n%s", what, screen)
		return ""
	}
	waitFor("GET http://127.0.0.1")
	tm("send-keys", "-t", sess, "j", "j", "M-Enter")
	screen := waitFor(`"smoke": "works"`)
	if !strings.Contains(screen, "200 OK") || !strings.Contains(screen, "✓ 1") {
		t.Errorf("title missing status or expectation:\n%s", screen)
	}
}
