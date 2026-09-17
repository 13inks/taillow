package upstream

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestOllamaLive talks to a real Ollama, because a fake server only proves the
// client matches what its author believed the API does. It is skipped unless
// TAILLOW_LIVE_OLLAMA names a model that server has:
//
//	TAILLOW_LIVE_OLLAMA=qwen3:8b go test ./internal/upstream -run Live -v
//
// OLLAMA_URL overrides the default local address.
func TestOllamaLive(t *testing.T) {
	model := os.Getenv("TAILLOW_LIVE_OLLAMA")
	if model == "" {
		t.Skip("set TAILLOW_LIVE_OLLAMA to a model name to run against a real Ollama")
	}
	base := os.Getenv("OLLAMA_URL")
	if base == "" {
		base = "http://127.0.0.1:11434"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// 64 tokens is deliberately small: it is the budget at which a reasoning
	// model left to think returns no text at all.
	got, err := (&Ollama{BaseURL: base}).Complete(ctx, Request{Model: model, Prompt: "Say hello in five words.", MaxTokens: 64})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(got.Text) == "" {
		t.Errorf("no text came back (stop reason %q, %d output tokens)", got.StopReason, got.OutputTokens)
	}
	if got.InputTokens <= 0 || got.OutputTokens <= 0 || got.OutputTokens > 64 {
		t.Errorf("token counts = %d in, %d out; want both positive and out within the 64 asked for", got.InputTokens, got.OutputTokens)
	}
	t.Logf("%q (%d in, %d out, stop %q)", got.Text, got.InputTokens, got.OutputTokens, got.StopReason)
}
