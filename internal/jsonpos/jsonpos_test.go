package jsonpos

import "testing"

const doc = `{
  "data": {
    "items": [
      {"id": 7, "a-b": "x\"y"},
      12
    ],
    "accessToken": "tok"
  },
  "ok": true
}`

func TestPathAt(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		line, col int
		want      string
		ok        bool
	}{
		{"root brace", doc, 0, 0, ".", true},
		{"key", doc, 1, 4, ".data", true},
		{"nested key", doc, 6, 6, ".data.accessToken", true},
		{"string value", doc, 6, 22, ".data.accessToken", true},
		{"number in object in array", doc, 3, 13, ".data.items[0].id", true},
		{"number in array", doc, 4, 7, ".data.items[1]", true},
		{"odd key", doc, 3, 18, `.data.items[0].["a-b"]`, true},
		{"escaped string value", doc, 3, 27, `.data.items[0].["a-b"]`, true},
		{"nested object bracket", doc, 3, 6, ".data.items[0]", true},
		{"array bracket", doc, 2, 13, ".data.items", true},
		{"literal", doc, 8, 9, ".ok", true},
		{"compact", `{"a":[1,{"b":null}]}`, 0, 16, ".a[1].b", true},
		{"compact array elem", `{"a":[1,{"b":null}]}`, 0, 6, ".a[0]", true},
		{"scalar root", `  42 `, 0, 3, ".", true},
		{"whitespace outside", ` 42 `, 0, 0, "", false},
		{"past line", doc, 99, 0, "", false},
		{"past col", doc, 0, 5, "", false},
		{"invalid", `{"a": }`, 0, 1, "", false},
		{"trailing junk", `{"a":1} x`, 0, 1, "", false},
		{"empty", ``, 0, 0, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := PathAt(tt.text, tt.line, tt.col)
			if got != tt.want || ok != tt.ok {
				t.Errorf("got %q,%v want %q,%v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestVarName(t *testing.T) {
	tests := map[string]string{
		".data.accessToken": "accessToken",
		".items[0]":         "items",
		".":                 "value",
		".[0]":              "value",
		`.a.["a-b"]`:        "a_b",
		`.["a-b"][2]`:       "a_b",
		`.["9x"]`:           "_9x",
		".a.b[1].c":         "c",
	}
	for in, want := range tests {
		if got := VarName(in); got != want {
			t.Errorf("VarName(%q)=%q want %q", in, got, want)
		}
	}
}
