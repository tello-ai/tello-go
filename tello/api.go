package tello

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultAPIURL is the production REST API base URL.
	DefaultAPIURL = "https://api.telloai.io/v1"
	// EnvAPIURL names the environment variable NewAPI reads the base URL
	// from when WithAPIURL is not given.
	EnvAPIURL = "TELLO_API_URL"
)

const (
	defaultAPITimeout     = 30 * time.Second
	defaultAPILongTimeout = 120 * time.Second
	// maxAPIResponseBody caps how much of a response is read.
	maxAPIResponseBody = 8 << 20
)

// Object is a JSON object of the REST API. Responses are decoded with
// json.Decoder.UseNumber, so numbers arrive as json.Number and fields this
// SDK does not know yet are kept.
type Object = map[string]any

// API is the client of the Tello public REST API (/v1): tools, agents and
// crews, crew generation, and call drafts. It is separate from the WebSocket
// Client and safe for concurrent use.
//
// It never retries a request. Creates, updates and deletes are saved,
// deployed and live for the next call as soon as they succeed.
type API struct {
	Tools  *Tools
	Agents *Agents
	Crews  *Crews
	// CrewGenerations reads the jobs Crews.Generate starts.
	CrewGenerations *CrewGenerations
	// CallDrafts reviews a call scenario before dialing; Confirm places a
	// real, billed call.
	CallDrafts *CallDrafts

	t *apiTransport
}

// APIOption configures NewAPI.
type APIOption func(*apiConfig)

type apiConfig struct {
	url         string
	timeout     time.Duration
	longTimeout time.Duration
}

// WithAPIURL sets the REST API base URL, for example
// "https://api.telloai.io/v1". It must be https; http is accepted only for
// 127.0.0.1, localhost and ::1.
func WithAPIURL(url string) APIOption {
	return func(config *apiConfig) {
		config.url = url
	}
}

// WithAPITimeout sets how long a request may take, response included
// (default 30s).
func WithAPITimeout(timeout time.Duration) APIOption {
	return func(config *apiConfig) {
		config.timeout = timeout
	}
}

// WithAPILongTimeout sets the timeout of CallDrafts.Create, CallDrafts.Revise,
// CallDrafts.Confirm and Crews.Generate, which the server may take long to
// answer (default 120s).
func WithAPILongTimeout(timeout time.Duration) APIOption {
	return func(config *apiConfig) {
		config.longTimeout = timeout
	}
}

// NewAPI builds a REST API client. apiKey falls back to TELLO_API_KEY, as for
// NewClient. The base URL is WithAPIURL if given, otherwise TELLO_API_URL,
// otherwise DefaultAPIURL. The key is sent only in the Authorization header,
// never in errors or formatted output.
func NewAPI(apiKey string, opts ...APIOption) (*API, error) {
	apiKey, err := resolveAPIKey(apiKey)
	if err != nil {
		return nil, err
	}
	config := apiConfig{
		url:         getenv(EnvAPIURL, DefaultAPIURL),
		timeout:     defaultAPITimeout,
		longTimeout: defaultAPILongTimeout,
	}
	for _, opt := range opts {
		opt(&config)
	}
	baseURL, err := resolveAPIURL(config.url)
	if err != nil {
		return nil, err
	}
	if config.timeout <= 0 || config.longTimeout <= 0 {
		return nil, errors.New("tello api: timeouts must be positive")
	}
	t := &apiTransport{
		baseURL:     baseURL,
		key:         apiKey,
		timeout:     config.timeout,
		longTimeout: config.longTimeout,
		http: &http.Client{
			// A redirect is answered as an error: following it would turn a
			// POST into a GET or send the key to another host.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	return &API{
		Tools:           &Tools{t},
		Agents:          &Agents{t},
		Crews:           &Crews{t},
		CrewGenerations: &CrewGenerations{t},
		CallDrafts:      &CallDrafts{t},
		t:               t,
	}, nil
}

// resolveAPIURL validates raw and drops its trailing slashes.
func resolveAPIURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("tello api: invalid API URL: %w", err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("tello api: API URL %q has no host", raw)
	}
	switch u.Scheme {
	case "https":
	case "http":
		switch u.Hostname() {
		case "127.0.0.1", "localhost", "::1":
		default:
			return "", fmt.Errorf("tello api: API URL %q must use https (http is for 127.0.0.1, localhost and ::1 only)", raw)
		}
	default:
		return "", fmt.Errorf("tello api: API URL %q must use https", raw)
	}
	return strings.TrimRight(raw, "/"), nil
}

// apiTransport sends REST requests. It holds the key, so it is reached only
// through pointers: formatting an API never prints it.
type apiTransport struct {
	baseURL     string
	key         string
	timeout     time.Duration
	longTimeout time.Duration
	http        *http.Client
}

type apiRequest struct {
	method string
	// path is escaped and relative to the base URL, e.g. "/tools/a%2Fb".
	path  string
	query url.Values
	// body is sent as JSON when non-nil.
	body           any
	idempotencyKey string
	// long selects the long timeout.
	long bool
	// accept lists the success statuses the API documents for the request;
	// another 2xx is an invalid_response.
	accept []int
	// require names response fields that must be non-empty strings;
	// a response without one is an invalid_response.
	require []string
}

// do sends r and returns the response object; a 204 returns nil.
func (t *apiTransport) do(ctx context.Context, r apiRequest) (Object, error) {
	if r.idempotencyKey != "" {
		if err := validateIdempotencyKey(r.idempotencyKey); err != nil {
			return nil, err
		}
	}
	var body io.Reader
	if r.body != nil {
		raw, err := json.Marshal(r.body)
		if err != nil {
			return nil, fmt.Errorf("tello api: encode %s %s body: %w", r.method, r.path, err)
		}
		body = bytes.NewReader(raw)
	}
	timeout := t.timeout
	if r.long {
		timeout = t.longTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	target := t.baseURL + r.path
	if len(r.query) > 0 {
		target += "?" + r.query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, r.method, target, body)
	if err != nil {
		return nil, fmt.Errorf("tello api: build %s %s: %w", r.method, r.path, err)
	}
	req.Header.Set("Authorization", "Bearer "+t.key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "tello-go-sdk/"+Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", r.idempotencyKey)
	}

	resp, err := t.http.Do(req)
	if err != nil {
		return nil, connectionError(r, "request failed", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBody))
	if err != nil {
		return nil, connectionError(r, "response could not be read", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, errorResponse(resp.StatusCode, raw, r.idempotencyKey)
	}
	if !slices.Contains(r.accept, resp.StatusCode) {
		return nil, invalidResponse(resp.StatusCode, r, fmt.Sprintf("unexpected status %d for %s %s", resp.StatusCode, r.method, r.path))
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	var obj Object
	if err := decodeJSON(raw, &obj); err != nil || obj == nil {
		return nil, invalidResponse(resp.StatusCode, r, fmt.Sprintf("response to %s %s is not a JSON object", r.method, r.path))
	}
	for _, field := range r.require {
		if value, _ := obj[field].(string); value == "" {
			return nil, invalidResponse(resp.StatusCode, r, fmt.Sprintf("response to %s %s has no %s", r.method, r.path, field))
		}
	}
	return obj, nil
}

func decodeJSON(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(v)
}

// validateIdempotencyKey enforces what the API accepts: 1-255 printable
// ASCII characters (0x20-0x7E) with no comma.
func validateIdempotencyKey(key string) error {
	valid := len(key) >= 1 && len(key) <= 255
	for i := 0; valid && i < len(key); i++ {
		c := key[i]
		valid = c >= 0x20 && c <= 0x7e && c != ','
	}
	if !valid {
		return errors.New("tello api: idempotency key must be 1-255 printable ASCII characters with no comma")
	}
	return nil
}

// connectionError reports a request that got no complete response. The
// message names the request, never the URL's credentials or the key.
func connectionError(r apiRequest, what string, err error) *APIConnectionError {
	cause := err
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		cause = urlErr.Err
	}
	return &APIConnectionError{
		TelloError:     TelloError{Message: fmt.Sprintf("tello api: %s %s %s: %v", r.method, r.path, what, cause)},
		IdempotencyKey: r.idempotencyKey,
		Err:            err,
	}
}

func invalidResponse(status int, r apiRequest, message string) *APIError {
	return &APIError{
		TelloError:     TelloError{Code: "invalid_response", Message: message},
		Status:         status,
		IdempotencyKey: r.idempotencyKey,
	}
}

// errorResponse turns a non-2xx response into an *APIError. A body without
// the {statusCode, message, error, data} envelope gets code http_<status>.
func errorResponse(status int, raw []byte, idempotencyKey string) *APIError {
	var envelope struct {
		Message any    `json:"message"`
		Error   string `json:"error"`
		Data    any    `json:"data"`
	}
	statusText := http.StatusText(status)
	if statusText == "" {
		statusText = "HTTP " + strconv.Itoa(status)
	}
	if err := decodeJSON(raw, &envelope); err != nil || envelope.Error == "" {
		return &APIError{
			TelloError:     TelloError{Code: "http_" + strconv.Itoa(status), Message: statusText},
			Status:         status,
			IdempotencyKey: idempotencyKey,
		}
	}
	message, _ := envelope.Message.(string)
	if message == "" {
		message = statusText
	}
	data, _ := envelope.Data.(map[string]any)
	return &APIError{
		TelloError:     TelloError{Code: envelope.Error, Message: message},
		Status:         status,
		Data:           data,
		IdempotencyKey: idempotencyKey,
	}
}
