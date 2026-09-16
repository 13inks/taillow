package grant

import (
	"errors"
	"slices"
	"testing"

	"tailscale.com/tailcfg"
)

func TestFromCapMap(t *testing.T) {
	t.Run("nil_map", func(t *testing.T) {
		var cm tailcfg.PeerCapMap = nil
		g, err := FromCapMap(cm)
		if !errors.Is(err, ErrNoGrant) {
			t.Fatalf("got %v (%T), want ErrNoGrant", err, err)
		}
		if !isZero(g) {
			t.Fatalf("got %+v, want zero Grant", g)
		}
	})

	t.Run("map_without_key", func(t *testing.T) {
		cm := tailcfg.PeerCapMap{}
		g, err := FromCapMap(cm)
		if !errors.Is(err, ErrNoGrant) {
			t.Fatalf("got %v (%T), want ErrNoGrant", err, err)
		}
		if !isZero(g) {
			t.Fatalf("got %+v, want zero Grant", g)
		}
	})

	t.Run("key_with_empty_slice", func(t *testing.T) {
		cm := tailcfg.PeerCapMap{Capability: []tailcfg.RawMessage{}}
		g, err := FromCapMap(cm)
		if !errors.Is(err, ErrNoGrant) {
			t.Fatalf("got %v (%T), want ErrNoGrant", err, err)
		}
		if !isZero(g) {
			t.Fatalf("got %+v, want zero Grant", g)
		}
	})

	t.Run("one_valid_value", func(t *testing.T) {
		raw := `{"models":["a","b"],"dailyTokens":100}`
		cm := tailcfg.PeerCapMap{Capability: []tailcfg.RawMessage{tailcfg.RawMessage(raw)}}
		g, err := FromCapMap(cm)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := Grant{Models: []string{"a", "b"}, DailyTokens: 100}
		if !slices.Equal(g.Models, want.Models) || g.DailyTokens != want.DailyTokens {
			t.Fatalf("got %+v, want %+v", g, want)
		}
	})

	t.Run("two_valid_values_merge", func(t *testing.T) {
		raw1 := `{"models":["b","c"],"dailyTokens":200}`
		raw2 := `{"models":["a","d"],"dailyTokens":50}`
		cm := tailcfg.PeerCapMap{Capability: []tailcfg.RawMessage{tailcfg.RawMessage(raw1), tailcfg.RawMessage(raw2)}}
		g, err := FromCapMap(cm)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		wantModels := []string{"a", "b", "c", "d"}
		if !slices.Equal(g.Models, wantModels) || g.DailyTokens != 50 {
			t.Fatalf("got %+v, want models=%v dailyTokens=50", g, wantModels)
		}
	})

	t.Run("not_json", func(t *testing.T) {
		cm := tailcfg.PeerCapMap{Capability: []tailcfg.RawMessage{"not json"}}
		g, err := FromCapMap(cm)
		var inv *InvalidError
		if !errors.As(err, &inv) {
			t.Fatalf("got %v (%T), want *InvalidError", err, err)
		}
		if inv.Index != 0 || inv.Field != "" || inv.Reason != "must be a JSON object" {
			t.Fatalf("InvalidError: Index=%d Field=%q Reason=%q", inv.Index, inv.Field, inv.Reason)
		}
		if !isZero(g) {
			t.Fatalf("got %+v, want zero Grant", g)
		}
	})

	t.Run("json_null", func(t *testing.T) {
		cm := tailcfg.PeerCapMap{Capability: []tailcfg.RawMessage{"null"}}
		g, err := FromCapMap(cm)
		var inv *InvalidError
		if !errors.As(err, &inv) {
			t.Fatalf("got %v (%T), want *InvalidError", err, err)
		}
		if inv.Index != 0 || inv.Field != "" || inv.Reason != "must be a JSON object" {
			t.Fatalf("InvalidError: Index=%d Field=%q Reason=%q", inv.Index, inv.Field, inv.Reason)
		}
		if !isZero(g) {
			t.Fatalf("got %+v, want zero Grant", g)
		}
	})

	t.Run("json_array", func(t *testing.T) {
		cm := tailcfg.PeerCapMap{Capability: []tailcfg.RawMessage{"[1,2]"}}
		g, err := FromCapMap(cm)
		var inv *InvalidError
		if !errors.As(err, &inv) {
			t.Fatalf("got %v (%T), want *InvalidError", err, err)
		}
		if inv.Index != 0 || inv.Field != "" || inv.Reason != "must be a JSON object" {
			t.Fatalf("InvalidError: Index=%d Field=%q Reason=%q", inv.Index, inv.Field, inv.Reason)
		}
		if !isZero(g) {
			t.Fatalf("got %+v, want zero Grant", g)
		}
	})

	t.Run("unknown_field_modles", func(t *testing.T) {
		cm := tailcfg.PeerCapMap{Capability: []tailcfg.RawMessage{`{"modles":["a"],"dailyTokens":10}`}}
		g, err := FromCapMap(cm)
		var inv *InvalidError
		if !errors.As(err, &inv) {
			t.Fatalf("got %v (%T), want *InvalidError", err, err)
		}
		if inv.Index != 0 || inv.Field != "modles" || inv.Reason != "unknown field" {
			t.Fatalf("InvalidError: Index=%d Field=%q Reason=%q", inv.Index, inv.Field, inv.Reason)
		}
		if !isZero(g) {
			t.Fatalf("got %+v, want zero Grant", g)
		}
	})

	t.Run("two_unknown_fields", func(t *testing.T) {
		cm := tailcfg.PeerCapMap{Capability: []tailcfg.RawMessage{`{"zzz":["a"],"aaa":1}`}}
		g, err := FromCapMap(cm)
		var inv *InvalidError
		if !errors.As(err, &inv) {
			t.Fatalf("got %v (%T), want *InvalidError", err, err)
		}
		if inv.Index != 0 || inv.Field != "aaa" || inv.Reason != "unknown field" {
			t.Fatalf("InvalidError: Index=%d Field=%q Reason=%q", inv.Index, inv.Field, inv.Reason)
		}
		if !isZero(g) {
			t.Fatalf("got %+v, want zero Grant", g)
		}
	})

	t.Run("missing_models", func(t *testing.T) {
		cm := tailcfg.PeerCapMap{Capability: []tailcfg.RawMessage{`{"dailyTokens":10}`}}
		g, err := FromCapMap(cm)
		var inv *InvalidError
		if !errors.As(err, &inv) {
			t.Fatalf("got %v (%T), want *InvalidError", err, err)
		}
		if inv.Index != 0 || inv.Field != "models" || inv.Reason != "missing" {
			t.Fatalf("InvalidError: Index=%d Field=%q Reason=%q", inv.Index, inv.Field, inv.Reason)
		}
		if !isZero(g) {
			t.Fatalf("got %+v, want zero Grant", g)
		}
	})

	t.Run("models_as_string", func(t *testing.T) {
		cm := tailcfg.PeerCapMap{Capability: []tailcfg.RawMessage{`{"models":"a","dailyTokens":10}`}}
		g, err := FromCapMap(cm)
		var inv *InvalidError
		if !errors.As(err, &inv) {
			t.Fatalf("got %v (%T), want *InvalidError", err, err)
		}
		if inv.Index != 0 || inv.Field != "models" || inv.Reason != "must be a list of model names" {
			t.Fatalf("InvalidError: Index=%d Field=%q Reason=%q", inv.Index, inv.Field, inv.Reason)
		}
		if !isZero(g) {
			t.Fatalf("got %+v, want zero Grant", g)
		}
	})

	t.Run("empty_models_list", func(t *testing.T) {
		cm := tailcfg.PeerCapMap{Capability: []tailcfg.RawMessage{`{"models":[],"dailyTokens":10}`}}
		g, err := FromCapMap(cm)
		var inv *InvalidError
		if !errors.As(err, &inv) {
			t.Fatalf("got %v (%T), want *InvalidError", err, err)
		}
		if inv.Index != 0 || inv.Field != "models" || inv.Reason != "must name at least one model" {
			t.Fatalf("InvalidError: Index=%d Field=%q Reason=%q", inv.Index, inv.Field, inv.Reason)
		}
		if !isZero(g) {
			t.Fatalf("got %+v, want zero Grant", g)
		}
	})

	t.Run("empty_model_name", func(t *testing.T) {
		cm := tailcfg.PeerCapMap{Capability: []tailcfg.RawMessage{`{"models":[""],"dailyTokens":10}`}}
		g, err := FromCapMap(cm)
		var inv *InvalidError
		if !errors.As(err, &inv) {
			t.Fatalf("got %v (%T), want *InvalidError", err, err)
		}
		if inv.Index != 0 || inv.Field != "models" || inv.Reason != "model names must be non-empty" {
			t.Fatalf("InvalidError: Index=%d Field=%q Reason=%q", inv.Index, inv.Field, inv.Reason)
		}
		if !isZero(g) {
			t.Fatalf("got %+v, want zero Grant", g)
		}
	})

	t.Run("missing_dailyTokens", func(t *testing.T) {
		cm := tailcfg.PeerCapMap{Capability: []tailcfg.RawMessage{`{"models":["a"]}`}}
		g, err := FromCapMap(cm)
		var inv *InvalidError
		if !errors.As(err, &inv) {
			t.Fatalf("got %v (%T), want *InvalidError", err, err)
		}
		if inv.Index != 0 || inv.Field != "dailyTokens" || inv.Reason != "missing" {
			t.Fatalf("InvalidError: Index=%d Field=%q Reason=%q", inv.Index, inv.Field, inv.Reason)
		}
		if !isZero(g) {
			t.Fatalf("got %+v, want zero Grant", g)
		}
	})

	t.Run("dailyTokens_zero", func(t *testing.T) {
		cm := tailcfg.PeerCapMap{Capability: []tailcfg.RawMessage{`{"models":["a"],"dailyTokens":0}`}}
		g, err := FromCapMap(cm)
		var inv *InvalidError
		if !errors.As(err, &inv) {
			t.Fatalf("got %v (%T), want *InvalidError", err, err)
		}
		if inv.Index != 0 || inv.Field != "dailyTokens" || inv.Reason != "must be a positive integer" {
			t.Fatalf("InvalidError: Index=%d Field=%q Reason=%q", inv.Index, inv.Field, inv.Reason)
		}
		if !isZero(g) {
			t.Fatalf("got %+v, want zero Grant", g)
		}
	})

	t.Run("dailyTokens_negative", func(t *testing.T) {
		cm := tailcfg.PeerCapMap{Capability: []tailcfg.RawMessage{`{"models":["a"],"dailyTokens":-5}`}}
		g, err := FromCapMap(cm)
		var inv *InvalidError
		if !errors.As(err, &inv) {
			t.Fatalf("got %v (%T), want *InvalidError", err, err)
		}
		if inv.Index != 0 || inv.Field != "dailyTokens" || inv.Reason != "must be a positive integer" {
			t.Fatalf("InvalidError: Index=%d Field=%q Reason=%q", inv.Index, inv.Field, inv.Reason)
		}
		if !isZero(g) {
			t.Fatalf("got %+v, want zero Grant", g)
		}
	})

	t.Run("dailyTokens_string", func(t *testing.T) {
		cm := tailcfg.PeerCapMap{Capability: []tailcfg.RawMessage{`{"models":["a"],"dailyTokens":"100"}`}}
		g, err := FromCapMap(cm)
		var inv *InvalidError
		if !errors.As(err, &inv) {
			t.Fatalf("got %v (%T), want *InvalidError", err, err)
		}
		if inv.Index != 0 || inv.Field != "dailyTokens" || inv.Reason != "must be a positive integer" {
			t.Fatalf("InvalidError: Index=%d Field=%q Reason=%q", inv.Index, inv.Field, inv.Reason)
		}
		if !isZero(g) {
			t.Fatalf("got %+v, want zero Grant", g)
		}
	})

	t.Run("dailyTokens_fraction", func(t *testing.T) {
		cm := tailcfg.PeerCapMap{Capability: []tailcfg.RawMessage{`{"models":["a"],"dailyTokens":1.5}`}}
		g, err := FromCapMap(cm)
		var inv *InvalidError
		if !errors.As(err, &inv) {
			t.Fatalf("got %v (%T), want *InvalidError", err, err)
		}
		if inv.Index != 0 || inv.Field != "dailyTokens" || inv.Reason != "must be a positive integer" {
			t.Fatalf("InvalidError: Index=%d Field=%q Reason=%q", inv.Index, inv.Field, inv.Reason)
		}
		if !isZero(g) {
			t.Fatalf("got %+v, want zero Grant", g)
		}
	})

	t.Run("dailyTokens_one", func(t *testing.T) {
		cm := tailcfg.PeerCapMap{Capability: []tailcfg.RawMessage{`{"models":["a"],"dailyTokens":1}`}}
		g, err := FromCapMap(cm)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := Grant{Models: []string{"a"}, DailyTokens: 1}
		if !slices.Equal(g.Models, want.Models) || g.DailyTokens != want.DailyTokens {
			t.Fatalf("got %+v, want %+v", g, want)
		}
	})

	t.Run("one_valid_one_invalid_index_1", func(t *testing.T) {
		raw0 := `{"models":["a"],"dailyTokens":10}`
		raw1 := `{"models":["b"],"dailyTokens":-5}`
		cm := tailcfg.PeerCapMap{Capability: []tailcfg.RawMessage{tailcfg.RawMessage(raw0), tailcfg.RawMessage(raw1)}}
		g, err := FromCapMap(cm)
		var inv *InvalidError
		if !errors.As(err, &inv) {
			t.Fatalf("got %v (%T), want *InvalidError", err, err)
		}
		if inv.Index != 1 || inv.Field != "dailyTokens" || inv.Reason != "must be a positive integer" {
			t.Fatalf("InvalidError: Index=%d Field=%q Reason=%q", inv.Index, inv.Field, inv.Reason)
		}
		if !isZero(g) {
			t.Fatalf("got %+v, want zero Grant", g)
		}
	})

	t.Run("error_text_with_field", func(t *testing.T) {
		cm := tailcfg.PeerCapMap{Capability: []tailcfg.RawMessage{`{"models":["a"],"dailyTokens":0}`}}
		_, err := FromCapMap(cm)
		if err == nil {
			t.Fatal("expected error")
		}
		want := `grant value 0: field "dailyTokens": must be a positive integer`
		if err.Error() != want {
			t.Fatalf("Error() = %q, want %q", err.Error(), want)
		}
	})

	t.Run("error_text_without_field", func(t *testing.T) {
		cm := tailcfg.PeerCapMap{Capability: []tailcfg.RawMessage{"not json"}}
		_, err := FromCapMap(cm)
		if err == nil {
			t.Fatal("expected error")
		}
		want := `grant value 0: must be a JSON object`
		if err.Error() != want {
			t.Fatalf("Error() = %q, want %q", err.Error(), want)
		}
	})
}

// isZero reports whether g is the zero Grant. Grant holds a slice, so == cannot compare it.
func isZero(g Grant) bool { return g.Models == nil && g.DailyTokens == 0 }

func TestFromCapMapRefusesDuplicateFields(t *testing.T) {
	tests := []struct {
		name  string
		value tailcfg.RawMessage
		field string
	}{
		{"a repeated budget cannot quietly raise the ceiling", `{"models":["a"],"dailyTokens":1,"dailyTokens":999}`, "dailyTokens"},
		{"a repeated model list is ambiguous too", `{"models":["a"],"models":["b"],"dailyTokens":1}`, "models"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g, err := FromCapMap(tailcfg.PeerCapMap{Capability: {tc.value}})
			var inv *InvalidError
			if !errors.As(err, &inv) {
				t.Fatalf("err = %v, want *InvalidError", err)
			}
			if inv.Index != 0 || inv.Field != tc.field || inv.Reason != "duplicate field" {
				t.Errorf("got %+v, want index 0, field %q, reason %q", inv, tc.field, "duplicate field")
			}
			if !isZero(g) {
				t.Errorf("grant = %+v, want the zero Grant", g)
			}
		})
	}
}
