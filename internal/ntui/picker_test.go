package ntui

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/iskaa02/barq/internal/runner"
)

func TestPickerList(t *testing.T) {
	d, st := t.TempDir(), filepath.Join(t.TempDir(), "store")
	for _, f := range []string{"a.http", "sub/b.http", "Dockerfile", ".hid/c.http", "node_modules/d.http", "../store/s.http", "../store/x/t.http"} {
		p := filepath.Join(d, f)
		if strings.HasPrefix(f, "../store") {
			p = filepath.Join(st, strings.TrimPrefix(f, "../store"))
		}
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("GET http://x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want := []pickerItem{
		{filepath.Join(st, "s.http"), ".barq/s.http", true},
		{filepath.Join(st, "x", "t.http"), ".barq/x/t.http", true},
		{filepath.Join(d, "a.http"), "a.http", false},
		{filepath.Join(d, "sub", "b.http"), "sub/b.http", false},
	}
	if got := pickerList(runner.Roots{Project: d, Store: st}); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	a := &App{cwd: d, store: st}
	a.snapshotPickerFiles()
	pickerFiles.Lock()
	defer pickerFiles.Unlock()
	if pickerFiles.cwd != d || len(pickerFiles.list) != 4 {
		t.Fatalf("snapshot %v %v", pickerFiles.cwd, pickerFiles.list)
	}
}

func TestPickerPayloadUsesLowerCaseKeys(t *testing.T) {
	pickerFiles.Lock()
	pickerFiles.cwd = "/proj"
	pickerFiles.list = []pickerItem{{Path: "/proj/a.http", Label: "a.http"}, {Path: "/s/b.http", Label: ".barq/b.http", Store: true}}
	pickerFiles.Unlock()
	res, _ := pickerPayload()
	files, ok := res["files"].([]map[string]any)
	if !ok || len(files) != 2 {
		t.Fatalf("files = %#v", res["files"])
	}
	if files[1]["path"] != "/s/b.http" || files[1]["label"] != ".barq/b.http" || files[1]["store"] != true {
		t.Errorf("item = %#v", files[1])
	}
	if res["cwd"] != "/proj" {
		t.Errorf("cwd = %#v", res["cwd"])
	}
}
