package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"
)

// okReply is a Messages API success with a thinking block ahead of two text
// blocks, and input tokens split across the three counters the API reports.
const okReply = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5",
"content":[{"type":"thinking","thinking":"hmm","signature":"s"},{"type":"text","text":"Hello"},{"type":"text","text":" world"}],
"stop_reason":"end_turn","stop_sequence":null,
"usage":{"input_tokens":7,"cache_creation_input_tokens":2,"cache_read_input_tokens":1,"output_tokens":5}}`

func newFakeAnthropic(t *testing.T, h http.HandlerFunc) *Anthropic {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewAnthropic(option.WithBaseURL(srv.URL), option.WithAPIKey("test-key"))
}

func TestAnthropicComplete(t *testing.T) {
	var gotPath, gotKey string
	var gotBody map[string]any
	p := newFakeAnthropic(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.URL.Path, r.Header.Get("x-api-key")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, okReply)
	})

	got, err := p.Complete(context.Background(), Request{Model: "claude-opus-5", Prompt: "hi", MaxTokens: 64})
	if err != nil {
		t.Fatal(err)
	}
	// Thinking is skipped, text blocks are joined, and every kind of input
	// token is counted: they are all billed.
	if want := (Response{Text: "Hello world", InputTokens: 10, OutputTokens: 5, StopReason: "end_turn"}); got != want {
		t.Errorf("Response = %+v, want %+v", got, want)
	}
	if gotPath != "/v1/messages" || gotKey != "test-key" {
		t.Errorf("request went to %s with key %q", gotPath, gotKey)
	}
	if gotBody["model"] != "claude-opus-5" || gotBody["max_tokens"] != float64(64) {
		t.Errorf("body = %v", gotBody)
	}
	if p.Name() != "anthropic" {
		t.Errorf("Name = %q", p.Name())
	}
}

func TestAnthropicKeepsTheUpstreamStatus(t *testing.T) {
	p := newFakeAnthropic(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
	})
	_, err := p.Complete(context.Background(), Request{Model: "claude-opus-5", Prompt: "hi", MaxTokens: 64})
	var ue *Error
	if !errors.As(err, &ue) || ue.Provider != "anthropic" || ue.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v, want *Error from anthropic with status 401", err)
	}
}

// The SDK retries 5xx twice by default. A gateway that retries quietly holds
// the caller's budget reservation for three attempts and then reports one.
func TestAnthropicNeverRetries(t *testing.T) {
	var calls atomic.Int32
	p := newFakeAnthropic(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	_, err := p.Complete(context.Background(), Request{Model: "claude-opus-5", Prompt: "hi", MaxTokens: 64})
	var ue *Error
	if !errors.As(err, &ue) || ue.Status != http.StatusInternalServerError {
		t.Fatalf("err = %v, want *Error with status 500", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("upstream was called %d times, want exactly 1", n)
	}
}

func TestAnthropicUnreachableIsStatusZero(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	p := NewAnthropic(option.WithBaseURL(srv.URL), option.WithAPIKey("test-key"))

	_, err := p.Complete(context.Background(), Request{Model: "claude-opus-5", Prompt: "hi", MaxTokens: 64})
	var ue *Error
	if !errors.As(err, &ue) || ue.Provider != "anthropic" || ue.Status != 0 {
		t.Fatalf("err = %v, want *Error from anthropic with status 0", err)
	}
}
