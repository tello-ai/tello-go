**English** | [한국어](README.ko.md)

# tello-go

Go WebSocket SDK for the Tello `/sdk` protocol. The SDK is the "conversation
brain": the gateway streams each caller turn from a live phone call, and your
handler's reply is forwarded back into the call.

> repo: `tello-go` · module: `github.com/tello-ai/tello-go` · package: `tello`
>
> Transport is WebSocket only. There is no REST or webhook surface.

## 1. Install

```bash
go get github.com/tello-ai/tello-go
```

Requires Go 1.22+. The only runtime dependency is `github.com/gorilla/websocket`.

## 2. API key

`NewClient("")` reads `TELLO_API_KEY`; `WithURL` overrides the default
`ws://localhost:3000/sdk`, which itself falls back to `TELLO_URL`.

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
| `EventTypeError` | `error` | `Code`, `Message`, `RequestID`, `Question` |
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
| `toRequired` | `*ValidationError` |
| `callIdRequired` | `*ValidationError` |
| `dtmfDigitsRequired` | `*ValidationError` |
| `dtmfDigitsInvalid` | `*ValidationError` |
| `callAlreadyActive` | `*CallAlreadyActiveError` |
| `noActiveCall` | `*NoActiveCallError` |
| `callNotFound` | `*ValidationError` |
| `callNotCompleted` | `*ValidationError` |
| `callRejected` | `*CallRejectedError` (with `.Question`) |
| `internalError` | `*TelloServerError` |

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
into the typed error with `tello.ErrorFor`:

```go
client.On(tello.EventTypeError, func(_ context.Context, event tello.Event) error {
	err := tello.ErrorFor(event.Code, event.Message, event.Question)
	var invalid *tello.ValidationError
	if errors.As(err, &invalid) {
		log.Printf("command %s rejected: %s", event.RequestID, invalid.Code)
	}
	return nil
})
```

`WaitClosed` returns:

- auth failure (`unauthenticated` frame, close 4401, or `auth.ok` timeout) → `*AuthenticationError`, returned from `Connect`
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

## 7. Examples

Runnable programs live in [`examples/`](examples/README.md):

```bash
go run ./examples/basic-call      # connect, one call, answer each turn
go run ./examples/agent-callback  # full lifecycle, history, cancel, typed errors
go run ./examples/call-summary    # gated live scenario ending in call.summary
```

They place real calls. Read [`examples/README.md`](examples/README.md) first.

## 8. Version compatibility

`tello-go 0.2.x` implements Tello WS protocol `1.0` (`tello.ProtocolVersion`).

The full frame contract is in [`docs/protocol/sdk-ws.v1.md`](docs/protocol/sdk-ws.v1.md),
with [`docs/events/sdk-events.v1.schema.json`](docs/events/sdk-events.v1.schema.json)
and [`docs/errors/errors.v1.json`](docs/errors/errors.v1.json). Those three files
are generated copies of the canonical contract that lives beside the gateway
implementation — read them here, edit them there.
