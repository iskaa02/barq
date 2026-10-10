package jsonpos

import (
	"strings"
	"testing"

	"github.com/iskaa02/barq/internal/core"
)

func TestPathsAreValidJQ(t *testing.T) {
	tests := []struct{ text, needle, want string }{
		{`[{"id": 5}, {"id": 6}]`, `6`, ".[1].id"},
		{`[1, 2]`, `2`, ".[1]"},
		{`[[1, 2], [3]]`, `3`, ".[1][0]"},
		{`{"a": [{"x-y": 4}]}`, `4`, `.a[0].["x-y"]`},
		{`7`, `7`, "."},
	}
	for _, tc := range tests {
		off := strings.Index(tc.text, tc.needle)
		got, ok := PathAt(tc.text, 0, off)
		if !ok || got != tc.want {
			t.Errorf("%s: got %q %v, want %q", tc.text, got, ok, tc.want)
			continue
		}
		if _, err := core.RunJQ(got, []byte(tc.text)); err != nil {
			t.Errorf("%s: path %q not valid jq: %v", tc.text, got, err)
		}
	}
}
