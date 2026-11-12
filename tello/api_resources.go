package tello

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// RequestOption sets an option of a single REST request.
type RequestOption func(*requestOptions)

type requestOptions struct {
	idempotencyKey string
}

// WithIdempotencyKey sends key as the Idempotency-Key header: resending the
// same request with the same key replays the first answer instead of acting
// twice. The key must be 1-255 printable ASCII characters (0x20-0x7E) with
// no comma; otherwise the method fails without sending anything.
func WithIdempotencyKey(key string) RequestOption {
	return func(o *requestOptions) {
		o.idempotencyKey = key
	}
}

func collectRequestOptions(opts []RequestOption) requestOptions {
	var o requestOptions
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// Tools manages the account's tools. Creates, updates and deletes are live
// for the next call as soon as they succeed.
type Tools struct {
	t *apiTransport
}

// ToolListOptions is the GET /tools query; zero values are not sent.
type ToolListOptions struct {
	// Limit is the page size, 1-100 (server default 50).
	Limit int
	// Cursor is the nextCursor of the previous page.
	Cursor string
}

// List returns one page by ascending id as the API sends it:
// {"data": [tool...], "nextCursor": string|null}.
func (r *Tools) List(ctx context.Context, opts ToolListOptions) (Object, error) {
	query := url.Values{}
	if opts.Limit != 0 {
		query.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Cursor != "" {
		query.Set("cursor", opts.Cursor)
	}
	return r.t.do(ctx, apiRequest{method: http.MethodGet, path: "/tools", query: query, accept: []int{http.StatusOK}})
}

// Get returns one tool.
func (r *Tools) Get(ctx context.Context, id string) (Object, error) {
	return r.t.getItem(ctx, "tools", id)
}

// Create creates a tool from body and returns it with its revision.
// WithIdempotencyKey makes the create safe to resend.
func (r *Tools) Create(ctx context.Context, body Object, opts ...RequestOption) (Object, error) {
	return r.t.createItem(ctx, "tools", body, opts)
}

// Update changes only the fields in patch and returns the tool.
func (r *Tools) Update(ctx context.Context, id string, patch Object) (Object, error) {
	return r.t.updateItem(ctx, "tools", id, patch)
}

// Delete deletes a tool.
func (r *Tools) Delete(ctx context.Context, id string) error {
	return r.t.deleteItem(ctx, "tools", id)
}

// Agents manages the account's agents. Creates, updates and deletes are
// live for the next call as soon as they succeed.
type Agents struct {
	t *apiTransport
}

// List returns the agents as the API sends them:
// {"data": [{"id", "name", "description", "isDefault"}...]}.
func (r *Agents) List(ctx context.Context) (Object, error) {
	return r.t.listItems(ctx, "agents")
}

// Get returns one agent with its full settings.
func (r *Agents) Get(ctx context.Context, id string) (Object, error) {
	return r.t.getItem(ctx, "agents", id)
}

// Create creates an agent from body and returns it. WithIdempotencyKey
// makes the create safe to resend.
func (r *Agents) Create(ctx context.Context, body Object, opts ...RequestOption) (Object, error) {
	return r.t.createItem(ctx, "agents", body, opts)
}

// Update changes only the fields in patch and returns the agent.
func (r *Agents) Update(ctx context.Context, id string, patch Object) (Object, error) {
	return r.t.updateItem(ctx, "agents", id, patch)
}

// Delete deletes an agent.
func (r *Agents) Delete(ctx context.Context, id string) error {
	return r.t.deleteItem(ctx, "agents", id)
}

// Crews manages the account's crews. Creates, updates and deletes are live
// for the next call as soon as they succeed.
type Crews struct {
	t *apiTransport
}

// List returns the crews as the API sends them: {"data": [crew...]}.
func (r *Crews) List(ctx context.Context) (Object, error) {
	return r.t.listItems(ctx, "crews")
}

// Get returns one crew with its full settings.
func (r *Crews) Get(ctx context.Context, id string) (Object, error) {
	return r.t.getItem(ctx, "crews", id)
}

// Create creates a crew from body and returns it. WithIdempotencyKey makes
// the create safe to resend.
func (r *Crews) Create(ctx context.Context, body Object, opts ...RequestOption) (Object, error) {
	return r.t.createItem(ctx, "crews", body, opts)
}

// Update changes only the fields in patch and returns the crew.
func (r *Crews) Update(ctx context.Context, id string, patch Object) (Object, error) {
	return r.t.updateItem(ctx, "crews", id, patch)
}

// Delete deletes a crew.
func (r *Crews) Delete(ctx context.Context, id string) error {
	return r.t.deleteItem(ctx, "crews", id)
}

// itemPath is the escaped path of one item of collection.
func itemPath(collection, id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("tello api: %s id is required", collection)
	}
	return "/" + collection + "/" + url.PathEscape(id), nil
}

// orEmpty sends a nil body as {} rather than null.
func orEmpty(body Object) Object {
	if body == nil {
		return Object{}
	}
	return body
}

func (t *apiTransport) getItem(ctx context.Context, collection, id string) (Object, error) {
	path, err := itemPath(collection, id)
	if err != nil {
		return nil, err
	}
	return t.do(ctx, apiRequest{method: http.MethodGet, path: path, accept: []int{http.StatusOK}})
}

func (t *apiTransport) listItems(ctx context.Context, collection string) (Object, error) {
	return t.do(ctx, apiRequest{method: http.MethodGet, path: "/" + collection, accept: []int{http.StatusOK}})
}

func (t *apiTransport) createItem(ctx context.Context, collection string, body Object, opts []RequestOption) (Object, error) {
	o := collectRequestOptions(opts)
	return t.do(ctx, apiRequest{
		method: http.MethodPost, path: "/" + collection, body: orEmpty(body),
		idempotencyKey: o.idempotencyKey, long: true, accept: []int{http.StatusCreated},
	})
}

func (t *apiTransport) updateItem(ctx context.Context, collection, id string, patch Object) (Object, error) {
	path, err := itemPath(collection, id)
	if err != nil {
		return nil, err
	}
	return t.do(ctx, apiRequest{method: http.MethodPatch, path: path, body: orEmpty(patch), long: true, accept: []int{http.StatusOK}})
}

func (t *apiTransport) deleteItem(ctx context.Context, collection, id string) error {
	path, err := itemPath(collection, id)
	if err != nil {
		return err
	}
	_, err = t.do(ctx, apiRequest{method: http.MethodDelete, path: path, long: true, accept: []int{http.StatusNoContent}})
	return err
}
