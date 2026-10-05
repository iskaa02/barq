package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/iskaa02/barq/internal/core"
)

func filterModel(t *testing.T, query string) Model {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	ws, _ := core.LoadWorkspace(t.TempDir())
	ws.Folders = []core.Folder{
		{ID: "admin", Name: "Admin"},
		{ID: "stores", Name: "Stores", Parent: "admin", Collapsed: true},
		{ID: "auth", Name: "Auth", Collapsed: true},
		{ID: "empty", Name: "Empty Stores"},
	}
	ws.Requests = []core.Request{
		{ID: "r1", Name: "List stores", Method: "GET", URL: "{{baseUrl}}/admin/stores", Folder: "stores"},
		{ID: "r2", Name: "Create store", Method: "POST", URL: "{{baseUrl}}/admin/stores", Folder: "stores"},
		{ID: "r3", Name: "Log in", Method: "POST", URL: "{{baseUrl}}/auth/login", Folder: "auth"},
		{ID: "r4", Name: "Health", Method: "GET", URL: "{{baseUrl}}/health"},
	}
	m := New(ws, nil)
	m.sideFilter.SetValue(query)
	return m
}

func rowIDs(m Model) string {
	var ids []string
	for _, r := range m.sideRows() {
		ids = append(ids, r.id)
	}
	return strings.Join(ids, " ")
}

func TestSidebarFilter(t *testing.T) {
	for query, want := range map[string]string{
		"":            "admin stores auth empty r4", // no filter: folds respected
		"post stores": "admin stores r2",            // every word must match
		"LOGIN":       "auth r3",                    // case-insensitive, folded folder opened
		"admin":       "admin stores r1 r2",         // folder path matches its contents
		"empty":       "empty",                      // matching folder shown even if empty
		"nothing":     "",
	} {
		if got := rowIDs(filterModel(t, query)); got != want {
			t.Errorf("filter %q: rows %q, want %q", query, got, want)
		}
	}
	// Filtering doesn't change the saved fold state.
	m := filterModel(t, "login")
	if !m.ws.Folders[m.ws.FindFolder("auth")].Collapsed {
		t.Error("filter unfolded the folder for real")
	}
}

func TestHighlightTerms(t *testing.T) {
	got := highlightTerms("Create store", []string{"store", "cr"}, filterHit)
	if ansi.Strip(got) != "Create store" {
		t.Errorf("text changed: %q", ansi.Strip(got))
	}
}
