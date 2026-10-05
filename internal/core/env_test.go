package core

import (
	"strings"
	"testing"
)

func TestSubstitute(t *testing.T) {
	vars := map[string]string{"base": "https://x.com", "token": "abc"}
	missing := map[string]bool{}
	got := substitute("{{base}}/u?t={{ token }}&n={{nope}}", vars, missing)
	if got != "https://x.com/u?t=abc&n={{nope}}" {
		t.Errorf("got %q", got)
	}
	if !missing["nope"] || len(missing) != 1 {
		t.Errorf("missing = %v", missing)
	}
	if id := substitute("{{$uuid}}", nil, map[string]bool{}); len(id) != 36 || strings.Contains(id, "{") {
		t.Errorf("$uuid gave %q", id)
	}
}
