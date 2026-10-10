package nvimpane

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func needNvim(t *testing.T) {
	if _, err := exec.LookPath("nvim"); err != nil {
		t.Skip("nvim not installed")
	}
}

// waitLines waits until the current buffer is exactly the given lines.
func waitLines(t *testing.T, p *Pane, want ...string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var got []string
	for time.Now().Before(deadline) {
		if l, err := p.Lines(); err == nil {
			got = l
			if strings.Join(l, "\n") == strings.Join(want, "\n") {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("buffer = %q, want %q", got, want)
}

func alive(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	return err == nil && !strings.Contains(string(b), ") Z")
}

func waitDead(t *testing.T, pid int, d time.Duration) {
	t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if !alive(pid) {
			return
		}
	}
	t.Fatalf("process %d still alive", pid)
}

func TestStaleSwapFileDoesNotHang(t *testing.T) {
	needNvim(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "r.http")
	os.WriteFile(file, []byte("GET example.com\n"), 0o644)
	// Leave a swap file behind: an nvim that is killed -9 while editing.
	old := exec.Command("nvim", "--headless", "--clean", "--cmd", "set directory="+dir, file, "-c", "sleep 30")
	if err := old.Start(); err != nil {
		t.Fatal(err)
	}
	swap := filepath.Join(dir, "r.http.swp")
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(swap); err == nil {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	old.Process.Kill()
	old.Wait()
	if _, err := os.Stat(swap); err != nil {
		t.Skip("no swap file created")
	}

	done := make(chan *Pane)
	go func() {
		p, err := New(40, 10, Options{Args: []string{"--clean", "--cmd", "set directory=" + dir}})
		if err != nil {
			t.Error(err)
		}
		if err == nil {
			err = p.OpenAsync(file)
		}
		if err != nil {
			t.Error(err)
		}
		done <- p
	}()
	var p *Pane
	select {
	case p = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("New + OpenAsync hung")
	}
	if p == nil {
		return
	}
	defer p.Close()
	waitLines(t, p, "GET example.com")
}

func TestBlocking(t *testing.T) {
	needNvim(t)
	p, err := New(40, 10, Options{Args: []string{"--clean"}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.Blocking() {
		t.Fatal("blocking at start")
	}
	_, _ = p.v.Input(`:echo "a\nb\nc"` + "<CR>") // hit-enter prompt
	deadline := time.Now().Add(2 * time.Second)
	for !p.Blocking() {
		if time.Now().After(deadline) {
			t.Fatal("never blocking")
		}
		time.Sleep(20 * time.Millisecond)
	}
	done := make(chan error, 3)
	go func() {
		_, err := p.Lines()
		done <- err
		done <- p.Command("echo 1")
		_, _, _, _, err = p.Current()
		done <- err
	}()
	for range 3 {
		select {
		case err := <-done:
			if !errors.Is(err, ErrBlocked) {
				t.Errorf("err = %v, want ErrBlocked", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("helper hung while blocked")
		}
	}
	p.Key(tea.KeyPressMsg{Code: tea.KeyEnter})
	deadline = time.Now().Add(2 * time.Second)
	for p.Blocking() {
		if time.Now().After(deadline) {
			t.Fatal("still blocking after <CR>")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := p.Lines(); err != nil {
		t.Errorf("Lines after unblock: %v", err)
	}
}

func TestCloseKillsNvim(t *testing.T) {
	needNvim(t)
	p, err := New(40, 10, Options{Args: []string{"--clean"}})
	if err != nil {
		t.Fatal(err)
	}
	pid := p.Pid()
	p.Close()
	p.Close() // harmless twice
	if alive(pid) {
		t.Fatal("nvim alive after Close")
	}
	if err := syscall.Kill(-pid, 0); err == nil {
		t.Fatal("process group still exists after Close")
	}
}

// TestHelperPane is not a test: it is the child process of TestOrphan.
func TestHelperPane(t *testing.T) {
	if os.Getenv("BARQ_PANE_HELPER") == "" {
		t.Skip("helper")
	}
	p, err := New(40, 10, Options{Args: []string{"--clean"}})
	if err != nil {
		fmt.Println("error", err)
		os.Exit(1)
	}
	fmt.Println(p.Pid())
	time.Sleep(time.Minute)
}

func TestNvimDiesWithParent(t *testing.T) {
	needNvim(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperPane$")
	cmd.Env = append(os.Environ(), "BARQ_PANE_HELPER=1")
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	sc := bufio.NewScanner(out)
	if !sc.Scan() {
		t.Fatal("helper printed nothing")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(sc.Text()))
	if err != nil {
		t.Fatalf("helper said %q", sc.Text())
	}
	if !alive(pid) {
		t.Fatal("nvim not running")
	}
	cmd.Process.Kill()
	waitDead(t, pid, 2*time.Second)
}
