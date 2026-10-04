package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestMigrateSingleFileWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws, _ := loadWorkspace("/proj")
	old := `{"cwd":"/proj","requests":[{"id":"r1","name":"A","method":"GET","url":"x"}],
	  "tabs":[{"saved_id":"r1","request":{"id":"r1","name":"A","method":"GET","url":"x?edited"}}],
	  "active_tab":0,"recent":["req:r1"]}`
	os.MkdirAll(filepath.Dir(ws.path), 0o700)
	if err := os.WriteFile(ws.path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}

	ws, err := loadWorkspace("/proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(ws.Requests) != 1 || len(ws.Tabs) != 1 || ws.Tabs[0].Request.URL != "x?edited" || ws.Recent[0] != "req:r1" {
		t.Fatalf("migration lost data: %+v", ws)
	}
	if err := ws.save(); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(ws.path)
	if strings.Contains(string(content), "tabs") {
		t.Error("tabs should move to the session file")
	}
	again, _ := loadWorkspace("/proj")
	if len(again.Tabs) != 1 || again.Tabs[0].Request.URL != "x?edited" {
		t.Errorf("session not restored after the split: %+v", again.Tabs)
	}
}

func TestMutateKeepsBothWriters(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tui, _ := loadWorkspace("/proj")
	cli, _ := loadWorkspace("/proj") // loaded before tui writes: stale

	if err := tui.mutate(func(w *workspace) error { w.addRequest(request{Name: "from tui"}); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := cli.mutate(func(w *workspace) error { w.addRequest(request{Name: "from cli"}); return nil }); err != nil {
		t.Fatal(err)
	}
	if !tui.changedOnDisk() {
		t.Error("tui should notice the cli's write")
	}
	tui.reloadContent()
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
			ws, _ := loadWorkspace("/proj") // each like a separate process
			if err := ws.mutate(func(w *workspace) error { w.addRequest(request{}); return nil }); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	ws, _ := loadWorkspace("/proj")
	if len(ws.Requests) != 20 {
		t.Errorf("%d of 20 concurrent writes kept", len(ws.Requests))
	}
}

func TestTUIPicksUpCLIChanges(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws, _ := loadWorkspace("/proj")
	ws.mutate(func(w *workspace) error {
		w.addRequest(request{ID: "clean", Name: "Clean", Method: "GET", URL: "a"})
		w.addRequest(request{ID: "dirty", Name: "Dirty", Method: "GET", URL: "b"})
		w.addRequest(request{ID: "gone", Name: "Gone", Method: "GET", URL: "c"})
		return nil
	})
	m := newModel(ws, nil)
	m.openSaved(ws.find("clean"))
	m.openSaved(ws.find("gone"))
	m.openSaved(ws.find("dirty"))
	m.url.SetValue("b?edited") // unsaved edit in the active tab

	cli, _ := loadWorkspace("/proj")
	cli.mutate(func(w *workspace) error {
		w.updateRequest("clean", func(r *request) { r.URL = "a2"; r.Name = "Renamed" })
		w.updateRequest("dirty", func(r *request) { r.URL = "b2" })
		return w.deleteRequest("gone")
	})

	m.syncFromDisk()
	if tb := m.tabs[m.tabIndexOfSaved("clean")]; tb.req.URL != "a2" {
		t.Errorf("clean tab should take the new version: %q", tb.req.URL)
	}
	if m.url.Value() != "b?edited" {
		t.Errorf("dirty active tab lost its edit: %q", m.url.Value())
	}
	for _, tb := range m.tabs {
		if tb.req.Name == "Gone" && tb.savedID != "" {
			t.Error("tab of a deleted request should become a draft")
		}
	}
	if m.tabName(m.tabIndexOfSaved("clean")) != "Renamed" {
		t.Error("tab name should follow the rename")
	}
}

func (m model) tabIndexOfSaved(id string) int {
	for i, tb := range m.tabs {
		if tb.savedID == id {
			return i
		}
	}
	return -1
}
