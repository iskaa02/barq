package core

import (
	"net/http"
	"testing"
)

func TestHeaderCookieCaptures(t *testing.T) {
	h := http.Header{}
	h.Add("X-Request-Id", "abc")
	h.Add("X-Request-Id", "second")
	h.Add("Set-Cookie", "other=o1; Path=/")
	h.Add("Set-Cookie", "session_id=s1; Path=/")
	resp := &Response{Headers: h, Body: []byte("not json")}
	var caps []Capture
	for _, spec := range []string{"a = header x-request-id", "b = cookie session_id", "c = header X-Nope", "d = cookie nope"} {
		c, err := ParseCapture(spec)
		if err != nil {
			t.Fatal(spec, err)
		}
		caps = append(caps, c)
	}
	got := EvalCaptures(caps, resp)
	if got[0].Value != "abc" || got[1].Value != "s1" {
		t.Errorf("values: %+v", got)
	}
	if got[2].Error != "no header X-Nope" || got[3].Error != "no cookie nope" {
		t.Errorf("errors: %+v", got)
	}
}

func TestParseCaptureSources(t *testing.T) {
	for _, bad := range []string{"a = header", "a = cookie ", "a = header Bad Name", "a = header B@d"} {
		if _, err := ParseCapture(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
	if c, err := ParseCapture("a = .headers"); err != nil || func() bool { k, _ := c.Source(); return k != "jq" }() {
		t.Errorf("jq: %v", err)
	}
}
