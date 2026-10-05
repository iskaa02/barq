package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestFuzzyScore(t *testing.T) {
	if _, ok := fuzzyScore("ctab", "Tab: Close other tabs"); !ok {
		t.Error("subsequence should match")
	}
	if _, ok := fuzzyScore("xyz", "Tab: Close other tabs"); ok {
		t.Error("non-subsequence should not match")
	}
	if _, ok := fuzzyScore("close tab", "Tab: Close tab"); !ok {
		t.Error("spaces in the query should be ignored")
	}
	// Word-start matches beat scattered ones.
	a, _ := fuzzyScore("cc", "Request: Copy as curl")
	b, _ := fuzzyScore("cc", "Request: Cancel running request")
	if a <= b {
		t.Errorf("expected word-start match to score higher: %d vs %d", a, b)
	}
}

func TestOverlay(t *testing.T) {
	bg := strings.Join([]string{"\x1b[31maaaaaaaaaa\x1b[0m", "bbbbbbbbbb", "cccccccccc"}, "\n")
	out := strings.Split(overlay(bg, "XX\nYY", 3, 1), "\n")
	if got := ansi.Strip(out[1]); got != "bbbXXbbbbb" {
		t.Errorf("row 1 = %q", got)
	}
	if got := ansi.Strip(out[2]); got != "cccYYccccc" {
		t.Errorf("row 2 = %q", got)
	}
	if got := ansi.Strip(out[0]); got != "aaaaaaaaaa" {
		t.Errorf("row 0 should be untouched, got %q", got)
	}
}
