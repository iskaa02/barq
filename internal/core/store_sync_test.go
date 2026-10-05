package core

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestMigrateSingleFileWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws, _ := LoadWorkspace("/proj")
	old := `{"cwd":"/proj","requests":[{"id":"r1","name":"A","method":"GET","url":"x"}],
	  "tabs":[{"saved_id":"r1","request":{"id":"r1","name":"A","method":"GET","url":"x?edited"}}],
	  "active_tab":0,"recent":["req:r1"]}`
	os.MkdirAll(filepath.Dir(ws.Path), 0o700)
	if err := os.WriteFile(ws.Path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}

	ws, err := LoadWorkspace("/proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(ws.Requests) != 1 || len(ws.Tabs) != 1 || ws.Tabs[0].Request.URL != "x?edited" || ws.Recent[0] != "req:r1" {
		t.Fatalf("migration lost data: %+v", ws)
	}
	if err := ws.save(); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(ws.Path)
	if strings.Contains(string(content), "tabs") {
		t.Error("tabs should move to the session file")
	}
	again, _ := LoadWorkspace("/proj")
	if len(again.Tabs) != 1 || again.Tabs[0].Request.URL != "x?edited" {
		t.Errorf("session not restored after the split: %+v", again.Tabs)
	}
}

func TestMutateKeepsBothWriters(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tui, _ := LoadWorkspace("/proj")
	cli, _ := LoadWorkspace("/proj") // loaded before tui writes: stale

	if err := tui.Mutate(func(w *Workspace) error { w.AddRequest(Request{Name: "from tui"}); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := cli.Mutate(func(w *Workspace) error { w.AddRequest(Request{Name: "from cli"}); return nil }); err != nil {
		t.Fatal(err)
	}
	if !tui.ChangedOnDisk() {
		t.Error("tui should notice the cli's write")
	}
	tui.ReloadContent()
	if len(tui.Requests) != 2 {
		t.Errorf("lost a write: %+v", tui.Requests)
	}
}

func TestMutateConcurrent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ws, _ := LoadWorkspace("/proj") // each like a separate process
			if err := ws.Mutate(func(w *Workspace) error { w.AddRequest(Request{}); return nil }); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	ws, _ := LoadWorkspace("/proj")
	if len(ws.Requests) != 20 {
		t.Errorf("%d of 20 concurrent writes kept", len(ws.Requests))
	}
}
