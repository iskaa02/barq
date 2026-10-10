package ntui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScratchBlockURL(t *testing.T) {
	b, line, err := scratchBlock("https://example.com/x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(b, "### GET example.com/x\n") || !strings.Contains(b, "GET https://example.com/x") || line != 1 {
		t.Fatalf("got %q line %d", b, line)
	}
}

func TestScratchBlockCurl(t *testing.T) {
	b, _, err := scratchBlock(`curl -X POST https://example.com/a -H 'X-A: 1' -d '{"a":1}'`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"### POST example.com/a", "POST https://example.com/a", "X-A: 1", `{"a":1}`} {
		if !strings.Contains(b, want) {
			t.Errorf("missing %q in %q", want, b)
		}
	}
}

func TestAppendScratch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not", "yet") // the store is created on first write
	if _, _, err := AppendScratch(dir, "https://a.test/1"); err != nil {
		t.Fatal(err)
	}
	p, line, err := AppendScratch(dir, "https://b.test/2")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ScratchFile))
	if got := strings.Split(string(data), "\n")[line-1]; got != "GET https://b.test/2" {
		t.Fatalf("line %d is %q in %q (%s)", line, got, data, p)
	}
}

func TestScratchFormMultipart(t *testing.T) {
	arg := `curl https://example.com/up -F a=1 -F file=@x.png`
	b, _, err := scratchBlock(arg)
	if err != nil || !strings.Contains(b, "Content-Type: multipart/form-data\n\na: 1\nfile: @x.png\n") || strings.Contains(b, "\n##") {
		t.Errorf("%v %q", err, b)
	}
	if w := ScratchWarnings(arg); len(w) != 0 {
		t.Errorf("%v", w)
	}
}
