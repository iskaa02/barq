package core

import (
	"reflect"
	"testing"
)

func TestToCurlRoundTrip(t *testing.T) {
	r := Request{
		Method: "POST",
		URL:    "https://api.x.com/users?a=1&b=2",
		Headers: []SavedHeader{
			{Key: "Content-Type", Value: "application/json", Enabled: true},
			{Key: "X-Off", Value: "skipped", Enabled: false},
			{Key: "X-Quote", Value: "it's", Enabled: true},
		},
		Body: `{"name": "O'Brien"}`,
	}
	got, _, err := ParseCurl(ToCurl(r))
	if err != nil {
		t.Fatal(err)
	}
	want := curlRequest{
		Method: "POST",
		URL:    r.URL,
		Headers: []HeaderRow{
			{Key: "Content-Type", Value: "application/json", Enabled: true},
			{Key: "X-Quote", Value: "it's", Enabled: true},
		},
		Body: r.Body,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, want)
	}
}
