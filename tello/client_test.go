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

func TestNewClientResolvesURL(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		options []Option
		want    string
	}{
		{name: "production gateway by default", want: "wss://api.telloai.io/sdk"},
		{name: "TELLO_URL overrides the default", env: "ws://env.example/sdk", want: "ws://env.example/sdk"},
		{
			name:    "WithURL overrides TELLO_URL",
			env:     "ws://env.example/sdk",
			options: []Option{WithURL("ws://option.example/sdk")},
			want:    "ws://option.example/sdk",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvURL, tt.env)
			client, err := NewClient("key-1", tt.options...)
			if err != nil {
				t.Fatal(err)
			}
			if client.config.URL != tt.want {
				t.Fatalf("URL = %q, want %q", client.config.URL, tt.want)
			}
		})
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
func errorFrame(code, requestID string) map[string]any {
	frame := map[string]any{"type": "error", "version": "1.0", "code": code, "message": code}
	if requestID != "" {
		frame["requestId"] = requestID
	}
	return frame
}

func eventFrame(eventType string) map[string]any {
	return map[string]any{
		"type":      eventType,
		"version":   "1.0",
		"callId":    "c1",
		"sessionId": "s1",
		"status":    "completed",
		"timestamp": "t",
	}
}

func sendError(t *testing.T, conn *websocket.Conn, code, requestID string) {
	t.Helper()
	if err := conn.WriteJSON(errorFrame(code, requestID)); err != nil {
		t.Errorf("writing %s error: %v", code, err)
	}
}

func sendEvent(t *testing.T, conn *websocket.Conn, eventType string) {
	t.Helper()
	if err := conn.WriteJSON(eventFrame(eventType)); err != nil {
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

// fakeGateway is a scripted /sdk gateway. It authenticates each connection
// and hands it to the test, which reads the client's commands and writes the
// frames a scenario needs, in order. Like the real gateway it answers cancel
// with call.statusChanged status cancelled, the call's terminal (sdk-ws.v1
// section 4.4).
type fakeGateway struct {
	url   string
	conns chan *gatewayConn
}

type gatewayConn struct {
	conn     *websocket.Conn
	writeMu  sync.Mutex
	commands chan map[string]any
}

func newFakeGateway(t *testing.T) *fakeGateway {
	t.Helper()
	gw := &fakeGateway{conns: make(chan *gatewayConn, 4)}
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		readAuth(t, conn)
		gc := &gatewayConn{conn: conn, commands: make(chan map[string]any, 16)}
		if err := gc.write(map[string]any{"type": "auth.ok", "version": "1.0", "accountId": "acc-1"}); err != nil {
			return
		}
		gw.conns <- gc
		for {
			var frame map[string]any
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			if frame["event"] == "cancel" {
				_ = gc.write(map[string]any{
					"type":           "call.statusChanged",
					"version":        "1.0",
					"callId":         "c1",
					"status":         "cancelled",
					"previousStatus": "inProgress",
					"timestamp":      "t",
				})
				continue
			}
			gc.commands <- frame
		}
	}))
	t.Cleanup(server.Close)
	gw.url = "ws" + server.URL[len("http"):] + "/sdk"
	return gw
}

// newClient returns a client for this gateway that is closed when the test
// ends.
func (gw *fakeGateway) newClient(t *testing.T) *Client {
	t.Helper()
	client, err := NewClient("key-1", WithURL(gw.url))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// connect connects client and returns the gateway side of that connection.
func (gw *fakeGateway) connect(t *testing.T, client *Client) *gatewayConn {
	t.Helper()
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case gc := <-gw.conns:
		return gc
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the gateway connection")
		return nil
	}
}

func (gc *gatewayConn) write(frame map[string]any) error {
	gc.writeMu.Lock()
	defer gc.writeMu.Unlock()
	return gc.conn.WriteJSON(frame)
}

func (gc *gatewayConn) send(t *testing.T, frame map[string]any) {
	t.Helper()
	if err := gc.write(frame); err != nil {
		t.Fatalf("gateway writing %v: %v", frame["type"], err)
	}
}

// next reads the client's next command, fails the test unless it is event,
// and returns its requestId.
func (gc *gatewayConn) next(t *testing.T, event string) string {
	t.Helper()
	select {
	case frame := <-gc.commands:
		if frame["event"] != event {
			t.Fatalf("expected %s command, got %v", event, frame["event"])
		}
		data, _ := frame["data"].(map[string]any)
		requestID, _ := data["requestId"].(string)
		return requestID
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s command", event)
		return ""
	}
}

// drop closes the socket without a close frame, like a network failure.
func (gc *gatewayConn) drop() {
	_ = gc.conn.Close()
}

// waitCtx closes entered the first time WaitClosed selects on it. WaitClosed
// attaches to the current call before it selects, so once entered is closed
// the wait belongs to the call that was current at that point.
type waitCtx struct {
	context.Context
	once    sync.Once
	entered chan struct{}
}

func (w *waitCtx) Done() <-chan struct{} {
	w.once.Do(func() { close(w.entered) })
	return w.Context.Done()
}

// startWait runs WaitClosed in the background, bounded at two seconds, and
// returns once the wait is attached to the current call.
func startWait(t *testing.T, client *Client) <-chan error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	wctx := &waitCtx{Context: ctx, entered: make(chan struct{})}
	result := make(chan error, 1)
	go func() { result <- client.WaitClosed(wctx) }()
	select {
	case <-wctx.entered:
	case <-ctx.Done():
		t.Fatal("WaitClosed never started waiting")
	}
	return result
}

func createCall(t *testing.T, client *Client, requestID string) {
	t.Helper()
	if err := client.CreateCall(context.Background(), "+821012345678", "", nil, requestID); err != nil {
		t.Fatal(err)
	}
}

func TestClientWaitClosedEndsOnCallAlreadyActiveForOpeningCreateCall(t *testing.T) {
	gw := newFakeGateway(t)
	client := gw.newClient(t)
	gc := gw.connect(t, client)

	createCall(t, client, "r-a")
	gc.next(t, "createCall")
	w1 := startWait(t, client)
	// The gateway still holds the previous call during its cleanup window
	// (sdk-ws.v1 section 4.1): this call never starts, no call.created.
	gc.send(t, errorFrame("callAlreadyActive", "r-a"))
	var active *CallAlreadyActiveError
	if err := <-w1; !errors.As(err, &active) || active.Code != "callAlreadyActive" {
		t.Fatalf("expected the refused opening createCall to end the wait with CallAlreadyActiveError, got %T: %v", err, err)
	}

	createCall(t, client, "r-b")
	gc.next(t, "createCall")
	w2 := startWait(t, client)
	gc.send(t, eventFrame("call.completed"))
	if err := <-w2; err != nil {
		t.Fatalf("expected the retried call's completion to end the wait cleanly, got %T: %v", err, err)
	}
}

func TestClientWaitClosedSurvivesCreateCallRefusedDuringLiveCall(t *testing.T) {
	gw := newFakeGateway(t)
	client := gw.newClient(t)
	gc := gw.connect(t, client)

	createCall(t, client, "r-a")
	gc.next(t, "createCall")
	gc.send(t, eventFrame("call.created"))
	w1 := startWait(t, client)

	createCall(t, client, "r-b")
	gc.next(t, "createCall")
	// Refusing a createCall sent during a live call leaves that call running.
	gc.send(t, errorFrame("callAlreadyActive", "r-b"))
	gc.send(t, errorFrame("internalError", "r-a"))
	var server *TelloServerError
	if err := <-w1; !errors.As(err, &server) || server.Code != "internalError" {
		t.Fatalf("expected the live call's stream failure to end the wait, got %T: %v", err, err)
	}
}

func TestClientWaitClosedReturnsWhenTerminalHandlerStartsFollowUpCall(t *testing.T) {
	gw := newFakeGateway(t)
	client := gw.newClient(t)
	client.On(EventTypeCallCompleted, func(ctx context.Context, _ Event) error {
		return client.CreateCall(ctx, "+821012345678", "", nil, "r-b")
	})
	gc := gw.connect(t, client)

	createCall(t, client, "r-a")
	gc.next(t, "createCall")
	gc.send(t, eventFrame("call.created"))
	w1 := startWait(t, client)
	gc.send(t, eventFrame("call.completed"))
	if err := <-w1; err != nil {
		t.Fatalf("expected the first call's completion to end its wait, got %T: %v", err, err)
	}

	if got := gc.next(t, "createCall"); got != "r-b" {
		t.Fatalf("expected the handler's follow-up createCall, got requestId %q", got)
	}
	w2 := startWait(t, client)
	gc.send(t, errorFrame("internalError", "r-a"))
	if err := client.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-w2; err != nil {
		t.Fatalf("expected only the follow-up call's cancelled terminal to end its wait, got %T: %v", err, err)
	}
}

func TestClientWaitClosedIgnoresErrorsForCallOfPreviousConnection(t *testing.T) {
	gw := newFakeGateway(t)
	client := gw.newClient(t)
	gc := gw.connect(t, client)

	createCall(t, client, "r-a")
	gc.next(t, "createCall")
	gc.send(t, eventFrame("call.created"))
	w1 := startWait(t, client)
	gc.drop()
	var closed *ConnectionClosedError
	if err := <-w1; !errors.As(err, &closed) {
		t.Fatalf("expected the mid-call drop to end the wait with ConnectionClosedError, got %T: %v", err, err)
	}

	gc = gw.connect(t, client)
	createCall(t, client, "r-c")
	gc.next(t, "createCall")
	w2 := startWait(t, client)
	gc.send(t, errorFrame("internalError", "r-a"))
	if err := client.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-w2; err != nil {
		t.Fatalf("expected only the new call's cancelled terminal to end its wait, got %T: %v", err, err)
	}
}

func TestClientCreateCallSendFailureEndsTheCall(t *testing.T) {
	tests := []struct {
		name   string
		client func(t *testing.T) *Client
	}{
		{
			name: "never connected",
			client: func(t *testing.T) *Client {
				client, err := NewClient("key-1", WithURL("ws://127.0.0.1:1/sdk"))
				if err != nil {
					t.Fatal(err)
				}
				return client
			},
		},
		{
			name: "socket closed by the gateway",
			client: func(t *testing.T) *Client {
				gw := newFakeGateway(t)
				client := gw.newClient(t)
				disconnected := make(chan struct{})
				client.On(EventTypeDisconnected, func(context.Context, Event) error {
					close(disconnected)
					return nil
				})
				gw.connect(t, client).drop()
				select {
				case <-disconnected:
				case <-time.After(time.Second):
					t.Fatal("timed out waiting for the client to see the close")
				}
				return client
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := tt.client(t)
			var closed *ConnectionClosedError
			if err := client.CreateCall(context.Background(), "+821012345678", "", nil, "r-a"); !errors.As(err, &closed) {
				t.Fatalf("expected CreateCall to fail with ConnectionClosedError, got %T: %v", err, err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := client.WaitClosed(ctx); !errors.As(err, &closed) {
				t.Fatalf("expected the wait to return the send failure, got %T: %v", err, err)
			}
		})
	}
}
