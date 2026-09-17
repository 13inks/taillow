// Package strictjson decodes one JSON object the way a permission check needs
// it: exact key names, no unknown keys, no repeated keys.
//
// encoding/json alone is too forgiving for that job. It matches struct keys
// case-insensitively, and it keeps the last of two identical keys, so
// {"limit":1,"limit":999} quietly means 999.
package strictjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
)

// FieldError names the key that was refused, so the refusal a person reads
// tells them what to fix instead of only that something was wrong.
type FieldError struct {
	Field  string // the offending key; "" when the fault is the document itself
	Reason string
}

// Error leads with the field when there is one.
func (e *FieldError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("field %q: %s", e.Field, e.Reason)
	}
	return e.Reason
}

// Object returns data's top-level keys, or a *FieldError naming the first fault.
// Checks run in a fixed order (object, unknown key, repeated key) so the same
// bad document always produces the same refusal.
func Object(data []byte, allowed ...string) (map[string]json.RawMessage, error) {
	// A nil map means the document was the literal null.
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, &FieldError{Reason: "must be a JSON object"}
	}
	if m == nil {
		return nil, &FieldError{Reason: "must be a JSON object"}
	}

	// Unknown keys. There is deliberately no "empty allowed list means anything
	// goes" shortcut: that would turn a forgotten argument into a check that
	// passes everything.
	var unknownKeys []string
	for k := range m {
		if !slices.Contains(allowed, k) {
			unknownKeys = append(unknownKeys, k)
		}
	}
	if len(unknownKeys) > 0 {
		slices.Sort(unknownKeys)
		return nil, &FieldError{Field: unknownKeys[0], Reason: "unknown field"}
	}

	// Repeated keys. Unmarshal kept only the last of each, so walk the tokens.
	seen := make(map[string]struct{})
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, &FieldError{Reason: "must be a JSON object"}
	}
	if tok != json.Delim('{') {
		return nil, &FieldError{Reason: "must be a JSON object"}
	}

	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, &FieldError{Reason: "must be a JSON object"}
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, &FieldError{Reason: "must be a JSON object"}
		}

		if _, exists := seen[key]; exists {
			return nil, &FieldError{Field: key, Reason: "duplicate field"}
		}
		seen[key] = struct{}{}

		// Skip the value.
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, &FieldError{Reason: "must be a JSON object"}
		}
	}

	// Nothing can follow the object: Unmarshal above already refused trailing
	// data, so there is no second check for it here.
	return m, nil
}
