package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/13inks/taillow/internal/grant"
	"github.com/13inks/taillow/internal/ident"
	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
)

// fakeWhoIs answers WhoIs from fixed tables. An address in neither table is
// reported as unknown, the same way the real local client reports it.
type fakeWhoIs struct {
	resps map[string]*apitype.WhoIsResponse
	errs  map[string]error
}

func (f fakeWhoIs) WhoIs(_ context.Context, addr string) (*apitype.WhoIsResponse, error) {
	if err, ok := f.errs[addr]; ok {
		return nil, err
	}
	if r, ok := f.resps[addr]; ok {
		return r, nil
	}
	return nil, local.ErrPeerNotFound
}

func TestRoutes(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantAllow  string
		wantBody   string
	}{
		{"healthz answers GET", http.MethodGet, "/healthz", http.StatusOK, "", `{"status":"ok"}` + "\n"},
		{"healthz refuses POST and says what is allowed", http.MethodPost, "/healthz", http.StatusMethodNotAllowed, "GET, HEAD", ""},
		{"whoami refuses POST and says what is allowed", http.MethodPost, "/whoami", http.StatusMethodNotAllowed, "GET, HEAD", ""},
		{"unknown path is 404", http.MethodGet, "/nope", http.StatusNotFound, "", ""},
	}

	mux := newMux(&gateway{who: fakeWhoIs{}})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if got := rec.Header().Get("Allow"); got != tc.wantAllow {
				t.Errorf("Allow = %q, want %q", got, tc.wantAllow)
			}
			if tc.wantBody != "" && rec.Body.String() != tc.wantBody {
				t.Errorf("body = %q, want %q", rec.Body.String(), tc.wantBody)
			}
		})
	}
}

// peer builds a WhoIs answer for a user-owned node, or for a tagged node when
// tags are given. Every peer also holds another app's capability, so the tests
// prove that only taillow's own capability counts.
func peer(values []tailcfg.RawMessage, tags ...string) *apitype.WhoIsResponse {
	cm := tailcfg.PeerCapMap{"example.com/other-app/cap": {`{"models":["x"],"dailyTokens":1}`}}
	if values != nil {
		cm[grant.Capability] = values
	}
	return &apitype.WhoIsResponse{
		Node:        &tailcfg.Node{ComputedName: "alice-laptop", Tags: tags},
		UserProfile: &tailcfg.UserProfile{LoginName: "alice@example.com", DisplayName: "Alice"},
		CapMap:      cm,
	}
}

// whoamiBody decodes both shapes /whoami answers with: a refusal or a grant.
type whoamiBody struct {
	Status int           `json:"status"`
	Reason string        `json:"reason"`
	Caller *ident.Caller `json:"caller"`
	Grant  *grant.Grant  `json:"grant"`
}

func TestWhoami(t *testing.T) {
	const (
		unknown    = "192.0.2.1:41000"
		broken     = "192.0.2.2:41000"
		noGrant    = "192.0.2.3:41000"
		zeroBudget = "192.0.2.4:41000"
		typo       = "192.0.2.5:41000"
		twoGrants  = "192.0.2.6:41000"
		tagged     = "192.0.2.7:41000"
	)
	who := fakeWhoIs{
		errs: map[string]error{broken: errors.New("local api down secret-detail")},
		resps: map[string]*apitype.WhoIsResponse{
			noGrant:    peer(nil),
			zeroBudget: peer([]tailcfg.RawMessage{`{"models":["claude"],"dailyTokens":0}`}),
			typo:       peer([]tailcfg.RawMessage{`{"modles":["claude"],"dailyTokens":10}`}),
			twoGrants: peer([]tailcfg.RawMessage{
				`{"models":["b","a"],"dailyTokens":500}`,
				`{"models":["a","c"],"dailyTokens":100}`,
			}),
			tagged: peer([]tailcfg.RawMessage{`{"models":["claude"],"dailyTokens":10}`}, "tag:ci"),
		},
	}

	tests := []struct {
		name       string
		addr       string
		wantStatus int
		wantReason string
		check      func(t *testing.T, b whoamiBody, raw string)
	}{
		{
			name:       "unknown caller is refused without a caller",
			addr:       unknown,
			wantStatus: http.StatusForbidden,
			wantReason: "caller identity not resolvable on this tailnet",
			check: func(t *testing.T, b whoamiBody, _ string) {
				if b.Caller != nil {
					t.Errorf("caller = %+v, want none", b.Caller)
				}
			},
		},
		{
			name:       "lookup failure is refused and does not leak the error",
			addr:       broken,
			wantStatus: http.StatusServiceUnavailable,
			wantReason: "identity lookup failed; refusing rather than guessing",
			check: func(t *testing.T, _ whoamiBody, raw string) {
				if strings.Contains(raw, "secret-detail") {
					t.Errorf("body leaks the lookup error: %s", raw)
				}
			},
		},
		{
			name:       "known caller without a grant is refused and told who they are",
			addr:       noGrant,
			wantStatus: http.StatusForbidden,
			wantReason: "no grant for this app: the tailnet policy gives this caller no " + string(grant.Capability) + " capability",
			check: func(t *testing.T, b whoamiBody, _ string) {
				if b.Caller == nil || b.Caller.Login != "alice@example.com" {
					t.Errorf("caller = %+v, want login alice@example.com", b.Caller)
				}
				if b.Grant != nil {
					t.Errorf("grant = %+v, want none", b.Grant)
				}
			},
		},
		{
			name:       "zero budget is refused, not read as unlimited",
			addr:       zeroBudget,
			wantStatus: http.StatusForbidden,
			wantReason: `grant value 0: field "dailyTokens": must be a positive integer`,
			check:      func(*testing.T, whoamiBody, string) {},
		},
		{
			name:       "misspelled field is refused by name",
			addr:       typo,
			wantStatus: http.StatusForbidden,
			wantReason: `grant value 0: field "modles": unknown field`,
			check:      func(*testing.T, whoamiBody, string) {},
		},
		{
			name:       "two grants merge: union of models, smallest budget",
			addr:       twoGrants,
			wantStatus: http.StatusOK,
			check: func(t *testing.T, b whoamiBody, _ string) {
				if b.Grant == nil {
					t.Fatal("no grant in body")
				}
				if want := []string{"a", "b", "c"}; !slices.Equal(b.Grant.Models, want) {
					t.Errorf("models = %v, want %v", b.Grant.Models, want)
				}
				if b.Grant.DailyTokens != 100 {
					t.Errorf("dailyTokens = %d, want 100", b.Grant.DailyTokens)
				}
				if b.Caller == nil || b.Caller.Login != "alice@example.com" || b.Caller.Node != "alice-laptop" {
					t.Errorf("caller = %+v, want alice@example.com on alice-laptop", b.Caller)
				}
			},
		},
		{
			name:       "tagged node is identified by its tags, not its creator",
			addr:       tagged,
			wantStatus: http.StatusOK,
			check: func(t *testing.T, b whoamiBody, _ string) {
				if b.Caller == nil {
					t.Fatal("no caller in body")
				}
				if b.Caller.Login != "" {
					t.Errorf("login = %q, want empty for a tagged node", b.Caller.Login)
				}
				if want := []string{"tag:ci"}; !slices.Equal(b.Caller.Tags, want) {
					t.Errorf("tags = %v, want %v", b.Caller.Tags, want)
				}
			},
		},
	}

	mux := newMux(&gateway{who: who})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/whoami", nil)
			req.RemoteAddr = tc.addr
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tc.wantStatus, rec.Body)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			raw := rec.Body.String()
			var b whoamiBody
			if err := json.Unmarshal([]byte(raw), &b); err != nil {
				t.Fatalf("body is not JSON: %v: %s", err, raw)
			}
			if b.Reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", b.Reason, tc.wantReason)
			}
			if tc.wantStatus != http.StatusOK && b.Status != tc.wantStatus {
				t.Errorf("body status = %d, want %d", b.Status, tc.wantStatus)
			}
			tc.check(t, b, raw)
		})
	}
}
