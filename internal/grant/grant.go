// Package grant reads what the tailnet policy file allows a caller to do.
//
// Permissions live in the policy file's grants, not in a local config, so the
// people who already manage network access also manage model access.
package grant

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"tailscale.com/tailcfg"
)

// Capability is the app capability name taillow looks for in a caller's
// grants. It must be URL-shaped so it cannot collide with another app's.
const Capability tailcfg.PeerCapability = "github.com/13inks/taillow/cap/llm"

// ErrNoGrant means no grant names this app for the caller. It is an error,
// not an empty Grant: a missing capability must never read as "no limits".
var ErrNoGrant = errors.New("no grant for this app")

// Grant represents the aggregated permissions from all matching capability map entries.
type Grant struct {
	Models      []string `json:"models"`
	DailyTokens int64    `json:"dailyTokens"`
}

// InvalidError indicates a specific entry in the capability map failed validation.
type InvalidError struct {
	Index  int
	Field  string
	Reason string
}

// Error names the value and the field, so a refusal tells the policy author
// exactly what to fix.
func (e *InvalidError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("grant value %d: field %q: %s", e.Index, e.Field, e.Reason)
	}
	return fmt.Sprintf("grant value %d: %s", e.Index, e.Reason)
}

// FromCapMap extracts and validates the LLM grant from the provided capability map.
func FromCapMap(cm tailcfg.PeerCapMap) (Grant, error) {
	raw := cm[Capability]
	if len(raw) == 0 {
		return Grant{}, ErrNoGrant
	}

	var merged Grant
	for i, val := range raw {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(val), &fields); err != nil || fields == nil {
			return Grant{}, &InvalidError{Index: i, Reason: "must be a JSON object"}
		}

		// Check for unknown keys first.
		var unknownKeys []string
		for k := range fields {
			switch k {
			case "models", "dailyTokens":
				continue
			default:
				unknownKeys = append(unknownKeys, k)
			}
		}
		if len(unknownKeys) > 0 {
			slices.Sort(unknownKeys)
			return Grant{}, &InvalidError{Index: i, Field: unknownKeys[0], Reason: "unknown field"}
		}

		// encoding/json keeps the last of two identical keys, so
		// {"dailyTokens":1,"dailyTokens":999} would quietly grant 999.
		if dup := duplicateKey(val); dup != "" {
			return Grant{}, &InvalidError{Index: i, Field: dup, Reason: "duplicate field"}
		}

		// Validate "models".
		var models []string
		if rawModels, ok := fields["models"]; ok {
			if err := json.Unmarshal(rawModels, &models); err != nil {
				return Grant{}, &InvalidError{Index: i, Field: "models", Reason: "must be a list of model names"}
			}
			if len(models) == 0 {
				return Grant{}, &InvalidError{Index: i, Field: "models", Reason: "must name at least one model"}
			}
			for _, m := range models {
				if m == "" {
					return Grant{}, &InvalidError{Index: i, Field: "models", Reason: "model names must be non-empty"}
				}
			}
			merged.Models = slices.Concat(merged.Models, models)
		} else {
			return Grant{}, &InvalidError{Index: i, Field: "models", Reason: "missing"}
		}

		// Validate "dailyTokens".
		if rawTokens, ok := fields["dailyTokens"]; ok {
			var tokens int64
			if err := json.Unmarshal(rawTokens, &tokens); err != nil {
				return Grant{}, &InvalidError{Index: i, Field: "dailyTokens", Reason: "must be a positive integer"}
			}
			if tokens <= 0 {
				return Grant{}, &InvalidError{Index: i, Field: "dailyTokens", Reason: "must be a positive integer"}
			}
			// The smallest budget wins: a second grant can widen the
			// model list, but it can never raise the spend ceiling.
			if i == 0 || tokens < merged.DailyTokens {
				merged.DailyTokens = tokens
			}
		} else {
			// No default: a grant that forgets its budget is refused, not unlimited.
			return Grant{}, &InvalidError{Index: i, Field: "dailyTokens", Reason: "missing"}
		}
	}

	// Sort and de-duplicate so the result does not depend on grant order.
	slices.Sort(merged.Models)
	merged.Models = slices.Compact(merged.Models)

	return merged, nil
}

// duplicateKey returns the first top-level key that appears twice in obj, or
// "" when every key is unique. obj has already decoded as a JSON object, so
// the token walk cannot fail on well-formed input.
func duplicateKey(obj tailcfg.RawMessage) string {
	dec := json.NewDecoder(strings.NewReader(string(obj)))
	if _, err := dec.Token(); err != nil { // the opening brace
		return ""
	}
	seen := make(map[string]bool)
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		key, _ := tok.(string)
		if seen[key] {
			return key
		}
		seen[key] = true
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return ""
		}
	}
	return ""
}
