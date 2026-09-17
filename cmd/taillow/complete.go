package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/13inks/taillow/internal/audit"
	"github.com/13inks/taillow/internal/budget"
	"github.com/13inks/taillow/internal/ident"
	"github.com/13inks/taillow/internal/strictjson"
	"github.com/13inks/taillow/internal/upstream"
)

// maxTokensCap bounds one request. taillow does not stream, and a non-streaming
// call asking for much more than this can outlive the upstream's HTTP timeout.
const maxTokensCap int64 = 16000

// reserveMargin covers the few tokens a provider adds around a prompt (role
// markers and the like), so the reservation stays an upper bound.
const reserveMargin int64 = 64

// completeRequest is the body of POST /v1/complete. The tags are for readers:
// decoding goes through strictjson, never straight into this struct.
type completeRequest struct {
	Model     string `json:"model"`
	Prompt    string `json:"prompt"`
	MaxTokens int64  `json:"max_tokens"`
}

// completeResponse answers an allowed request. It always carries the stop
// reason and the token counts, so a truncated answer or a surprising bill is
// visible in the response that caused it.
type completeResponse struct {
	Model           string    `json:"model"`
	Text            string    `json:"text"`
	StopReason      string    `json:"stopReason"`
	InputTokens     int64     `json:"inputTokens"`
	OutputTokens    int64     `json:"outputTokens"`
	BudgetRemaining int64     `json:"budgetRemaining"`
	BudgetResetAt   time.Time `json:"budgetResetAt"`
}

// decodeCompleteRequest refuses anything it does not fully understand, and
// names the field. A missing key fails the same check as a wrong type: both
// leave nothing to unmarshal.
func decodeCompleteRequest(body []byte) (completeRequest, error) {
	raw, err := strictjson.Object(body, "model", "prompt", "max_tokens")
	if err != nil {
		return completeRequest{}, err
	}

	var req completeRequest
	if err := json.Unmarshal(raw["model"], &req.Model); err != nil || req.Model == "" {
		return completeRequest{}, &strictjson.FieldError{Field: "model", Reason: "must be a non-empty string"}
	}
	if err := json.Unmarshal(raw["prompt"], &req.Prompt); err != nil || req.Prompt == "" {
		return completeRequest{}, &strictjson.FieldError{Field: "prompt", Reason: "must be a non-empty string"}
	}
	if err := json.Unmarshal(raw["max_tokens"], &req.MaxTokens); err != nil || req.MaxTokens < 1 || req.MaxTokens > maxTokensCap {
		return completeRequest{}, &strictjson.FieldError{Field: "max_tokens", Reason: "must be an integer from 1 to 16000"}
	}
	return req, nil
}

// budgetIdentity is the name a caller's tokens are counted under. A person is
// their login, so the budget follows them from laptop to phone. A tagged node
// is its tags, for the same reason ident reports tags and not the person who
// registered the machine.
func budgetIdentity(c ident.Caller) string {
	if c.Login != "" {
		return c.Login
	}
	tags := slices.Clone(c.Tags)
	slices.Sort(tags)
	return "tags:" + strings.Join(tags, ",")
}

// callerLabel is budgetIdentity for the audit log, where a caller the tailnet
// could not name still needs a value in the column.
func callerLabel(c ident.Caller) string {
	if c.Login == "" && len(c.Tags) == 0 {
		return "unknown"
	}
	return budgetIdentity(c)
}

// finish is the only way out of completeHandler. The audit line is written
// first: if it cannot be written the caller gets a 500 and not the answer,
// because an answer nobody can account for is what this gateway exists to
// prevent.
func (g *gateway) finish(w http.ResponseWriter, e audit.Entry, status int, body any) {
	e.Status = status
	if err := g.audit.Write(e); err != nil {
		log.Printf("audit write failed: %v", err)
		writeJSON(w, http.StatusInternalServerError, refusal{
			Status: http.StatusInternalServerError,
			Reason: "audit log not writable; refusing to answer unaudited",
		})
		return
	}
	writeJSON(w, status, body)
}

// completeHandler forwards one prompt to a model, or refuses and says why.
//
// The order of the checks is the design: who is calling, what they are
// granted, whether the request makes sense, whether the model is theirs to
// use, whether they can afford it, and only then the upstream. Nothing about
// the request is trusted before the tailnet has said who sent it.
func (g *gateway) completeHandler(w http.ResponseWriter, r *http.Request) {
	caller, gr, ref := authorize(r.Context(), g.who, r.RemoteAddr)
	if ref != nil {
		entry := audit.Entry{Caller: callerLabel(caller), Node: caller.Node, Decision: "refused_grant", Reason: ref.Reason}
		if ref.Caller == nil {
			// The address goes to the audit log only. It is the one lead on
			// a caller the tailnet could not name.
			entry.Decision = "refused_identity"
			entry.Reason += " (from " + r.RemoteAddr + ")"
		}
		g.finish(w, entry, ref.Status, ref)
		return
	}

	entry := audit.Entry{Caller: callerLabel(caller), Node: caller.Node}
	refuse := func(status int, decision, reason string, extra func(*refusal)) {
		ref := refusal{Status: status, Reason: reason, Caller: &caller}
		if extra != nil {
			extra(&ref)
		}
		entry.Decision, entry.Reason = decision, reason
		g.finish(w, entry, status, ref)
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		refuse(http.StatusBadRequest, "refused_request", "request body unreadable or larger than 1 MiB", nil)
		return
	}
	req, err := decodeCompleteRequest(body)
	if err != nil {
		refuse(http.StatusBadRequest, "refused_request", "request "+err.Error(), nil)
		return
	}
	entry.Model = req.Model

	if !slices.Contains(gr.Models, req.Model) {
		refuse(http.StatusForbidden, "refused_model",
			fmt.Sprintf("model %q is not in this caller's grant; granted: %s", req.Model, strings.Join(gr.Models, ", ")), nil)
		return
	}

	provider, err := g.router.For(req.Model)
	if err != nil {
		refuse(http.StatusServiceUnavailable, "no_provider", err.Error(), nil)
		return
	}

	// Reserve an upper bound before the call and settle to the real cost
	// after it. A token is at least one byte, so the prompt's length in bytes
	// can only overestimate its tokens. Both terms are already bounded (the
	// max_tokens cap and the 1 MiB body limit), so the sum cannot overflow.
	id := budgetIdentity(caller)
	reserve := req.MaxTokens + int64(len(req.Prompt)) + reserveMargin
	_, resetAt, err := g.ledger.Spend(id, reserve, gr.DailyTokens)
	if errors.Is(err, budget.ErrExhausted) {
		retryAfter := max(1, int64(time.Until(resetAt).Seconds()))
		w.Header().Set("Retry-After", strconv.FormatInt(retryAfter, 10))
		refuse(http.StatusTooManyRequests, "refused_budget",
			fmt.Sprintf("daily token budget exhausted for %s: this request reserves %d tokens", id, reserve),
			func(ref *refusal) {
				ref.Budget = &budgetInfo{
					Identity:    id,
					DailyTokens: gr.DailyTokens,
					Remaining:   max(0, gr.DailyTokens-g.ledger.Used(id)),
					ResetAt:     resetAt,
				}
			})
		return
	}
	if err != nil {
		refuse(http.StatusInternalServerError, "refused_budget", "budget check failed; refusing rather than guessing", nil)
		return
	}

	resp, err := provider.Complete(r.Context(), upstream.Request{Model: req.Model, Prompt: req.Prompt, MaxTokens: req.MaxTokens})
	if err != nil {
		// Nothing was billed, so the whole reservation comes back.
		g.ledger.Settle(id, resetAt, reserve, 0)
		refuse(http.StatusBadGateway, "upstream_error", err.Error(), func(ref *refusal) {
			var ue *upstream.Error
			if errors.As(err, &ue) {
				ref.Upstream = &upstreamInfo{Provider: ue.Provider, Status: ue.Status}
			}
		})
		return
	}

	// Settle before looking at the answer: a model that declines has still
	// been paid for.
	g.ledger.Settle(id, resetAt, reserve, resp.InputTokens+resp.OutputTokens)
	entry.InputTokens, entry.OutputTokens = resp.InputTokens, resp.OutputTokens

	// A declined request comes back from the provider as a success with no
	// text. Passing that on as 200 would be a silent failure, so it becomes a
	// refusal of its own.
	if resp.StopReason == "refusal" {
		refuse(http.StatusUnprocessableEntity, "model_refused", "the model declined this request (stop reason: refusal)", nil)
		return
	}

	// The same goes for an answer with no text in it. A reasoning model can
	// spend all of max_tokens thinking and return nothing, and a 200 with an
	// empty text field looks like success to anything checking the status.
	if strings.TrimSpace(resp.Text) == "" {
		refuse(http.StatusUnprocessableEntity, "empty_answer",
			fmt.Sprintf("the model returned no text (stop reason: %s); if it stopped for length, raise max_tokens", resp.StopReason), nil)
		return
	}

	entry.Decision = "allowed"
	g.finish(w, entry, http.StatusOK, completeResponse{
		Model:           req.Model,
		Text:            resp.Text,
		StopReason:      resp.StopReason,
		InputTokens:     resp.InputTokens,
		OutputTokens:    resp.OutputTokens,
		BudgetRemaining: max(0, gr.DailyTokens-g.ledger.Used(id)),
		BudgetResetAt:   resetAt,
	})
}
