package tello

import (
	"context"
	"fmt"
	"net/http"
)

// CallDrafts reviews a call scenario before dialing, for agents that
// generate outbound scenarios: create a draft, revise it, then confirm it to
// place the call. Confirm places a real, billed call.
type CallDrafts struct {
	t *apiTransport
}

// Create asks for a draft from body (the agent request of a call create:
// agentId, to, instructions, ...) and returns it. Every draft status
// (pendingConfirmation, needsClarification, refused, confirmed) is a
// success; confirmed means the agent's stored scenario was dialed at once,
// with the call in callId. WithIdempotencyKey makes the request safe to
// resend.
func (r *CallDrafts) Create(ctx context.Context, body Object, opts ...RequestOption) (Object, error) {
	o := collectRequestOptions(opts)
	return r.t.do(ctx, apiRequest{
		method: http.MethodPost, path: "/call-drafts", body: orEmpty(body),
		idempotencyKey: o.idempotencyKey, long: true, accept: []int{http.StatusCreated},
	})
}

// Revise sends request (what to change) and returns the revised draft,
// which has a new id; draftID can no longer be confirmed.
// WithIdempotencyKey makes the request safe to resend.
func (r *CallDrafts) Revise(ctx context.Context, draftID, request string, opts ...RequestOption) (Object, error) {
	path, err := itemPath("call-drafts", draftID)
	if err != nil {
		return nil, err
	}
	o := collectRequestOptions(opts)
	return r.t.do(ctx, apiRequest{
		method: http.MethodPost, path: path + "/revise", body: map[string]string{"request": request},
		idempotencyKey: o.idempotencyKey, long: true, accept: []int{http.StatusOK},
	})
}

// Confirm places the call of draftID and returns the call object. This
// dials a real, billed call.
//
// The API requires an Idempotency-Key: WithIdempotencyKey sets it, otherwise
// a UUID v4 is generated. Every error carries the key in IdempotencyKey;
// after an *APIConnectionError the call may already be placed, so retry with
// that same key and the server replays the first answer instead of dialing
// again.
func (r *CallDrafts) Confirm(ctx context.Context, draftID string, opts ...RequestOption) (Object, error) {
	path, err := itemPath("call-drafts", draftID)
	if err != nil {
		return nil, err
	}
	key := collectRequestOptions(opts).idempotencyKey
	if key == "" {
		if key, err = newUUIDv4(); err != nil {
			return nil, fmt.Errorf("tello api: generate idempotency key: %w", err)
		}
	}
	return r.t.do(ctx, apiRequest{
		method: http.MethodPost, path: path + "/confirm",
		idempotencyKey: key, long: true, accept: []int{http.StatusCreated},
	})
}
