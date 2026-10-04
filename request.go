package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// maxBodySize caps how much of a response body is read into memory.
const maxBodySize = 10 << 20 // 10 MiB

var methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}

type response struct {
	Status     string
	StatusCode int
	Proto      string
	Headers    http.Header
	Body       []byte
	Truncated  bool
	Duration   time.Duration
}

type responseMsg struct {
	tabUID int
	resp   *response
	pretty string // formatted body, prepared off the UI thread
	err    error
}

func normalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw != "" && !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	return raw
}

// runRequest sends a resolved request and reads the response. cwd is the
// project directory that form-data file paths are relative to.
func runRequest(ctx context.Context, r request, cwd string) (*response, error) {
	body, contentType, err := buildBody(r, cwd)
	if err != nil {
		return nil, err
	}
	headers := headersOf(r)
	if contentType != "" {
		// The multipart boundary must match the body.
		headers.Set("Content-Type", contentType)
	}
	msg := doRequest(ctx, r.Method, r.URL, headers, body)
	return msg.resp, msg.err
}

// sendRequest sends a resolved request in the background for the TUI.
func sendRequest(ctx context.Context, tabUID int, r request, cwd string) tea.Cmd {
	return func() tea.Msg {
		resp, err := runRequest(ctx, r, cwd)
		msg := responseMsg{tabUID: tabUID, resp: resp, err: err}
		// Formatting a large body takes a while; do it here rather than
		// in Update so the UI stays responsive.
		if msg.resp != nil {
			pretty, isJSON := prettyBody(msg.resp)
			if isJSON {
				pretty = highlightJSON(pretty)
			}
			msg.pretty = pretty
		}
		return msg
	}
}

func doRequest(ctx context.Context, method, rawURL string, headers http.Header, body []byte) responseMsg {
	var reqBody io.Reader
	if len(body) > 0 {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, normalizeURL(rawURL), reqBody)
	if err != nil {
		return responseMsg{err: err}
	}
	req.Header = headers
	if len(body) > 0 && req.Header.Get("Content-Type") == "" && json.Valid(body) {
		req.Header.Set("Content-Type", "application/json")
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "barq/0.1")
	}

	start := time.Now()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return responseMsg{err: err}
	}
	defer res.Body.Close()

	data, err := io.ReadAll(io.LimitReader(res.Body, maxBodySize+1))
	if err != nil {
		return responseMsg{err: err}
	}
	truncated := len(data) > maxBodySize
	if truncated {
		data = data[:maxBodySize]
	}

	return responseMsg{resp: &response{
		Status:     res.Status,
		StatusCode: res.StatusCode,
		Proto:      res.Proto,
		Headers:    res.Header,
		Body:       data,
		Truncated:  truncated,
		Duration:   time.Since(start),
	}}
}

// prettyBody indents JSON bodies; anything else is returned as-is.
func prettyBody(r *response) (string, bool) {
	trimmed := bytes.TrimSpace(r.Body)
	isJSON := strings.Contains(r.Headers.Get("Content-Type"), "json") || json.Valid(trimmed)
	if isJSON {
		var buf bytes.Buffer
		if err := json.Indent(&buf, trimmed, "", "  "); err == nil {
			return buf.String(), true
		}
	}
	return string(r.Body), false
}

func humanSize(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
}
