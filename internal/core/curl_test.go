package core

import (
	"reflect"
	"testing"
)

func TestParseCurl(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want curlRequest
	}{
		{
			name: "multi-line with continuations",
			cmd: `curl --request GET \
  --url 'https://example.com/api/tx?date_from=2026-07-02&page_size=1000' \
  --header 'Authorization: Bearer abc' \
  --header 'content-type: application/json' `,
			want: curlRequest{
				Method: "GET",
				URL:    "https://example.com/api/tx?date_from=2026-07-02&page_size=1000",
				Headers: []HeaderRow{
					{Key: "Authorization", Value: "Bearer abc", Enabled: true},
					{Key: "content-type", Value: "application/json", Enabled: true},
				},
			},
		},
		{
			name: "continuations flattened to spaces",
			cmd:  `curl --request GET \   --url 'https://example.com/x' \   --header 'A: b'`,
			want: curlRequest{
				Method:  "GET",
				URL:     "https://example.com/x",
				Headers: []HeaderRow{{Key: "A", Value: "b", Enabled: true}},
			},
		},
		{
			name: "data implies POST and form content type",
			cmd:  `curl -sSL https://example.com -d 'a=1' -d "b=2"`,
			want: curlRequest{
				Method: "POST",
				URL:    "https://example.com",
				Headers: []HeaderRow{
					{Key: "Content-Type", Value: "application/x-www-form-urlencoded", Enabled: true},
				},
				Body: "a=1&b=2",
			},
		},
		{
			name: "bundled -X and --json",
			cmd:  `curl -XPUT --json '{"x": 1}' https://example.com/items/1`,
			want: curlRequest{
				Method: "PUT",
				URL:    "https://example.com/items/1",
				Headers: []HeaderRow{
					{Key: "Content-Type", Value: "application/json", Enabled: true},
					{Key: "Accept", Value: "application/json", Enabled: true},
				},
				Body: `{"x": 1}`,
			},
		},
		{
			name: "-G moves data to the query, -u becomes basic auth",
			cmd:  `curl -G https://example.com/s?x=1 -d q=go -u user:pass`,
			want: curlRequest{
				Method: "GET",
				URL:    "https://example.com/s?x=1&q=go",
				Headers: []HeaderRow{
					{Key: "Authorization", Value: "Basic dXNlcjpwYXNz", Enabled: true},
				},
			},
		},
		{
			name: "browser copy-as-curl with $'...' and --compressed",
			cmd:  `curl 'https://example.com/' -H 'accept: */*' --data-raw $'{"a":"it\'s"}' --compressed`,
			want: curlRequest{
				Method: "POST",
				URL:    "https://example.com/",
				Headers: []HeaderRow{
					{Key: "accept", Value: "*/*", Enabled: true},
					{Key: "Content-Type", Value: "application/x-www-form-urlencoded", Enabled: true},
				},
				Body: `{"a":"it's"}`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := ParseCurl(tt.cmd)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestParseCurlErrors(t *testing.T) {
	for _, cmd := range []string{`curl -H 'A: b'`, `curl 'https://x`, `wget https://x`} {
		if _, _, err := ParseCurl(cmd); err == nil {
			t.Errorf("%q: expected an error", cmd)
		}
	}
}

func TestParseCurlCmd(t *testing.T) {
	// Chrome's "Copy as cURL (cmd)".
	cmd := "curl --url ^\"https://example.com/boards/6ab4/import-excel?q=a^%^20b^\" ^\r\n" +
		"  -X ^\"POST^\" ^\r\n" +
		"  -H ^\"accept: application/json, text/plain, */*^\" ^\r\n" +
		"  -H ^\"sec-ch-ua: ^\\^\"Not A^(Brand^\\^\";v=^\\^\"99^\\^\"^\" ^\r\n" +
		"  -H ^\"user-agent: Mozilla/5.0 ^(Windows NT 10.0; Win64; x64^)^\" ^\r\n" +
		"  --data-raw ^\"^{^\\^\"a^\\^\":^\\^\"x ^& y^\\^\"^}^\" ^\r\n"
	got, _, err := ParseCurl(cmd)
	if err != nil {
		t.Fatal(err)
	}
	want := curlRequest{
		Method: "POST",
		URL:    "https://example.com/boards/6ab4/import-excel?q=a%20b",
		Headers: []HeaderRow{
			{Key: "accept", Value: "application/json, text/plain, */*", Enabled: true},
			{Key: "sec-ch-ua", Value: `"Not A(Brand";v="99"`, Enabled: true},
			{Key: "user-agent", Value: "Mozilla/5.0 (Windows NT 10.0; Win64; x64)", Enabled: true},
			{Key: "Content-Type", Value: "application/x-www-form-urlencoded", Enabled: true},
		},
		Body: `{"a":"x & y"}`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}

	// The same, typed by hand: plain quotes, and newlines flattened by a
	// single-line input.
	got, _, err = ParseCurl(`curl "https://example.com/x" ^ -H "a: \"b\""`)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://example.com/x" || len(got.Headers) != 1 || got.Headers[0].Value != `"b"` {
		t.Errorf("got %+v", got)
	}
}
