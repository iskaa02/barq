package core

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
// by base64. Old runs pruned by count, then by total size.

const (
	histPerRequest = 50
	histTotal      = 1000
	HistBodyLimit  = MaxBodySize // the whole body, as received
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
	Size     int           `json:"size"`
	Error    string        `json:"error,omitempty"`
	ReqHash  string        `json:"req_hash"`         // of the request as typed, to spot edits
	Stored   int64         `json:"stored,omitempty"` // bytes on disk for this run
}

type HistEntry struct {
	Meta          HistMeta    `json:"meta"`
	Request       Request     `json:"request"` // as typed, with {{variables}}
	Sent          Request     `json:"sent"`    // as sent
	Proto         string      `json:"proto,omitempty"`
	Headers       http.Header `json:"headers,omitempty"`
	Body          []byte      `json:"body,omitempty"` // only in runs saved before .body files
	BodyTruncated bool        `json:"body_truncated,omitempty"`
}

// Response rebuilds the stored response for rendering.
func (e *HistEntry) Response() *Response {
	return &Response{
		Status: e.Meta.Status, StatusCode: e.Meta.Code, Proto: e.Proto,
		Headers: e.Headers, Body: e.Body, Truncated: e.BodyTruncated, Duration: e.Meta.Duration,
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

// storedSize is a run's disk usage, estimated for runs recorded before it
// was tracked.
func storedSize(m HistMeta) int64 {
	if m.Stored > 0 {
		return m.Stored
	}
	return int64(m.Size)*4/3 + 2048
}

func (h *History) add(e *HistEntry) error {
	if len(e.Body) > HistBodyLimit {
		e.Body, e.BodyTruncated = e.Body[:HistBodyLimit], true
	}
	if err := os.MkdirAll(filepath.Join(h.dir, "runs"), 0o700); err != nil {
		return err
	}
	body := e.Body
	meta := *e
	meta.Body = nil
	if len(body) > 0 {
		if err := os.WriteFile(h.bodyPath(e.Meta.ID), body, 0o600); err != nil {
			return err
		}
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if err := os.WriteFile(h.runPath(e.Meta.ID), data, 0o600); err != nil {
		return err
	}
	e.Meta.Stored = int64(len(data) + len(body))
	return WithWriteLock(filepath.Join(h.dir, ".write.lock"), func() error { return h.appendLocked(e) })
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

func (h *History) Load(id string) (*HistEntry, error) {
	data, err := os.ReadFile(h.runPath(id))
	if err != nil {
		return nil, err
	}
	var e HistEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, err
	}
	if len(e.Body) == 0 {
		body, err := os.ReadFile(h.bodyPath(id))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		e.Body = body
	}
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

// prune drops the oldest runs beyond the per-request count, the total
// count and the total size. The newest run is always kept.
func (h *History) prune() error {
	perKey := map[string]int{}
	drop := map[string]bool{}
	total, bytes := 0, int64(0)
	for i := len(h.Index) - 1; i >= 0; i-- {
		m := h.Index[i]
		perKey[m.Key]++
		total++
		bytes += storedSize(m)
		newest := i == len(h.Index)-1
		if !newest && (perKey[m.Key] > histPerRequest || total > histTotal || bytes > histMaxBytes) {
			drop[m.ID] = true
			bytes -= storedSize(m)
		}
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
	if err != nil {
		e.Meta.Error = err.Error()
	} else {
		e.Meta.Status, e.Meta.Code, e.Meta.Duration, e.Meta.Size = resp.Status, resp.StatusCode, resp.Duration, len(resp.Body)
		e.Proto, e.Headers, e.Body, e.BodyTruncated = resp.Proto, resp.Headers, resp.Body, resp.Truncated
	}
	// Tokens sent or returned must not end up in plain text on disk: secret
	// values, then credential-like headers and fields.
	secrets := w.secretValues()
	hideSecretsInRun(e, secrets)
	rd := Redactor{secrets: secrets}
	e.Sent = rd.Request(e.Sent, false)
	e.Headers = rd.Headers(e.Headers)
	e.Body = []byte(rd.Body(string(e.Body), e.Headers.Get("Content-Type"), false))
	return h.add(e)
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
