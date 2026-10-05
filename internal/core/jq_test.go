package core

import (
	"reflect"
	"testing"
)

func TestRunJQ(t *testing.T) {
	body := []byte(`{"data":[{"id":12345678901234567890,"name":"a"},{"id":2,"name":"b"}],"token":"xyz"}`)

	out, err := RunJQ(".data[].name", body)
	if err != nil || !reflect.DeepEqual(out, []any{"a", "b"}) {
		t.Errorf("names: %v %v", out, err)
	}
	out, err = RunJQ(".data[0].id", body)
	if err != nil || MarshalJQ(out[0], false) != "12345678901234567890" {
		t.Errorf("big int should stay exact: %v %v", out, err)
	}
	if _, err := RunJQ(".data[", body); err == nil {
		t.Error("expected a parse error")
	}
	if _, err := RunJQ(".", []byte("<html>")); err == nil {
		t.Error("expected an error for non-JSON")
	}
	if _, err := RunJQ("def f: f; f", body); err == nil {
		t.Error("expected an endless filter to be stopped")
	}
}
