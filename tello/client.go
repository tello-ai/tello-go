package tello

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	closeUnauthenticated = 4401
	closeSessionReplaced = 4429
)

type Client struct {
	*EventEmitter
	config        Config
	conn          *websocket.Conn
	mu            sync.Mutex
	writeMu       sync.Mutex
	closed        chan struct{}
	authDone      chan struct{}
	closeErr      error
	authenticated bool
	connGen       int
	// call is the generation WaitClosed attaches to: the live call while
	// active, otherwise the last call of this connection (or an unstarted
	// placeholder). Each CreateCall that finds no live call replaces it.
	call   *callGeneration
	active bool
	// callRequestIDs holds the requestIds of the createCall commands sent
	// during the current call; openingRequestID is the one that started it.
	callRequestIDs   map[string]struct{}
	openingRequestID string
}

// callGeneration is one call from the SDK's view. done closes when it ends,
// after err records the outcome (nil for a terminal event).
type callGeneration struct {
	done chan struct{}
	err  error
	// surfaced is set once a WaitClosed has returned err, so a wait started
	// after the call ended reports it only once.
	surfaced bool
}

func newCallGeneration() *callGeneration {
	return &callGeneration{done: make(chan struct{})}
}

func NewClient(apiKey string, options ...Option) (*Client, error) {
	config, err := resolveConfig(apiKey, options...)
	if err != nil {
		return nil, err
	}
	return &Client{
		EventEmitter: NewEventEmitter(),
		config:       config,
		call:         newCallGeneration(),
		closed:       make(chan struct{}),
	}, nil
}

func (c *Client) Connect(ctx context.Context) error {
	dialer := websocket.Dialer{HandshakeTimeout: c.config.OpenTimeout}
	// No Authorization header and no query-string token: the API key is
	// authenticated from the first application frame instead (see authenticate).
	dialURL, err := withClientIdentity(c.config.URL)
	if err != nil {
		return err
	}
	conn, _, err := dialer.DialContext(ctx, dialURL, nil)
	if err != nil {
		return err
	}
	c.mu.Lock()
	// A call still live on a replaced connection can no longer end there.
	c.endCallLocked(&ConnectionClosedError{TelloError{Message: "connection replaced before call terminated"}})
	c.conn = conn
	c.connGen++
	gen := c.connGen
	c.call = newCallGeneration()
	c.callRequestIDs = nil
	c.openingRequestID = ""
	c.closed = make(chan struct{})
	c.authDone = make(chan struct{})
	c.closeErr = nil
	c.authenticated = false
	closed := c.closed
	authDone := c.authDone
	c.mu.Unlock()
	go c.recvLoop(gen, conn)

	if err := c.authenticate(ctx, conn, closed, authDone); err != nil {
		_ = conn.Close()
		return err
	}
	return nil
}

// authenticate sends the mandatory first application frame and blocks until the
// server confirms with auth.ok. It never returns until authentication resolves,
// so no other command can be sent before the socket is authenticated. The API
// key is only ever placed in the frame payload, never in logs or errors.
func (c *Client) authenticate(ctx context.Context, conn *websocket.Conn, closed, authDone chan struct{}) error {
	c.writeMu.Lock()
	writeErr := conn.WriteJSON(AuthFrame(c.config.APIKey, ""))
	c.writeMu.Unlock()
	if writeErr != nil {
		return &ConnectionClosedError{TelloError{Message: "failed to send authentication frame"}}
	}

	timeout := c.config.OpenTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-authDone:
	case <-closed:
	case <-timer.C:
	case <-ctx.Done():
	}

	c.mu.Lock()
	authenticated := c.authenticated
	authErr := c.closeErr
	c.mu.Unlock()

	if authenticated {
		return nil
	}
	if authErr != nil {
		return authErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// A missing auth.ok is an authentication failure, not a transport one: the
	// gateway closes with 4401 on its own 10s deadline either way
	// (docs/protocol/sdk-ws.v1.md section 2).
	return &AuthenticationError{TelloError{Message: "timed out waiting for authentication"}}
}

func (c *Client) Close() error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return nil
	}
	return conn.Close()
}

// WaitClosed blocks until the call reaches a terminal event or the connection
// closes, and returns the error that ended it.
//
// A wait started during a call returns when that call ends, with its
// outcome, even if an event handler starts a follow-up call meanwhile; a
// wait started afterwards waits for the follow-up. A wait started when no
// call is live returns the last call's outcome once (nil afterwards), or,
// before any call on this connection, waits for the connection to close.
//
// A gateway error frame ends the call only when it answers one of the call's
// CreateCall commands (its requestId matches) and is neither noActiveCall nor
// a callAlreadyActive refusing a CreateCall sent during the live call. A
// callAlreadyActive answering the CreateCall that started the call does end
// it: the gateway was still finishing the previous call (sdk-ws.v1 section
// 4.1). Errors of other commands (answer, sendDtmf, getSummary, cancel) do not
// end the call per sdk-ws.v1 section 6 and are delivered only to
// EventTypeError handlers.
func (c *Client) WaitClosed(ctx context.Context) error {
	c.mu.Lock()
	call := c.call
	live := c.active
	closed := c.closed
	c.mu.Unlock()

	select {
	case <-call.done:
	case <-closed:
	case <-ctx.Done():
		return ctx.Err()
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if live {
		// A live call always ends before its connection's closed channel
		// closes, so call.err is final here.
		call.surfaced = true
		return call.err
	}
	if c.closeErr != nil {
		return c.closeErr
	}
	select {
	case <-call.done:
	default:
		return nil
	}
	if call.surfaced {
		return nil
	}
	call.surfaced = true
	return call.err
}

// CreateCall starts a call. The frame always carries a requestId: requestID
// when non-empty, otherwise a generated UUID, so the client can tell the
// gateway's answer to this command apart from errors of other commands.
//
// Sent during a live call, CreateCall only records its requestId against that
// call; the gateway refuses it with callAlreadyActive and the call continues.
func (c *Client) CreateCall(ctx context.Context, to, prompt string, metadata map[string]any, requestID string) error {
	if requestID == "" {
		generated, err := newRequestID()
		if err != nil {
			return err
		}
		requestID = generated
	}
	c.mu.Lock()
	var opened *callGeneration
	if c.active {
		c.callRequestIDs[requestID] = struct{}{}
	} else {
		opened = newCallGeneration()
		c.call = opened
		c.callRequestIDs = map[string]struct{}{requestID: {}}
		c.openingRequestID = requestID
		c.active = true
	}
	c.mu.Unlock()

	err := c.send(ctx, CreateCallFrame(to, prompt, metadata, requestID))
	if err != nil && opened != nil {
		c.mu.Lock()
		if c.call == opened {
			c.endCallLocked(err)
		}
		c.mu.Unlock()
	}
	return err
}

// newRequestID returns a random RFC 4122 version 4 UUID string.
func newRequestID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate createCall requestId: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func (c *Client) Answer(ctx context.Context, text, messageID, requestID string) error {
	return c.send(ctx, AnswerFrame(text, messageID, requestID))
}

func (c *Client) SendDtmf(ctx context.Context, digits, messageID, requestID string) error {
	return c.send(ctx, SendDtmfFrame(digits, messageID, requestID))
}

func (c *Client) Cancel(ctx context.Context) error {
	return c.send(ctx, CancelFrame())
}

func (c *Client) GetSummary(ctx context.Context, callID, requestID string) error {
	return c.send(ctx, GetSummaryFrame(callID, requestID))
}

func (c *Client) send(_ context.Context, frame CommandFrame) error {
	c.mu.Lock()
	conn := c.conn
	closeErr := c.closeErr
	c.mu.Unlock()
	if closeErr != nil {
		return closeErr
	}
	if conn == nil {
		return &ConnectionClosedError{TelloError{Message: "client is not connected"}}
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := conn.WriteJSON(frame); err != nil {
		return &ConnectionClosedError{TelloError{Message: err.Error()}}
	}
	return nil
}

func (c *Client) recvLoop(gen int, conn *websocket.Conn) {
	for {
		var frame map[string]any
		if err := conn.ReadJSON(&frame); err != nil {
			c.noteClose(gen, err)
			c.finish(gen)
			return
		}
		c.dispatch(gen, frame)
	}
}

func (c *Client) dispatch(gen int, frame map[string]any) {
	event := ParseEvent(frame)
	c.mu.Lock()
	if gen != c.connGen {
		c.mu.Unlock()
		return
	}
	if event.Type == EventTypeAuthOK {
		c.authenticated = true
		closeIfOpen(c.authDone)
		c.mu.Unlock()
		return
	}
	// The call ends before the event reaches handlers, so a handler that
	// starts a follow-up call starts a new generation.
	if event.Type == EventTypeError {
		err := ErrorFor(event.Code, event.Message, event.Question)
		if event.Code == "unauthenticated" {
			c.closeErr = err
			closeIfOpen(c.authDone)
		} else if c.endsCallLocked(event) {
			c.endCallLocked(err)
		}
	} else if IsTerminal(event) {
		c.endCallLocked(nil)
	}
	c.mu.Unlock()
	_ = c.Emit(context.Background(), event.Type, event)
}

// endsCallLocked reports whether error event ends the live call: only an
// answer to one of its createCall commands does (sdk-ws.v1 section 6), and a
// callAlreadyActive only when it refuses the createCall that started the call.
func (c *Client) endsCallLocked(event Event) bool {
	if !c.active || event.Code == "noActiveCall" {
		return false
	}
	if _, ok := c.callRequestIDs[event.RequestID]; !ok {
		return false
	}
	return event.Code != "callAlreadyActive" || event.RequestID == c.openingRequestID
}

// endCallLocked ends the live call, if any, with outcome err and releases
// every wait attached to it.
func (c *Client) endCallLocked(err error) {
	if !c.active {
		return
	}
	c.active = false
	c.call.err = err
	close(c.call.done)
}

func (c *Client) noteClose(gen int, err error) {
	var closeErr *websocket.CloseError
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.connGen {
		return
	}
	if c.closeErr != nil {
		return
	}
	if errors.As(err, &closeErr) {
		switch closeErr.Code {
		case closeUnauthenticated:
			c.closeErr = &AuthenticationError{TelloError{Code: "unauthenticated", Message: closeErr.Text}}
		case closeSessionReplaced:
			c.closeErr = &SessionReplacedError{TelloError{Message: closeErr.Text}}
		}
	}
	if c.closeErr == nil && c.active {
		c.closeErr = &ConnectionClosedError{TelloError{Message: "connection closed before call terminated"}}
	}
}

func (c *Client) finish(gen int) {
	c.mu.Lock()
	if gen != c.connGen {
		c.mu.Unlock()
		return
	}
	c.endCallLocked(c.closeErr)
	c.conn = nil
	closeIfOpen(c.closed)
	c.mu.Unlock()
	_ = c.Emit(context.Background(), EventTypeDisconnected, Event{Type: EventTypeDisconnected, Raw: map[string]any{}})
}

func closeIfOpen(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

// withClientIdentity tags the upgrade URL with sdk, version and protocol so
// the gateway can log which client connected. User query pairs are kept
// byte-for-byte; only empty pieces and pairs whose form-decoded key is one of
// the three identity keys are dropped before the identity pairs are appended.
func withClientIdentity(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	var kept []string
	for _, piece := range strings.Split(u.RawQuery, "&") {
		if piece == "" {
			continue
		}
		key, _, _ := strings.Cut(piece, "=")
		if decoded, err := url.QueryUnescape(key); err == nil {
			key = decoded
		}
		if key == "sdk" || key == "version" || key == "protocol" {
			continue
		}
		kept = append(kept, piece)
	}
	kept = append(kept,
		"sdk=go",
		"version="+url.QueryEscape(Version),
		"protocol="+url.QueryEscape(ProtocolVersion),
	)
	u.RawQuery = strings.Join(kept, "&")
	return u.String(), nil
}
