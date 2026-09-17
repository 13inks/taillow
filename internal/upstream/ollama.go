package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// Ollama implements the Provider interface for local Ollama instances.
type Ollama struct {
	BaseURL string       // BaseURL is the scheme, host and port of a local Ollama instance.
	Client  *http.Client // Client is the HTTP client to use; nil defaults to http.DefaultClient.
}

// Name returns the provider identifier for Ollama.
func (o *Ollama) Name() string {
	return "ollama"
}

// Complete sends a chat completion request to the Ollama API and returns the result.
func (o *Ollama) Complete(ctx context.Context, req Request) (Response, error) {
	url := strings.TrimRight(o.BaseURL, "/") + "/api/chat"

	requestPayload := struct {
		Model    string          `json:"model"`
		Messages []ollamaMessage `json:"messages"`
		Stream   bool            `json:"stream"`
		Think    bool            `json:"think"`
		Options  ollamaOptions   `json:"options"`
	}{
		Model: req.Model,
		Messages: []ollamaMessage{
			{Role: "user", Content: req.Prompt},
		},
		Stream: false,
		// Reasoning models spend num_predict on a hidden thinking channel
		// first. taillow returns only the answer, so that thinking is pure
		// cost to the caller: measured against a local Ollama, a 64-token
		// request came back as 64 thinking tokens and no text at all.
		// Ollama accepts think=false for models that cannot think.
		Think: false,
		Options: ollamaOptions{
			NumPredict: req.MaxTokens,
		},
	}

	bodyBytes, err := json.Marshal(requestPayload)
	if err != nil {
		return Response{}, &Error{Provider: "ollama", Status: 0, Message: err.Error()}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return Response{}, &Error{Provider: "ollama", Status: 0, Message: err.Error()}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := o.Client
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return Response{}, &Error{Provider: "ollama", Status: 0, Message: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Response{}, &Error{Provider: "ollama", Status: resp.StatusCode, Message: readErrorMessage(resp.Body)}
	}

	return parseSuccessResponse(resp.Body, resp.StatusCode)
}

// readErrorMessage returns what a non-2xx body says went wrong: Ollama's own
// "error" field when the body is its usual JSON, otherwise the raw text. The
// read is capped so a misbehaving upstream cannot make a refusal unbounded.
func readErrorMessage(body io.Reader) string {
	bodyBytes, _ := io.ReadAll(io.LimitReader(body, 4096))

	var apiErr struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(bodyBytes, &apiErr); err == nil && apiErr.Error != "" {
		return apiErr.Error
	}
	return strings.TrimSpace(string(bodyBytes))
}

// parseSuccessResponse decodes a 2xx body. status rides along because a body
// that will not decode is still an answer from the upstream: reporting it with
// status 0 would tell the caller Ollama was unreachable, which is a different
// fault with a different fix.
func parseSuccessResponse(body io.Reader, status int) (Response, error) {
	limitReader := io.LimitReader(body, 8<<20)
	var apiResp struct {
		Message         ollamaMessage `json:"message"`
		PromptEvalCount int64         `json:"prompt_eval_count"`
		EvalCount       int64         `json:"eval_count"`
		DoneReason      string        `json:"done_reason"`
	}

	if err := json.NewDecoder(limitReader).Decode(&apiResp); err != nil {
		return Response{}, &Error{Provider: "ollama", Status: status, Message: "undecodable response: " + err.Error()}
	}

	return Response{
		Text:         apiResp.Message.Content,
		InputTokens:  apiResp.PromptEvalCount,
		OutputTokens: apiResp.EvalCount,
		StopReason:   apiResp.DoneReason,
	}, nil
}

// ollamaMessage is one chat turn in Ollama's wire format. Unexported: it is a
// detail of this provider, not part of the package's API.
type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ollamaOptions carries num_predict, which is how Ollama spells max_tokens.
type ollamaOptions struct {
	NumPredict int64 `json:"num_predict"`
}
