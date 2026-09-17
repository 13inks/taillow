package upstream

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type stubProvider string

func (s stubProvider) Name() string { return string(s) }
func (s stubProvider) Complete(context.Context, Request) (Response, error) {
	return Response{}, nil
}

func TestRouterFor(t *testing.T) {
	full := Router{Anthropic: stubProvider("anthropic"), Ollama: stubProvider("ollama")}
	tests := []struct {
		name    string
		router  Router
		model   string
		want    string
		wantErr bool
	}{
		{name: "claude- prefix goes to Anthropic", router: full, model: "claude-opus-5", want: "anthropic"},
		{name: "anything else goes to Ollama", router: full, model: "qwen3:8b", want: "ollama"},
		{name: "the prefix is a prefix, not a substring", router: full, model: "my-claude-clone", want: "ollama"},
		{name: "no Anthropic configured", router: Router{Ollama: stubProvider("ollama")}, model: "claude-opus-5", wantErr: true},
		{name: "no Ollama configured", router: Router{Anthropic: stubProvider("anthropic")}, model: "qwen3:8b", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := tt.router.For(tt.model)
			if tt.wantErr {
				if !errors.Is(err, ErrNoProvider) || !strings.Contains(err.Error(), tt.model) || p != nil {
					t.Fatalf("For(%q) = %v, %v; want nil and ErrNoProvider naming the model", tt.model, p, err)
				}
				return
			}
			if err != nil || p.Name() != tt.want {
				t.Fatalf("For(%q) = %v, %v; want %s", tt.model, p, err, tt.want)
			}
		})
	}
}

func TestErrorText(t *testing.T) {
	answered := &Error{Provider: "anthropic", Status: 429, Message: "slow down"}
	if got := answered.Error(); got != "anthropic upstream returned 429: slow down" {
		t.Errorf("with a status: %q", got)
	}
	// Status 0 is "no HTTP answer at all", which is a different fault from
	// any status the upstream could send.
	silent := &Error{Provider: "ollama", Message: "connection refused"}
	if got := silent.Error(); got != "ollama upstream unreachable: connection refused" {
		t.Errorf("without a status: %q", got)
	}
}
