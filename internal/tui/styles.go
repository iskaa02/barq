package tui

import (
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
)

var (
	colorAccent = lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#A78BFA"}
	colorMuted  = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#6B7280"}
	colorBorder = lipgloss.AdaptiveColor{Light: "#D1D5DB", Dark: "#3F3F46"}
	colorGreen  = lipgloss.AdaptiveColor{Light: "#059669", Dark: "#34D399"}
	colorYellow = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}
	colorRed    = lipgloss.AdaptiveColor{Light: "#DC2626", Dark: "#F87171"}
	colorBlue   = lipgloss.AdaptiveColor{Light: "#2563EB", Dark: "#60A5FA"}
	colorCyan   = lipgloss.AdaptiveColor{Light: "#0E7490", Dark: "#22D3EE"}

	paneStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(0, 1)
	focusedPaneStyle = paneStyle.BorderForeground(colorAccent)

	titleStyle     = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	mutedStyle     = lipgloss.NewStyle().Foreground(colorMuted)
	activeTabStyle = lipgloss.NewStyle().Bold(true).Foreground(colorAccent).Underline(true)
	tabStyle       = lipgloss.NewStyle().Foreground(colorMuted)
	errorStyle     = lipgloss.NewStyle().Foreground(colorRed)
	headerKeyStyle = lipgloss.NewStyle().Foreground(colorBlue)

	activeSuggStyle = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)

	jsonKeyStyle    = lipgloss.NewStyle().Foreground(colorBlue)
	jsonStringStyle = lipgloss.NewStyle().Foreground(colorGreen)
	jsonNumberStyle = lipgloss.NewStyle().Foreground(colorYellow)
	jsonLitStyle    = lipgloss.NewStyle().Foreground(colorCyan)
)

func methodColor(m string) lipgloss.TerminalColor {
	switch m {
	case "GET":
		return colorGreen
	case "POST":
		return colorYellow
	case "PUT", "PATCH":
		return colorBlue
	case "DELETE":
		return colorRed
	default:
		return colorCyan
	}
}

func statusColor(code int) lipgloss.TerminalColor {
	switch {
	case code >= 500:
		return colorRed
	case code >= 400:
		return colorYellow
	case code >= 300:
		return colorCyan
	default:
		return colorGreen
	}
}

// styleCodes holds a style's escape sequences, so hot loops can wrap text
// in them directly instead of calling Render for every token.
type styleCodes struct{ pre, suf string }

func codesOf(st lipgloss.Style) styleCodes {
	const mark = "\uE000" // private-use rune: never part of real output
	pre, suf, ok := strings.Cut(st.Render(mark), mark)
	if !ok {
		return styleCodes{}
	}
	return styleCodes{pre, suf}
}

func (c styleCodes) write(b *strings.Builder, tok string) {
	b.WriteString(c.pre)
	b.WriteString(tok)
	b.WriteString(c.suf)
}

// The codes depend on the terminal's color support and background, so
// they're resolved on first use, once the program is running.
var (
	jsonCodesOnce                          sync.Once
	jsonKeyC, jsonStrC, jsonNumC, jsonLitC styleCodes
)

// highlightJSON colors already-indented JSON. It assumes valid input.
func highlightJSON(s string) string {
	jsonCodesOnce.Do(func() {
		jsonKeyC, jsonStrC = codesOf(jsonKeyStyle), codesOf(jsonStringStyle)
		jsonNumC, jsonLitC = codesOf(jsonNumberStyle), codesOf(jsonLitStyle)
	})
	var b strings.Builder
	b.Grow(len(s) * 2)
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
			tok := s[i:j]
			k := j
			for k < len(s) && s[k] == ' ' {
				k++
			}
			if k < len(s) && s[k] == ':' {
				jsonKeyC.write(&b, tok)
			} else {
				jsonStrC.write(&b, tok)
			}
			i = j
		case c == '-' || (c >= '0' && c <= '9'):
			j := i
			for j < len(s) && strings.IndexByte("+-0123456789.eE", s[j]) >= 0 {
				j++
			}
			jsonNumC.write(&b, s[i:j])
			i = j
		case c == 't' || c == 'f' || c == 'n':
			j := i
			for j < len(s) && s[j] >= 'a' && s[j] <= 'z' {
				j++
			}
			jsonLitC.write(&b, s[i:j])
			i = j
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}
