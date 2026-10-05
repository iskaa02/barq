package core

import (
	"reflect"
	"testing"
)

func TestParseParams(t *testing.T) {
	got := ParamRows("https://x.com/s?q=a%20b&tag=1&tag=2&flag#top", nil)
	want := []HeaderRow{
		{Key: "q", Value: "a b", Enabled: true},
		{Key: "tag", Value: "1", Enabled: true},
		{Key: "tag", Value: "2", Enabled: true},
		{Key: "flag", Value: "", Enabled: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
}

func TestBuildURL(t *testing.T) {
	rows := []HeaderRow{
		{Key: "q", Value: "a&b c", Enabled: true},
		{Key: "off", Value: "1", Enabled: false},
		{Key: "token", Value: "{{token}}", Enabled: true},
		{Key: "flag", Enabled: true},
		{Key: "", Value: "ignored", Enabled: true},
	}
	got := BuildURL("{{baseUrl}}/s?old=1#frag", rows)
	want := "{{baseUrl}}/s?q=a%26b+c&token={{token}}&flag#frag"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if got := BuildURL("https://x.com/s?a=1", nil); got != "https://x.com/s" {
		t.Errorf("no params should drop the '?': %s", got)
	}
}

func TestParamsRoundTrip(t *testing.T) {
	u := "https://x.com/s?q=a+b&n=1&id={{id}}"
	if got := BuildURL(u, ParamRows(u, nil)); got != u {
		t.Errorf("round trip changed the URL: %s", got)
	}
}
