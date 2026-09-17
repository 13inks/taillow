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

	"github.com/13inks/taillow/internal/strictjson"
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
		// Exact keys, no repeats: strictjson explains why encoding/json alone
		// is too forgiving for a permission check.
		fields, err := strictjson.Object([]byte(val), "models", "dailyTokens")
		if err != nil {
			var fe *strictjson.FieldError
			if errors.As(err, &fe) {
				return Grant{}, &InvalidError{Index: i, Field: fe.Field, Reason: fe.Reason}
			}
			return Grant{}, &InvalidError{Index: i, Reason: err.Error()}
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
