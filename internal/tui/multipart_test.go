package tui

import (
	"testing"

	"github.com/iskaa02/barq/internal/core"
)

func TestFormVariablesAndRawBodyUntouched(t *testing.T) {
	m := Model{ws: &core.Workspace{Environments: []core.Environment{{ID: "e", Vars: []core.SavedHeader{{Key: "dir", Value: "img", Enabled: true}}}}, ActiveEnv: "e"}}
	r, missing := m.resolve(core.Request{BodyMode: core.BodyForm, Form: []core.SavedHeader{
		{Key: "f", Value: "@{{dir}}/a.png", Enabled: true},
		{Key: "x", Value: "{{nope}}", Enabled: false}, // off: not checked
	}})
	if r.Form[0].Value != "@img/a.png" || len(missing) != 0 {
		t.Errorf("resolve: %+v missing %v", r.Form, missing)
	}
	// Raw mode ignores form fields entirely.
	body, ctype, _ := core.BuildBody(core.Request{Body: `{"a":1}`, Form: []core.SavedHeader{{Key: "f", Value: "@x", Enabled: true}}}, "/")
	if string(body) != `{"a":1}` || ctype != "" {
		t.Errorf("raw body: %q %q", body, ctype)
	}
}
