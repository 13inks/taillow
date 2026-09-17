package upstream

import (
	"context"
	"errors"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Anthropic implements the Provider interface using the official Anthropic Go SDK.
type Anthropic struct {
	client anthropic.Client
}

// NewAnthropic creates a new Anthropic provider with the given request options.
// It disables automatic retries so the caller sees the upstream's real answer.
func NewAnthropic(opts ...option.RequestOption) *Anthropic {
	all := make([]option.RequestOption, len(opts), len(opts)+1)
	copy(all, opts)
	all = append(all, option.WithMaxRetries(0))
	return &Anthropic{
		client: anthropic.NewClient(all...),
	}
}

// Name returns the provider identifier for Anthropic.
func (a *Anthropic) Name() string {
	return "anthropic"
}

// Complete sends a request to the Anthropic API and returns the result.
func (a *Anthropic) Complete(ctx context.Context, req Request) (Response, error) {
	msg, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: req.MaxTokens,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(req.Prompt)),
		},
	})
	if err != nil {
		var apierr *anthropic.Error
		if errors.As(err, &apierr) {
			return Response{}, &Error{
				Provider: "anthropic",
				Status:   apierr.StatusCode,
				Message:  apierr.Error(),
			}
		}
		return Response{}, &Error{
			Provider: "anthropic",
			Status:   0,
			Message:  err.Error(),
		}
	}

	var textParts []string
	for _, block := range msg.Content {
		switch b := block.AsAny().(type) {
		case anthropic.TextBlock:
			textParts = append(textParts, b.Text)
		}
	}

	return Response{
		Text:         strings.Join(textParts, ""),
		InputTokens:  msg.Usage.InputTokens + msg.Usage.CacheCreationInputTokens + msg.Usage.CacheReadInputTokens,
		OutputTokens: msg.Usage.OutputTokens,
		StopReason:   string(msg.StopReason),
	}, nil
}
