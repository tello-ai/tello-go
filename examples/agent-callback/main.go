// Advanced example: an agent that holds a conversation over a live call.
//
// Shows the full lifecycle and every inbound event:
//
//   - explicit Connect / Close
//   - per-turn response generation with conversation history
//   - answering user.turn, observing agent.turn / call.statusChanged
//   - all terminal states (completed / noAnswer / failed) and error
//   - ending the call early with Cancel on an intent keyword
//   - mapping error frames / auth failure to typed errors with errors.As
//
// Run against a locally-running turn-provider-gateway:
//
//	TELLO_API_KEY=tello_live_xxx TELLO_URL=ws://localhost:3000/sdk \
//		go run ./examples/agent-callback
package main

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/tello-ai/tello-go/tello"
)

// callTimeout bounds the demo so it cannot hang forever if the call never
// reaches a terminal state.
const callTimeout = 120 * time.Second

// hangupHints are keywords that end the call.
var hangupHints = []string{"괜찮습니다", "감사합니다", "그만", "bye"}

type message struct {
	role string
	text string
}

// agent is a toy conversation brain. Replace generate with your own LLM / rules.
type agent struct {
	mu sync.Mutex
	// history is (role, text) the way your own model would consume it.
	history []message
}

func (a *agent) respond(text string) string {
	reply := generate(text)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.history = append(a.history, message{role: "user", text: text}, message{role: "assistant", text: reply})
	return reply
}

func (a *agent) turns() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.history)
}

func (a *agent) wantsHangup(text string) bool {
	for _, hint := range hangupHints {
		if strings.Contains(text, hint) {
			return true
		}
	}
	return false
}

// generate stands in for real inference. Deterministic so the example is testable.
func generate(text string) string {
	if strings.Contains(text, "예약") {
		return "네, 예약 도와드리겠습니다. 원하시는 날짜를 말씀해 주세요."
	}
	if strings.ContainsFunc(text, unicode.IsDigit) {
		return "확인했습니다. 해당 시간으로 예약을 진행할까요?"
	}
	return "말씀 감사합니다. 좀 더 자세히 알려주시겠어요?"
}

func main() {
	ctx := context.Background()

	brain := &agent{}
	client, err := tello.NewClient("") // reads TELLO_API_KEY / TELLO_URL
	if err != nil {
		log.Fatalf("agent-callback example failed: %v", err)
	}

	register(brain, client)

	if err := client.Connect(ctx); err != nil {
		report(err)
		return
	}
	defer client.Close()

	if err := run(ctx, client); err != nil {
		report(err)
	}
}

func register(brain *agent, client *tello.Client) {
	client.On(tello.EventTypeCallStatusChanged, func(_ context.Context, event tello.Event) error {
		log.Printf("[status] %s -> %s", event.PreviousStatus, event.Status)
		return nil
	})

	client.On(tello.EventTypeUserTurn, func(ctx context.Context, event tello.Event) error {
		log.Printf("[user #%d] %s", event.TurnIndex, event.Text)
		if brain.wantsHangup(event.Text) {
			log.Print("[agent] hangup intent -> cancel")
			return client.Cancel(ctx)
		}
		return client.Answer(ctx, brain.respond(event.Text), "", "")
	})

	client.On(tello.EventTypeAgentTurn, func(_ context.Context, event tello.Event) error {
		// Confirmation that our answer was accepted into the call.
		log.Printf("[agent #%d] %s", event.TurnIndex, event.Text)
		return nil
	})

	client.On(tello.EventTypeCallNoAnswer, func(_ context.Context, event tello.Event) error {
		log.Printf("[noAnswer] reason=%s", event.FailureReason)
		return nil
	})

	client.On(tello.EventTypeCallFailed, func(_ context.Context, event tello.Event) error {
		log.Printf("[failed] reason=%s", event.FailureReason)
		return nil
	})

	client.On(tello.EventTypeCallCompleted, func(_ context.Context, event tello.Event) error {
		log.Printf("[completed] %s (%d turns)", event.CallID, brain.turns())
		return nil
	})

	client.On(tello.EventTypeError, func(_ context.Context, event tello.Event) error {
		// Non-fatal command errors arrive here without closing the socket.
		note := ""
		if event.Question != "" {
			note = " (" + event.Question + ")"
		}
		log.Printf("[error] %s: %s%s", event.Code, event.Message, note)
		return nil
	})

	client.On(tello.EventTypeDisconnected, func(_ context.Context, _ tello.Event) error {
		// No auto-reconnect: the gateway has no resume protocol. Restart the
		// call on a fresh connection if you need to continue.
		log.Print("[disconnected]")
		return nil
	})
}

func run(ctx context.Context, client *tello.Client) error {
	if err := client.CreateCall(ctx, "+821012345678", "예약 확인 전화", map[string]any{
		"source": "agent-callback-example",
	}, ""); err != nil {
		return err
	}

	waitCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	err := client.WaitClosed(waitCtx)
	if errors.Is(err, context.DeadlineExceeded) {
		log.Print("[timeout] cancelling call")
		return client.Cancel(ctx)
	}
	return err
}

// report maps the SDK's typed errors. They do not share a common wrapper, so
// each concrete type is matched with its own errors.As target.
func report(err error) {
	var (
		authErr     *tello.AuthenticationError
		rejectedErr *tello.CallRejectedError
		activeErr   *tello.CallAlreadyActiveError
		closedErr   *tello.ConnectionClosedError
		replacedErr *tello.SessionReplacedError
	)
	switch {
	case errors.As(err, &authErr):
		log.Print("auth failed: check TELLO_API_KEY")
	case errors.As(err, &rejectedErr):
		log.Printf("call rejected: %s (question: %s)", rejectedErr.Message, rejectedErr.Question)
	case errors.As(err, &activeErr):
		log.Print("a call is already active on this session")
	case errors.As(err, &replacedErr):
		log.Print("session replaced by a newer connection")
	case errors.As(err, &closedErr):
		log.Printf("connection closed: %s", closedErr.Message)
	default:
		log.Printf("tello error: %v", err)
	}
}
