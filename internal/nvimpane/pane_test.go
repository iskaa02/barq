package nvimpane

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestTypeAndRead(t *testing.T) {
	if _, err := exec.LookPath("nvim"); err != nil {
		t.Skip("nvim not installed")
	}
	file := filepath.Join(t.TempDir(), "r.http")
	os.WriteFile(file, []byte("GET example.com\n"), 0o644)
	p, err := New(40, 10, Options{Args: []string{"--clean"}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.OpenAsync(file); err != nil {
		t.Fatal(err)
	}
	waitLines(t, p, "GET example.com")
	typeKeys := func(s string) {
		for _, r := range s {
			p.Key(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
	}
	esc := func() { p.Key(tea.KeyPressMsg{Code: tea.KeyEscape}) }
	typeKeys("A/users")
	esc()
	typeKeys("oAccept: <x>")
	esc()
	p.Paste("pasted")
	esc()
	p.Key(tea.KeyPressMsg{Code: 'u', Text: "u"}) // undo the paste
	lines, err := p.Lines()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(lines, "\n"); got != "GET example.com/users\nAccept: <x>" {
		t.Errorf("buffer = %q", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(ansi.Strip(p.View()), "Accept: <x>") {
		if time.Now().After(deadline) {
			t.Fatalf("screen never showed the edit:\n%s", ansi.Strip(p.View()))
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Log("\n" + ansi.Strip(p.View()))
}

func TestKeyNotation(t *testing.T) {
	key := func(code rune, text string, mod tea.KeyMod) tea.KeyPressMsg {
		return tea.KeyPressMsg{Code: code, Text: text, Mod: mod}
	}
	for _, c := range []struct {
		k    tea.KeyPressMsg
		want string
	}{
		{key('a', "a", 0), "a"},
		{key('A', "A", tea.ModShift), "A"},
		{key('<', "<", 0), "<lt>"},
		{key('x', "x", tea.ModAlt), "<M-x>"},
		{key('w', "", tea.ModCtrl), "<C-w>"},
		{key('\\', "", tea.ModCtrl), "<C-Bslash>"},
		{key(tea.KeyEnter, "", 0), "<CR>"},
		{key(tea.KeyEnter, "", tea.ModCtrl), "<C-CR>"},
		{key(tea.KeyEnter, "", tea.ModShift), "<S-CR>"},
		{key(tea.KeyTab, "", tea.ModShift), "<S-Tab>"},
		{key(tea.KeyLeft, "", tea.ModCtrl), "<C-Left>"},
		{key(tea.KeyF5, "", 0), "<F5>"},
		{key(tea.KeyF20, "", tea.ModAlt), "<M-F20>"},
		{key(tea.KeyEscape, "", 0), "<Esc>"},
		{key(tea.KeyBackspace, "", 0), "<BS>"},
		{key(tea.KeyPgDown, "", 0), "<PageDown>"},
		{key(tea.KeySpace, " ", 0), " "},
		{key(tea.KeySpace, " ", tea.ModCtrl), "<C-Space>"},
		{key('a', "a", tea.ModSuper), "<D-a>"},
	} {
		if got := keyNotation(c.k); got != c.want {
			t.Errorf("%+v: got %q want %q", c.k, got, c.want)
		}
	}
}

func TestOpenCurrentScratch(t *testing.T) {
	if _, err := exec.LookPath("nvim"); err != nil {
		t.Skip("nvim not installed")
	}
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a file.http"), filepath.Join(dir, "b.http")
	os.WriteFile(a, []byte("one\ntwo\n"), 0o644)
	os.WriteFile(b, []byte("x\n"), 0o644)
	p, err := New(40, 10, Options{Args: []string{"--clean"}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	bufA, err := p.Open(a)
	if err != nil {
		t.Fatal(err)
	}
	p.Key(tea.KeyPressMsg{Code: 'j', Text: "j"})
	buf, path, line, col, err := p.Current()
	if err != nil || buf != bufA || path != a || line != 1 || col != 0 {
		t.Errorf("Current = %v %q %d %d %v", buf, path, line, col, err)
	}
	if _, err := p.Open(b); err != nil {
		t.Fatal(err)
	}
	if lines, _ := p.BufLines(bufA); strings.Join(lines, "|") != "one|two" {
		t.Errorf("BufLines(a) = %q", lines)
	}
	s1, err := p.SetScratch("resp", "json", []string{"{", "}"})
	if err != nil {
		t.Fatal(err)
	}
	if lines, _ := p.Lines(); strings.Join(lines, "") != "{}" {
		t.Errorf("current after scratch = %q", lines)
	}
	if _, err := p.Open(a); err != nil {
		t.Fatal(err)
	}
	s2, err := p.SetScratch("resp", "text", []string{"again"})
	if err != nil || s1 != s2 {
		t.Fatalf("scratch not reused: %v %v %v", s1, s2, err)
	}
	var ft string
	var mod bool
	if err := p.ExecLua("return vim.bo.filetype", &ft); err != nil || ft != "text" {
		t.Errorf("filetype = %q %v", ft, err)
	}
	if err := p.ExecLua("return vim.bo.modifiable", &mod); err != nil || mod {
		t.Errorf("modifiable = %v %v", mod, err)
	}
}

func TestUndefinedHighlightUsesDefaultColors(t *testing.T) {
	p := &Pane{hl: map[int]attr{}, sgr: map[int]string{}, defFg: 0xffffff, defBg: -1}
	if got, want := p.style(0), rgb(38, 0xffffff); got != want {
		t.Errorf("style(0) = %q, want %q", got, want)
	}
}
