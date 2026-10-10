// Package jsonpos maps a cursor position in JSON text to a jq path.
package jsonpos

import (
	"encoding/json"
	"strconv"
	"strings"
)

// PathAt returns the jq path of the innermost JSON value at the 0-based line and byte column of text.
// A cursor on an object key counts as that key's value. Returns ok=false if text isn't valid JSON
// or the position is outside any value.
func PathAt(text string, line, col int) (string, bool) {
	off, ok := offset(text, line, col)
	if !ok {
		return "", false
	}
	s := &scanner{src: text, off: off, hit: -1}
	s.skipWS()
	if s.value("") != nil {
		return "", false
	}
	s.skipWS()
	if s.pos != len(text) || s.hit < 0 {
		return "", false
	}
	if s.path == "" {
		return ".", true
	}
	return s.path, true
}

// offset converts a 0-based line and byte column to a byte offset.
func offset(text string, line, col int) (int, bool) {
	if line < 0 || col < 0 {
		return 0, false
	}
	start := 0
	for ; line > 0; line-- {
		i := strings.IndexByte(text[start:], '\n')
		if i < 0 {
			return 0, false
		}
		start += i + 1
	}
	end := strings.IndexByte(text[start:], '\n')
	if end < 0 {
		end = len(text) - start
	}
	if col >= end {
		return 0, false
	}
	return start + col, true
}

type scanner struct {
	src  string
	pos  int
	off  int // cursor offset
	hit  int
	path string
}

type syntaxError struct{}

func (syntaxError) Error() string { return "invalid json" }

func (s *scanner) skipWS() {
	for s.pos < len(s.src) {
		switch s.src[s.pos] {
		case ' ', '\t', '\r', '\n':
			s.pos++
		default:
			return
		}
	}
}

// mark records path when the cursor lies in [start, s.pos); inner values mark later and win.
func (s *scanner) mark(start int, path string) {
	if start <= s.off && s.off < s.pos {
		s.hit, s.path = start, path
	}
}

func (s *scanner) value(path string) error {
	if s.pos >= len(s.src) {
		return syntaxError{}
	}
	start := s.pos
	var err error
	switch c := s.src[s.pos]; {
	case c == '{':
		err = s.object(start, path)
	case c == '[':
		err = s.array(start, path)
	case c == '"':
		_, err = s.str()
	case c == '-' || (c >= '0' && c <= '9'):
		err = s.number()
	default:
		err = s.literal()
	}
	if err != nil {
		return err
	}
	if c := s.src[start]; c != '{' && c != '[' {
		s.mark(start, path)
	}
	return nil
}

// containerMark marks a container's span; called before children so they override it.
func (s *scanner) containerMark(start int, path string) {
	end := s.matchEnd(start)
	if end > start && start <= s.off && s.off < end {
		s.hit, s.path = start, path
	}
}

// matchEnd finds the end of the container starting at start by skipping strings.
func (s *scanner) matchEnd(start int) int {
	depth := 0
	for i := start; i < len(s.src); i++ {
		switch s.src[i] {
		case '"':
			for i++; i < len(s.src) && s.src[i] != '"'; i++ {
				if s.src[i] == '\\' {
					i++
				}
			}
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

func (s *scanner) object(start int, path string) error {
	s.containerMark(start, path)
	s.pos++
	s.skipWS()
	if s.peek() == '}' {
		s.pos++
		return nil
	}
	for {
		s.skipWS()
		if s.peek() != '"' {
			return syntaxError{}
		}
		ks := s.pos
		key, err := s.str()
		if err != nil {
			return err
		}
		child := path + keySeg(key)
		s.mark(ks, child)
		s.skipWS()
		if s.peek() != ':' {
			return syntaxError{}
		}
		s.pos++
		s.skipWS()
		if err := s.value(child); err != nil {
			return err
		}
		s.skipWS()
		switch s.peek() {
		case ',':
			s.pos++
		case '}':
			s.pos++
			return nil
		default:
			return syntaxError{}
		}
	}
}

func (s *scanner) array(start int, path string) error {
	s.containerMark(start, path)
	s.pos++
	s.skipWS()
	if s.peek() == ']' {
		s.pos++
		return nil
	}
	for i := 0; ; i++ {
		s.skipWS()
		seg := "[" + strconv.Itoa(i) + "]"
		if path == "" {
			seg = "." + seg
		}
		if err := s.value(path + seg); err != nil {
			return err
		}
		s.skipWS()
		switch s.peek() {
		case ',':
			s.pos++
		case ']':
			s.pos++
			return nil
		default:
			return syntaxError{}
		}
	}
}

func (s *scanner) peek() byte {
	if s.pos < len(s.src) {
		return s.src[s.pos]
	}
	return 0
}

// str scans a string literal and returns its decoded value.
func (s *scanner) str() (string, error) {
	start := s.pos
	for s.pos++; s.pos < len(s.src); s.pos++ {
		switch c := s.src[s.pos]; {
		case c == '\\':
			s.pos++
		case c == '"':
			s.pos++
			var out string
			if err := json.Unmarshal([]byte(s.src[start:s.pos]), &out); err != nil {
				return "", err
			}
			return out, nil
		case c < 0x20:
			return "", syntaxError{}
		}
	}
	return "", syntaxError{}
}

func (s *scanner) number() error {
	start := s.pos
	for s.pos < len(s.src) && strings.IndexByte("+-.eE0123456789", s.src[s.pos]) >= 0 {
		s.pos++
	}
	var n json.Number
	return json.Unmarshal([]byte(s.src[start:s.pos]), &n)
}

func (s *scanner) literal() error {
	for _, lit := range []string{"true", "false", "null"} {
		if strings.HasPrefix(s.src[s.pos:], lit) {
			s.pos += len(lit)
			return nil
		}
	}
	return syntaxError{}
}

func isIdent(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func keySeg(k string) string {
	if isIdent(k) {
		return "." + k
	}
	q, _ := json.Marshal(k)
	return `.[` + string(q) + `]`
}

// VarName suggests a variable name from a path: the last object key, made into a valid identifier.
func VarName(path string) string {
	last := ""
	for i := 0; i < len(path); {
		switch {
		case strings.HasPrefix(path[i:], `.["`):
			j := i + 3
			for j < len(path) && path[j] != '"' {
				if path[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(path) {
				return ident(last)
			}
			if err := json.Unmarshal([]byte(path[i+2:j+1]), &last); err != nil {
				return ident(last)
			}
			i = j + 2
		case path[i] == '.':
			j := i + 1
			for j < len(path) && path[j] != '.' && path[j] != '[' {
				j++
			}
			if j > i+1 {
				last = path[i+1 : j]
			}
			i = j
		default:
			i++
		}
	}
	return ident(last)
}

func ident(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "value"
	}
	if out[0] >= '0' && out[0] <= '9' {
		out = "_" + out
	}
	return out
}
