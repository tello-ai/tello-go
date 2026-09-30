// Minimal end-to-end example: connect, start a call, answer each user turn.
//
// Run against a locally-running turn-provider-gateway:
//
//	TELLO_API_KEY=tello_live_xxx TELLO_URL=ws://localhost:3000/sdk \
//		go run ./examples/basic-call
package main

import (
	"context"
	"log"

	"github.com/tello-ai/tello-go/tello"
)

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatalf("basic-call example failed: %v", err)
	}
}

func run(ctx context.Context) error {
	// NewClient("") reads TELLO_API_KEY; the URL falls back to TELLO_URL and
	// then to wss://api.telloai.io/sdk. Use tello.WithURL to override it.
	client, err := tello.NewClient("")
	if err != nil {
		return err
	}

	// Handlers are registered before Connect so no frame is missed.
	client.On(tello.EventTypeUserTurn, func(ctx context.Context, event tello.Event) error {
		log.Printf("[user #%d] %s", event.TurnIndex, event.Text)
		return client.Answer(ctx, "확인했습니다. 계속 말씀해주세요.", "", "")
	})

	client.On(tello.EventTypeCallStatusChanged, func(_ context.Context, event tello.Event) error {
		log.Printf("[status] %s -> %s", event.PreviousStatus, event.Status)
		return nil
	})

	client.On(tello.EventTypeCallCompleted, func(_ context.Context, event tello.Event) error {
		log.Printf("[completed] %s", event.CallID)
		return nil
	})

	client.On(tello.EventTypeError, func(_ context.Context, event tello.Event) error {
		log.Printf("[error] %s: %s", event.Code, event.Message)
		return nil
	})

	// Connect authenticates the API key in-band and returns only after auth.ok.
	if err := client.Connect(ctx); err != nil {
		return err
	}
	defer client.Close()

	if err := client.CreateCall(ctx, "+821012345678", "예약 확인", nil, ""); err != nil {
		return err
	}
	// WaitClosed returns when the call reaches a terminal state, and re-raises
	// the error that ended it (so a rejected createCall does not hang).
	return client.WaitClosed(ctx)
}
