package upstream

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Request encapsulates the parameters for a single LLM completion call.
type Request struct {
	Model     string
	Prompt    string
	MaxTokens int64
}

// Response holds the result returned by an upstream provider after processing a Request.
type Response struct {
	Text         string
	InputTokens  int64
	OutputTokens int64
	StopReason   string
}

// Provider defines the contract for any LLM service integration.
type Provider interface {
	Name() string
	Complete(ctx context.Context, req Request) (Response, error)
}

// Error represents a failure originating from an upstream provider, capturing its identity and status.
type Error struct {
	Provider string
	Status   int // the upstream's HTTP status; 0 means no HTTP answer at all
	Message  string
}

// Error formats the error message based on whether an HTTP status was received.
func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("%s upstream returned %d: %s", e.Provider, e.Status, e.Message)
	}
	return fmt.Sprintf("%s upstream unreachable: %s", e.Provider, e.Message)
}

// ErrNoProvider indicates that no configured provider can handle the requested model.
var ErrNoProvider = errors.New("no upstream provider configured for this model")

// Router dispatches requests to the appropriate Provider based on the target model name.
type Router struct {
	Anthropic Provider
	Ollama    Provider
}

// For selects the correct Provider for the given model string.
func (r Router) For(model string) (Provider, error) {
	var p Provider
	if strings.HasPrefix(model, "claude-") {
		p = r.Anthropic
	} else {
		p = r.Ollama
	}

	if p == nil {
		return nil, fmt.Errorf("%w: %q", ErrNoProvider, model)
	}
	return p, nil
}
