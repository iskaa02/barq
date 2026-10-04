package main

import (
	"bufio"
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
	histBodyLimit  = maxBodySize // the whole body, as received
)

// histMaxBytes caps the disk used by one workspace's history. Override
// with BARQ_HISTORY_MB.
var histMaxBytes = func() int64 {
	if mb, err := strconv.Atoi(os.Getenv("BARQ_HISTORY_MB")); err == nil && mb > 0 {
		return int64(mb) << 20
	}
	return 500 << 20
}()

type histMeta struct {
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

type histEntry struct {
	Meta          histMeta    `json:"meta"`
	Request       request     `json:"request"` // as typed, with {{variables}}
	Sent          request     `json:"sent"`    // as sent
	Proto         string      `json:"proto,omitempty"`
	Headers       http.Header `json:"headers,omitempty"`
	Body          []byte      `json:"body,omitempty"` // only in runs saved before .body files
	BodyTruncated bool        `json:"body_truncated,omitempty"`
}

// response rebuilds the stored response for rendering.
func (e *histEntry) response() *response {
	return &response{
		Status: e.Meta.Status, StatusCode: e.Meta.Code, Proto: e.Proto,
		Headers: e.Headers, Body: e.Body, Truncated: e.BodyTruncated, Duration: e.Meta.Duration,
	}
}

func requestHash(r request) string {
	// Hash the original fields alone when form-data isn't used, so runs
	// recorded before it existed aren't all flagged as edited.
	var data []byte
	if r.BodyMode == "" && len(r.Form) == 0 {
		data, _ = json.Marshal(struct {
			Method, URL, Body string
			Headers, Params   []savedHeader
		}{r.Method, r.URL, r.Body, r.Headers, r.DisabledParams})
	} else {
		data, _ = json.Marshal(struct {
			Method, URL, Body, BodyMode string
			Headers, Params, Form       []savedHeader
		}{r.Method, r.URL, r.Body, r.BodyMode, r.Headers, r.DisabledParams, r.Form})
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

type history struct {
	dir   string
	index []histMeta // oldest first
	stamp fileStamp  // index file as last read or written
}

func openHistory(ws *workspace) (*history, error) {
	h := &history{dir: strings.TrimSuffix(ws.path, ".json") + ".history"}
	return h, h.loadIndex()
}

func (h *history) indexPath() string { return filepath.Join(h.dir, "index.jsonl") }

// loadIndex reads the index from disk.
func (h *history) loadIndex() error {
	h.index = nil
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
		var m histMeta
		if json.Unmarshal(sc.Bytes(), &m) == nil && m.ID != "" {
			h.index = append(h.index, m)
		}
	}
	h.stamp = stampOf(h.indexPath())
	return sc.Err()
}

// changedOnDisk reports whether another process recorded or pruned runs.
func (h *history) changedOnDisk() bool { return stampOf(h.indexPath()) != h.stamp }

func (h *history) runPath(id string) string {
	return filepath.Join(h.dir, "runs", id+".json")
}

func (h *history) bodyPath(id string) string {
	return filepath.Join(h.dir, "runs", id+".body")
}

// storedSize is a run's disk usage, estimated for runs recorded before it
// was tracked.
func storedSize(m histMeta) int64 {
	if m.Stored > 0 {
		return m.Stored
	}
	return int64(m.Size)*4/3 + 2048
}

func (h *history) add(e *histEntry) error {
	if len(e.Body) > histBodyLimit {
		e.Body, e.BodyTruncated = e.Body[:histBodyLimit], true
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
	return withWriteLock(filepath.Join(h.dir, ".write.lock"), func() error { return h.appendLocked(e) })
}

// appendLocked adds a run to the index while holding the write lock. Runs
// recorded by another process since the last read are loaded first, so
// pruning sees all of them.
func (h *history) appendLocked(e *histEntry) error {
	if h.changedOnDisk() {
		if err := h.loadIndex(); err != nil {
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
	h.index = append(h.index, e.Meta)
	h.stamp = stampOf(h.indexPath())
	return h.prune()
}

func (h *history) load(id string) (*histEntry, error) {
	data, err := os.ReadFile(h.runPath(id))
	if err != nil {
		return nil, err
	}
	var e histEntry
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

// forKey returns a request's runs, newest first.
func (h *history) forKey(key string) []histMeta {
	var out []histMeta
	for i := len(h.index) - 1; i >= 0; i-- {
		if h.index[i].Key == key {
			out = append(out, h.index[i])
		}
	}
	return out
}

// all returns every run, newest first.
func (h *history) all() []histMeta {
	out := slices.Clone(h.index)
	slices.Reverse(out)
	return out
}

// previous returns the run of the same request just before id, if any.
func (h *history) previous(id string) (histMeta, bool) {
	i := slices.IndexFunc(h.index, func(m histMeta) bool { return m.ID == id })
	if i < 0 {
		return histMeta{}, false
	}
	for j := i - 1; j >= 0; j-- {
		if h.index[j].Key == h.index[i].Key {
			return h.index[j], true
		}
	}
	return histMeta{}, false
}

func (h *history) remove(drop func(histMeta) bool) error {
	var keep []histMeta
	for _, m := range h.index {
		if drop(m) {
			os.Remove(h.runPath(m.ID))
			os.Remove(h.bodyPath(m.ID))
		} else {
			keep = append(keep, m)
		}
	}
	if len(keep) == len(h.index) {
		return nil
	}
	h.index = keep
	return h.writeIndex()
}

// rekey moves runs to another key, e.g. when a draft is saved.
func (h *history) rekey(from, to string) error {
	changed := false
	for i := range h.index {
		if h.index[i].Key == from {
			h.index[i].Key = to
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
func (h *history) prune() error {
	perKey := map[string]int{}
	drop := map[string]bool{}
	total, bytes := 0, int64(0)
	for i := len(h.index) - 1; i >= 0; i-- {
		m := h.index[i]
		perKey[m.Key]++
		total++
		bytes += storedSize(m)
		newest := i == len(h.index)-1
		if !newest && (perKey[m.Key] > histPerRequest || total > histTotal || bytes > histMaxBytes) {
			drop[m.ID] = true
			bytes -= storedSize(m)
		}
	}
	return h.remove(func(m histMeta) bool { return drop[m.ID] })
}

func (h *history) writeIndex() error {
	var b strings.Builder
	for _, m := range h.index {
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
