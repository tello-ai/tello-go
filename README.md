**English** | [한국어](README.ko.md)

# tello-go

Go WebSocket SDK for the Tello `/sdk` protocol. The SDK is the "conversation
brain": the gateway streams each caller turn from a live phone call, and your
handler's reply is forwarded back into the call.

> repo: `tello-go` · module: `github.com/tello-tft/tello-go` · package: `tello`
>
> Transport is WebSocket only. There is no REST or webhook surface.

## 1. Install

```bash
go get github.com/tello-tft/tello-go
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

	"github.com/tello-tft/tello-go/tello"
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
response frame; it is not an idempotency key.

`client.WaitClosed(ctx)` returns when the call reaches a terminal state
(`call.completed` / `call.noAnswer` / `call.failed`, or a cancelled status) or
the connection closes. Bound it with `context.WithTimeout`.

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

Command-level errors are also delivered to `EventTypeError` subscribers without
closing the socket. `WaitClosed` returns the relevant error so a failed
`CreateCall` (e.g. `toRequired`, `callRejected`) does not hang:

- auth failure (`unauthenticated` frame, close 4401, or `auth.ok` timeout) → `*AuthenticationError`, returned from `Connect`
- a call-start rejection → its mapped error above
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

`tello-go 0.1.x` implements Tello WS protocol `1.0` (`tello.ProtocolVersion`).

The full frame contract is in [`docs/protocol/sdk-ws.v1.md`](docs/protocol/sdk-ws.v1.md),
with [`docs/events/sdk-events.v1.schema.json`](docs/events/sdk-events.v1.schema.json)
and [`docs/errors/errors.v1.json`](docs/errors/errors.v1.json). Those three files
are generated copies of the canonical contract that lives beside the gateway
implementation — read them here, edit them there.
