package ntui

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPickerList(t *testing.T) {
	d := t.TempDir()
	for _, f := range []string{"a.http", "sub/b.http", "Dockerfile", ".hid/c.http", "node_modules/d.http"} {
		p := filepath.Join(d, f)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("GET http://x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := pickerList(d), []string{"a.http", "sub/b.http"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	a := &App{cwd: d}
	a.snapshotPickerFiles()
	pickerFiles.Lock()
	defer pickerFiles.Unlock()
	if pickerFiles.cwd != d || len(pickerFiles.list) != 2 {
		t.Fatalf("snapshot %v %v", pickerFiles.cwd, pickerFiles.list)
	}
}
