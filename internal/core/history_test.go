package core

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestHistory(t *testing.T) (*History, *Workspace) {
	t.Setenv("HOME", t.TempDir())
	ws, err := LoadWorkspace("/proj")
	if err != nil {
		t.Fatal(err)
	}
	h, err := OpenHistory(ws)
	if err != nil {
		t.Fatal(err)
	}
	return h, ws
}

func addRun(t *testing.T, h *History, key, url string) *HistEntry {
	r := Request{Method: "GET", URL: url}
	e := &HistEntry{
		Meta:    HistMeta{ID: NewID(), Key: key, Time: time.Now(), Method: "GET", URL: url, Code: 200, ReqHash: requestHash(r)},
		Request: r, Sent: r, Body: []byte(`{"ok":true}`),
	}
	if err := h.add(e); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestHistoryRecordAndReopen(t *testing.T) {
	h, ws := newTestHistory(t)
	a1 := addRun(t, h, "a", "x/1")
	addRun(t, h, "b", "y/1")
	a2 := addRun(t, h, "a", "x/2")

	runs := h.ForKey("a")
	if len(runs) != 2 || runs[0].ID != a2.Meta.ID || runs[1].ID != a1.Meta.ID {
		t.Fatalf("forKey should be newest first: %+v", runs)
	}
	if prev, ok := h.Previous(a2.Meta.ID); !ok || prev.ID != a1.Meta.ID {
		t.Errorf("previous(a2) = %v %v", prev, ok)
	}
	if _, ok := h.Previous(a1.Meta.ID); ok {
		t.Error("first run has no previous")
	}
	if runs[0].ReqHash == runs[1].ReqHash {
		t.Error("different URLs should hash differently")
	}

	// A fresh process sees the same runs, and full entries load on demand.
	h2, err := OpenHistory(ws)
	if err != nil || len(h2.Index) != 3 {
		t.Fatalf("reopen: %d runs, %v", len(h2.Index), err)
	}
	e, err := h2.Load(a2.Meta.ID)
	if err != nil || string(e.Body) != `{"ok":true}` || e.Request.URL != "x/2" {
		t.Errorf("load: %+v %v", e, err)
	}
}

func TestHistoryRekeyAndRemove(t *testing.T) {
	h, ws := newTestHistory(t)
	r := addRun(t, h, "draft", "x")
	if err := h.Rekey("draft", "saved"); err != nil {
		t.Fatal(err)
	}
	h2, _ := OpenHistory(ws)
	if len(h2.ForKey("saved")) != 1 || len(h2.ForKey("draft")) != 0 {
		t.Error("rekey wasn't persisted")
	}
	if err := h2.Remove(func(m HistMeta) bool { return m.Key == "saved" }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(h2.runPath(r.Meta.ID)); !os.IsNotExist(err) {
		t.Error("run file should be deleted with its entry")
	}
}

func TestHistoryPrune(t *testing.T) {
	h, _ := newTestHistory(t)
	first := addRun(t, h, "a", "x")
	for i := 0; i < histPerRequest; i++ {
		addRun(t, h, "a", "x")
	}
	if n := len(h.ForKey("a")); n != histPerRequest {
		t.Errorf("kept %d runs, want %d", n, histPerRequest)
	}
	if _, err := os.Stat(h.runPath(first.Meta.ID)); !os.IsNotExist(err) {
		t.Error("oldest run should have been pruned")
	}
	files, _ := filepath.Glob(filepath.Join(h.dir, "runs", "*.json"))
	if len(files) != histPerRequest {
		t.Errorf("%d run files on disk, want %d", len(files), histPerRequest)
	}
}

func TestHistoryPruneBySize(t *testing.T) {
	h, _ := newTestHistory(t)
	old := histMaxBytes
	histMaxBytes = 3 << 20
	defer func() { histMaxBytes = old }()

	big := make([]byte, 1<<20)
	var ids []string
	for i := 0; i < 5; i++ {
		e := &HistEntry{Meta: HistMeta{ID: NewID(), Key: "k", Size: len(big)}, Body: big}
		if err := h.add(e); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.Meta.ID)
	}
	if n := len(h.Index); n < 2 || n > 3 {
		t.Errorf("kept %d runs of 1 MB under a 3 MB budget", n)
	}
	if _, err := os.Stat(h.bodyPath(ids[0])); !os.IsNotExist(err) {
		t.Error("oldest body file should be gone")
	}
	if e, err := h.Load(ids[4]); err != nil || len(e.Body) != len(big) {
		t.Errorf("newest run should load with its body: %v", err)
	}
}

func TestHistoryLoadsOldFormat(t *testing.T) {
	h, _ := newTestHistory(t)
	// Runs saved before .body files kept the body inside the JSON.
	if err := os.MkdirAll(filepath.Join(h.dir, "runs"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := `{"meta":{"id":"old1","key":"k"},"body":"eyJhIjoxfQ=="}` // {"a":1}
	if err := os.WriteFile(h.runPath("old1"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := h.Load("old1")
	if err != nil || string(e.Body) != `{"a":1}` {
		t.Errorf("old-format run: %q %v", e.Body, err)
	}
}
