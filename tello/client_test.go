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
		var ignored map[string]any
		if err := conn.ReadJSON(&ignored); err != nil {
			t.Error(err)
			return
		}
		_ = conn.WriteJSON(map[string]any{
			"type":      "user.turn",
			"version":   "1.0",
			"callId":    "c1",
			"turnIndex": 1,
			"text":      "hello",
			"timestamp": "t",
		})
		_ = conn.WriteJSON(map[string]any{
			"type":     "error",
			"version":  "1.0",
			"code":     "callRejected",
			"message":  "Call rejected",
			"question": "why?",
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

	err = client.WaitClosed(context.Background())
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
		sendAuthOK(t, conn)
		select {
		case firstReady <- conn:
			return
		default:
		}
		first := <-firstReady
		_ = first.Close()
		time.Sleep(20 * time.Millisecond)
		_ = conn.WriteJSON(map[string]any{
			"type":      "call.completed",
			"version":   "1.0",
			"callId":    "c1",
			"status":    "completed",
			"timestamp": "t",
		})
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
	if err := client.CreateCall(context.Background(), "+821012345678", "", nil, ""); err != nil {
		t.Fatal(err)
	}

	if err := client.WaitClosed(context.Background()); err != nil {
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
		// Give the client a moment to observe auth.ok before closing.
		time.Sleep(20 * time.Millisecond)
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
	if err := client.WaitClosed(context.Background()); err != nil {
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
