package tui

import (
	"testing"

	"github.com/iskaa02/barq/internal/core"
)

func TestTUIPicksUpCLIChanges(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws, _ := core.LoadWorkspace("/proj")
	ws.Mutate(func(w *core.Workspace) error {
		w.AddRequest(core.Request{ID: "clean", Name: "Clean", Method: "GET", URL: "a"})
		w.AddRequest(core.Request{ID: "dirty", Name: "Dirty", Method: "GET", URL: "b"})
		w.AddRequest(core.Request{ID: "gone", Name: "Gone", Method: "GET", URL: "c"})
		return nil
	})
	m := New(ws, nil)
	m.openSaved(ws.Find("clean"))
	m.openSaved(ws.Find("gone"))
	m.openSaved(ws.Find("dirty"))
	m.url.SetValue("b?edited") // unsaved edit in the active tab

	cli, _ := core.LoadWorkspace("/proj")
	cli.Mutate(func(w *core.Workspace) error {
		w.UpdateRequest("clean", func(r *core.Request) { r.URL = "a2"; r.Name = "Renamed" })
		w.UpdateRequest("dirty", func(r *core.Request) { r.URL = "b2" })
		return w.DeleteRequest("gone")
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

func (m Model) tabIndexOfSaved(id string) int {
	for i, tb := range m.tabs {
		if tb.savedID == id {
			return i
		}
	}
	return -1
}
