package strictjson

import (
	"errors"
	"testing"
)

func TestObject(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		allowed    []string
		wantField  string
		wantReason string // "" means the document is accepted
	}{
		{name: "accepts exactly the allowed keys", in: `{"a":1,"b":"x"}`, allowed: []string{"a", "b"}},
		{name: "accepts a subset", in: `{"a":1}`, allowed: []string{"a", "b"}},
		{name: "accepts an empty object", in: `{}`, allowed: []string{"a"}},

		{name: "null is not an object", in: `null`, allowed: []string{"a"}, wantReason: "must be a JSON object"},
		{name: "an array is not an object", in: `[1]`, allowed: []string{"a"}, wantReason: "must be a JSON object"},
		{name: "broken JSON is not an object", in: `{"a":`, allowed: []string{"a"}, wantReason: "must be a JSON object"},
		{name: "two objects are not an object", in: `{"a":1} {"a":2}`, allowed: []string{"a"}, wantReason: "must be a JSON object"},

		{name: "unknown key is named", in: `{"a":1,"z":2}`, allowed: []string{"a"}, wantField: "z", wantReason: "unknown field"},
		{name: "first unknown key alphabetically", in: `{"z":1,"m":2}`, allowed: []string{"a"}, wantField: "m", wantReason: "unknown field"},
		// encoding/json would match "A" to a field tagged "a". This must not.
		{name: "key case is exact", in: `{"A":1}`, allowed: []string{"a"}, wantField: "A", wantReason: "unknown field"},
		// A forgotten argument must not turn into "every key is fine".
		{name: "no allowed keys allows none", in: `{"a":1}`, wantField: "a", wantReason: "unknown field"},

		// encoding/json would keep 999.
		{name: "repeated key is named", in: `{"a":1,"a":999}`, allowed: []string{"a"}, wantField: "a", wantReason: "duplicate field"},
		{name: "unknown is reported before repeated", in: `{"a":1,"a":2,"z":3}`, allowed: []string{"a"}, wantField: "z", wantReason: "unknown field"},
		{name: "a repeat inside a value is not this check's business", in: `{"a":{"x":1,"x":2}}`, allowed: []string{"a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Object([]byte(tt.in), tt.allowed...)
			if tt.wantReason == "" {
				if err != nil {
					t.Fatalf("Object(%s) err = %v, want nil", tt.in, err)
				}
				if got == nil {
					t.Fatalf("Object(%s) = nil map, want the keys", tt.in)
				}
				return
			}
			var fe *FieldError
			if !errors.As(err, &fe) {
				t.Fatalf("Object(%s) err = %v, want a *FieldError", tt.in, err)
			}
			if fe.Field != tt.wantField || fe.Reason != tt.wantReason {
				t.Errorf("Object(%s) = field %q reason %q, want field %q reason %q", tt.in, fe.Field, fe.Reason, tt.wantField, tt.wantReason)
			}
			if got != nil {
				t.Errorf("Object(%s) returned keys alongside an error", tt.in)
			}
		})
	}
}

func TestObjectKeepsRawValues(t *testing.T) {
	got, err := Object([]byte(`{"n": 12, "s": "x"}`), "n", "s")
	if err != nil {
		t.Fatal(err)
	}
	if string(got["n"]) != "12" || string(got["s"]) != `"x"` {
		t.Errorf("raw values = %s, %s; want 12, \"x\"", got["n"], got["s"])
	}
}

func TestFieldErrorText(t *testing.T) {
	if got := (&FieldError{Field: "limit", Reason: "duplicate field"}).Error(); got != `field "limit": duplicate field` {
		t.Errorf("with a field: %q", got)
	}
	if got := (&FieldError{Reason: "must be a JSON object"}).Error(); got != "must be a JSON object" {
		t.Errorf("without a field: %q", got)
	}
}
