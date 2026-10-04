package main

import (
	"reflect"
	"testing"
)

func TestParseParams(t *testing.T) {
	got := paramRows("https://x.com/s?q=a%20b&tag=1&tag=2&flag#top", nil)
	want := []headerRow{
		{key: "q", value: "a b", enabled: true},
		{key: "tag", value: "1", enabled: true},
		{key: "tag", value: "2", enabled: true},
		{key: "flag", value: "", enabled: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
}

func TestBuildURL(t *testing.T) {
	rows := []headerRow{
		{key: "q", value: "a&b c", enabled: true},
		{key: "off", value: "1", enabled: false},
		{key: "token", value: "{{token}}", enabled: true},
		{key: "flag", enabled: true},
		{key: "", value: "ignored", enabled: true},
	}
	got := buildURL("{{baseUrl}}/s?old=1#frag", rows)
	want := "{{baseUrl}}/s?q=a%26b+c&token={{token}}&flag#frag"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if got := buildURL("https://x.com/s?a=1", nil); got != "https://x.com/s" {
		t.Errorf("no params should drop the '?': %s", got)
	}
}

func TestParamsRoundTrip(t *testing.T) {
	u := "https://x.com/s?q=a+b&n=1&id={{id}}"
	if got := buildURL(u, paramRows(u, nil)); got != u {
		t.Errorf("round trip changed the URL: %s", got)
	}
}
