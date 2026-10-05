package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

// oneByteAtATime feeds a writer byte by byte, the worst case for anything
// split across writes.
func oneByteAtATime(w io.WriteCloser, s string) {
	for i := 0; i < len(s); i++ {
		w.Write([]byte{s[i]})
	}
	w.Close()
}

func TestSecretReplacerAcrossWrites(t *testing.T) {
	secrets := []secretValue{{"long", "abcdef123"}, {"short", "f123"}, {"tok", "tok-9999"}}
	in := "x abcdef123 y f123 z tok-9999tok-9999 abcdef12"
	want := "x «secret:long» y «secret:short» z «secret:tok»«secret:tok» abcdef12"
	for _, chunked := range []bool{false, true} {
		var buf bytes.Buffer
		sw := newSecretReplacer(&buf, secrets, secretMarker)
		if chunked {
			oneByteAtATime(sw, in)
		} else {
			sw.Write([]byte(in))
			sw.Close()
		}
		if buf.String() != want {
			t.Errorf("chunked=%v:\n got %q\nwant %q", chunked, buf.String(), want)
		}
	}
}

func jsonRedact(s string, byteWise bool) string {
	var buf bytes.Buffer
	jw := &jsonRedactor{w: &buf}
	if byteWise {
		oneByteAtATime(jw, s)
	} else {
		jw.Write([]byte(s))
		jw.Close()
	}
	return buf.String()
}

// The streaming redactor hides exactly what Redactor.Body hides.
func TestJSONRedactorMatchesBody(t *testing.T) {
	rd := Redactor{}
	for _, in := range []string{
		`{"user":{"id":7,"name":"a"},"accessToken":"eyJ.a.b","ok":true}`,
		`{"auth":{"user":"x","pin":1234,"list":["a",2,null,false]},"x":"y"}`,
		`[{"password":""},{"password":"p"},{"Session_ID":12.5e3},{"otp":-1}]`,
		`{"\u0074oken":"escaped key","note":"has \"quotes\" and {braces} and token"}`,
		`{"apiKey":null,"secret":true,"items":[{"refresh_token":"r"}],"n":[1,2,3]}`,
		`{ "a" : { "token" : "t" } , "b" : [ ] , "c" : { } }`,
	} {
		for _, byteWise := range []bool{false, true} {
			got := jsonRedact(in, byteWise)
			var gotV, wantV any
			if err := json.Unmarshal([]byte(got), &gotV); err != nil {
				t.Fatalf("%s: output isn't JSON: %v\n%s", in, err, got)
			}
			json.Unmarshal([]byte(rd.Body(in, "application/json", false)), &wantV)
			if !reflect.DeepEqual(gotV, wantV) {
				t.Errorf("%s:\n got %s\nwant %s", in, got, rd.Body(in, "application/json", false))
			}
		}
	}
}

func TestJSONRedactorKeepsBytes(t *testing.T) {
	// Nothing sensitive: byte for byte the same, formatting included.
	in := "{\n  \"a\": [1, 2.5e-3, \"x\\\"y\"],\n\t\"b\" :{ }\n}\n"
	if got := jsonRedact(in, true); got != in {
		t.Errorf("changed:\n%q\n%q", in, got)
	}
	// Only the sensitive value changes.
	in = `{ "id": 7,  "token":   "abc" , "n": 1 }`
	want := `{ "id": 7,  "token":   "«redacted»" , "n": 1 }`
	if got := jsonRedact(in, true); got != want {
		t.Errorf("got %s", got)
	}
	// Several values one after another, as jq prints them.
	in = "\"x\"\n{\"password\":\"p\"}\n"
	want = "\"x\"\n{\"password\":\"«redacted»\"}\n"
	if got := jsonRedact(in, true); got != want {
		t.Errorf("got %q", got)
	}
}

func TestJSONRedactorOnCutAndNonJSON(t *testing.T) {
	// The start of a body that was cut off mid-way.
	got := jsonRedact(`{"items":[{"id":1,"token":"aaa"},{"id":2,"tok`, true)
	if strings.Contains(got, "aaa") || !strings.Contains(got, `"id":2`) {
		t.Errorf("cut body: %s", got)
	}
	got = jsonRedact(`{"token":"abcdefgh`, true)
	if strings.Contains(got, "abc") {
		t.Errorf("a sensitive value cut off mid-way must not leak: %s", got)
	}
	for _, in := range []string{"<html>token: x</html>", "plain text", ""} {
		if got := jsonRedact(in, true); got != in {
			t.Errorf("non-JSON changed: %q -> %q", in, got)
		}
	}
}

func bigServer(t *testing.T, body []byte) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// bigJSON is a JSON body of about n bytes with a secret value and a
// credential field placed right where the preview ends.
func bigJSON(n int, secret string) []byte {
	var b bytes.Buffer
	b.WriteString(`{"items":[`)
	for b.Len() < PreviewLimit-40 {
		b.WriteString(`{"id":1,"v":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},`)
	}
	b.WriteString(`{"echo":"` + secret + `","password":"pw-42"}`)
	for b.Len() < n {
		b.WriteString(`,{"id":2,"v":"yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy"}`)
	}
	b.WriteString(`]}`)
	return b.Bytes()
}

func TestLargeBodyIsKeptWhole(t *testing.T) {
	body := bigJSON(PreviewLimit+3<<20, "tok-123456")
	srv := bigServer(t, body)
	resp, err := RunRequest(context.Background(), Request{Method: "GET", URL: srv.URL}, "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Close()
	if !resp.Partial() || len(resp.Body) != PreviewLimit || resp.Size != int64(len(body)) || resp.CutAtCap {
		t.Fatalf("partial=%v preview=%d size=%d cut=%v", resp.Partial(), len(resp.Body), resp.Size, resp.CutAtCap)
	}
	full, err := resp.FullBody(JQLimit)
	if err != nil || !bytes.Equal(full, body) {
		t.Fatalf("whole body: %v (%d bytes)", err, len(full))
	}
	if _, err := resp.FullBody(1 << 20); err == nil {
		t.Error("FullBody should refuse bodies over its limit")
	}
	tmp := resp.BodyFile()
	resp.Close()
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("Close should remove the temporary file")
	}
}

func TestMaxBodyCap(t *testing.T) {
	old := MaxBody
	MaxBody = PreviewLimit + 1<<20
	defer func() { MaxBody = old }()
	srv := bigServer(t, bigJSON(PreviewLimit+3<<20, "x"))
	resp, err := RunRequest(context.Background(), Request{Method: "GET", URL: srv.URL}, "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Close()
	fi, _ := os.Stat(resp.BodyFile())
	if !resp.CutAtCap || resp.Size != MaxBody || fi.Size() != MaxBody {
		t.Errorf("cut=%v size=%d file=%d, want %d", resp.CutAtCap, resp.Size, fi.Size(), MaxBody)
	}
}

func TestHistoryKeepsWholeBodyScrubbed(t *testing.T) {
	h, ws := newTestHistory(t)
	ws.Mutate(func(w *Workspace) error {
		w.AddEnv("dev", []SavedHeader{{Key: "token", Value: "tok-123456", Enabled: true}})
		return nil
	})
	body := bigJSON(PreviewLimit+2<<20, "tok-123456")
	srv := bigServer(t, body)
	r := Request{Method: "GET", URL: srv.URL}
	resp, err := RunRequest(context.Background(), r, "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Close()
	e := ws.NewRun("k", "big", ws.ActiveEnv, r, r)
	if err := ws.RecordRun(h, e, resp, nil); err != nil {
		t.Fatal(err)
	}

	stored, err := os.ReadFile(h.BodyPath(e.Meta.ID))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("tok-123456")) || bytes.Contains(stored, []byte("pw-42")) {
		t.Error("stored body leaks a secret or credential field")
	}
	if !bytes.Contains(stored, []byte("«secret:token»")) || !bytes.HasSuffix(stored, []byte(`"yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy"}]}`)) {
		t.Error("stored body should be whole, with a marker for the secret")
	}
	if e.Meta.Size != int64(len(body)) {
		t.Errorf("meta size %d, want %d", e.Meta.Size, len(body))
	}

	loaded, err := h.Load(e.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	lr := loaded.Response()
	if !lr.Partial() || len(lr.Body) != PreviewLimit || lr.Size != int64(len(stored)) {
		t.Errorf("loaded run: partial=%v preview=%d size=%d", lr.Partial(), len(lr.Body), lr.Size)
	}
	if lr.Close(); h.BodyPath(e.Meta.ID) == "" {
		t.Error("closing a history response must not delete the stored body")
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"0": 0, "512": 512, "500KB": 500 << 10, "50MB": 50 << 20, "1GB": 1 << 30, "1.5m": 3 << 19, " 2 gb ": 2 << 30} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "x", "-1MB", "MB"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) should fail", bad)
		}
	}
}
