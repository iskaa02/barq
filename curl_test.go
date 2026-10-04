package main

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
				Headers: []headerRow{
					{key: "Authorization", value: "Bearer abc", enabled: true},
					{key: "content-type", value: "application/json", enabled: true},
				},
			},
		},
		{
			name: "continuations flattened to spaces",
			cmd:  `curl --request GET \   --url 'https://example.com/x' \   --header 'A: b'`,
			want: curlRequest{
				Method:  "GET",
				URL:     "https://example.com/x",
				Headers: []headerRow{{key: "A", value: "b", enabled: true}},
			},
		},
		{
			name: "data implies POST and form content type",
			cmd:  `curl -sSL https://example.com -d 'a=1' -d "b=2"`,
			want: curlRequest{
				Method: "POST",
				URL:    "https://example.com",
				Headers: []headerRow{
					{key: "Content-Type", value: "application/x-www-form-urlencoded", enabled: true},
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
				Headers: []headerRow{
					{key: "Content-Type", value: "application/json", enabled: true},
					{key: "Accept", value: "application/json", enabled: true},
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
				Headers: []headerRow{
					{key: "Authorization", value: "Basic dXNlcjpwYXNz", enabled: true},
				},
			},
		},
		{
			name: "browser copy-as-curl with $'...' and --compressed",
			cmd:  `curl 'https://example.com/' -H 'accept: */*' --data-raw $'{"a":"it\'s"}' --compressed`,
			want: curlRequest{
				Method: "POST",
				URL:    "https://example.com/",
				Headers: []headerRow{
					{key: "accept", value: "*/*", enabled: true},
					{key: "Content-Type", value: "application/x-www-form-urlencoded", enabled: true},
				},
				Body: `{"a":"it's"}`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := parseCurl(tt.cmd)
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
		if _, _, err := parseCurl(cmd); err == nil {
			t.Errorf("%q: expected an error", cmd)
		}
	}
}
