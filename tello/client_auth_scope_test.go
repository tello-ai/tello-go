package tello

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// authGateway answers the auth frame with frame (when non-nil), then closes
// with closeCode and reason.
func authGateway(t *testing.T, frame map[string]any, closeCode int, reason string) string {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		readAuth(t, conn)
		if frame != nil {
			_ = conn.WriteJSON(frame)
		}
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(closeCode, reason))
		drain(conn)
	}))
	t.Cleanup(server.Close)
	return "ws" + server.URL[len("http"):] + "/sdk"
}

func connectErr(t *testing.T, url string) error {
	t.Helper()
	client, err := NewClient("key-1", WithURL(url), WithOpenTimeout(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client.Connect(context.Background())
}

func TestClientConnectFailsOnIPNotAllowedWithTheIP(t *testing.T) {
	url := authGateway(t, map[string]any{
		"type": "error", "version": "1.0", "code": "ipNotAllowed",
		"message": "The request IP address is not in this server key's allowed IP list", "ip": "203.0.113.7",
	}, closeUnauthenticated, "ip_not_allowed")

	err := connectErr(t, url)

	var authErr *AuthenticationError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected AuthenticationError, got %T: %v", err, err)
	}
	if authErr.Code != "ipNotAllowed" || authErr.IP != "203.0.113.7" {
		t.Fatalf("error = %+v", authErr)
	}
}

func TestClientConnectReportsKeyCheckFailureAsServerError(t *testing.T) {
	url := authGateway(t, map[string]any{
		"type": "error", "version": "1.0", "code": "internalError", "message": "Internal error",
	}, websocket.CloseInternalServerErr, "internal_error")

	err := connectErr(t, url)

	var serverErr *TelloServerError
	if !errors.As(err, &serverErr) || serverErr.Code != "internalError" {
		t.Fatalf("expected TelloServerError internalError, got %T: %v", err, err)
	}
	var authErr *AuthenticationError
	if errors.As(err, &authErr) {
		t.Fatal("a key that could not be checked is not an authentication failure")
	}
}

func TestClientConnectReports1011CloseAsServerError(t *testing.T) {
	url := authGateway(t, nil, websocket.CloseInternalServerErr, "internal_error")

	err := connectErr(t, url)

	var serverErr *TelloServerError
	if !errors.As(err, &serverErr) || serverErr.Code != "internalError" {
		t.Fatalf("expected TelloServerError internalError, got %T: %v", err, err)
	}
}

func scopeFrame(requestID, scope string) map[string]any {
	frame := errorFrame("insufficientScope", requestID)
	frame["requiredScope"] = scope
	return frame
}

func TestClientWaitClosedReturnsPermissionDeniedForCreateCall(t *testing.T) {
	gw := newFakeGateway(t)
	client := gw.newClient(t)
	gc := gw.connect(t, client)

	createCall(t, client, "r-a")
	gc.next(t, "createCall")
	w := startWait(t, client)
	gc.send(t, scopeFrame("r-a", "calls:write"))

	var denied *PermissionDeniedError
	if err := <-w; !errors.As(err, &denied) || denied.Code != "insufficientScope" || denied.RequiredScope != "calls:write" {
		t.Fatalf("expected PermissionDeniedError calls:write, got %T: %v", err, err)
	}
	// Only the command is refused: the connection stays usable.
	if err := client.GetSummary(context.Background(), "c1", "r-s"); err != nil {
		t.Fatalf("connection closed after a scope refusal: %v", err)
	}
	gc.next(t, "getSummary")
}

func TestClientScopeRefusalOfOtherCommandsDoesNotEndTheCall(t *testing.T) {
	gw := newFakeGateway(t)
	client := gw.newClient(t)
	events := make(chan Event, 1)
	client.On(EventTypeError, func(_ context.Context, event Event) error {
		events <- event
		return nil
	})
	gc := gw.connect(t, client)

	createCall(t, client, "r-a")
	gc.next(t, "createCall")
	gc.send(t, eventFrame("call.created"))
	w := startWait(t, client)

	if err := client.GetSummary(context.Background(), "c1", "r-s"); err != nil {
		t.Fatal(err)
	}
	gc.next(t, "getSummary")
	gc.send(t, scopeFrame("r-s", "calls:read"))

	select {
	case event := <-events:
		var denied *PermissionDeniedError
		if err := ErrorForEvent(event); !errors.As(err, &denied) || denied.RequiredScope != "calls:read" {
			t.Fatalf("event maps to %T: %v", err, err)
		}
	case <-time.After(time.Second):
		t.Fatal("the scope refusal never reached the error handler")
	}

	gc.send(t, eventFrame("call.completed"))
	if err := <-w; err != nil {
		t.Fatalf("expected the call's own completion to end the wait, got %T: %v", err, err)
	}
}
