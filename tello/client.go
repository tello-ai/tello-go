package tello

import (
	"context"
	"errors"
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
	callDone      chan struct{}
	closed        chan struct{}
	authDone      chan struct{}
	closeErr      error
	callErr       error
	active        bool
	authenticated bool
	callGen       int
	connGen       int
}

func NewClient(apiKey string, options ...Option) (*Client, error) {
	config, err := resolveConfig(apiKey, options...)
	if err != nil {
		return nil, err
	}
	return &Client{
		EventEmitter: NewEventEmitter(),
		config:       config,
		callDone:     make(chan struct{}),
		closed:       make(chan struct{}),
	}, nil
}

func (c *Client) Connect(ctx context.Context) error {
	dialer := websocket.Dialer{HandshakeTimeout: c.config.OpenTimeout}
	// No Authorization header and no query-string token: the API key is
	// authenticated from the first application frame instead (see authenticate).
	conn, _, err := dialer.DialContext(ctx, c.config.URL, nil)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.conn = conn
	c.connGen++
	gen := c.connGen
	c.callDone = make(chan struct{})
	c.closed = make(chan struct{})
	c.authDone = make(chan struct{})
	c.closeErr = nil
	c.callErr = nil
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

func (c *Client) WaitClosed(ctx context.Context) error {
	c.mu.Lock()
	callDone := c.callDone
	closed := c.closed
	c.mu.Unlock()

	select {
	case <-callDone:
	case <-closed:
	case <-ctx.Done():
		return ctx.Err()
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closeErr != nil {
		return c.closeErr
	}
	if c.callErr != nil {
		err := c.callErr
		c.callErr = nil
		return err
	}
	return nil
}

func (c *Client) CreateCall(ctx context.Context, to, prompt string, metadata map[string]any, requestID string) error {
	c.mu.Lock()
	c.callGen++
	c.callDone = make(chan struct{})
	c.callErr = nil
	c.active = true
	c.mu.Unlock()
	return c.send(ctx, CreateCallFrame(to, prompt, metadata, requestID))
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
	c.mu.Lock()
	if gen != c.connGen {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	event := ParseEvent(frame)
	if event.Type == EventTypeAuthOK {
		c.mu.Lock()
		if gen == c.connGen {
			c.authenticated = true
			closeIfOpen(c.authDone)
		}
		c.mu.Unlock()
		return
	}
	if event.Type == EventTypeError {
		err := ErrorFor(event.Code, event.Message, event.Question)
		c.mu.Lock()
		if event.Code == "unauthenticated" {
			c.closeErr = err
			closeIfOpen(c.authDone)
		} else if c.active && event.Code != "noActiveCall" && event.Code != "callAlreadyActive" {
			c.callErr = err
			c.active = false
			closeIfOpen(c.callDone)
		}
		c.mu.Unlock()
		_ = c.Emit(context.Background(), EventTypeError, event)
		return
	}

	c.mu.Lock()
	callGen := c.callGen
	c.mu.Unlock()
	_ = c.Emit(context.Background(), event.Type, event)
	c.mu.Lock()
	if IsTerminal(event) && c.callGen == callGen {
		c.active = false
		closeIfOpen(c.callDone)
	}
	c.mu.Unlock()
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
	c.active = false
	c.conn = nil
	closeIfOpen(c.closed)
	closeIfOpen(c.callDone)
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
