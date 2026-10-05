package tui

import (
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// referenceHighlight is the straightforward per-token version.
func referenceHighlight(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '"':
			j := i + 1
			for j < len(s) && s[j] != '"' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			if j < len(s) {
				j++
			}
			k := j
			for k < len(s) && s[k] == ' ' {
				k++
			}
			if k < len(s) && s[k] == ':' {
				b.WriteString(jsonKeyStyle.Render(s[i:j]))
			} else {
				b.WriteString(jsonStringStyle.Render(s[i:j]))
			}
			i = j
		case c == '-' || (c >= '0' && c <= '9'):
			j := i
			for j < len(s) && strings.IndexByte("+-0123456789.eE", s[j]) >= 0 {
				j++
			}
			b.WriteString(jsonNumberStyle.Render(s[i:j]))
			i = j
		case c == 't' || c == 'f' || c == 'n':
			j := i
			for j < len(s) && s[j] >= 'a' && s[j] <= 'z' {
				j++
			}
			b.WriteString(jsonLitStyle.Render(s[i:j]))
			i = j
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

func TestHighlightJSONMatchesRender(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	jsonCodesOnce = sync.Once{} // pick up the color profile

	in := "{\n  \"name\": \"a \\\"q\\\"\",\n  \"n\": -1.5e3,\n  \"ok\": true,\n  \"x\": null,\n  \"list\": [1, false]\n}"
	got, want := highlightJSON(in), referenceHighlight(in)
	if got != want {
		t.Errorf("mismatch:\n got %q\nwant %q", got, want)
	}
	if !strings.Contains(got, "\x1b[") {
		t.Error("expected color codes with a color profile set")
	}
}
