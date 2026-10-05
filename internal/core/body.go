package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Response bodies are kept whole. The first PreviewLimit bytes are held in
// memory (Response.Body) for display, and anything longer is written to a
// file as it arrives, up to MaxBody. History keeps the whole body too,
// scrubbed of secrets.

const (
	// PreviewLimit is how much of a body is held in memory and shown.
	PreviewLimit = 10 << 20 // 10 MiB
	// JQLimit is the largest body jq filters and captures read whole.
	// Parsed, a JSON body takes about ten times its size in memory.
	JQLimit = 128 << 20 // 128 MiB
)

// MaxBody stops reading a response that goes on longer than this, such as
// an endless stream. 0 means no limit. Override with BARQ_MAX_BODY_MB or
// the CLI's --max-body.
var MaxBody = func() int64 {
	if mb, err := strconv.Atoi(os.Getenv("BARQ_MAX_BODY_MB")); err == nil && mb >= 0 {
		return int64(mb) << 20
	}
	return 1 << 30
}()

// ParseSize reads sizes like 500KB, 50MB, 1GB or a plain number of bytes.
// Units are binary: 1MB is 1024 KB.
func ParseSize(s string) (int64, error) {
	t := strings.ToUpper(strings.TrimSpace(s))
	mult := int64(1)
	for _, u := range []struct {
		suffix string
		mult   int64
	}{{"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}, {"B", 1}} {
		if rest, ok := strings.CutSuffix(t, u.suffix); ok {
			t, mult = strings.TrimSpace(rest), u.mult
			break
		}
	}
	n, err := strconv.ParseFloat(t, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q (e.g. 50MB, 1GB)", s)
	}
	return int64(n * float64(mult)), nil
}

// Partial reports whether Body holds only the start of the body.
func (r *Response) Partial() bool { return int64(len(r.Body)) < r.Size }

// OpenBody reads the whole body.
func (r *Response) OpenBody() (io.ReadCloser, error) {
	if r.bodyFile == "" {
		return io.NopCloser(bytes.NewReader(r.Body)), nil
	}
	return os.Open(r.bodyFile)
}

// BodyFile is the file holding the whole body, if there is one.
func (r *Response) BodyFile() string { return r.bodyFile }

var ErrBodyTooLarge = errors.New("body too large")

// FullBody returns the whole body when it's at most limit bytes.
func (r *Response) FullBody(limit int64) ([]byte, error) {
	if !r.Partial() {
		return r.Body, nil
	}
	if r.Size > limit {
		return nil, fmt.Errorf("%w: it's %s and at most %s is read whole", ErrBodyTooLarge, HumanSize(r.Size), HumanSize(limit))
	}
	return os.ReadFile(r.bodyFile)
}

// Close removes the temporary file holding a large body. Responses loaded
// from history don't own their file.
func (r *Response) Close() error {
	if r == nil || !r.temp || r.bodyFile == "" {
		return nil
	}
	r.temp = false
	return os.Remove(r.bodyFile)
}

// readBody keeps the first PreviewLimit bytes in memory and writes the
// whole body to a temporary file when it's longer. It stops at MaxBody.
func readBody(src io.Reader, resp *Response) error {
	if MaxBody > 0 {
		src = io.LimitReader(src, MaxBody+1)
	}
	overCap := func(n int64) bool { return MaxBody > 0 && n > MaxBody }
	preview, err := io.ReadAll(io.LimitReader(src, PreviewLimit+1))
	if err != nil {
		return err
	}
	if len(preview) <= PreviewLimit {
		if overCap(int64(len(preview))) {
			preview, resp.CutAtCap = preview[:MaxBody], true
		}
		resp.Body, resp.Size = preview, int64(len(preview))
		return nil
	}
	f, err := os.CreateTemp("", "barq-body-*")
	if err != nil {
		return err
	}
	resp.bodyFile, resp.temp = f.Name(), true
	n, err := io.Copy(f, io.MultiReader(bytes.NewReader(preview), src))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && overCap(n) {
		n, resp.CutAtCap = MaxBody, true
		err = os.Truncate(resp.bodyFile, n)
	}
	if err != nil {
		resp.Close()
		return err
	}
	resp.Body, resp.Size = preview[:min(PreviewLimit, n)], n
	return nil
}

// Scrubbing ------------------------------------------------------------------

// scrubBody copies a response body to w with secret values replaced by
// marker(name) and credential-like JSON fields redacted, without holding
// it all in memory. Form-encoded bodies that fit in memory get the same
// field rules as Redactor.Body.
func scrubBody(w io.Writer, r io.Reader, contentType string, secrets []secretValue, marker func(string) string) error {
	if strings.Contains(contentType, "x-www-form-urlencoded") {
		b, err := io.ReadAll(io.LimitReader(r, PreviewLimit+1))
		if err != nil {
			return err
		}
		if len(b) <= PreviewLimit {
			b = replaceSecrets(b, secrets, marker)
			rd := Redactor{secrets: secrets}
			_, err = io.WriteString(w, rd.Body(string(b), contentType, false))
			return err
		}
		r = io.MultiReader(bytes.NewReader(b), r)
	}
	jw := &jsonRedactor{w: w}
	sw := newSecretReplacer(jw, secrets, marker)
	if _, err := io.Copy(sw, r); err != nil {
		return err
	}
	if err := sw.Close(); err != nil {
		return err
	}
	return jw.Close()
}

func replaceSecrets(b []byte, secrets []secretValue, marker func(string) string) []byte {
	var buf bytes.Buffer
	sw := newSecretReplacer(&buf, secrets, marker)
	sw.Write(b)
	sw.Close()
	return buf.Bytes()
}

// BodyTo writes a body redacted for display, like Body, streaming: secret
// values and credential-like JSON fields. It also works on the start of a
// body that was cut off.
func (rd Redactor) BodyTo(w io.Writer, r io.Reader, contentType string) error {
	return scrubBody(w, r, contentType, rd.secrets, redactedMarker)
}

// TextTo replaces secret values in a stream, like Text.
func (rd Redactor) TextTo(w io.Writer, r io.Reader) error {
	sw := newSecretReplacer(w, rd.secrets, redactedMarker)
	if _, err := io.Copy(sw, r); err != nil {
		return err
	}
	return sw.Close()
}

func redactedMarker(name string) string { return "«redacted:" + name + "»" }

// secretReplacer replaces secret values in a stream. It holds back just
// enough bytes to catch a value split across writes.
type secretReplacer struct {
	w       io.Writer
	secrets []secretValue // longest first
	marker  func(string) string
	keep    int
	buf     []byte
}

func newSecretReplacer(w io.Writer, secrets []secretValue, marker func(string) string) *secretReplacer {
	keep := 0
	for _, sv := range secrets {
		keep = max(keep, len(sv.value)-1)
	}
	return &secretReplacer{w: w, secrets: secrets, marker: marker, keep: keep}
}

func (s *secretReplacer) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	if err := s.flush(false); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *secretReplacer) Close() error { return s.flush(true) }

// flush writes out what can no longer be part of a secret value. A match
// starting before limit is complete, and no longer value can start before
// it and still be unfinished.
func (s *secretReplacer) flush(final bool) error {
	for {
		limit := len(s.buf)
		if !final {
			limit -= s.keep
		}
		if limit <= 0 {
			return nil
		}
		at, match := -1, -1
		for i, sv := range s.secrets {
			j := bytes.Index(s.buf, []byte(sv.value))
			if j >= 0 && j < limit && (at < 0 || j < at) {
				at, match = j, i
			}
		}
		if at < 0 {
			if _, err := s.w.Write(s.buf[:limit]); err != nil {
				return err
			}
			s.buf = append(s.buf[:0], s.buf[limit:]...)
			return nil
		}
		sv := s.secrets[match]
		if _, err := s.w.Write(s.buf[:at]); err != nil {
			return err
		}
		if _, err := io.WriteString(s.w, s.marker(sv.name)); err != nil {
			return err
		}
		s.buf = append(s.buf[:0], s.buf[at+len(sv.value):]...)
	}
}

// jsonRedactor passes JSON through byte for byte, except that strings and
// numbers under credential-like keys (at any depth below them) become
// "«redacted»", as Redactor.Body does. Empty strings, booleans and null
// are kept. Bodies that aren't JSON, or stop being valid, pass through.
type jsonRedactor struct {
	w    io.Writer
	out  []byte
	mode int // 0 undecided, 1 JSON, 2 pass through

	stack     []jsonFrame
	keySens   bool // the key just read is credential-like
	inString  bool
	escaped   bool
	isKey     bool
	hide      bool // the current scalar is being replaced
	strLen    int
	key       []byte
	inScalar  bool
	afterKey  bool // between a key and its colon
	expectVal bool // after a colon
}

type jsonFrame struct {
	object    bool
	sensitive bool
	wantKey   bool
}

const maxKeyLen = 4 << 10

func (j *jsonRedactor) Write(p []byte) (int, error) {
	for _, c := range p {
		j.step(c)
	}
	if len(j.out) >= 32<<10 {
		if err := j.flush(); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (j *jsonRedactor) flush() error {
	_, err := j.w.Write(j.out)
	j.out = j.out[:0]
	return err
}

func (j *jsonRedactor) Close() error {
	if j.inScalar && j.hide {
		j.out = append(j.out, `"`+Redacted+`"`...)
	}
	return j.flush()
}

// valueSensitive reports whether the value starting now is under a
// credential-like key.
func (j *jsonRedactor) valueSensitive() bool {
	if len(j.stack) == 0 {
		return false
	}
	top := j.stack[len(j.stack)-1]
	return top.sensitive || top.object && j.keySens
}

func (j *jsonRedactor) step(c byte) {
	switch j.mode {
	case 2:
		j.out = append(j.out, c)
		return
	case 0:
		if isJSONSpace(c) {
			j.out = append(j.out, c)
			return
		}
		if c != '{' && c != '[' && c != '"' {
			j.mode = 2
			j.out = append(j.out, c)
			return
		}
		j.mode = 1
	}

	if j.inString {
		j.stringByte(c)
		return
	}
	if j.inScalar {
		if isJSONSpace(c) || strings.IndexByte(",:]}", c) >= 0 {
			j.endScalar()
		} else {
			if !j.hide {
				j.out = append(j.out, c)
			}
			return
		}
	}
	if isJSONSpace(c) {
		j.out = append(j.out, c)
		return
	}
	var top *jsonFrame
	if len(j.stack) > 0 {
		top = &j.stack[len(j.stack)-1]
	}
	switch {
	case c == '"' && top != nil && top.object && top.wantKey:
		j.inString, j.isKey, j.key = true, true, j.key[:0]
		j.out = append(j.out, c)
	case c == ':' && j.afterKey:
		j.afterKey, j.expectVal = false, true
		j.out = append(j.out, c)
	case c == ',' && top != nil:
		top.wantKey = top.object
		j.out = append(j.out, c)
	case (c == '}' && top != nil && top.object) || (c == ']' && top != nil && !top.object):
		j.stack = j.stack[:len(j.stack)-1]
		j.out = append(j.out, c)
	case c == '{' || c == '[':
		if top != nil && top.object && !j.expectVal {
			j.passThrough(c)
			return
		}
		j.stack = append(j.stack, jsonFrame{object: c == '{', sensitive: j.valueSensitive(), wantKey: c == '{'})
		j.expectVal = false
		j.out = append(j.out, c)
	case c == '"':
		if top != nil && top.object && !j.expectVal {
			j.passThrough(c)
			return
		}
		j.inString, j.isKey, j.strLen = true, false, 0
		j.hide = j.valueSensitive()
		j.expectVal = false
		if !j.hide {
			j.out = append(j.out, c)
		}
	case c == '-' || c >= '0' && c <= '9' || c == 't' || c == 'f' || c == 'n':
		if top != nil && top.object && !j.expectVal {
			j.passThrough(c)
			return
		}
		j.inScalar, j.expectVal = true, false
		// Booleans and null are kept, as in Redactor.Body.
		j.hide = j.valueSensitive() && c != 't' && c != 'f' && c != 'n'
		if !j.hide {
			j.out = append(j.out, c)
		}
	default:
		j.passThrough(c)
	}
}

func (j *jsonRedactor) stringByte(c byte) {
	switch {
	case j.escaped:
		j.escaped = false
	case c == '\\':
		j.escaped = true
	case c == '"':
		j.inString = false
		if j.isKey {
			var k string
			if json.Unmarshal(append(append([]byte{'"'}, j.key...), '"'), &k) != nil {
				k = string(j.key)
			}
			j.keySens = sensitiveName.MatchString(k)
			j.afterKey = true
			j.stack[len(j.stack)-1].wantKey = false
			j.out = append(j.out, c)
			return
		}
		if j.hide {
			if j.strLen == 0 {
				j.out = append(j.out, `""`...)
			} else {
				j.out = append(j.out, `"`+Redacted+`"`...)
			}
			j.hide = false
			return
		}
		j.out = append(j.out, c)
		return
	}
	if j.isKey {
		if len(j.key) < maxKeyLen {
			j.key = append(j.key, c)
		}
	} else {
		j.strLen++
	}
	if !j.hide {
		j.out = append(j.out, c)
	}
}

func (j *jsonRedactor) endScalar() {
	j.inScalar = false
	if j.hide {
		j.out = append(j.out, `"`+Redacted+`"`...)
		j.hide = false
	}
}

// passThrough gives up on JSON: the rest is copied as is (secret values
// are still replaced before this).
func (j *jsonRedactor) passThrough(c byte) {
	j.mode = 2
	j.out = append(j.out, c)
}

func isJSONSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
