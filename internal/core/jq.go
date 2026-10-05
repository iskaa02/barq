package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/itchyny/gojq"
)

const (
	jqTimeout   = 500 * time.Millisecond
	JQMaxOutput = 2 << 20
)

// jq --------------------------------------------------------------------------

// normalizeJSON converts json.Number values into the int/float/big.Int
// types gojq expects, keeping large integers exact.
func normalizeJSON(v any) any {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil && i == int64(int(i)) {
			return int(i)
		}
		if b, ok := new(big.Int).SetString(x.String(), 10); ok {
			return b
		}
		f, _ := x.Float64()
		return f
	case []any:
		for i := range x {
			x[i] = normalizeJSON(x[i])
		}
	case map[string]any:
		for k := range x {
			x[k] = normalizeJSON(x[k])
		}
	}
	return v
}

func ParseJSONBody(body []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, errors.New("response isn't JSON")
	}
	return normalizeJSON(v), nil
}

// RunJQ evaluates a jq filter against a JSON body and returns its outputs.
func RunJQ(filter string, body []byte) ([]any, error) {
	if _, err := gojq.Parse(filter); err != nil {
		return nil, err // report syntax errors before parsing the body
	}
	input, err := ParseJSONBody(body)
	if err != nil {
		return nil, err
	}
	return RunJQOn(filter, input)
}

// RunJQOn evaluates a jq filter against already-parsed JSON.
func RunJQOn(filter string, input any) ([]any, error) {
	q, err := gojq.Parse(filter)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), jqTimeout)
	defer cancel()

	var out []any
	iter := q.RunWithContext(ctx, input)
	for {
		v, ok := iter.Next()
		if !ok {
			return out, nil
		}
		if err, isErr := v.(error); isErr {
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, errors.New("filter took too long")
			}
			return nil, err
		}
		out = append(out, v)
		if len(out) > 10000 {
			return nil, errors.New("too many results")
		}
	}
}

func MarshalJQ(v any, indent bool) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}
