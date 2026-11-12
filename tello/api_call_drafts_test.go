package tello

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"testing"
	"time"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestCallDraftsCreateReturnsEveryDraftStatus(t *testing.T) {
	for _, status := range []string{"pendingConfirmation", "needsClarification", "refused", "confirmed"} {
		t.Run(status, func(t *testing.T) {
			f := newFakeAPI(t, reply(http.StatusCreated, `{"id":"cd_481","status":"`+status+`","revision":1}`))
			api := newTestAPI(t, f)

			draft, err := api.CallDrafts.Create(context.Background(), Object{"agentId": "12", "to": "+821012345678"})
			if err != nil {
				t.Fatalf("a %s draft is not an error: %v", status, err)
			}
			if draft["status"] != status {
				t.Fatalf("draft = %v", draft)
			}
			req := f.only(t)
			if req.Method != http.MethodPost || req.Path != "/v1/call-drafts" {
				t.Fatalf("request = %s %s", req.Method, req.Path)
			}
			if body := decodeBody(t, req); body["agentId"] != "12" || body["to"] != "+821012345678" {
				t.Fatalf("body = %v", body)
			}
			if _, sent := req.Header["Idempotency-Key"]; sent {
				t.Fatal("sent an Idempotency-Key nobody asked for")
			}
		})
	}
}

func TestCallDraftsCreateSendsGivenIdempotencyKey(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusCreated, `{"id":"cd_481","status":"pendingConfirmation"}`))
	api := newTestAPI(t, f)

	if _, err := api.CallDrafts.Create(context.Background(), Object{}, WithIdempotencyKey("draft-1")); err != nil {
		t.Fatal(err)
	}
	if got := f.only(t).Header.Get("Idempotency-Key"); got != "draft-1" {
		t.Fatalf("Idempotency-Key = %q", got)
	}
}

func TestCallDraftsCreateRefusesUndocumentedSuccessStatus(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusOK, `{"id":"cd_481","status":"pendingConfirmation"}`))
	api := newTestAPI(t, f)

	_, err := api.CallDrafts.Create(context.Background(), Object{})

	if apiErr := asAPIError(t, err); apiErr.Code != "invalid_response" {
		t.Fatalf("error = %+v", apiErr)
	}
}

func TestCallDraftsRevise(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusOK, `{"id":"cd_482","status":"pendingConfirmation","revision":2}`))
	api := newTestAPI(t, f)

	draft, err := api.CallDrafts.Revise(context.Background(), "cd_481", "4시도 안 되면 6시로", WithIdempotencyKey("revise-1"))
	if err != nil {
		t.Fatal(err)
	}
	if draft["id"] != "cd_482" {
		t.Fatalf("draft = %v", draft)
	}
	req := f.only(t)
	if req.Method != http.MethodPost || req.Path != "/v1/call-drafts/cd_481/revise" {
		t.Fatalf("request = %s %s", req.Method, req.Path)
	}
	if body := decodeBody(t, req); len(body) != 1 || body["request"] != "4시도 안 되면 6시로" {
		t.Fatalf("body = %v", body)
	}
	if got := req.Header.Get("Idempotency-Key"); got != "revise-1" {
		t.Fatalf("Idempotency-Key = %q", got)
	}
}

func TestCallDraftsReviseRefusesUndocumentedSuccessStatus(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusCreated, `{"id":"cd_482","status":"pendingConfirmation"}`))
	api := newTestAPI(t, f)

	_, err := api.CallDrafts.Revise(context.Background(), "cd_481", "r")

	if apiErr := asAPIError(t, err); apiErr.Code != "invalid_response" {
		t.Fatalf("error = %+v", apiErr)
	}
}

func TestCallDraftsConfirmGeneratesIdempotencyKey(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusCreated, `{"id":"c_1","status":"queued"}`))
	api := newTestAPI(t, f)

	call, err := api.CallDrafts.Confirm(context.Background(), "cd_481")
	if err != nil {
		t.Fatal(err)
	}
	if call["id"] != "c_1" {
		t.Fatalf("call = %v", call)
	}
	req := f.only(t)
	if req.Method != http.MethodPost || req.Path != "/v1/call-drafts/cd_481/confirm" {
		t.Fatalf("request = %s %s", req.Method, req.Path)
	}
	if req.HasBody || req.Header.Get("Content-Type") != "" {
		t.Fatalf("confirm sent a body: %q", req.Body)
	}
	if key := req.Header.Get("Idempotency-Key"); !uuidV4.MatchString(key) {
		t.Fatalf("Idempotency-Key = %q, want a UUID v4", key)
	}
}

func TestCallDraftsConfirmUsesAFreshKeyEachCall(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusCreated, `{"id":"c_1"}`))
	api := newTestAPI(t, f)

	for range 2 {
		if _, err := api.CallDrafts.Confirm(context.Background(), "cd_481"); err != nil {
			t.Fatal(err)
		}
	}
	reqs := f.all()
	if reqs[0].Header.Get("Idempotency-Key") == reqs[1].Header.Get("Idempotency-Key") {
		t.Fatal("two confirms shared one generated key")
	}
}

func TestCallDraftsConfirmSendsGivenKey(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusCreated, `{"id":"c_1"}`))
	api := newTestAPI(t, f)

	if _, err := api.CallDrafts.Confirm(context.Background(), "cd_481", WithIdempotencyKey("confirm-cd_481")); err != nil {
		t.Fatal(err)
	}
	if got := f.only(t).Header.Get("Idempotency-Key"); got != "confirm-cd_481" {
		t.Fatalf("Idempotency-Key = %q", got)
	}
}

func TestCallDraftsConfirmErrorCarriesGeneratedKey(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusNotFound,
		`{"statusCode":404,"message":"Call draft not found","error":"draft_not_found","data":null}`))
	api := newTestAPI(t, f)

	_, err := api.CallDrafts.Confirm(context.Background(), "cd_999999")

	apiErr := asAPIError(t, err)
	if apiErr.Code != "draft_not_found" || apiErr.Status != 404 {
		t.Fatalf("error = %+v", apiErr)
	}
	if sent := f.only(t).Header.Get("Idempotency-Key"); apiErr.IdempotencyKey != sent || !uuidV4.MatchString(sent) {
		t.Fatalf("error key %q, sent %q", apiErr.IdempotencyKey, sent)
	}
}

func TestCallDraftsConfirmAlreadyConfirmedCarriesCallID(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusConflict,
		`{"statusCode":409,"message":"Call draft already confirmed","error":"draft_already_confirmed","data":{"callId":"c_9"}}`))
	api := newTestAPI(t, f)

	_, err := api.CallDrafts.Confirm(context.Background(), "cd_481")

	if apiErr := asAPIError(t, err); apiErr.Code != "draft_already_confirmed" || apiErr.Data["callId"] != "c_9" {
		t.Fatalf("error = %+v", apiErr)
	}
}

func TestCallDraftsConfirmConnectionErrorCarriesGeneratedKey(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusCreated, `{}`))
	api := newTestAPI(t, f)
	f.server.Close()

	_, err := api.CallDrafts.Confirm(context.Background(), "cd_481")

	var connErr *APIConnectionError
	if !errors.As(err, &connErr) {
		t.Fatalf("expected *APIConnectionError, got %T: %v", err, err)
	}
	if !uuidV4.MatchString(connErr.IdempotencyKey) {
		t.Fatalf("IdempotencyKey = %q", connErr.IdempotencyKey)
	}
}

func TestCallDraftsConfirmRefusesUndocumentedSuccessStatus(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusOK, `{"id":"c_1"}`))
	api := newTestAPI(t, f)

	_, err := api.CallDrafts.Confirm(context.Background(), "cd_481")

	apiErr := asAPIError(t, err)
	if apiErr.Code != "invalid_response" || !uuidV4.MatchString(apiErr.IdempotencyKey) {
		t.Fatalf("error = %+v", apiErr)
	}
}

func TestCallDraftsRefuseBadArgumentsWithoutRequest(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusCreated, `{}`))
	api := newTestAPI(t, f)
	ctx := context.Background()

	if _, err := api.CallDrafts.Revise(ctx, "", "r"); err == nil {
		t.Error("revise accepted an empty draft id")
	}
	if _, err := api.CallDrafts.Confirm(ctx, ""); err == nil {
		t.Error("confirm accepted an empty draft id")
	}
	if _, err := api.CallDrafts.Confirm(ctx, "cd_481", WithIdempotencyKey("a,b")); err == nil {
		t.Error("confirm accepted an invalid key")
	}
	if _, err := api.CallDrafts.Create(ctx, Object{}, WithIdempotencyKey("bad\nkey")); err == nil {
		t.Error("create accepted an invalid key")
	}
	if n := len(f.all()); n != 0 {
		t.Fatalf("expected no request, got %d", n)
	}
}

// Every write waits for the long timeout: call drafts, crew generation and
// tool, agent and crew creates, updates and deletes (they save, deploy and
// publish in one request). Reads keep the short one.
func TestLongTimeoutAppliesToEveryWrite(t *testing.T) {
	tests := []struct {
		name   string
		status int
		call   func(*API, context.Context) error
		long   bool
	}{
		{name: "callDrafts.create", status: 201, long: true, call: func(a *API, ctx context.Context) error {
			_, err := a.CallDrafts.Create(ctx, Object{})
			return err
		}},
		{name: "callDrafts.revise", status: 200, long: true, call: func(a *API, ctx context.Context) error {
			_, err := a.CallDrafts.Revise(ctx, "cd_1", "r")
			return err
		}},
		{name: "callDrafts.confirm", status: 201, long: true, call: func(a *API, ctx context.Context) error {
			_, err := a.CallDrafts.Confirm(ctx, "cd_1")
			return err
		}},
		{name: "crews.generate", status: 202, long: true, call: func(a *API, ctx context.Context) error {
			_, err := a.Crews.Generate(ctx, Object{"goal": "g"})
			return err
		}},
		{name: "tools.create", status: 201, long: true, call: func(a *API, ctx context.Context) error {
			_, err := a.Tools.Create(ctx, Object{})
			return err
		}},
		{name: "agents.update", status: 200, long: true, call: func(a *API, ctx context.Context) error {
			_, err := a.Agents.Update(ctx, "1", Object{})
			return err
		}},
		{name: "crews.delete", status: 204, long: true, call: func(a *API, ctx context.Context) error {
			return a.Crews.Delete(ctx, "1")
		}},
		{name: "tools.get", status: 200, call: func(a *API, ctx context.Context) error {
			_, err := a.Tools.Get(ctx, "1")
			return err
		}},
		{name: "agents.list", status: 200, call: func(a *API, ctx context.Context) error {
			_, err := a.Agents.List(ctx)
			return err
		}},
		{name: "crewGenerations.get", status: 200, call: func(a *API, ctx context.Context) error {
			_, err := a.CrewGenerations.Get(ctx, "1")
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeAPI(t, func(int, fakeRequest) fakeResponse {
				return fakeResponse{status: tt.status, body: `{"id":"1","status":"running"}`, delay: 150 * time.Millisecond}
			})
			api := newTestAPI(t, f, WithAPITimeout(30*time.Millisecond), WithAPILongTimeout(5*time.Second))

			err := tt.call(api, context.Background())

			if tt.long && err != nil {
				t.Fatalf("long request timed out: %v", err)
			}
			if !tt.long && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("short request outlived the short timeout: %v", err)
			}
		})
	}
}
