**English** | [한국어](README.ko.md)

# tello-go

Go SDK for Tello. `tello.Client` speaks the WebSocket `/sdk` protocol and is the
"conversation brain": the gateway streams each caller turn from a live phone
call, and your handler's reply is forwarded back into the call. `tello.API` is
a separate client for the public REST resource API (tools, agents, crews, crew
generation and call drafts; see [section 7](#7-rest-resource-api)).

> repo: `tello-go` · module: `github.com/tello-ai/tello-go` · package: `tello`
>
> There is no webhook surface. The call endpoints of the REST API
> (`/v1/calls`: create, get, list, end, transfer) are not part of this SDK.

## 1. Install

```bash
go get github.com/tello-ai/tello-go/tello
```

Requires Go 1.22+. The only runtime dependency is `github.com/gorilla/websocket`;
the REST client uses `net/http`.

## 2. API key

`NewClient("")` reads `TELLO_API_KEY`. The URL comes from `WithURL` if given,
otherwise from `TELLO_URL`, otherwise the production gateway
`wss://api.telloai.io/sdk`.

`Connect` authenticates the API key internally: after the socket opens it sends an `auth` frame (`{"event":"auth","data":{"token":"<apiKey>"}}`) and returns only once the server confirms with `auth.ok`. No `Authorization` header or query-string token is used, and the key never appears in logs or error messages. `Connect` returns an error if authentication fails, the server closes with code `4401`, or `auth.ok` does not arrive within `WithOpenTimeout`. No commands run before authentication completes.

## 3. Connect + start a call

```go
package main

import (
	"context"
	"log"

	"github.com/tello-ai/tello-go/tello"
)

func main() {
	ctx := context.Background()
	client, err := tello.NewClient("")
	if err != nil {
		log.Fatal(err)
	}

	client.On(tello.EventTypeUserTurn, func(ctx context.Context, event tello.Event) error {
		return client.Answer(ctx, "heard: "+event.Text, "", "")
	})
	// Send DTMF digits: client.SendDtmf(ctx, "1234#", "", "")

	if err := client.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	if err := client.CreateCall(ctx, "+821012345678", "reservation check", nil, ""); err != nil {
		log.Fatal(err)
	}
	if err := client.WaitClosed(ctx); err != nil {
		log.Fatal(err)
	}
}
```

Register handlers before `Connect` so no frame is missed.

## 4. Realtime turn events (pub/sub)

`client.On(eventType string, handler func(context.Context, tello.Event) error)`
subscribes per event type. All events arrive as one `tello.Event` struct; the
fields populated depend on the type, and `event.Raw` always holds the decoded
frame.

| constant | value | populated fields |
| --- | --- | --- |
| `EventTypeCallCreated` | `call.created` | `CallID`, `SessionID` |
| `EventTypeUserTurn` | `user.turn` | `TurnIndex`, `Text` |
| `EventTypeAgentTurn` | `agent.turn` | `TurnIndex`, `Text` |
| `EventTypeAnswerAccepted` | `answer.accepted` | `RequestID`, `MessageID` |
| `EventTypeDtmfAccepted` | `dtmf.accepted` | `RequestID`, `MessageID`, `Digits` |
| `EventTypeCallSummary` | `call.summary` | `RequestID`, `Status`, `DurationSeconds`, `Transcript`, `Summary`, `CreditCharged` |
| `EventTypeCallStatusChanged` | `call.statusChanged` | `Status`, `PreviousStatus` |
| `EventTypeCallCompleted` | `call.completed` | `Status` |
| `EventTypeCallNoAnswer` | `call.noAnswer` | `Status`, `FailureReason` |
| `EventTypeCallFailed` | `call.failed` | `Status`, `FailureReason` |
| `EventTypeError` | `error` | `Code`, `Message`, `RequestID`, `Question`, `RequiredScope`, `IP` |
| `EventTypeDisconnected` | `disconnected` | SDK-local; emitted when the WS closes |

`auth.ok` is consumed internally by `Connect` and never re-emitted.

## 5. Commands

```go
client.CreateCall(ctx, to, prompt string, metadata map[string]any, requestID string) error
client.Answer(ctx, text, messageID, requestID string) error
client.SendDtmf(ctx, digits, messageID, requestID string) error
client.Cancel(ctx) error
client.GetSummary(ctx, callID, requestID string) error
```

Pass `""` for any optional string. `requestID` correlates a command with its
response frame; it is not an idempotency key. `CreateCall` always sends a
`requestId`: yours when non-empty, otherwise a generated UUID. Give each
command its own `requestID` and never reuse the `CreateCall` one on another
command: an error echoing a `CreateCall` requestId ends the wait.

`client.WaitClosed(ctx)` returns when the call reaches a terminal state
(`call.completed` / `call.noAnswer` / `call.failed`, or `call.statusChanged`
with status `cancelled`), when an error answers this call's `CreateCall`, or
when the connection closes. Bound it with `context.WithTimeout`.

A `WaitClosed` in progress returns when its own call ends, even if a handler
starts a follow-up call on that call's terminal event; call `WaitClosed` again
to wait for the follow-up.

`Cancel` has no reply of its own: once the gateway applies it, it sends
`call.statusChanged` with status `cancelled` (and the real `PreviousStatus`),
and that frame is the call's terminal event.

## 6. Error handling

Gateway error frames map 1:1 to typed errors:

| gateway `code` | error |
| --- | --- |
| `unauthenticated` | `*AuthenticationError` (auth handshake; also close code 4401) |
| `ipNotAllowed` | `*AuthenticationError` with `.IP` (auth handshake: the key's allowed IP list excludes this connection; close 4401) |
| `insufficientScope` | `*PermissionDeniedError` with `.RequiredScope` (only that command is refused; the connection stays open) |
| `toRequired` | `*ValidationError` |
| `callIdRequired` | `*ValidationError` |
| `dtmfDigitsRequired` | `*ValidationError` |
| `dtmfDigitsInvalid` | `*ValidationError` |
| `callAlreadyActive` | `*CallAlreadyActiveError` |
| `noActiveCall` | `*NoActiveCallError` |
| `callNotFound` | `*ValidationError` |
| `callNotCompleted` | `*ValidationError` |
| `callRejected` | `*CallRejectedError` (with `.Question`) |
| `internalError` | `*TelloServerError` (answering auth with close 1011: the key could not be checked; reconnect later) |

Every error carries the gateway code on `.Code` — branch on that, never on
`.Message`, which is display text the gateway may reword.

`createCall` can also be refused before any call exists — no `call.created`, no
`callId`, no charge. The gateway never retries these; any retry policy is yours.

| gateway `code` | error | what to do |
| --- | --- | --- |
| `insufficientCredit` | `*CallRefusedError` | tell the user to top up; do not resend |
| `concurrentLimitExceeded` | `*CallRefusedError` | wait for one of your own calls to end, then retry |
| `callerNotVerified` | `*CallRefusedError` | tell the user to verify the number; do not resend |
| `noRepresentativeNumber` | `*CallRefusedError` | tell the user to configure a caller number; do not resend |
| `callProviderUnauthorized` | `*CallProviderError` | service fault; report it, resending never helps |
| `callProviderDraining` | `*CallProviderError` | retry later at your own pace |
| `callProviderUnavailable` | `*CallProviderError` | retry later at your own pace |
| `callSetupFailed` | `*CallProviderError` | surface as a failure and report it |

Command-level errors are delivered to `EventTypeError` subscribers without
closing the socket. Only an error that answers this call's `CreateCall` (its
`requestId` matches) ends `WaitClosed`, so a failed `CreateCall` (e.g.
`toRequired`, `callRejected`) does not hang. A failed `Answer`, `SendDtmf`,
`GetSummary` or `Cancel` does not end the call, so its error is delivered only
as an `EventTypeError` event and `WaitClosed` keeps waiting. Two codes are
special:

- `callAlreadyActive` ends the wait only when it answers the `CreateCall` that
  opened the call. The gateway is still finishing the previous call for a
  moment after its terminal event, so this call never started: retry shortly.
  A `callAlreadyActive` answering a `CreateCall` sent during a live call is
  only an event, and the live call continues.
- `noActiveCall` never ends the wait.

An `EventTypeError` handler receives the frame as a `tello.Event`; turn it
into the typed error, with `.Question`, `.RequiredScope` or `.IP` filled in,
with `tello.ErrorForEvent`:

```go
client.On(tello.EventTypeError, func(_ context.Context, event tello.Event) error {
	err := tello.ErrorForEvent(event)
	var invalid *tello.ValidationError
	if errors.As(err, &invalid) {
		log.Printf("command %s rejected: %s", event.RequestID, invalid.Code)
	}
	return nil
})
```

`WaitClosed` returns:

- auth failure (`unauthenticated` or `ipNotAllowed` frame, close 4401, or `auth.ok` timeout) → `*AuthenticationError`, returned from `Connect`
- the key could not be checked at auth (`internalError` frame or close 1011) → `*TelloServerError`, returned from `Connect`; not a refusal, so reconnect later
- a call-start rejection, or a failure of the call's stream after `call.created` (reported against `CreateCall`) → its mapped error above
- the connection dropping mid-call → `*ConnectionClosedError`
- the session being displaced (close 4429) → `*SessionReplacedError`

These types do not share a wrapper, so match each one with its own `errors.As`
target:

```go
var rejected *tello.CallRejectedError
if errors.As(err, &rejected) {
	log.Printf("call rejected: %s (%s)", rejected.Message, rejected.Question)
}
```

The gateway drives a WS-level ping heartbeat; `gorilla/websocket` answers pongs
automatically. There is no reconnect/resume — treat an abnormal close as
reconnect-worthy and restart the call.

## 7. REST resource API

`tello.API` calls the public REST API (`/v1`) with a server API key. It is
separate from the WebSocket `tello.Client`, safe for concurrent use, and never
retries a request.

> **Writes go live at once.** A create, update or delete of a tool, agent or
> crew is saved, deployed and used from the next call; there is no draft to
> publish. **`CallDrafts.Confirm` places a real, billed call**, and so does
> `CallDrafts.Create` when the agent already has a ready scenario (status
> `confirmed`).

```go
api, err := tello.NewAPI("") // TELLO_API_KEY
if err != nil {
	log.Fatal(err)
}
agents, err := api.Agents.List(ctx)
tool, err := api.Tools.Create(ctx, tello.Object{
	"type":         "endCall",
	"name":         "End call",
	"functionName": "end_call",
}, tello.WithIdempotencyKey("tool-end-call-001"))
```

- **Key**: the `NewAPI` argument, otherwise `TELLO_API_KEY` (same rule and
  error as `NewClient`). It is sent only as `Authorization: Bearer <key>` and
  never appears in errors or formatted output.
- **Base URL**: `WithAPIURL`, otherwise `TELLO_API_URL`, otherwise
  `https://api.telloai.io/v1`. It must be `https`; `http` is accepted only for
  `127.0.0.1`, `localhost` and `::1`. Trailing `/` is dropped.
- **Timeouts**: `WithAPITimeout` (default 30s) for every request except
  `CallDrafts.Create`, `CallDrafts.Revise`, `CallDrafts.Confirm` and
  `Crews.Generate`, which use `WithAPILongTimeout` (default 120s). `ctx` can
  end a request sooner.
- **Headers**: `Accept: application/json`, `Content-Type: application/json`
  when there is a body, and `User-Agent: tello-go-sdk/<tello.Version>`.
- **Bodies and results** are `tello.Object` (`map[string]any`). Responses are
  decoded with `UseNumber`, so numbers arrive as `json.Number` and fields this
  SDK does not know yet are kept.

| method | HTTP | success | returns |
| --- | --- | --- | --- |
| `api.Tools.List(ctx, tello.ToolListOptions{Limit, Cursor})` | `GET /tools?limit&cursor` | 200 | `{data, nextCursor}` (`nextCursor` is `nil` on the last page) |
| `api.Tools.Get(ctx, id)` | `GET /tools/{id}` | 200 | tool |
| `api.Tools.Create(ctx, body, opts...)` | `POST /tools` | 201 | tool with `revision` |
| `api.Tools.Update(ctx, id, patch)` | `PATCH /tools/{id}` | 200 | tool |
| `api.Tools.Delete(ctx, id)` | `DELETE /tools/{id}` | 204 | only `error` |
| `api.Agents.List(ctx)` | `GET /agents` | 200 | `{data: [{id, name, description, isDefault}]}` |
| `api.Agents.Get` / `Create` / `Update` / `Delete` | `/agents[/{id}]` | 200 / 201 / 200 / 204 | agent / only `error` |
| `api.Crews.List(ctx)` | `GET /crews` | 200 | `{data: [...]}` |
| `api.Crews.Get` / `Create` / `Update` / `Delete` | `/crews[/{id}]` | 200 / 201 / 200 / 204 | crew / only `error` |
| `api.Crews.Generate(ctx, input, opts...)` | `POST /crews/generate` | 202 (200 when the same goal's job is returned) | job `{id, status: "running", ...}` |
| `api.Crews.GenerateAndWait(ctx, input, tello.GenerateAndWaitOptions{})` | `Generate`, then polls `GET /crew-generations/{id}` | — | the job once `status` is not `running` |
| `api.CrewGenerations.Get(ctx, id)` | `GET /crew-generations/{id}` | 200 | job `{id, status, crew, error, ...}` |
| `api.CallDrafts.Create(ctx, body, opts...)` | `POST /call-drafts` | 201 | draft |
| `api.CallDrafts.Revise(ctx, draftID, request, opts...)` | `POST /call-drafts/{id}/revise` with `{"request": ...}` | 200 | the revised draft, with a **new id** |
| `api.CallDrafts.Confirm(ctx, draftID, opts...)` | `POST /call-drafts/{id}/confirm`, no body | 201 | call object |

Path ids are URL-encoded; an empty id fails without sending anything. A
success status other than the one listed, or a body that is not a JSON
object, is an `*APIError` with code `invalid_response`.

Every draft status (`pendingConfirmation`, `needsClarification`, `refused`,
`confirmed`) is a 201 success, not an error: branch on `draft["status"]`.

`GenerateAndWait` polls every `PollInterval` (default 2s) for up to `Timeout`
(default 600s). A `failed` job is returned, not an error. When `Timeout`
passes first it returns `*tello.CrewGenerationTimeoutError` with `JobID`
(`errors.Is(err, context.DeadlineExceeded)` holds); the job keeps running.
Errors of `Generate` such as `crew_generation_in_progress` are returned as
they are.

### Idempotency keys

`tello.WithIdempotencyKey(key)` sends `Idempotency-Key` on the tool, agent and
crew creates, `Crews.Generate` and `CallDrafts.Create`/`Revise`/`Confirm`;
without it nothing is sent. `CallDrafts.Confirm` needs a key, so it generates
a UUID v4 when you give none. A key must be 1-255 printable ASCII characters
(0x20-0x7E) with no comma; otherwise the method fails without sending anything. Every
error carries the key the request sent in `IdempotencyKey`, so you can retry
with it.

### Errors

| error | when | fields |
| --- | --- | --- |
| `*tello.APIError` | the API answered with an error, or with a response the SDK cannot accept | `Status`, `Code`, `Message`, `Data`, `IdempotencyKey` |
| `*tello.APIConnectionError` | no complete response: connection failure, timeout, `ctx` ended | `Message`, `IdempotencyKey`, `Err` (the cause) |
| `*tello.CrewGenerationTimeoutError` | `GenerateAndWait` timed out | `JobID` |

`Code` is the server's `error` code verbatim (snake_case), so it matches the
public REST error docs. A response without the error envelope (a proxy page,
say) gets `http_<status>` with the HTTP status text. Branch on `Code` and
`Status`, never on `Message`. A write that failed with
`*APIConnectionError` **may already have been applied** (and a confirm may
already be dialing): check with get or list before sending it again, and
resend with the same idempotency key.

Some common codes (the full list is in the public REST error docs):

| HTTP | `Code` | meaning |
| --- | --- | --- |
| 401 | `unauthenticated` | the key is missing, wrong or revoked |
| 403 | `insufficient_scope` | the key lacks the scope; `Data["requiredScope"]` names it |
| 400 | `invalid_tool` / `invalid_agent` / `invalid_crew` | a field failed validation; see `Data["detail"]` |
| 404 | `tool_not_found` / `agent_not_found` / `crew_not_found` | no such item in this account |
| 404 | `crew_generation_not_found` / `draft_not_found` | no such job or draft |
| 409 | `idempotency_key_reused` / `idempotency_in_progress` | the key was used for another body / is still being processed |
| 409 | `crew_generation_in_progress` | another goal is being built; `Data["id"]` is its job |
| 409 | `draft_already_confirmed` | already dialed; `Data["callId"]` is the call |
| 422 | `tool_not_publishable` / `agent_not_publishable` / `crew_not_publishable` | nothing was saved; see `Data["reason"]` |
| 502 | `write_outcome_unknown` | the write may have been applied; check before retrying with a new key |
| 503 | `upstream_unavailable` | not applied; send the same request again later |

```go
call, err := api.CallDrafts.Confirm(ctx, draftID)
var apiErr *tello.APIError
var connErr *tello.APIConnectionError
switch {
case errors.As(err, &apiErr) && apiErr.Code == "draft_already_confirmed":
	log.Printf("already dialed: call %v", apiErr.Data["callId"])
case errors.As(err, &apiErr):
	log.Printf("confirm refused: %d %s: %s", apiErr.Status, apiErr.Code, apiErr.Message)
case errors.As(err, &connErr):
	// The call may already be placed: retry with the same key, never a new one.
	call, err = api.CallDrafts.Confirm(ctx, draftID, tello.WithIdempotencyKey(connErr.IdempotencyKey))
}
```

## 8. Examples

Runnable programs live in [`examples/`](examples/README.md):

```bash
go run ./examples/basic-call      # connect, one call, answer each turn
go run ./examples/agent-callback  # full lifecycle, history, cancel, typed errors
go run ./examples/call-summary    # gated live scenario ending in call.summary
```

They place real calls. Read [`examples/README.md`](examples/README.md) first.

## 9. Version compatibility

`tello-go 0.2.x` implements Tello WS protocol `1.0` (`tello.ProtocolVersion`).

`Connect` appends `?sdk=go&version=<tello.Version>&protocol=<tello.ProtocolVersion>`
to the URL so the gateway can log which client connected. Your path and other query
pairs are kept exactly as written (order, encoding, bare flags); existing `sdk`,
`version` and `protocol` pairs are replaced. Releasing: bump `tello.Version`, commit,
then tag `vX.Y.Z` — the release-guard workflow fails if the tag and `tello.Version` differ.

The full frame contract is in [`docs/protocol/sdk-ws.v1.md`](docs/protocol/sdk-ws.v1.md),
with [`docs/events/sdk-events.v1.schema.json`](docs/events/sdk-events.v1.schema.json)
and [`docs/errors/errors.v1.json`](docs/errors/errors.v1.json). Those three files
are generated copies of the canonical contract that lives beside the gateway
implementation — read them here, edit them there.
