package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOllamaComplete(t *testing.T) {
	var gotBody map[string]any
	var gotPath, gotMethod, gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod, gotType = r.URL.Path, r.Method, r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		io.WriteString(w, `{"message":{"role":"assistant","content":"Hello"},"prompt_eval_count":11,"eval_count":4,"done_reason":"stop"}`)
	}))
	defer srv.Close()

	// The trailing slash must not produce "//api/chat".
	o := &Ollama{BaseURL: srv.URL + "/"}
	got, err := o.Complete(context.Background(), Request{Model: "qwen3:8b", Prompt: "hi", MaxTokens: 64})
	if err != nil {
		t.Fatal(err)
	}
	if want := (Response{Text: "Hello", InputTokens: 11, OutputTokens: 4, StopReason: "stop"}); got != want {
		t.Errorf("Response = %+v, want %+v", got, want)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/chat" || gotType != "application/json" {
		t.Errorf("request = %s %s (%s), want POST /api/chat (application/json)", gotMethod, gotPath, gotType)
	}
	msgs, _ := gotBody["messages"].([]any)
	opts, _ := gotBody["options"].(map[string]any)
	if gotBody["model"] != "qwen3:8b" || gotBody["stream"] != false || len(msgs) != 1 || opts["num_predict"] != float64(64) {
		t.Fatalf("body = %v", gotBody)
	}
	if m := msgs[0].(map[string]any); m["role"] != "user" || m["content"] != "hi" {
		t.Errorf("message = %v, want the prompt as one user turn", m)
	}
	if o.Name() != "ollama" {
		t.Errorf("Name = %q", o.Name())
	}
}

func TestOllamaFailuresKeepTheUpstreamStatus(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		wantStatus  int
		wantMessage string
	}{
		{name: "Ollama's own error field", status: 404, body: `{"error":"model 'nope' not found"}`, wantStatus: 404, wantMessage: "model 'nope' not found"},
		{name: "a plain text error", status: 502, body: "  bad gateway\n", wantStatus: 502, wantMessage: "bad gateway"},
		// A 200 that will not decode is still an answer. Reporting it as
		// status 0 would say "unreachable", which is a different fault.
		{name: "a success that will not decode", status: 200, body: `not json`, wantStatus: 200, wantMessage: "undecodable response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				io.WriteString(w, tt.body)
			}))
			defer srv.Close()

			_, err := (&Ollama{BaseURL: srv.URL}).Complete(context.Background(), Request{Model: "m", Prompt: "p", MaxTokens: 1})
			var ue *Error
			if !errors.As(err, &ue) {
				t.Fatalf("err = %v, want *Error", err)
			}
			if ue.Provider != "ollama" || ue.Status != tt.wantStatus || !strings.HasPrefix(ue.Message, tt.wantMessage) {
				t.Errorf("got %+v, want status %d and message starting %q", ue, tt.wantStatus, tt.wantMessage)
			}
		})
	}
}

func TestOllamaUnreachableIsStatusZero(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close() // nothing is listening now

	_, err := (&Ollama{BaseURL: srv.URL}).Complete(context.Background(), Request{Model: "m", Prompt: "p", MaxTokens: 1})
	var ue *Error
	if !errors.As(err, &ue) || ue.Status != 0 || ue.Provider != "ollama" {
		t.Fatalf("err = %v, want *Error with status 0", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = (&Ollama{BaseURL: "http://127.0.0.1:1"}).Complete(ctx, Request{Model: "m", Prompt: "p", MaxTokens: 1})
	if !errors.As(err, &ue) || ue.Status != 0 {
		t.Fatalf("cancelled: err = %v, want *Error with status 0", err)
	}
}
