// Run a controlled call, then fetch its summary.
//
// This is intentionally not a test: it creates a real call. Read
// examples/README.md before running it.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tello-ai/tello-go/tello"
)

// errTimeout marks a stage that never arrived, so the caller can attempt one
// cancel before failing.
var errTimeout = errors.New("timed out")

type config struct {
	apiKey  string
	url     string
	callTo  string
	timeout time.Duration
	prompt  string
	reply   string
}

// requireEnvironment fails closed before a client is constructed or a WebSocket
// is opened.
func requireEnvironment() (config, error) {
	if os.Getenv("ALLOW_LIVE_SIDE_EFFECTS") != "true" {
		return config{}, errors.New("set ALLOW_LIVE_SIDE_EFFECTS=true to run this live call scenario")
	}

	var missing []string
	for _, name := range []string{"TELLO_API_KEY", "TELLO_URL", "LIVE_CALL_TO", "LIVE_CALL_TIMEOUT_SECONDS"} {
		if os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return config{}, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}

	seconds, err := strconv.ParseFloat(os.Getenv("LIVE_CALL_TIMEOUT_SECONDS"), 64)
	if err != nil || seconds <= 0 {
		return config{}, errors.New("LIVE_CALL_TIMEOUT_SECONDS must be a positive number")
	}

	return config{
		apiKey:  os.Getenv("TELLO_API_KEY"),
		url:     os.Getenv("TELLO_URL"),
		callTo:  os.Getenv("LIVE_CALL_TO"),
		timeout: time.Duration(seconds * float64(time.Second)),
		prompt: getenv("LIVE_CALL_PROMPT",
			"Run a controlled SDK live scenario and keep the conversation brief."),
		reply: getenv("LIVE_CALL_REPLY",
			"This is a controlled TPG SDK live scenario. Thank you."),
	}, nil
}

func getenv(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// latch is a one-shot signal carrying the value that resolved it. Only the
// first set wins, matching "resolve a future once" in the other SDKs.
type latch[T any] struct {
	once  sync.Once
	ready chan struct{}
	value T
}

func newLatch[T any]() *latch[T] {
	return &latch[T]{ready: make(chan struct{})}
}

func (l *latch[T]) set(value T) {
	l.once.Do(func() {
		l.value = value
		close(l.ready)
	})
}

func (l *latch[T]) done() bool {
	select {
	case <-l.ready:
		return true
	default:
		return false
	}
}

// get is only valid once done reports true; closing ready publishes the value.
func (l *latch[T]) get() T {
	return l.value
}

// waitForStage waits for one correlated response, surfacing any error frame first.
func waitForStage[T any](ctx context.Context, stage *latch[T], failed *latch[string], label string, timeout time.Duration) (T, error) {
	var zero T
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-stage.ready:
	case <-failed.ready:
	case <-timer.C:
		return zero, fmt.Errorf("%w waiting for %s", errTimeout, label)
	case <-ctx.Done():
		return zero, ctx.Err()
	}

	if failed.done() {
		return zero, errors.New(failed.get())
	}
	return stage.get(), nil
}

func randomID(prefix string) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(buf), nil
}

func main() {
	cfg, err := requireEnvironment()
	if err != nil {
		log.Fatalf("live call-summary scenario failed: %v", err)
	}
	if err := run(context.Background(), cfg); err != nil {
		log.Fatalf("live call-summary scenario failed: %v", err)
	}
	log.Print("live call-summary scenario completed")
}

func run(ctx context.Context, cfg config) error {
	summaryRequestID, err := randomID("live-summary-")
	if err != nil {
		return err
	}
	answerRequestID, err := randomID("live-answer-")
	if err != nil {
		return err
	}
	answerMessageID, err := randomID("live-message-")
	if err != nil {
		return err
	}

	var (
		callCreated       = newLatch[string]()
		answerAccepted    = newLatch[tello.Event]()
		agentTurnReceived = newLatch[tello.Event]()
		completed         = newLatch[string]()
		summaryReceived   = newLatch[tello.Event]()
		failed            = newLatch[string]()
		callTerminal      atomic.Bool
		answerSent        atomic.Bool
	)
	fail := func(message string) { failed.set(message) }

	client, err := tello.NewClient(cfg.apiKey, tello.WithURL(cfg.url))
	if err != nil {
		return err
	}

	client.On(tello.EventTypeCallCreated, func(_ context.Context, event tello.Event) error {
		if !callCreated.done() {
			log.Printf("[call.created] callId=%s", event.CallID)
			callCreated.set(event.CallID)
		}
		return nil
	})

	client.On(tello.EventTypeCallStatusChanged, func(_ context.Context, event tello.Event) error {
		log.Printf("[status] %s -> %s", event.PreviousStatus, event.Status)
		if event.Status == "cancelled" {
			callTerminal.Store(true)
			fail("call ended with unexpected terminal status: cancelled")
		}
		return nil
	})

	client.On(tello.EventTypeUserTurn, func(ctx context.Context, event tello.Event) error {
		log.Printf("[user.turn #%d] %s", event.TurnIndex, event.Text)
		if !callCreated.done() {
			fail("received user.turn before call.created")
			return nil
		}
		if event.CallID != callCreated.get() {
			fail("received user.turn for a different call")
			return nil
		}
		// Answer once and never retry: every answer is spoken to the caller.
		if answerSent.CompareAndSwap(false, true) {
			if err := client.Answer(ctx, cfg.reply, answerMessageID, answerRequestID); err != nil {
				fail(fmt.Sprintf("could not send answer: %v", err))
				return nil
			}
			log.Printf("[answer] requestId=%s", answerRequestID)
		}
		return nil
	})

	client.On(tello.EventTypeAnswerAccepted, func(_ context.Context, event tello.Event) error {
		if event.RequestID != answerRequestID {
			return nil
		}
		if !callCreated.done() {
			fail("received answer.accepted before call.created")
			return nil
		}
		if event.CallID != callCreated.get() || event.MessageID != answerMessageID {
			fail("answer.accepted did not match the submitted answer")
			return nil
		}
		if !answerAccepted.done() {
			log.Printf("[answer.accepted] messageId=%s", event.MessageID)
			answerAccepted.set(event)
		}
		return nil
	})

	client.On(tello.EventTypeAgentTurn, func(_ context.Context, event tello.Event) error {
		log.Printf("[agent.turn #%d] %s", event.TurnIndex, event.Text)
		if !callCreated.done() {
			fail("received agent.turn before call.created")
			return nil
		}
		if answerAccepted.done() && event.CallID == callCreated.get() && event.Text == cfg.reply {
			agentTurnReceived.set(event)
		}
		return nil
	})

	client.On(tello.EventTypeCallCompleted, func(_ context.Context, event tello.Event) error {
		callTerminal.Store(true)
		switch {
		case event.Status != "completed":
			fail(fmt.Sprintf("call.completed carried unexpected status: %s", event.Status))
		case !answerAccepted.done() || !agentTurnReceived.done():
			fail("call completed before the answer acknowledgement and agent turn")
		case event.CallID != callCreated.get():
			fail("call.completed belonged to a different call")
		default:
			log.Printf("[call.completed] callId=%s", event.CallID)
			completed.set(event.CallID)
		}
		return nil
	})

	client.On(tello.EventTypeCallNoAnswer, func(_ context.Context, event tello.Event) error {
		callTerminal.Store(true)
		fail("call ended with noAnswer: " + reasonOr(event.FailureReason))
		return nil
	})

	client.On(tello.EventTypeCallFailed, func(_ context.Context, event tello.Event) error {
		callTerminal.Store(true)
		fail("call failed: " + reasonOr(event.FailureReason))
		return nil
	})

	client.On(tello.EventTypeCallSummary, func(_ context.Context, event tello.Event) error {
		if event.RequestID == summaryRequestID && !summaryReceived.done() {
			log.Printf("[call.summary] callId=%s status=%s", event.CallID, event.Status)
			summaryReceived.set(event)
		}
		return nil
	})

	client.On(tello.EventTypeError, func(_ context.Context, event tello.Event) error {
		fail(fmt.Sprintf("gateway error %s: %s", event.Code, event.Message))
		return nil
	})

	client.On(tello.EventTypeDisconnected, func(_ context.Context, _ tello.Event) error {
		fail("gateway disconnected before scenario completed")
		return nil
	})

	if err := client.Connect(ctx); err != nil {
		return err
	}
	defer client.Close()

	log.Printf("[createCall] to=%s", cfg.callTo)
	if err := client.CreateCall(ctx, cfg.callTo, cfg.prompt, map[string]any{
		"source": "tello-go-call-summary-example",
	}, ""); err != nil {
		return err
	}

	callID, err := awaitCompletedCall(ctx, cfg, callCreated, answerAccepted, agentTurnReceived, completed, failed)
	if err != nil {
		// A summary request is never sent after a non-completed terminal state.
		if errors.Is(err, errTimeout) && !callTerminal.Load() {
			log.Print("[timeout] attempting to cancel the active call")
			if cancelErr := client.Cancel(ctx); cancelErr != nil {
				log.Printf("[cancel] failed: %v", cancelErr)
			}
		}
		return err
	}

	log.Printf("[getSummary] requestId=%s", summaryRequestID)
	if err := client.GetSummary(ctx, callID, summaryRequestID); err != nil {
		return err
	}
	summary, err := waitForStage(ctx, summaryReceived, failed, "call.summary", cfg.timeout)
	if err != nil {
		return err
	}
	if summary.CallID != callID || summary.Status != "completed" {
		return errors.New("call.summary did not confirm the completed call")
	}
	return nil
}

func awaitCompletedCall(
	ctx context.Context,
	cfg config,
	callCreated *latch[string],
	answerAccepted *latch[tello.Event],
	agentTurnReceived *latch[tello.Event],
	completed *latch[string],
	failed *latch[string],
) (string, error) {
	if _, err := waitForStage(ctx, callCreated, failed, "call.created", cfg.timeout); err != nil {
		return "", err
	}
	if _, err := waitForStage(ctx, answerAccepted, failed, "answer.accepted", cfg.timeout); err != nil {
		return "", err
	}
	if _, err := waitForStage(ctx, agentTurnReceived, failed, "agent.turn", cfg.timeout); err != nil {
		return "", err
	}
	return waitForStage(ctx, completed, failed, "call.completed", cfg.timeout)
}

func reasonOr(failureReason string) string {
	if failureReason == "" {
		return "no reason supplied"
	}
	return failureReason
}
