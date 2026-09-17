package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/13inks/taillow/internal/audit"
	"github.com/13inks/taillow/internal/budget"
	"github.com/13inks/taillow/internal/upstream"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
)

// stubProvider answers with a fixed response or error and records what it saw.
type stubProvider struct {
	resp  upstream.Response
	err   error
	calls int
	got   upstream.Request
}

func (s *stubProvider) Name() string { return "stub" }
func (s *stubProvider) Complete(_ context.Context, req upstream.Request) (upstream.Response, error) {
	s.calls++
	s.got = req
	return s.resp, s.err
}

// failingWriter makes every audit write fail.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

const (
	addrUnknown = "192.0.2.1:41000"
	addrBroken  = "192.0.2.2:41000"
	addrNoGrant = "192.0.2.3:41000"
	addrAlice   = "192.0.2.4:41000"
	addrCI      = "192.0.2.5:41000"

	aliceGrant = `{"models":["claude-opus-5","qwen3:8b"],"dailyTokens":1000}`
)

var testNoon = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

type harness struct {
	gw       *gateway
	provider *stubProvider
	auditBuf *bytes.Buffer
}

func newHarness(resp upstream.Response, err error) *harness {
	h := &harness{provider: &stubProvider{resp: resp, err: err}, auditBuf: &bytes.Buffer{}}
	h.gw = &gateway{
		who: fakeWhoIs{
			errs: map[string]error{addrBroken: errors.New("local api down secret-detail")},
			resps: map[string]*apitype.WhoIsResponse{
				addrNoGrant: peer(nil),
				addrAlice:   peer([]tailcfg.RawMessage{aliceGrant}),
				addrCI:      peer([]tailcfg.RawMessage{aliceGrant}, "tag:ci", "tag:build"),
			},
		},
		ledger: budget.New(func() time.Time { return testNoon }),
		audit:  audit.New(h.auditBuf),
		router: upstream.Router{Anthropic: h.provider, Ollama: h.provider},
	}
	return h
}

// completeBody decodes both shapes /v1/complete answers with.
type completeBody struct {
	Status   int                     `json:"status"`
	Reason   string                  `json:"reason"`
	Caller   *struct{ Login string } `json:"caller"`
	Budget   *budgetInfo             `json:"budget"`
	Upstream *upstreamInfo           `json:"upstream"`

	Text            string `json:"text"`
	StopReason      string `json:"stopReason"`
	InputTokens     int64  `json:"inputTokens"`
	OutputTokens    int64  `json:"outputTokens"`
	BudgetRemaining int64  `json:"budgetRemaining"`
}

func (h *harness) post(t *testing.T, addr, body string) (*httptest.ResponseRecorder, completeBody) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/complete", strings.NewReader(body))
	req.RemoteAddr = addr
	rec := httptest.NewRecorder()
	newMux(h.gw).ServeHTTP(rec, req)

	var got completeBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, rec.Body)
	}
	return rec, got
}

// lastAudit returns the newest audit line, and fails unless exactly want lines exist.
func (h *harness) lastAudit(t *testing.T, want int) audit.Entry {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(h.auditBuf.String(), "\n"), "\n")
	if len(lines) != want {
		t.Fatalf("audit has %d lines, want %d:\n%s", len(lines), want, h.auditBuf)
	}
	var e audit.Entry
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &e); err != nil {
		t.Fatal(err)
	}
	return e
}

const okRequest = `{"model":"claude-opus-5","prompt":"hello","max_tokens":100}`

func TestCompleteRefusals(t *testing.T) {
	tests := []struct {
		name         string
		addr         string
		body         string
		wantStatus   int
		wantReason   string // substring
		wantDecision string
		wantCaller   bool
	}{
		{name: "unknown caller", addr: addrUnknown, body: okRequest,
			wantStatus: 403, wantReason: "caller identity not resolvable", wantDecision: "refused_identity"},
		{name: "identity lookup down", addr: addrBroken, body: okRequest,
			wantStatus: 503, wantReason: "refusing rather than guessing", wantDecision: "refused_identity"},
		{name: "no grant for this app", addr: addrNoGrant, body: okRequest,
			wantStatus: 403, wantReason: "no grant for this app", wantDecision: "refused_grant", wantCaller: true},

		{name: "body is not JSON", addr: addrAlice, body: `hello`,
			wantStatus: 400, wantReason: "request must be a JSON object", wantDecision: "refused_request", wantCaller: true},
		{name: "unknown field is named", addr: addrAlice, body: `{"model":"claude-opus-5","prompt":"hi","max_tokens":1,"temperature":2}`,
			wantStatus: 400, wantReason: `request field "temperature": unknown field`, wantDecision: "refused_request", wantCaller: true},
		{name: "repeated field is named", addr: addrAlice, body: `{"model":"claude-opus-5","prompt":"hi","max_tokens":1,"max_tokens":16000}`,
			wantStatus: 400, wantReason: `request field "max_tokens": duplicate field`, wantDecision: "refused_request", wantCaller: true},
		{name: "wrong key case is not forgiven", addr: addrAlice, body: `{"Model":"claude-opus-5","prompt":"hi","max_tokens":1}`,
			wantStatus: 400, wantReason: `request field "Model": unknown field`, wantDecision: "refused_request", wantCaller: true},
		{name: "missing prompt", addr: addrAlice, body: `{"model":"claude-opus-5","max_tokens":1}`,
			wantStatus: 400, wantReason: `request field "prompt": must be a non-empty string`, wantDecision: "refused_request", wantCaller: true},
		{name: "empty model", addr: addrAlice, body: `{"model":"","prompt":"hi","max_tokens":1}`,
			wantStatus: 400, wantReason: `request field "model": must be a non-empty string`, wantDecision: "refused_request", wantCaller: true},
		{name: "max_tokens zero", addr: addrAlice, body: `{"model":"claude-opus-5","prompt":"hi","max_tokens":0}`,
			wantStatus: 400, wantReason: `request field "max_tokens": must be an integer from 1 to 16000`, wantDecision: "refused_request", wantCaller: true},
		{name: "max_tokens over the cap", addr: addrAlice, body: `{"model":"claude-opus-5","prompt":"hi","max_tokens":16001}`,
			wantStatus: 400, wantReason: `request field "max_tokens"`, wantDecision: "refused_request", wantCaller: true},
		{name: "max_tokens as a string", addr: addrAlice, body: `{"model":"claude-opus-5","prompt":"hi","max_tokens":"12"}`,
			wantStatus: 400, wantReason: `request field "max_tokens"`, wantDecision: "refused_request", wantCaller: true},
		{name: "body over 1 MiB", addr: addrAlice, body: `{"model":"claude-opus-5","max_tokens":1,"prompt":"` + strings.Repeat("a", 1<<20) + `"}`,
			wantStatus: 400, wantReason: "larger than 1 MiB", wantDecision: "refused_request", wantCaller: true},

		{name: "model outside the grant", addr: addrAlice, body: `{"model":"claude-fable-5-1","prompt":"hi","max_tokens":1}`,
			wantStatus: 403, wantReason: `model "claude-fable-5-1" is not in this caller's grant; granted: claude-opus-5, qwen3:8b`, wantDecision: "refused_model", wantCaller: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(upstream.Response{Text: "must never be returned"}, nil)
			rec, got := h.post(t, tt.addr, tt.body)

			if rec.Code != tt.wantStatus || got.Status != tt.wantStatus {
				t.Errorf("status = %d (body says %d), want %d", rec.Code, got.Status, tt.wantStatus)
			}
			if !strings.Contains(got.Reason, tt.wantReason) {
				t.Errorf("reason = %q, want it to contain %q", got.Reason, tt.wantReason)
			}
			if (got.Caller != nil) != tt.wantCaller {
				t.Errorf("caller echoed = %v, want %v", got.Caller != nil, tt.wantCaller)
			}
			if strings.Contains(rec.Body.String(), "secret-detail") || strings.Contains(rec.Body.String(), "192.0.2.") {
				t.Errorf("response leaks lookup detail or an address: %s", rec.Body)
			}
			if h.provider.calls != 0 {
				t.Errorf("a refused request reached the upstream %d time(s)", h.provider.calls)
			}
			if e := h.lastAudit(t, 1); e.Decision != tt.wantDecision || e.Status != tt.wantStatus {
				t.Errorf("audit = %+v, want decision %q status %d", e, tt.wantDecision, tt.wantStatus)
			}
		})
	}
}

func TestCompleteUnknownCallerAddressIsAuditedNotReturned(t *testing.T) {
	h := newHarness(upstream.Response{}, nil)
	rec, _ := h.post(t, addrUnknown, okRequest)
	if strings.Contains(rec.Body.String(), addrUnknown) {
		t.Errorf("response carries the caller's address: %s", rec.Body)
	}
	if e := h.lastAudit(t, 1); e.Caller != "unknown" || !strings.Contains(e.Reason, addrUnknown) {
		t.Errorf("audit = %+v, want caller unknown and the address in the reason", e)
	}
}

func TestCompleteAllowed(t *testing.T) {
	h := newHarness(upstream.Response{Text: "Hello there", InputTokens: 12, OutputTokens: 30, StopReason: "end_turn"}, nil)
	rec, got := h.post(t, addrAlice, okRequest)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if got.Text != "Hello there" || got.StopReason != "end_turn" || got.InputTokens != 12 || got.OutputTokens != 30 {
		t.Errorf("body = %+v", got)
	}
	if want := (upstream.Request{Model: "claude-opus-5", Prompt: "hello", MaxTokens: 100}); h.provider.got != want {
		t.Errorf("upstream saw %+v, want %+v", h.provider.got, want)
	}
	// 100 + len("hello") + 64 = 169 were reserved; the ledger must end at the
	// 42 that were really spent, and the response must agree with the ledger.
	if used := h.gw.ledger.Used("alice@example.com"); used != 42 {
		t.Errorf("ledger = %d after settling, want 42", used)
	}
	if got.BudgetRemaining != 1000-42 {
		t.Errorf("budgetRemaining = %d, want %d", got.BudgetRemaining, 1000-42)
	}
	e := h.lastAudit(t, 1)
	want := audit.Entry{Time: e.Time, Caller: "alice@example.com", Node: "alice-laptop", Model: "claude-opus-5",
		InputTokens: 12, OutputTokens: 30, Decision: "allowed", Status: 200}
	if e != want {
		t.Errorf("audit = %+v\n want  %+v", e, want)
	}
}

func TestCompleteTaggedNodeIsBudgetedAsItsTags(t *testing.T) {
	h := newHarness(upstream.Response{InputTokens: 1, OutputTokens: 1, StopReason: "end_turn"}, nil)
	if rec, _ := h.post(t, addrCI, okRequest); rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	// Sorted, so the identity does not depend on the order the tailnet lists tags in.
	const id = "tags:tag:build,tag:ci"
	if used := h.gw.ledger.Used(id); used != 2 {
		t.Errorf("ledger[%s] = %d, want 2", id, used)
	}
	if used := h.gw.ledger.Used("alice@example.com"); used != 0 {
		t.Errorf("the person who registered the node was charged %d tokens", used)
	}
	if e := h.lastAudit(t, 1); e.Caller != id {
		t.Errorf("audit caller = %q, want %q", e.Caller, id)
	}
}

func TestCompleteBudgetExhausted(t *testing.T) {
	h := newHarness(upstream.Response{}, nil)
	// 900 + len("hello") + 64 = 969 fits in 1000 once, not twice.
	big := `{"model":"claude-opus-5","prompt":"hello","max_tokens":900}`
	h.provider.resp = upstream.Response{InputTokens: 400, OutputTokens: 500, StopReason: "end_turn"}
	if rec, _ := h.post(t, addrAlice, big); rec.Code != http.StatusOK {
		t.Fatalf("first request: status = %d: %s", rec.Code, rec.Body)
	}

	rec, got := h.post(t, addrAlice, big)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(got.Reason, "alice@example.com") || !strings.Contains(got.Reason, "969") {
		t.Errorf("reason = %q, want the identity and the reservation", got.Reason)
	}
	midnight := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	if got.Budget == nil || got.Budget.Identity != "alice@example.com" || got.Budget.DailyTokens != 1000 ||
		got.Budget.Remaining != 100 || !got.Budget.ResetAt.Equal(midnight) {
		t.Errorf("budget = %+v, want identity, limit 1000, remaining 100, reset at next UTC midnight", got.Budget)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After header on a 429")
	}
	if h.provider.calls != 1 {
		t.Errorf("upstream calls = %d, want 1: the refused request must not reach it", h.provider.calls)
	}
	if e := h.lastAudit(t, 2); e.Decision != "refused_budget" || e.Status != 429 {
		t.Errorf("audit = %+v", e)
	}
}

func TestCompleteUpstreamFailureRefundsAndKeepsTheStatus(t *testing.T) {
	h := newHarness(upstream.Response{}, &upstream.Error{Provider: "anthropic", Status: 529, Message: "overloaded"})
	rec, got := h.post(t, addrAlice, okRequest)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", rec.Code, rec.Body)
	}
	if got.Upstream == nil || got.Upstream.Provider != "anthropic" || got.Upstream.Status != 529 {
		t.Errorf("upstream = %+v, want the provider and its own status 529", got.Upstream)
	}
	if !strings.Contains(got.Reason, "529") {
		t.Errorf("reason = %q, want the upstream status in words too", got.Reason)
	}
	if used := h.gw.ledger.Used("alice@example.com"); used != 0 {
		t.Errorf("ledger = %d after a failed call, want the whole reservation back", used)
	}
	if e := h.lastAudit(t, 1); e.Decision != "upstream_error" || e.Status != 502 {
		t.Errorf("audit = %+v", e)
	}
}

func TestCompleteModelRefusalIsNotASilentSuccess(t *testing.T) {
	h := newHarness(upstream.Response{InputTokens: 12, OutputTokens: 3, StopReason: "refusal"}, nil)
	rec, got := h.post(t, addrAlice, okRequest)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(got.Reason, "declined") {
		t.Errorf("reason = %q", got.Reason)
	}
	// The provider billed for the declined request, so the ledger does too.
	if used := h.gw.ledger.Used("alice@example.com"); used != 15 {
		t.Errorf("ledger = %d, want 15", used)
	}
	if e := h.lastAudit(t, 1); e.Decision != "model_refused" || e.InputTokens != 12 || e.OutputTokens != 3 {
		t.Errorf("audit = %+v", e)
	}
}

func TestCompleteNoProviderForModel(t *testing.T) {
	h := newHarness(upstream.Response{}, nil)
	h.gw.router = upstream.Router{Anthropic: h.provider} // no Ollama
	rec, got := h.post(t, addrAlice, `{"model":"qwen3:8b","prompt":"hi","max_tokens":1}`)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(got.Reason, "qwen3:8b") {
		t.Errorf("status = %d reason = %q, want 503 naming the model", rec.Code, got.Reason)
	}
	if used := h.gw.ledger.Used("alice@example.com"); used != 0 {
		t.Errorf("ledger = %d, want nothing reserved for a model nobody serves", used)
	}
}

func TestCompleteUnauditedAnswerIsNotReturned(t *testing.T) {
	h := newHarness(upstream.Response{Text: "the answer", InputTokens: 1, OutputTokens: 1, StopReason: "end_turn"}, nil)
	h.gw.audit = audit.New(failingWriter{})
	rec, got := h.post(t, addrAlice, okRequest)

	if rec.Code != http.StatusInternalServerError || !strings.Contains(got.Reason, "audit log not writable") {
		t.Errorf("status = %d reason = %q, want 500 about the audit log", rec.Code, got.Reason)
	}
	if strings.Contains(rec.Body.String(), "the answer") {
		t.Errorf("the unaudited answer was returned anyway: %s", rec.Body)
	}
}

func TestCompleteIsPostOnly(t *testing.T) {
	h := newHarness(upstream.Response{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/complete", nil)
	req.RemoteAddr = addrAlice
	rec := httptest.NewRecorder()
	newMux(h.gw).ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" {
		t.Errorf("GET = %d, Allow %q; want 405 and POST", rec.Code, rec.Header().Get("Allow"))
	}
}
