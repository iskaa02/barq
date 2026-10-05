package core

import (
	"io"
	"os"
	"path/filepath"
	"strings"
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
		Request: r, Sent: r,
	}
	body := func(w io.Writer) error { _, err := io.WriteString(w, `{"ok":true}`); return err }
	if err := h.add(e, body); err != nil {
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
	write := func(w io.Writer) error { _, err := w.Write(big); return err }
	var ids []string
	for i := 0; i < 5; i++ {
		e := &HistEntry{Meta: HistMeta{ID: NewID(), Key: "k", Size: int64(len(big))}}
		if err := h.add(e, write); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.Meta.ID)
	}
	// Bodies go first; the runs themselves stay.
	if n := len(h.Index); n != 5 {
		t.Errorf("kept %d runs, want all 5 (only bodies pruned)", n)
	}
	pruned := 0
	for _, m := range h.Index {
		if m.BodyPruned {
			pruned++
		}
	}
	if pruned < 2 || pruned > 3 {
		t.Errorf("pruned %d bodies of 1 MB under a 3 MB budget", pruned)
	}
	if _, err := os.Stat(h.bodyPath(ids[0])); !os.IsNotExist(err) {
		t.Error("oldest body file should be gone")
	}
	if e, err := h.Load(ids[0]); err != nil || !e.Meta.BodyPruned || len(e.Body) != 0 {
		t.Errorf("a pruned run should load without its body: %v", err)
	}
	if e, err := h.Load(ids[4]); err != nil || len(e.Body) != len(big) {
		t.Errorf("newest run should load with its body: %v", err)
	}

	// The pruning is recorded in the index, for other processes too.
	h2, _ := OpenHistory(&Workspace{Path: strings.TrimSuffix(h.dir, ".history") + ".json"})
	if !h2.Index[0].BodyPruned {
		t.Error("reopened index should know the body was pruned")
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
