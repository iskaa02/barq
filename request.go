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
	resp *response
	err  error
}

func normalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw != "" && !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	return raw
}

func sendRequest(ctx context.Context, method, rawURL string, headers http.Header, body string) tea.Cmd {
	return func() tea.Msg {
		var reqBody io.Reader
		if body != "" {
			reqBody = strings.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, normalizeURL(rawURL), reqBody)
		if err != nil {
			return responseMsg{err: err}
		}
		req.Header = headers
		if body != "" && req.Header.Get("Content-Type") == "" && json.Valid([]byte(body)) {
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
