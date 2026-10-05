package tui

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/iskaa02/barq/internal/core"
)

// A body over the preview size shows its start with a note, saves whole,
// and its temporary file goes when the response is replaced.
func TestLargeResponseInTUI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	body := bytes.Repeat([]byte("0123456789abcdef\n"), (core.PreviewLimit+2<<20)/17)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer srv.Close()

	ws, _ := core.LoadWorkspace(t.TempDir())
	var tm tea.Model = New(ws, nil)
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	send := func() *core.Response {
		resp, err := core.RunRequest(context.Background(), core.Request{Method: "GET", URL: srv.URL}, "/")
		if err != nil {
			t.Fatal(err)
		}
		tm, _ = tm.Update(responseMsg{tabUID: tm.(Model).cur().uid, resp: resp})
		return resp
	}

	first := send()
	m := tm.(Model)
	if text := ansi.Strip(m.responseText()); !strings.Contains(text, "showing the first 10.0 MB of 12.0 MB") {
		t.Errorf("no partial note in:\n…%s", text[max(len(text)-300, 0):])
	}
	if m.responseHeader(120) == "" || !strings.Contains(ansi.Strip(m.responseHeader(120)), "12.0 MB") {
		t.Errorf("status should show the whole size: %q", ansi.Strip(m.responseHeader(120)))
	}

	path := filepath.Join(t.TempDir(), "body.txt")
	if err := writeBody(m.cur().result, path); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); !bytes.Equal(data, body) {
		t.Errorf("saved %d bytes, want the whole %d", len(data), len(body))
	}

	tmp := first.BodyFile()
	send()
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("the replaced response's temporary file should be removed")
	}
	last := tm.(Model).cur().result.BodyFile()
	tm.(Model).Close()
	if _, err := os.Stat(last); !os.IsNotExist(err) {
		t.Error("Close should remove the remaining temporary files")
	}
}
