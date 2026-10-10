package ntui

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const sample = "### a\nGET http://x/a\n\n### b\nGET http://x/b\n\n###\nGET http://x/c\n"

func TestRenameBlock(t *testing.T) {
	lines := splitLines(sample)
	reqs := parseLines(lines)
	got := joinLines(renameBlock(lines, reqs[1], "bee"))
	if want := strings.Replace(sample, "### b", "### bee", 1); got != want {
		t.Fatalf("got %q", got)
	}
	// "###" with no name
	if got := renameBlock(lines, reqs[2], "see"); got[6] != "### see" {
		t.Fatalf("got %q", got)
	}
	// no ### at all
	l2 := splitLines("GET http://x\n")
	if got := renameBlock(l2, parseLines(l2)[0], "n"); !reflect.DeepEqual(got, []string{"### n", "GET http://x"}) {
		t.Fatalf("got %q", got)
	}
	// @name wins
	l3 := splitLines("###\n# @name old\nGET http://x\n")
	got3 := renameBlock(l3, parseLines(l3)[0], "new")
	if got3[1] != "# @name new" || got3[0] != "###" {
		t.Fatalf("got %q", got3)
	}
	if parseLines(got3)[0].Name != "new" {
		t.Fatal("not renamed")
	}
}

func TestRemoveBlock(t *testing.T) {
	lines := splitLines(sample)
	reqs := parseLines(lines)
	if got := joinLines(removeBlock(lines, reqs[1])); got != "### a\nGET http://x/a\n\n###\nGET http://x/c\n" {
		t.Fatalf("got %q", got)
	}
	if got := joinLines(removeBlock(lines, reqs[2])); got != "### a\nGET http://x/a\n\n### b\nGET http://x/b\n" {
		t.Fatalf("got %q", got)
	}
	if got := removeBlock(splitLines("GET http://x\n"), parseLines(splitLines("GET http://x\n"))[0]); len(got) != 0 {
		t.Fatalf("got %q", got)
	}
}

func TestInsertBlock(t *testing.T) {
	src := splitLines("GET http://x/first\n")
	blk := extractBlock(src, parseLines(src)[0])
	if blk[0] != "###" {
		t.Fatalf("block %q", blk)
	}
	lines := splitLines(sample)
	reqs := parseLines(lines)
	at, pos := afterReq(lines, reqs, 0)
	got := insertBlock(lines, at, blockWithName(blk, "first"))
	if want := "### a\nGET http://x/a\n\n### first\nGET http://x/first\n\n### b\nGET http://x/b\n\n###\nGET http://x/c\n"; joinLines(got) != want || pos != 1 {
		t.Fatalf("got %q pos %d", joinLines(got), pos)
	}
	at, pos = afterReq(lines, reqs, -1)
	got = insertBlock(lines, at, blk)
	if want := sample + "\n###\nGET http://x/first\n"; joinLines(got) != want || pos != 3 {
		t.Fatalf("got %q", joinLines(got))
	}
	// last block without a trailing blank
	l := splitLines("### a\nGET http://x\n")
	if got := joinLines(insertBlock(l, len(l), blk)); got != "### a\nGET http://x\n\n###\nGET http://x/first\n" {
		t.Fatalf("got %q", got)
	}
	if got := joinLines(insertBlock(nil, 0, blk)); got != "###\nGET http://x/first\n" {
		t.Fatalf("got %q", got)
	}
}

func TestNames(t *testing.T) {
	taken := map[string]bool{"a": true, "a copy": true}
	if got := uniqueName("a", func(s string) bool { return taken[s] }); got != "a copy 2" {
		t.Fatal(got)
	}
	if got := uniqueName("z", func(s string) bool { return taken[s] }); got != "z" {
		t.Fatal(got)
	}
	ex := map[string]bool{"x.http": true, "x-copy.http": true, "d": true}
	f := func(s string) bool { return ex[s] }
	if got := copyBase("x.http", f); got != "x-copy-2.http" {
		t.Fatal(got)
	}
	if got := copyBase("d", f); got != "d-copy" {
		t.Fatal(got)
	}
	if got := copyBase("y.http", f); got != "y.http" {
		t.Fatal(got)
	}
	if !within("/a/b", "/a/b/c") || !within("/a/b", "/a/b") || within("/a/b", "/a/bc") {
		t.Fatal("within")
	}
}

func TestSpliceRange(t *testing.T) {
	old := []string{"a", "b", "c", "d"}
	nw := []string{"a", "x", "y", "c", "d"}
	s, e, r := spliceRange(old, nw)
	got := append(append(append([]string{}, old[:s]...), r...), old[e:]...)
	if !reflect.DeepEqual(got, nw) {
		t.Fatalf("%d %d %q", s, e, r)
	}
}

func TestKeyPairs(t *testing.T) {
	lines := splitLines("GET http://x/1\n\n###\nGET http://x/2\n\n### n\nGET http://x/3\n")
	reqs := parseLines(lines)
	nl := removeBlock(lines, reqs[0])
	got := keyPairs("f.http", reqs, "f.http", parseLines(nl), deleteIdx(3, 0))
	want := []keyPair{{"f.http#2", "f.http#1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	// moving the file renames every key
	got = keyPairs("a/f.http", reqs, "b/f.http", reqs, identityIdx(3))
	if len(got) != 3 || got[2] != (keyPair{"a/f.http#n", "b/f.http#n"}) {
		t.Fatalf("%v", got)
	}
	if got := moveIdx(3, 0, -1); !reflect.DeepEqual(got, []int{2, 0, 1}) {
		t.Fatalf("%v", got)
	}
	if got := moveIdx(3, 2, 0); !reflect.DeepEqual(got, []int{0, 2, 1}) {
		t.Fatalf("%v", got)
	}
	if got := insertIdx(3, 1); !reflect.DeepEqual(got, []int{0, 2, 3}) {
		t.Fatalf("%v", got)
	}
}

func TestCopyPath(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "d", "s", "a.http"), "GET http://x\n")
	write(t, filepath.Join(root, "d", "b.http"), "GET http://y\n")
	if err := copyPath(filepath.Join(root, "d"), filepath.Join(root, "e")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "e", "s", "a.http")); string(b) != "GET http://x\n" {
		t.Fatalf("%q", b)
	}
	if len(httpFilesUnder(filepath.Join(root, "e"))) != 2 {
		t.Fatal("files")
	}
}
