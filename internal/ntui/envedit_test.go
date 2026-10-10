package ntui

import (
	"strings"
	"testing"

	"github.com/iskaa02/barq/internal/core"
)

func TestEnvRenderParseRoundTrip(t *testing.T) {
	e := core.Environment{Name: "dev", Protected: true, Vars: []core.SavedHeader{
		{Key: "base", Value: "http://x", Enabled: true},
		{Key: "token", Value: "s3cret", Enabled: true},
		{Key: "old", Value: "1"},
	}}
	lines := renderEnv(e)
	if strings.Contains(strings.Join(lines, "\n"), "s3cret") {
		t.Fatal("secret leaked into the buffer")
	}
	prot, vars, errs := parseEnv(lines)
	if len(errs) > 0 || prot != "writes" || len(vars) != 3 {
		t.Fatalf("prot=%q vars=%+v errs=%v", prot, vars, errs)
	}
	if !vars[1].Secret || vars[1].Value != envMask || !vars[2].Disabled || vars[0].Value != "http://x" {
		t.Fatalf("%+v", vars)
	}
}

func TestParseEnvErrors(t *testing.T) {
	_, _, errs := parseEnv([]string{"## protection: maybe", "nonsense", "a = 1", "a = 2", "b c = 3"})
	if len(errs) != 4 || errs[0].Line != 0 || errs[1].Line != 1 || errs[2].Line != 3 || errs[3].Line != 4 {
		t.Fatalf("%+v", errs)
	}
}

func TestApplyEnv(t *testing.T) {
	w := &core.Workspace{}
	id := w.AddEnv("dev", []core.SavedHeader{{Key: "token", Value: "s3cret", Enabled: true}})
	_, vars, _ := parseEnv([]string{"## protection: all", "token = " + envMask + " # secret", "base = http://x", "key = abc # secret"})
	if err := applyEnv(w, id, "all", vars); err != nil {
		t.Fatal(err)
	}
	e, _ := w.Env(id)
	if !e.Protected || !e.ProtectReads || len(e.Vars) != 3 || e.Vars[0].Value != "s3cret" ||
		!core.IsSecret(e.Vars[2]) || core.IsSecret(e.Vars[1]) {
		t.Fatalf("%+v", e)
	}
	_, vars, _ = parseEnv([]string{"nope = " + envMask + " # secret"})
	if err := applyEnv(w, id, "none", vars); err == nil {
		t.Fatal("mask without stored value accepted")
	}
	_, vars, _ = parseEnv([]string{"token = " + envMask})
	if err := applyEnv(w, id, "none", vars); err == nil {
		t.Fatal("unmarking a secret without retyping accepted")
	}
}
