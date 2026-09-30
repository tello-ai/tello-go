package tello

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// readAuth reads the first application frame and fails the test unless it is
// the mandatory auth command carrying the api key as the token field. It
// returns the parsed frame so the caller can assert on its payload.
func readAuth(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	var frame map[string]any
	if err := conn.ReadJSON(&frame); err != nil {
		t.Errorf("reading auth frame: %v", err)
		return nil
	}
	if frame["event"] != "auth" {
		t.Errorf("expected first frame to be auth, got %v", frame["event"])
	}
	data, _ := frame["data"].(map[string]any)
	if data == nil || data["token"] == nil || data["token"] == "" {
		t.Errorf("expected auth frame to carry a token, got %v", frame["data"])
	}
	return frame
}

func sendAuthOK(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	if err := conn.WriteJSON(map[string]any{
		"type":      "auth.ok",
		"version":   "1.0",
		"accountId": "acc-1",
	}); err != nil {
		t.Errorf("writing auth.ok: %v", err)
	}
}

func TestClientAuthenticatesFirstWithoutHeaderThenSendsCreateCall(t *testing.T) {
	var gotAuth string
	var authFrame map[string]any
	var createFrame map[string]any
	done := make(chan struct{})
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		gotAuth = r.Header.Get("Authorization")
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		authFrame = readAuth(t, conn)
		sendAuthOK(t, conn)
		if err := conn.ReadJSON(&createFrame); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()

	client, err := NewClient("key-1", WithURL("ws"+server.URL[len("http"):]+"/sdk"))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if err := client.CreateCall(context.Background(), "+821012345678", "prompt", map[string]any{"src": "test"}, "r1"); err != nil {
		t.Fatal(err)
	}
	<-done

	if gotAuth != "" {
		t.Fatalf("expected no Authorization header, got %q", gotAuth)
	}
	assertJSONEqual(t, map[string]any{
		"event": "auth",
		"data":  map[string]any{"token": "key-1"},
	}, authFrame)
	assertJSONEqual(t, map[string]any{
		"event": "createCall",
		"data": map[string]any{
			"to":        "+821012345678",
			"prompt":    "prompt",
			"metadata":  map[string]any{"src": "test"},
			"requestId": "r1",
		},
	}, createFrame)
	if data, _ := createFrame["data"].(map[string]any); data != nil {
		if _, exists := data["agentId"]; exists {
			t.Fatalf("createCall wire data must not contain agentId key, got %v", data)
		}
	}
}

func TestClientConnectFailsOnUnauthenticatedErrorFrame(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		readAuth(t, conn)
		_ = conn.WriteJSON(map[string]any{
			"type":    "error",
			"version": "1.0",
			"code":    "unauthenticated",
			"message": "invalid key",
		})
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(closeUnauthenticated, "unauthenticated"))
	}))
	defer server.Close()

	client, err := NewClient("key-1", WithURL("ws"+server.URL[len("http"):]+"/sdk"))
	if err != nil {
		t.Fatal(err)
	}
	err = client.Connect(context.Background())
	var authErr *AuthenticationError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected AuthenticationError, got %T: %v", err, err)
	}
}

func TestClientConnectFailsOn4401Close(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		readAuth(t, conn)
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(closeUnauthenticated, "unauthenticated"))
	}))
	defer server.Close()

	client, err := NewClient("key-1", WithURL("ws"+server.URL[len("http"):]+"/sdk"))
	if err != nil {
		t.Fatal(err)
	}
	err = client.Connect(context.Background())
	var authErr *AuthenticationError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected AuthenticationError, got %T: %v", err, err)
	}
}

func TestClientConnectFailsWhenAuthOKTimesOut(t *testing.T) {
	upgrader := websocket.Upgrader{}
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		readAuth(t, conn)
		// Never send auth.ok; keep the socket open until the test finishes.
		<-release
	}))
	defer server.Close()
	defer close(release)

	client, err := NewClient("key-1",
		WithURL("ws"+server.URL[len("http"):]+"/sdk"),
		WithOpenTimeout(200*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	err = client.Connect(context.Background())
	if err == nil {
		t.Fatal("expected Connect to fail on auth.ok timeout")
	}
	// A missing auth.ok is an authentication failure, not a transport one — the
	// gateway closes with 4401 on its own deadline either way.
	var authErr *AuthenticationError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected AuthenticationError, got %T: %v", err, err)
	}
}

func TestClientDoesNotSendBusinessCommandBeforeAuthOK(t *testing.T) {
	upgrader := websocket.Upgrader{}
	sawAuth := make(chan struct{})
	frames := make(chan map[string]any, 4)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		readAuth(t, conn)
		close(sawAuth)
		// Delay auth.ok. Any business command sent during this window would
		// arrive before auth.ok and be captured out of order below.
		<-release
		sendAuthOK(t, conn)
		for {
			var frame map[string]any
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			frames <- frame
		}
	}))
	defer server.Close()

	client, err := NewClient("key-1", WithURL("ws"+server.URL[len("http"):]+"/sdk"))
	if err != nil {
		t.Fatal(err)
	}

	connectDone := make(chan error, 1)
	go func() { connectDone <- client.Connect(context.Background()) }()

	<-sawAuth
	// Connect must still be blocked: it has not seen auth.ok yet.
	select {
	case err := <-connectDone:
		t.Fatalf("Connect returned before auth.ok: %v", err)
	case frame := <-frames:
		t.Fatalf("unexpected frame before auth.ok: %v", frame)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	if err := <-connectDone; err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer client.Close()

	if err := client.CreateCall(context.Background(), "+821012345678", "", nil, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-frames:
		if frame["event"] != "createCall" {
			t.Fatalf("expected createCall after auth.ok, got %v", frame["event"])
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for createCall frame")
	}
}

func TestClientEmitsUserTurnsAndSurfacesCallRejection(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		readAuth(t, conn)
		sendAuthOK(t, conn)
		requestID := readCommand(t, conn, "createCall")
		_ = conn.WriteJSON(map[string]any{
			"type":      "user.turn",
			"version":   "1.0",
			"callId":    "c1",
			"turnIndex": 1,
			"text":      "hello",
			"timestamp": "t",
		})
		_ = conn.WriteJSON(map[string]any{
			"type":      "error",
			"version":   "1.0",
			"code":      "callRejected",
			"message":   "Call rejected",
			"question":  "why?",
			"requestId": requestID,
		})
	}))
	defer server.Close()

	client, err := NewClient("key-1", WithURL("ws"+server.URL[len("http"):]+"/sdk"))
	if err != nil {
		t.Fatal(err)
	}
	var turns []string
	client.On(EventTypeUserTurn, func(_ context.Context, event Event) error {
		turns = append(turns, event.Text)
		return nil
	})
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.CreateCall(context.Background(), "+821012345678", "", nil, ""); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = client.WaitClosed(ctx)
	var rejected *CallRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("expected CallRejectedError, got %T: %v", err, err)
	}
	if rejected.Question != "why?" {
		t.Fatalf("expected question, got %q", rejected.Question)
	}
	if len(turns) != 1 || turns[0] != "hello" {
		encoded, _ := json.Marshal(turns)
		t.Fatalf("unexpected turns: %s", encoded)
	}
}

func TestClientReconnectReportsSecondConnectionClose(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		readAuth(t, conn)
		sendAuthOK(t, conn)
		// Wait for the next frame (or client close), then drop the socket.
		var ignored map[string]any
		_ = conn.ReadJSON(&ignored)
		_ = conn.Close()
	}))
	defer server.Close()

	client, err := NewClient("key-1", WithURL("ws"+server.URL[len("http"):]+"/sdk"))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()

	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.CreateCall(context.Background(), "+821012345678", "", nil, ""); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = client.WaitClosed(ctx)
	var closed *ConnectionClosedError
	if !errors.As(err, &closed) {
		t.Fatalf("expected ConnectionClosedError, got %T: %v", err, err)
	}
}

func TestClientIgnoresStaleCloseFromPreviousConnection(t *testing.T) {
	upgrader := websocket.Upgrader{}
	firstReady := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		readAuth(t, conn)
		select {
		case firstReady <- conn:
			sendAuthOK(t, conn)
			return
		default:
		}
		defer conn.Close()
		// Drop the first connection while the second one is in use, then
		// finish the second connection's call only after it was created.
		first := <-firstReady
		_ = first.Close()
		sendAuthOK(t, conn)
		readCommand(t, conn, "createCall")
		sendEvent(t, conn, "call.completed")
		drain(conn)
	}))
	defer server.Close()

	client, err := NewClient("key-1", WithURL("ws"+server.URL[len("http"):]+"/sdk"))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.CreateCall(context.Background(), "+821012345678", "", nil, ""); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.WaitClosed(ctx); err != nil {
		t.Fatalf("expected second call completion, got %T: %v", err, err)
	}
}

func TestClientReturnsTypedErrorForCommandAfterClose(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		readAuth(t, conn)
		sendAuthOK(t, conn)
		// The client reads auth.ok before it can observe this close.
		_ = conn.Close()
	}))
	defer server.Close()

	client, err := NewClient("key-1", WithURL("ws"+server.URL[len("http"):]+"/sdk"))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.WaitClosed(ctx); err != nil {
		t.Fatal(err)
	}

	err = client.Answer(context.Background(), "hello", "", "")
	var closed *ConnectionClosedError
	if !errors.As(err, &closed) {
		t.Fatalf("expected ConnectionClosedError, got %T: %v", err, err)
	}
}

func TestClientSerializesConcurrentCommandWrites(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		readAuth(t, conn)
		sendAuthOK(t, conn)
		for {
			var ignored map[string]any
			if err := conn.ReadJSON(&ignored); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	client, err := NewClient("key-1", WithURL("ws"+server.URL[len("http"):]+"/sdk"))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			errs <- client.Answer(context.Background(), "hello", "", "")
		}()
		go func() {
			defer wg.Done()
			errs <- client.Cancel(context.Background())
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// readCommand reads the next command frame, fails the test unless it is the
// expected event, and returns its requestId the way the gateway echoes it.
func readCommand(t *testing.T, conn *websocket.Conn, event string) string {
	t.Helper()
	var frame map[string]any
	if err := conn.ReadJSON(&frame); err != nil {
		t.Errorf("reading %s frame: %v", event, err)
		return ""
	}
	if frame["event"] != event {
		t.Errorf("expected %s frame, got %v", event, frame["event"])
	}
	data, _ := frame["data"].(map[string]any)
	requestID, _ := data["requestId"].(string)
	return requestID
}

// errorFrame builds a gateway error frame whose message is its code. An empty
// requestID omits the field.
func sendError(t *testing.T, conn *websocket.Conn, code, requestID string) {
	t.Helper()
	frame := map[string]any{"type": "error", "version": "1.0", "code": code, "message": code}
	if requestID != "" {
		frame["requestId"] = requestID
	}
	if err := conn.WriteJSON(frame); err != nil {
		t.Errorf("writing %s error: %v", code, err)
	}
}

func sendEvent(t *testing.T, conn *websocket.Conn, eventType string) {
	t.Helper()
	if err := conn.WriteJSON(map[string]any{
		"type":      eventType,
		"version":   "1.0",
		"callId":    "c1",
		"sessionId": "s1",
		"status":    "completed",
		"timestamp": "t",
	}); err != nil {
		t.Errorf("writing %s: %v", eventType, err)
	}
}

// drain keeps reading until the client closes the socket.
func drain(conn *websocket.Conn) {
	for {
		var ignored map[string]any
		if err := conn.ReadJSON(&ignored); err != nil {
			return
		}
	}
}

func TestClientWaitClosedIgnoresErrorsOfOtherCommands(t *testing.T) {
	upgrader := websocket.Upgrader{}
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		readAuth(t, conn)
		sendAuthOK(t, conn)
		readCommand(t, conn, "createCall")
		sendEvent(t, conn, "call.created")
		// A failed sendDtmf does not end the call (sdk-ws.v1 section 6); the
		// gateway echoes the sendDtmf requestId on its error.
		sendError(t, conn, "dtmfDigitsInvalid", readCommand(t, conn, "sendDtmf"))
		sendError(t, conn, "internalError", "")
		<-release
		sendEvent(t, conn, "call.completed")
		drain(conn)
	}))
	defer server.Close()

	client, err := NewClient("key-1", WithURL("ws"+server.URL[len("http"):]+"/sdk"))
	if err != nil {
		t.Fatal(err)
	}
	errorCodes := make(chan string, 2)
	client.On(EventTypeError, func(_ context.Context, event Event) error {
		errorCodes <- event.Code
		return nil
	})
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.CreateCall(context.Background(), "+821012345678", "", nil, "r-call"); err != nil {
		t.Fatal(err)
	}
	if err := client.SendDtmf(context.Background(), "12#x", "", "r-dtmf"); err != nil {
		t.Fatal(err)
	}

	// The client decides whether an error ends the wait before it emits the
	// error, so once both reached the handler both have been judged.
	for _, want := range []string{"dtmfDigitsInvalid", "internalError"} {
		select {
		case got := <-errorCodes:
			if got != want {
				t.Fatalf("expected %s delivered to error handlers, got %s", want, got)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s on error handlers", want)
		}
	}

	pending, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := client.WaitClosed(pending); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected WaitClosed to keep waiting while the call is live, got %T: %v", err, err)
	}

	close(release)
	done, cancelDone := context.WithTimeout(context.Background(), time.Second)
	defer cancelDone()
	if err := client.WaitClosed(done); err != nil {
		t.Fatalf("expected WaitClosed to end cleanly on call.completed, got %T: %v", err, err)
	}
}

func TestClientWaitClosedReturnsCreateCallErrors(t *testing.T) {
	tests := []struct {
		name        string
		callCreated bool
		code        string
		// codeOf returns the Code of the expected error type, or "" when err
		// is not of that type.
		codeOf func(error) string
	}{
		{
			name: "refusal before call.created",
			code: "insufficientCredit",
			codeOf: func(err error) string {
				var target *CallRefusedError
				if !errors.As(err, &target) {
					return ""
				}
				return target.Code
			},
		},
		{
			name:        "stream failure after call.created",
			callCreated: true,
			code:        "internalError",
			codeOf: func(err error) string {
				var target *TelloServerError
				if !errors.As(err, &target) {
					return ""
				}
				return target.Code
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				readAuth(t, conn)
				sendAuthOK(t, conn)
				requestID := readCommand(t, conn, "createCall")
				if tt.callCreated {
					sendEvent(t, conn, "call.created")
				}
				sendError(t, conn, tt.code, requestID)
				drain(conn)
			}))
			defer server.Close()

			client, err := NewClient("key-1", WithURL("ws"+server.URL[len("http"):]+"/sdk"))
			if err != nil {
				t.Fatal(err)
			}
			if err := client.Connect(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if err := client.CreateCall(context.Background(), "+821012345678", "", nil, ""); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err = client.WaitClosed(ctx)
			if tt.codeOf(err) != tt.code {
				t.Fatalf("expected %s to end the wait with its mapped error, got %T: %v", tt.code, err, err)
			}
		})
	}
}

func TestClientCreateCallAlwaysSendsRequestID(t *testing.T) {
	tests := []struct {
		name      string
		requestID string
	}{
		{name: "generated when omitted"},
		{name: "caller value unchanged", requestID: "r-given"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upgrader := websocket.Upgrader{}
			sent := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				readAuth(t, conn)
				sendAuthOK(t, conn)
				sent <- readCommand(t, conn, "createCall")
				drain(conn)
			}))
			defer server.Close()

			client, err := NewClient("key-1", WithURL("ws"+server.URL[len("http"):]+"/sdk"))
			if err != nil {
				t.Fatal(err)
			}
			if err := client.Connect(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if err := client.CreateCall(context.Background(), "+821012345678", "", nil, tt.requestID); err != nil {
				t.Fatal(err)
			}

			select {
			case got := <-sent:
				if tt.requestID == "" && got == "" {
					t.Fatal("expected a generated requestId on createCall, got none")
				}
				if tt.requestID != "" && got != tt.requestID {
					t.Fatalf("expected requestId %q unchanged, got %q", tt.requestID, got)
				}
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for createCall frame")
			}
		})
	}
}
