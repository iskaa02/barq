package core

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// pngBytes is the start of a PNG file, enough for content sniffing.
var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

type part struct{ name, filename, ctype, body string }

func readParts(t *testing.T, body []byte, contentType string) []part {
	t.Helper()
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatal(err)
	}
	r := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	var parts []part
	for {
		p, err := r.NextPart()
		if err == io.EOF {
			return parts
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(p)
		parts = append(parts, part{p.FormName(), p.FileName(), p.Header.Get("Content-Type"), string(data)})
	}
}

func TestBuildMultipartBody(t *testing.T) {
	cwd := t.TempDir()
	os.MkdirAll(filepath.Join(cwd, "img"), 0o755)
	os.WriteFile(filepath.Join(cwd, "img", "cat.png"), pngBytes, 0o644)
	abs := filepath.Join(t.TempDir(), "notes") // no extension: sniffed
	os.WriteFile(abs, []byte("hello"), 0o644)

	r := Request{BodyMode: BodyForm, Form: []SavedHeader{
		{Key: "label", Value: "Home; \"main\"", Enabled: true},
		{Key: "picture", Value: "@img/cat.png", Enabled: true},
		{Key: "doc", Value: "@" + abs, Enabled: true},
		{Key: "off", Value: "@missing.png", Enabled: false},
	}}
	body, ctype, err := BuildBody(r, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ctype, "multipart/form-data; boundary=") {
		t.Fatalf("content type %q", ctype)
	}
	got := readParts(t, body, ctype)
	want := []part{
		{"label", "", "", `Home; "main"`},
		{"picture", "cat.png", "image/png", string(pngBytes)},
		{"doc", "notes", "text/plain; charset=utf-8", "hello"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parts:\n got %+v\nwant %+v", got, want)
	}
}

func TestBuildMultipartErrors(t *testing.T) {
	cwd := t.TempDir()
	for value, wantErr := range map[string]string{
		"@nope.png": "no such file",
		"@":         "add a file path",
		"@.":        "is a directory",
	} {
		r := Request{BodyMode: BodyForm, Form: []SavedHeader{{Key: "f", Value: value, Enabled: true}}}
		if _, _, err := BuildBody(r, cwd); err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("%q: got %v, want %q", value, err, wantErr)
		}
	}
}

func TestCurlFormRoundTrip(t *testing.T) {
	cr, warnings, err := ParseCurl(`curl https://x.com/up -F 'file=@./img/cat.png;type=image/png' -F 'label=home' --form-string 'note=a;b'`)
	if err != nil || len(warnings) != 0 {
		t.Fatal(err, warnings)
	}
	wantForm := []HeaderRow{
		{Key: "file", Value: "@./img/cat.png", Enabled: true},
		{Key: "label", Value: "home", Enabled: true},
		{Key: "note", Value: "a;b", Enabled: true},
	}
	if cr.Method != "POST" || !reflect.DeepEqual(cr.Form, wantForm) {
		t.Fatalf("import: %s %+v", cr.Method, cr.Form)
	}

	r := Request{Method: "POST", URL: "https://x.com/up", BodyMode: BodyForm, Form: ToSavedHeaders(wantForm),
		Headers: []SavedHeader{{Key: "Content-Type", Value: "multipart/form-data", Enabled: true}}}
	out := ToCurl(r)
	if strings.Contains(out, "Content-Type") {
		t.Errorf("curl must set its own multipart Content-Type:\n%s", out)
	}
	back, _, err := ParseCurl(out)
	if err != nil || !reflect.DeepEqual(back.Form, wantForm) {
		t.Errorf("round trip:\n%s\n%+v %v", out, back.Form, err)
	}
}

func TestOpenAPIMultipartFields(t *testing.T) {
	spec, err := LoadSpec(writeSpec(t, "up.yaml", `
openapi: 3.0.0
info: {title: Up}
paths:
  /addresses:
    post:
      requestBody:
        content:
          multipart/form-data:
            schema:
              type: object
              required: [label, location]
              properties:
                label: {type: string, example: Home}
                picture: {type: string, format: binary}
                location: {type: object, properties: {lat: {type: number, example: 32.8}}}
                note: {type: string}
  /chat-file:
    post:
      requestBody:
        content:
          multipart/form-data:
            schema: {type: object, properties: {file: {type: string, format: binary}}}
`))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	ws, _ := LoadWorkspace("/p")
	if _, err := ImportOpenAPI(ws, spec); err != nil {
		t.Fatal(err)
	}
	addr := findReq(t, ws, "POST /addresses")
	want := []SavedHeader{
		{Key: "label", Value: "Home", Enabled: true},
		{Key: "picture", Value: "@", Enabled: false},
		{Key: "location", Value: `{ "lat": 32.8 }`, Enabled: true},
		{Key: "note", Value: "string", Enabled: false},
	}
	if addr.BodyMode != BodyForm || !reflect.DeepEqual(addr.Form, want) {
		t.Errorf("addresses:\n got %+v\nwant %+v", addr.Form, want)
	}
	if f := findReq(t, ws, "POST /chat-file").Form; len(f) != 1 || !f[0].Enabled {
		t.Errorf("a lone file field should be on: %+v", f)
	}
}

func TestSendMultipart(t *testing.T) {
	var got []part
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = readParts(t, body, r.Header.Get("Content-Type"))
		w.WriteHeader(201)
	}))
	defer srv.Close()

	cwd := t.TempDir()
	os.WriteFile(filepath.Join(cwd, "a.png"), pngBytes, 0o644)
	r := Request{Method: "POST", URL: srv.URL, BodyMode: BodyForm,
		Headers: []SavedHeader{{Key: "Content-Type", Value: "multipart/form-data", Enabled: true}}, // no boundary: replaced
		Form:    []SavedHeader{{Key: "file", Value: "@a.png", Enabled: true}, {Key: "n", Value: "1", Enabled: true}}}
	resp, err := RunRequest(context.Background(), r, cwd)
	if err != nil || resp.StatusCode != 201 {
		t.Fatalf("send: %v %+v", err, resp)
	}
	if len(got) != 2 || got[0].filename != "a.png" || got[0].ctype != "image/png" || got[1].body != "1" {
		t.Errorf("server got %+v", got)
	}

	r.Form[0].Value = "@gone.png"
	if _, err := RunRequest(context.Background(), r, cwd); err == nil || !strings.Contains(err.Error(), `form field "file"`) {
		t.Errorf("missing file should fail before sending: %v", err)
	}
}
