package core

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Every send is recorded with the request as typed, the request as sent
// (variables filled in) and the response. Runs are stored next to the
// workspace file:
//
//	<workspace>.history/index.jsonl    one small line per run, loaded at start
//	<workspace>.history/runs/<id>.json the run without its body
//	<workspace>.history/runs/<id>.body the response body, raw
//
// so a long history doesn't slow down startup, and bodies aren't inflated
// by base64. Bodies are kept whole, scrubbed of secrets. Old runs are
// pruned by count; over the size budget, the oldest bodies go first and
// their runs stay.

const (
	histPerRequest = 50
	histTotal      = 1000
)

// histMaxBytes caps the disk used by one workspace's history. Override
// with BARQ_HISTORY_MB.
var histMaxBytes = func() int64 {
	if mb, err := strconv.Atoi(os.Getenv("BARQ_HISTORY_MB")); err == nil && mb > 0 {
		return int64(mb) << 20
	}
	return 500 << 20
}()

type HistMeta struct {
	ID       string        `json:"id"`
	Key      string        `json:"key"` // saved request ID, or a tab's draft key
	Name     string        `json:"name"`
	Time     time.Time     `json:"time"`
	Env      string        `json:"env,omitempty"`
	Method   string        `json:"method"`
	URL      string        `json:"url"` // as sent
	Status   string        `json:"status,omitempty"`
	Code     int           `json:"code,omitempty"`
	Duration time.Duration `json:"duration"`
	Size     int64         `json:"size"` // of the whole body, as received
	Error    string        `json:"error,omitempty"`
	ReqHash  string        `json:"req_hash"`         // of the request as typed, to spot edits
	Stored   int64         `json:"stored,omitempty"` // bytes on disk for this run
	// BodyPruned is set when the body was deleted to stay under the size
	// budget; the rest of the run is kept.
	BodyPruned bool `json:"body_pruned,omitempty"`
	CutAtCap   bool `json:"cut_at_cap,omitempty"` // reading stopped at MaxBody
}

type HistEntry struct {
	Meta    HistMeta    `json:"meta"`
	Request Request     `json:"request"` // as typed, with {{variables}}
	Sent    Request     `json:"sent"`    // as sent
	Proto   string      `json:"proto,omitempty"`
	Headers http.Header `json:"headers,omitempty"`
	// Body is stored here only in runs saved before .body files. Loaded
	// runs hold the first PreviewLimit bytes of the body here.
	Body []byte `json:"body,omitempty"`
	// BodyTruncated marks runs from before bodies were kept whole, when
	// history stopped at 10 MiB.
	BodyTruncated bool `json:"body_truncated,omitempty"`

	bodyFile string // the stored body, if it has its own file
	bodySize int64  // of the stored (scrubbed) body
}

// Response rebuilds the stored response. Its body is the scrubbed body
// history keeps.
func (e *HistEntry) Response() *Response {
	return &Response{
		Status: e.Meta.Status, StatusCode: e.Meta.Code, Proto: e.Proto, Headers: e.Headers,
		Body: e.Body, Size: max(e.bodySize, int64(len(e.Body))), CutAtCap: e.Meta.CutAtCap,
		Duration: e.Meta.Duration, bodyFile: e.bodyFile,
	}
}

func requestHash(r Request) string {
	// Hash the original fields alone when form-data isn't used, so runs
	// recorded before it existed aren't all flagged as edited.
	var data []byte
	if r.BodyMode == "" && len(r.Form) == 0 {
		data, _ = json.Marshal(struct {
			Method, URL, Body string
			Headers, Params   []SavedHeader
		}{r.Method, r.URL, r.Body, r.Headers, r.DisabledParams})
	} else {
		data, _ = json.Marshal(struct {
			Method, URL, Body, BodyMode string
			Headers, Params, Form       []SavedHeader
		}{r.Method, r.URL, r.Body, r.BodyMode, r.Headers, r.DisabledParams, r.Form})
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

type History struct {
	dir   string
	Index []HistMeta // oldest first
	stamp fileStamp  // index file as last read or written
}

func OpenHistory(ws *Workspace) (*History, error) {
	h := &History{dir: strings.TrimSuffix(ws.Path, ".json") + ".history"}
	return h, h.LoadIndex()
}

func (h *History) indexPath() string { return filepath.Join(h.dir, "index.jsonl") }

// LoadIndex reads the index from disk.
func (h *History) LoadIndex() error {
	h.Index = nil
	f, err := os.Open(h.indexPath())
	if errors.Is(err, os.ErrNotExist) {
		h.stamp = fileStamp{}
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var m HistMeta
		if json.Unmarshal(sc.Bytes(), &m) == nil && m.ID != "" {
			h.Index = append(h.Index, m)
		}
	}
	h.stamp = stampOf(h.indexPath())
	return sc.Err()
}

// ChangedOnDisk reports whether another process recorded or pruned runs.
func (h *History) ChangedOnDisk() bool { return stampOf(h.indexPath()) != h.stamp }

func (h *History) runPath(id string) string {
	return filepath.Join(h.dir, "runs", id+".json")
}

func (h *History) bodyPath(id string) string {
	return filepath.Join(h.dir, "runs", id+".body")
}

// BodyPath is the file holding a run's whole (scrubbed) body, or "" if it
// has none, e.g. because it was pruned.
func (h *History) BodyPath(id string) string {
	if _, err := os.Stat(h.bodyPath(id)); err != nil {
		return ""
	}
	return h.bodyPath(id)
}

// storedSize is a run's disk usage, estimated for runs recorded before it
// was tracked.
func storedSize(m HistMeta) int64 {
	if m.Stored > 0 {
		return m.Stored
	}
	return int64(m.Size)*4/3 + 2048
}

// add writes a run. body, if given, writes the response body.
func (h *History) add(e *HistEntry, body func(io.Writer) error) error {
	if err := os.MkdirAll(filepath.Join(h.dir, "runs"), 0o700); err != nil {
		return err
	}
	var bodySize int64
	if body != nil {
		n, err := writeFileAtomic(h.bodyPath(e.Meta.ID), body)
		if err != nil {
			return err
		}
		bodySize = n
	}
	meta := *e
	meta.Body = nil
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if err := os.WriteFile(h.runPath(e.Meta.ID), data, 0o600); err != nil {
		return err
	}
	e.Meta.Stored = int64(len(data)) + bodySize
	return WithWriteLock(filepath.Join(h.dir, ".write.lock"), func() error { return h.appendLocked(e) })
}

// writeFileAtomic writes a file through a temporary one, so a crash never
// leaves half a body behind, and returns its size.
func writeFileAtomic(path string, write func(io.Writer) error) (int64, error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".body-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(f.Name())
	cw := &countWriter{w: bufio.NewWriterSize(f, 64<<10)}
	err = write(cw)
	if err == nil {
		err = cw.w.(*bufio.Writer).Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	if err := os.Chmod(f.Name(), 0o600); err != nil {
		return 0, err
	}
	return cw.n, os.Rename(f.Name(), path)
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// appendLocked adds a run to the index while holding the write lock. Runs
// recorded by another process since the last read are loaded first, so
// pruning sees all of them.
func (h *History) appendLocked(e *HistEntry) error {
	if h.ChangedOnDisk() {
		if err := h.LoadIndex(); err != nil {
			return err
		}
	}
	line, _ := json.Marshal(e.Meta)
	f, err := os.OpenFile(h.indexPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	h.Index = append(h.Index, e.Meta)
	h.stamp = stampOf(h.indexPath())
	return h.prune()
}

// Load reads a run, with the first PreviewLimit bytes of its body.
func (h *History) Load(id string) (*HistEntry, error) {
	data, err := os.ReadFile(h.runPath(id))
	if err != nil {
		return nil, err
	}
	var e HistEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, err
	}
	if len(e.Body) > 0 {
		e.bodySize = int64(len(e.Body))
		return &e, nil
	}
	f, err := os.Open(h.bodyPath(id))
	if errors.Is(err, os.ErrNotExist) {
		e.Meta.BodyPruned = e.Meta.Size > 0
		return &e, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil {
		e.bodySize = fi.Size()
	}
	if e.Body, err = io.ReadAll(io.LimitReader(f, PreviewLimit)); err != nil {
		return nil, err
	}
	e.bodyFile = h.bodyPath(id)
	return &e, nil
}

// ForKey returns a request's runs, newest first.
func (h *History) ForKey(key string) []HistMeta {
	var out []HistMeta
	for i := len(h.Index) - 1; i >= 0; i-- {
		if h.Index[i].Key == key {
			out = append(out, h.Index[i])
		}
	}
	return out
}

// All returns every run, newest first.
func (h *History) All() []HistMeta {
	out := slices.Clone(h.Index)
	slices.Reverse(out)
	return out
}

// Previous returns the run of the same request just before id, if any.
func (h *History) Previous(id string) (HistMeta, bool) {
	i := slices.IndexFunc(h.Index, func(m HistMeta) bool { return m.ID == id })
	if i < 0 {
		return HistMeta{}, false
	}
	for j := i - 1; j >= 0; j-- {
		if h.Index[j].Key == h.Index[i].Key {
			return h.Index[j], true
		}
	}
	return HistMeta{}, false
}

func (h *History) Remove(drop func(HistMeta) bool) error {
	var keep []HistMeta
	for _, m := range h.Index {
		if drop(m) {
			os.Remove(h.runPath(m.ID))
			os.Remove(h.bodyPath(m.ID))
		} else {
			keep = append(keep, m)
		}
	}
	if len(keep) == len(h.Index) {
		return nil
	}
	h.Index = keep
	return h.writeIndex()
}

// Rekey moves runs to another key, e.g. when a draft is saved.
func (h *History) Rekey(from, to string) error {
	changed := false
	for i := range h.Index {
		if h.Index[i].Key == from {
			h.Index[i].Key = to
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return h.writeIndex()
}

// prune drops the oldest runs beyond the per-request and total counts.
// Over the size budget, the oldest bodies are deleted first, keeping their
// runs; runs only go when that isn't enough. The newest run is always
// kept whole.
func (h *History) prune() error {
	perKey := map[string]int{}
	drop := map[string]bool{}
	prunedBody := false
	total, bytes := 0, int64(0)
	for i := len(h.Index) - 1; i >= 0; i-- {
		m := &h.Index[i]
		perKey[m.Key]++
		total++
		newest := i == len(h.Index)-1
		if !newest && (perKey[m.Key] > histPerRequest || total > histTotal) {
			drop[m.ID] = true
			continue
		}
		size := storedSize(*m)
		if !newest && bytes+size > histMaxBytes && !m.BodyPruned {
			if fi, err := os.Stat(h.bodyPath(m.ID)); err == nil && os.Remove(h.bodyPath(m.ID)) == nil {
				m.BodyPruned, prunedBody = true, true
				m.Stored = max(size-fi.Size(), 1)
				size = m.Stored
			}
		}
		if !newest && bytes+size > histMaxBytes {
			drop[m.ID] = true
			continue
		}
		bytes += size
	}
	if len(drop) == 0 {
		if prunedBody {
			return h.writeIndex()
		}
		return nil
	}
	return h.Remove(func(m HistMeta) bool { return drop[m.ID] })
}

func (h *History) writeIndex() error {
	var b strings.Builder
	for _, m := range h.Index {
		line, _ := json.Marshal(m)
		b.Write(line)
		b.WriteByte('\n')
	}
	tmp, err := os.CreateTemp(h.dir, ".index-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), h.indexPath()); err != nil {
		return err
	}
	h.stamp = stampOf(h.indexPath())
	return nil
}

// Recording -------------------------------------------------------------------

// NewRun starts a history entry for a request about to be sent.
func (w *Workspace) NewRun(key, name, envID string, typed, sent Request) *HistEntry {
	env := ""
	if e, err := w.Env(envID); err == nil {
		env = e.Name
	}
	return &HistEntry{
		Meta: HistMeta{
			ID: NewID(), Key: key, Name: name,
			Time: time.Now(), Env: env, Method: sent.Method, URL: sent.URL,
			ReqHash: requestHash(typed),
		},
		Request: typed,
		Sent:    sent,
	}
}

// RecordRun completes a run with its outcome and writes it to history,
// with secret values scrubbed. Cancelled requests aren't recorded.
func (w *Workspace) RecordRun(h *History, e *HistEntry, resp *Response, err error) error {
	if e == nil || h == nil || errors.Is(err, context.Canceled) {
		return nil
	}
	var body func(io.Writer) error
	secrets := w.secretValues()
	if err != nil {
		e.Meta.Error = err.Error()
	} else {
		e.Meta.Status, e.Meta.Code, e.Meta.Duration = resp.Status, resp.StatusCode, resp.Duration
		e.Meta.Size, e.Meta.CutAtCap = resp.Size, resp.CutAtCap
		e.Proto, e.Headers = resp.Proto, resp.Headers
		if resp.Size > 0 {
			ct := resp.Headers.Get("Content-Type")
			body = func(dst io.Writer) error { return resp.scrubTo(dst, ct, secrets, secretMarker) }
		}
	}
	// Tokens sent or returned must not end up in plain text on disk: secret
	// values, then credential-like headers and fields. The body is scrubbed
	// the same way as it's written.
	hideSecretsInRun(e, secrets)
	rd := Redactor{secrets: secrets}
	e.Sent = rd.Request(e.Sent, false)
	e.Headers = rd.Headers(e.Headers)
	return h.add(e, body)
}

// scrubTo writes the whole body with secrets replaced and credential-like
// fields redacted.
func (r *Response) scrubTo(w io.Writer, contentType string, secrets []secretValue, marker func(string) string) error {
	src, err := r.OpenBody()
	if err != nil {
		return err
	}
	defer src.Close()
	return scrubBody(w, src, contentType, secrets, marker)
}

func RequestText(r Request) string {
	var b strings.Builder
	b.WriteString(r.Method + " " + r.URL + "\n")
	for _, h := range r.Headers {
		prefix := ""
		if !h.Enabled {
			prefix = "# "
		}
		b.WriteString(prefix + h.Key + ": " + h.Value + "\n")
	}
	switch {
	case r.BodyMode == BodyForm:
		b.WriteString("\nform-data:\n")
		for _, f := range r.Form {
			prefix := "  "
			if !f.Enabled {
				prefix = "# "
			}
			b.WriteString(prefix + f.Key + ": " + f.Value + "\n")
		}
	case r.Body != "":
		b.WriteString("\n" + r.Body + "\n")
	}
	return b.String()
}
