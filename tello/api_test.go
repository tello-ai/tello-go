package tello

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testAPIKey = "sk_test_secret_key"

// fakeRequest is one request the fake REST API received.
type fakeRequest struct {
	Method     string
	Path       string // escaped path as sent, e.g. /v1/tools/a%2Fb
	RawQuery   string
	Header     http.Header
	Body       []byte
	HasBody    bool
	ReceivedAt time.Time
}

type fakeResponse struct {
	status int
	body   string
	// delay holds the response back; the handler gives up when the client
	// goes away.
	delay time.Duration
}

// fakeAPI is a local REST API: it records every request and answers the
// n-th one (0-based) with respond.
type fakeAPI struct {
	mu       sync.Mutex
	requests []fakeRequest
	respond  func(n int, req fakeRequest) fakeResponse
	server   *httptest.Server
}

func newFakeAPI(t *testing.T, respond func(n int, req fakeRequest) fakeResponse) *fakeAPI {
	t.Helper()
	f := &fakeAPI{respond: respond}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		req := fakeRequest{
			Method:     r.Method,
			Path:       r.URL.EscapedPath(),
			RawQuery:   r.URL.RawQuery,
			Header:     r.Header.Clone(),
			Body:       body,
			HasBody:    len(body) > 0,
			ReceivedAt: time.Now(),
		}
		f.mu.Lock()
		n := len(f.requests)
		f.requests = append(f.requests, req)
		f.mu.Unlock()
		resp := f.respond(n, req)
		if resp.delay > 0 {
			select {
			case <-time.After(resp.delay):
			case <-r.Context().Done():
				return
			}
		}
		if resp.body != "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(resp.status)
		_, _ = io.WriteString(w, resp.body)
	}))
	t.Cleanup(f.server.Close)
	return f
}

// reply answers every request the same way.
func reply(status int, body string) func(int, fakeRequest) fakeResponse {
	return func(int, fakeRequest) fakeResponse { return fakeResponse{status: status, body: body} }
}

func (f *fakeAPI) baseURL() string { return f.server.URL + "/v1" }

func (f *fakeAPI) all() []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeRequest(nil), f.requests...)
}

// only returns the single request the API received.
func (f *fakeAPI) only(t *testing.T) fakeRequest {
	t.Helper()
	reqs := f.all()
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request, got %d", len(reqs))
	}
	return reqs[0]
}

// newTestAPI builds an API against f with the test key.
func newTestAPI(t *testing.T, f *fakeAPI, opts ...APIOption) *API {
	t.Helper()
	api, err := NewAPI(testAPIKey, append([]APIOption{WithAPIURL(f.baseURL())}, opts...)...)
	if err != nil {
		t.Fatalf("NewAPI: %v", err)
	}
	return api
}

// asAPIError fails the test unless err is an *APIError.
func asAPIError(t *testing.T, err error) *APIError {
	t.Helper()
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	return apiErr
}

func decodeBody(t *testing.T, req fakeRequest) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatalf("request body is not a JSON object: %q", req.Body)
	}
	return body
}

func TestNewAPIRequiresAKey(t *testing.T) {
	t.Setenv(EnvAPIKey, "")
	_, err := NewAPI("")
	if err == nil || err.Error() != "apiKey is required" {
		t.Fatalf("expected apiKey is required, got %v", err)
	}
}

func TestNewAPIReadsKeyFromEnvironment(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusOK, `{"data":[]}`))
	t.Setenv(EnvAPIKey, "sk_from_env")
	api, err := NewAPI("", WithAPIURL(f.baseURL()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.t.do(context.Background(), apiRequest{method: http.MethodGet, path: "/agents", accept: []int{200}}); err != nil {
		t.Fatal(err)
	}
	if got := f.only(t).Header.Get("Authorization"); got != "Bearer sk_from_env" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestNewAPIResolvesBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		opts    []APIOption
		want    string
		wantErr bool
	}{
		{name: "production API by default", want: "https://api.telloai.io/v1"},
		{name: "TELLO_API_URL overrides the default", env: "https://env.example/v1", want: "https://env.example/v1"},
		{name: "WithAPIURL overrides TELLO_API_URL", env: "https://env.example/v1", opts: []APIOption{WithAPIURL("https://opt.example/v1")}, want: "https://opt.example/v1"},
		{name: "trailing slashes are dropped", opts: []APIOption{WithAPIURL("https://opt.example/v1//")}, want: "https://opt.example/v1"},
		{name: "http on 127.0.0.1", opts: []APIOption{WithAPIURL("http://127.0.0.1:8080/v1")}, want: "http://127.0.0.1:8080/v1"},
		{name: "http on localhost", opts: []APIOption{WithAPIURL("http://localhost:8080/v1")}, want: "http://localhost:8080/v1"},
		{name: "http on ::1", opts: []APIOption{WithAPIURL("http://[::1]:8080/v1")}, want: "http://[::1]:8080/v1"},
		{name: "http elsewhere is refused", opts: []APIOption{WithAPIURL("http://api.telloai.io/v1")}, wantErr: true},
		{name: "http from the environment is refused", env: "http://env.example/v1", wantErr: true},
		{name: "other schemes are refused", opts: []APIOption{WithAPIURL("wss://api.telloai.io/v1")}, wantErr: true},
		{name: "a URL without host is refused", opts: []APIOption{WithAPIURL("https:///v1")}, wantErr: true},
		{name: "an unparsable URL is refused", opts: []APIOption{WithAPIURL("https://exa mple.com/v1")}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvAPIURL, tt.env)
			api, err := NewAPI("key-1", tt.opts...)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got base %q", api.t.baseURL)
				}
				if strings.Contains(err.Error(), "key-1") {
					t.Fatalf("error leaks the key: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if api.t.baseURL != tt.want {
				t.Fatalf("base URL = %q, want %q", api.t.baseURL, tt.want)
			}
		})
	}
}

func TestNewAPIRefusesNonPositiveTimeouts(t *testing.T) {
	for _, opt := range []APIOption{WithAPITimeout(0), WithAPILongTimeout(-time.Second)} {
		if _, err := NewAPI("key-1", opt); err == nil {
			t.Fatal("expected an error for a non-positive timeout")
		}
	}
}

func TestAPISendsHeadersWithoutBody(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusOK, `{"data":[]}`))
	api := newTestAPI(t, f)

	if _, err := api.t.do(context.Background(), apiRequest{method: http.MethodGet, path: "/agents", accept: []int{200}}); err != nil {
		t.Fatal(err)
	}

	req := f.only(t)
	if req.Method != http.MethodGet || req.Path != "/v1/agents" {
		t.Fatalf("request = %s %s", req.Method, req.Path)
	}
	want := map[string]string{
		"Authorization":   "Bearer " + testAPIKey,
		"Accept":          "application/json",
		"User-Agent":      "tello-go-sdk/" + Version,
		"Content-Type":    "",
		"Idempotency-Key": "",
	}
	for name, value := range want {
		if got := req.Header.Get(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
	if req.HasBody {
		t.Errorf("GET sent a body: %q", req.Body)
	}
}

func TestAPISendsJSONBodyAndIdempotencyKey(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusCreated, `{"id":"1"}`))
	api := newTestAPI(t, f)

	_, err := api.t.do(context.Background(), apiRequest{
		method: http.MethodPost, path: "/tools", body: Object{"name": "x"},
		idempotencyKey: "key-123", accept: []int{201},
	})
	if err != nil {
		t.Fatal(err)
	}

	req := f.only(t)
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := req.Header.Get("Idempotency-Key"); got != "key-123" {
		t.Errorf("Idempotency-Key = %q", got)
	}
	if body := decodeBody(t, req); body["name"] != "x" {
		t.Errorf("body = %v", body)
	}
}

func TestAPIDecodesNumbersExactly(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusOK, `{"revision":9007199254740993,"nested":{"n":1.5}}`))
	api := newTestAPI(t, f)

	obj, err := api.t.do(context.Background(), apiRequest{method: http.MethodGet, path: "/tools/1", accept: []int{200}})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := obj["revision"].(json.Number); !ok || got.String() != "9007199254740993" {
		t.Fatalf("revision = %#v", obj["revision"])
	}
	if got, ok := obj["nested"].(map[string]any)["n"].(json.Number); !ok || got.String() != "1.5" {
		t.Fatalf("nested.n = %#v", obj["nested"])
	}
}

func TestAPIReturnsNilObjectFor204(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusNoContent, ""))
	api := newTestAPI(t, f)

	obj, err := api.t.do(context.Background(), apiRequest{method: http.MethodDelete, path: "/tools/1", accept: []int{204}})
	if err != nil {
		t.Fatal(err)
	}
	if obj != nil {
		t.Fatalf("expected no object, got %v", obj)
	}
}

func TestAPIErrorFromEnvelope(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusUnprocessableEntity,
		`{"statusCode":422,"message":"Tool is not publishable","error":"tool_not_publishable","data":{"reason":"missing_url","count":2}}`))
	api := newTestAPI(t, f)

	_, err := api.t.do(context.Background(), apiRequest{method: http.MethodPost, path: "/tools", body: Object{}, idempotencyKey: "idem-1", accept: []int{201}})

	apiErr := asAPIError(t, err)
	if apiErr.Status != 422 || apiErr.Code != "tool_not_publishable" || apiErr.Message != "Tool is not publishable" {
		t.Fatalf("error = %+v", apiErr)
	}
	if apiErr.Data["reason"] != "missing_url" || apiErr.Data["count"] != json.Number("2") {
		t.Fatalf("data = %#v", apiErr.Data)
	}
	if apiErr.IdempotencyKey != "idem-1" {
		t.Fatalf("IdempotencyKey = %q", apiErr.IdempotencyKey)
	}
	if !strings.Contains(apiErr.Error(), "tool_not_publishable") {
		t.Fatalf("Error() = %q", apiErr.Error())
	}
}

func TestAPIErrorWithNullDataAndNoKey(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusNotFound, `{"statusCode":404,"message":"Tool not found","error":"tool_not_found","data":null}`))
	api := newTestAPI(t, f)

	_, err := api.t.do(context.Background(), apiRequest{method: http.MethodGet, path: "/tools/9", accept: []int{200}})

	apiErr := asAPIError(t, err)
	if apiErr.Code != "tool_not_found" || apiErr.Data != nil || apiErr.IdempotencyKey != "" {
		t.Fatalf("error = %+v", apiErr)
	}
}

func TestAPIErrorFromNonEnvelopeResponse(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		code    string
		message string
	}{
		{name: "proxy HTML", status: 502, body: "<html>bad gateway</html>", code: "http_502", message: "Bad Gateway"},
		{name: "JSON without error", status: 503, body: `{"message":"down"}`, code: "http_503", message: "Service Unavailable"},
		{name: "empty body", status: 500, body: "", code: "http_500", message: "Internal Server Error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeAPI(t, reply(tt.status, tt.body))
			api := newTestAPI(t, f)

			_, err := api.t.do(context.Background(), apiRequest{method: http.MethodGet, path: "/tools", accept: []int{200}})

			apiErr := asAPIError(t, err)
			if apiErr.Status != tt.status || apiErr.Code != tt.code || apiErr.Message != tt.message || apiErr.Data != nil {
				t.Fatalf("error = %+v", apiErr)
			}
			if n := len(f.all()); n != 1 {
				t.Fatalf("expected 1 request, got %d", n)
			}
		})
	}
}

func TestAPIDoesNotFollowRedirects(t *testing.T) {
	var hits sync.Map
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Store(r.URL.Path, true)
		if r.URL.Path == "/v1/tools" {
			http.Redirect(w, r, "/v1/elsewhere", http.StatusTemporaryRedirect)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(server.Close)
	api, err := NewAPI(testAPIKey, WithAPIURL(server.URL+"/v1"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = api.t.do(context.Background(), apiRequest{method: http.MethodPost, path: "/tools", body: Object{}, accept: []int{201}})

	if apiErr := asAPIError(t, err); apiErr.Code != "http_307" {
		t.Fatalf("error = %+v", apiErr)
	}
	if _, followed := hits.Load("/v1/elsewhere"); followed {
		t.Fatal("the redirect was followed")
	}
}

func TestAPIInvalidResponse(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "undocumented success status", status: 200, body: `{"id":"1"}`},
		{name: "array body", status: 201, body: `[]`},
		{name: "null body", status: 201, body: `null`},
		{name: "not JSON", status: 201, body: `oops`},
		{name: "empty body", status: 201, body: ``},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeAPI(t, reply(tt.status, tt.body))
			api := newTestAPI(t, f)

			_, err := api.t.do(context.Background(), apiRequest{method: http.MethodPost, path: "/tools", body: Object{}, idempotencyKey: "idem-2", accept: []int{201}})

			apiErr := asAPIError(t, err)
			if apiErr.Code != "invalid_response" || apiErr.Status != tt.status || apiErr.IdempotencyKey != "idem-2" {
				t.Fatalf("error = %+v", apiErr)
			}
		})
	}
}

func TestAPIConnectionErrorOnTransportFailure(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusOK, `{}`))
	api := newTestAPI(t, f)
	f.server.Close()

	_, err := api.t.do(context.Background(), apiRequest{method: http.MethodPost, path: "/tools", body: Object{}, idempotencyKey: "idem-3", accept: []int{201}})

	var connErr *APIConnectionError
	if !errors.As(err, &connErr) {
		t.Fatalf("expected *APIConnectionError, got %T: %v", err, err)
	}
	if connErr.IdempotencyKey != "idem-3" || connErr.Unwrap() == nil {
		t.Fatalf("error = %+v", connErr)
	}
	if strings.Contains(err.Error(), testAPIKey) {
		t.Fatalf("error leaks the key: %v", err)
	}
}

func TestAPIConnectionErrorOnTimeout(t *testing.T) {
	f := newFakeAPI(t, func(int, fakeRequest) fakeResponse {
		return fakeResponse{status: 200, body: `{}`, delay: time.Second}
	})
	api := newTestAPI(t, f, WithAPITimeout(50*time.Millisecond))

	_, err := api.t.do(context.Background(), apiRequest{method: http.MethodGet, path: "/agents", accept: []int{200}})

	var connErr *APIConnectionError
	if !errors.As(err, &connErr) {
		t.Fatalf("expected *APIConnectionError, got %T: %v", err, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected the timeout as cause, got %v", err)
	}
}

func TestAPIConnectionErrorKeepsContextCancellation(t *testing.T) {
	f := newFakeAPI(t, func(int, fakeRequest) fakeResponse {
		return fakeResponse{status: 200, body: `{}`, delay: time.Second}
	})
	api := newTestAPI(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)

	_, err := api.t.do(ctx, apiRequest{method: http.MethodGet, path: "/agents", accept: []int{200}})

	var connErr *APIConnectionError
	if !errors.As(err, &connErr) || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected a connection error caused by cancellation, got %T: %v", err, err)
	}
}

func TestAPILongRequestsUseTheLongTimeout(t *testing.T) {
	f := newFakeAPI(t, func(int, fakeRequest) fakeResponse {
		return fakeResponse{status: 200, body: `{}`, delay: 150 * time.Millisecond}
	})
	api := newTestAPI(t, f, WithAPITimeout(30*time.Millisecond), WithAPILongTimeout(5*time.Second))

	if _, err := api.t.do(context.Background(), apiRequest{method: http.MethodPost, path: "/call-drafts", body: Object{}, long: true, accept: []int{200}}); err != nil {
		t.Fatalf("long request: %v", err)
	}
	if _, err := api.t.do(context.Background(), apiRequest{method: http.MethodPost, path: "/tools", body: Object{}, accept: []int{200}}); err == nil {
		t.Fatal("short request outlived the short timeout")
	}
}

func TestAPIDefaultTimeouts(t *testing.T) {
	api, err := NewAPI("key-1")
	if err != nil {
		t.Fatal(err)
	}
	if api.t.timeout != 30*time.Second || api.t.longTimeout != 120*time.Second {
		t.Fatalf("timeouts = %v / %v", api.t.timeout, api.t.longTimeout)
	}
}

func TestAPIRejectsInvalidIdempotencyKeysWithoutRequest(t *testing.T) {
	f := newFakeAPI(t, reply(http.StatusCreated, `{}`))
	api := newTestAPI(t, f)

	for _, key := range []string{"a,b", "line\nbreak", "tab\tkey", "del\x7f", strings.Repeat("k", 256)} {
		_, err := api.t.do(context.Background(), apiRequest{method: http.MethodPost, path: "/tools", body: Object{}, idempotencyKey: key, accept: []int{201}})
		if err == nil {
			t.Fatalf("key %q accepted", key)
		}
	}
	if n := len(f.all()); n != 0 {
		t.Fatalf("expected no request, got %d", n)
	}

	if _, err := api.t.do(context.Background(), apiRequest{method: http.MethodPost, path: "/tools", body: Object{}, idempotencyKey: strings.Repeat("k", 255), accept: []int{201}}); err != nil {
		t.Fatalf("255-byte key refused: %v", err)
	}
}

func TestAPINeverPrintsTheKey(t *testing.T) {
	api, err := NewAPI(testAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		fmt.Sprintf("%v", api), fmt.Sprintf("%+v", api), fmt.Sprintf("%#v", api),
		fmt.Sprintf("%+v", *api), fmt.Sprintf("%#v", *api),
	} {
		if strings.Contains(s, testAPIKey) {
			t.Fatalf("formatted API leaks the key: %s", s)
		}
	}
}
