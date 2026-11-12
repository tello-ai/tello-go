package tello

import (
	"context"
	"net/http"
	"testing"
)

// crudCase drives the CRUD methods every resource namespace shares.
type crudCase struct {
	collection string
	get        func(*API, context.Context, string) (Object, error)
	create     func(*API, context.Context, Object, ...RequestOption) (Object, error)
	update     func(*API, context.Context, string, Object) (Object, error)
	delete     func(*API, context.Context, string) error
}

var crudCases = []crudCase{
	{
		collection: "tools",
		get:        func(a *API, ctx context.Context, id string) (Object, error) { return a.Tools.Get(ctx, id) },
		create: func(a *API, ctx context.Context, body Object, opts ...RequestOption) (Object, error) {
			return a.Tools.Create(ctx, body, opts...)
		},
		update: func(a *API, ctx context.Context, id string, patch Object) (Object, error) {
			return a.Tools.Update(ctx, id, patch)
		},
		delete: func(a *API, ctx context.Context, id string) error { return a.Tools.Delete(ctx, id) },
	},
}

func TestResourceGet(t *testing.T) {
	for _, tc := range crudCases {
		t.Run(tc.collection, func(t *testing.T) {
			f := newFakeAPI(t, reply(http.StatusOK, `{"id":"a/b","name":"x"}`))
			api := newTestAPI(t, f)

			obj, err := tc.get(api, context.Background(), "a/b")
			if err != nil {
				t.Fatal(err)
			}
			if obj["name"] != "x" {
				t.Fatalf("object = %v", obj)
			}
			req := f.only(t)
			if req.Method != http.MethodGet || req.Path != "/v1/"+tc.collection+"/a%2Fb" {
				t.Fatalf("request = %s %s", req.Method, req.Path)
			}
		})
	}
}

func TestResourceCreate(t *testing.T) {
	for _, tc := range crudCases {
		t.Run(tc.collection, func(t *testing.T) {
			f := newFakeAPI(t, reply(http.StatusCreated, `{"id":"7","revision":1}`))
			api := newTestAPI(t, f)

			obj, err := tc.create(api, context.Background(), Object{"name": "new"})
			if err != nil {
				t.Fatal(err)
			}
			if obj["id"] != "7" {
				t.Fatalf("object = %v", obj)
			}
			req := f.only(t)
			if req.Method != http.MethodPost || req.Path != "/v1/"+tc.collection {
				t.Fatalf("request = %s %s", req.Method, req.Path)
			}
			if req.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("Content-Type = %q", req.Header.Get("Content-Type"))
			}
			if _, sent := req.Header["Idempotency-Key"]; sent {
				t.Fatal("sent an Idempotency-Key nobody asked for")
			}
			if body := decodeBody(t, req); body["name"] != "new" {
				t.Fatalf("body = %v", body)
			}
		})
	}
}

func TestResourceCreateSendsGivenIdempotencyKey(t *testing.T) {
	for _, tc := range crudCases {
		t.Run(tc.collection, func(t *testing.T) {
			f := newFakeAPI(t, reply(http.StatusCreated, `{"id":"7"}`))
			api := newTestAPI(t, f)

			if _, err := tc.create(api, context.Background(), Object{}, WithIdempotencyKey("create-1")); err != nil {
				t.Fatal(err)
			}
			if got := f.only(t).Header.Get("Idempotency-Key"); got != "create-1" {
				t.Fatalf("Idempotency-Key = %q", got)
			}
		})
	}
}

func TestResourceCreateSendsEmptyObjectForNilBody(t *testing.T) {
	for _, tc := range crudCases {
		t.Run(tc.collection, func(t *testing.T) {
			f := newFakeAPI(t, reply(http.StatusCreated, `{"id":"7"}`))
			api := newTestAPI(t, f)

			if _, err := tc.create(api, context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			if got := string(f.only(t).Body); got != "{}" {
				t.Fatalf("body = %q", got)
			}
		})
	}
}

func TestResourceCreateRefusesUndocumentedSuccessStatus(t *testing.T) {
	for _, tc := range crudCases {
		t.Run(tc.collection, func(t *testing.T) {
			f := newFakeAPI(t, reply(http.StatusOK, `{"id":"7"}`))
			api := newTestAPI(t, f)

			_, err := tc.create(api, context.Background(), Object{}, WithIdempotencyKey("create-2"))

			apiErr := asAPIError(t, err)
			if apiErr.Code != "invalid_response" || apiErr.IdempotencyKey != "create-2" {
				t.Fatalf("error = %+v", apiErr)
			}
		})
	}
}

func TestResourceCreateRefusesInvalidKeyWithoutRequest(t *testing.T) {
	for _, tc := range crudCases {
		t.Run(tc.collection, func(t *testing.T) {
			f := newFakeAPI(t, reply(http.StatusCreated, `{"id":"7"}`))
			api := newTestAPI(t, f)

			if _, err := tc.create(api, context.Background(), Object{}, WithIdempotencyKey("a,b")); err == nil {
				t.Fatal("expected an error")
			}
			if n := len(f.all()); n != 0 {
				t.Fatalf("expected no request, got %d", n)
			}
		})
	}
}

func TestResourceCreateErrorCarriesEnvelope(t *testing.T) {
	for _, tc := range crudCases {
		t.Run(tc.collection, func(t *testing.T) {
			f := newFakeAPI(t, reply(http.StatusConflict,
				`{"statusCode":409,"message":"Idempotency key reused","error":"idempotency_key_reused","data":null}`))
			api := newTestAPI(t, f)

			_, err := tc.create(api, context.Background(), Object{}, WithIdempotencyKey("create-3"))

			apiErr := asAPIError(t, err)
			if apiErr.Status != 409 || apiErr.Code != "idempotency_key_reused" || apiErr.IdempotencyKey != "create-3" {
				t.Fatalf("error = %+v", apiErr)
			}
		})
	}
}

func TestResourceUpdate(t *testing.T) {
	for _, tc := range crudCases {
		t.Run(tc.collection, func(t *testing.T) {
			f := newFakeAPI(t, reply(http.StatusOK, `{"id":"7","description":"d"}`))
			api := newTestAPI(t, f)

			obj, err := tc.update(api, context.Background(), "7", Object{"description": "d"})
			if err != nil {
				t.Fatal(err)
			}
			if obj["description"] != "d" {
				t.Fatalf("object = %v", obj)
			}
			req := f.only(t)
			if req.Method != http.MethodPatch || req.Path != "/v1/"+tc.collection+"/7" {
				t.Fatalf("request = %s %s", req.Method, req.Path)
			}
			if body := decodeBody(t, req); len(body) != 1 || body["description"] != "d" {
				t.Fatalf("body = %v", body)
			}
			if _, sent := req.Header["Idempotency-Key"]; sent {
				t.Fatal("update sent an Idempotency-Key")
			}
		})
	}
}

func TestResourceUpdateRefusesUndocumentedSuccessStatus(t *testing.T) {
	for _, tc := range crudCases {
		t.Run(tc.collection, func(t *testing.T) {
			f := newFakeAPI(t, reply(http.StatusCreated, `{"id":"7"}`))
			api := newTestAPI(t, f)

			_, err := tc.update(api, context.Background(), "7", Object{})

			if apiErr := asAPIError(t, err); apiErr.Code != "invalid_response" {
				t.Fatalf("error = %+v", apiErr)
			}
		})
	}
}

func TestResourceDelete(t *testing.T) {
	for _, tc := range crudCases {
		t.Run(tc.collection, func(t *testing.T) {
			f := newFakeAPI(t, reply(http.StatusNoContent, ""))
			api := newTestAPI(t, f)

			if err := tc.delete(api, context.Background(), "7"); err != nil {
				t.Fatal(err)
			}
			req := f.only(t)
			if req.Method != http.MethodDelete || req.Path != "/v1/"+tc.collection+"/7" {
				t.Fatalf("request = %s %s", req.Method, req.Path)
			}
			if req.HasBody || req.Header.Get("Content-Type") != "" {
				t.Fatalf("delete sent a body: %q", req.Body)
			}
		})
	}
}

func TestResourceDeleteRefusesUndocumentedSuccessStatus(t *testing.T) {
	for _, tc := range crudCases {
		t.Run(tc.collection, func(t *testing.T) {
			f := newFakeAPI(t, reply(http.StatusOK, `{}`))
			api := newTestAPI(t, f)

			if apiErr := asAPIError(t, tc.delete(api, context.Background(), "7")); apiErr.Code != "invalid_response" {
				t.Fatalf("error = %+v", apiErr)
			}
		})
	}
}

func TestResourceDeleteNotFound(t *testing.T) {
	for _, tc := range crudCases {
		t.Run(tc.collection, func(t *testing.T) {
			f := newFakeAPI(t, reply(http.StatusNotFound, `{"statusCode":404,"message":"Not found","error":"not_found_code","data":null}`))
			api := newTestAPI(t, f)

			apiErr := asAPIError(t, tc.delete(api, context.Background(), "7"))
			if apiErr.Status != 404 || apiErr.Code != "not_found_code" {
				t.Fatalf("error = %+v", apiErr)
			}
		})
	}
}

func TestResourceEmptyIDIsRefusedWithoutRequest(t *testing.T) {
	for _, tc := range crudCases {
		t.Run(tc.collection, func(t *testing.T) {
			f := newFakeAPI(t, reply(http.StatusOK, `{}`))
			api := newTestAPI(t, f)
			ctx := context.Background()

			if _, err := tc.get(api, ctx, ""); err == nil {
				t.Error("get accepted an empty id")
			}
			if _, err := tc.update(api, ctx, "", Object{}); err == nil {
				t.Error("update accepted an empty id")
			}
			if err := tc.delete(api, ctx, ""); err == nil {
				t.Error("delete accepted an empty id")
			}
			if n := len(f.all()); n != 0 {
				t.Fatalf("expected no request, got %d", n)
			}
		})
	}
}

func TestToolsList(t *testing.T) {
	tests := []struct {
		name  string
		opts  ToolListOptions
		query string
	}{
		{name: "no options", query: ""},
		{name: "limit and cursor", opts: ToolListOptions{Limit: 1, Cursor: "eyJpZCI6NDF9"}, query: "cursor=eyJpZCI6NDF9&limit=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeAPI(t, reply(http.StatusOK, `{"data":[{"id":"1"}],"nextCursor":null}`))
			api := newTestAPI(t, f)

			page, err := api.Tools.List(context.Background(), tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			if data, ok := page["data"].([]any); !ok || len(data) != 1 {
				t.Fatalf("page = %v", page)
			}
			if next, present := page["nextCursor"]; !present || next != nil {
				t.Fatalf("nextCursor = %#v", next)
			}
			req := f.only(t)
			if req.Method != http.MethodGet || req.Path != "/v1/tools" || req.RawQuery != tt.query {
				t.Fatalf("request = %s %s?%s", req.Method, req.Path, req.RawQuery)
			}
		})
	}
}
