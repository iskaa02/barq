package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// MaxBodySize caps how much of a response body is read into memory.
const MaxBodySize = 10 << 20 // 10 MiB

var Methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}

type Response struct {
	Status     string
	StatusCode int
	Proto      string
	Headers    http.Header
	Body       []byte
	Truncated  bool
	Duration   time.Duration
}

func NormalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw != "" && !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	return raw
}

// RunRequest sends a resolved request and reads the response. cwd is the
// project directory that form-data file paths are relative to.
func RunRequest(ctx context.Context, r Request, cwd string) (*Response, error) {
	body, contentType, err := BuildBody(r, cwd)
	if err != nil {
		return nil, err
	}
	headers := headersOf(r)
	if contentType != "" {
		// The multipart boundary must match the body.
		headers.Set("Content-Type", contentType)
	}
	return doRequest(ctx, r.Method, r.URL, headers, body)
}

func doRequest(ctx context.Context, method, rawURL string, headers http.Header, body []byte) (*Response, error) {
	var reqBody io.Reader
	if len(body) > 0 {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, NormalizeURL(rawURL), reqBody)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	defer res.Body.Close()

	data, err := io.ReadAll(io.LimitReader(res.Body, MaxBodySize+1))
	if err != nil {
		return nil, err
	}
	truncated := len(data) > MaxBodySize
	if truncated {
		data = data[:MaxBodySize]
	}

	return &Response{
		Status:     res.Status,
		StatusCode: res.StatusCode,
		Proto:      res.Proto,
		Headers:    res.Header,
		Body:       data,
		Truncated:  truncated,
		Duration:   time.Since(start),
	}, nil
}

// PrettyBody indents JSON bodies; anything else is returned as-is.
func PrettyBody(r *Response) (string, bool) {
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

func HumanSize(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
}
