package core

import (
	"reflect"
	"strings"
	"testing"
)

func TestWorkspaceRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ws, err := LoadWorkspace("/some/project")
	if err != nil {
		t.Fatal(err)
	}
	r := Request{ID: NewID(), Name: "List", Method: "GET", URL: "x/users",
		Headers: []SavedHeader{{Key: "A", Value: "b", Enabled: true}}}
	ws.Requests = append(ws.Requests, r)
	ws.Tabs = []SavedTab{{SavedID: r.ID, Request: r}}
	if err := ws.save(); err != nil {
		t.Fatal(err)
	}

	got, err := LoadWorkspace("/some/project")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Requests, ws.Requests) || !reflect.DeepEqual(got.Tabs, ws.Tabs) {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, ws)
	}

	other, _ := LoadWorkspace("/other/project")
	if len(other.Requests) != 0 || other.Path == ws.Path {
		t.Errorf("workspaces for different directories should be separate")
	}
	if !strings.Contains(ws.Path, "/.barq/workspaces/") {
		t.Errorf("unexpected path %s", ws.Path)
	}
}

func TestSuggestedName(t *testing.T) {
	for url, want := range map[string]string{
		"https://api.x.com/v1/users?page=2": "/v1/users",
		"localhost:8080/":                   "localhost:8080",
		"api.x.com":                         "api.x.com",
	} {
		if got := (Request{URL: url}).SuggestedName(); got != want {
			t.Errorf("%s: got %q, want %q", url, got, want)
		}
	}
}

func TestRepairAndSubtree(t *testing.T) {
	ws := &Workspace{
		Folders: []Folder{
			{ID: "a", Name: "A"},
			{ID: "b", Name: "B", Parent: "a"},
			{ID: "c", Name: "C", Parent: "missing"},
			{ID: "x", Name: "X", Parent: "y"}, // x and y form a cycle
			{ID: "y", Name: "Y", Parent: "x"},
		},
		Requests: []Request{
			{ID: "1", Folder: "b"},
			{ID: "2", Folder: "gone"},
		},
	}
	ws.repair()

	if p := ws.Folders[2].Parent; p != "" {
		t.Errorf("folder with missing parent should move to top level, got %q", p)
	}
	if ws.Folders[3].Parent != "" && ws.Folders[4].Parent != "" {
		t.Errorf("cycle x<->y was not broken")
	}
	if ws.Requests[1].Folder != "" {
		t.Errorf("request in missing folder should move to top level")
	}
	if got := ws.FolderPath("b"); got != "A / B" {
		t.Errorf("folderPath = %q", got)
	}
	if !reflect.DeepEqual(ws.Subtree("a"), map[string]bool{"a": true, "b": true}) {
		t.Errorf("subtree(a) = %v", ws.Subtree("a"))
	}
	if n := ws.CountIn("a"); n != 1 {
		t.Errorf("countIn(a) = %d, want 1", n)
	}
}
