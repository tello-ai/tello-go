package tello

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gorilla/websocket"
)

func TestClientConnectSendsClientIdentityQuery(t *testing.T) {
	cases := []struct {
		name      string
		suffix    string
		wantPath  string
		wantExtra map[string]string
	}{
		{"no query", "/sdk", "/sdk", nil},
		{"user query kept", "/sdk?region=kr&trace=1", "/sdk", map[string]string{"region": "kr", "trace": "1"}},
		{"same keys overwritten", "/sdk?sdk=custom&version=9&protocol=0.1", "/sdk", nil},
		{"nested path", "/edge/v2/sdk", "/edge/v2/sdk", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := make(chan *url.URL, 1)
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got <- r.URL
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				readAuth(t, conn)
				_ = conn.WriteJSON(map[string]any{"type": "authenticated", "version": "1.0", "accountId": 1, "authMethod": "api_key"})
				_, _, _ = conn.ReadMessage()
			}))
			defer server.Close()

			client, err := NewClient("key-1", WithURL("ws"+server.URL[len("http"):]+tc.suffix))
			if err != nil {
				t.Fatal(err)
			}
			_ = client.Connect(context.Background())
			defer client.Close()

			u := <-got
			if u.Path != tc.wantPath {
				t.Fatalf("path = %q, want %q", u.Path, tc.wantPath)
			}
			q := u.Query()
			want := map[string]string{"sdk": "go", "version": Version, "protocol": ProtocolVersion}
			for k, v := range tc.wantExtra {
				want[k] = v
			}
			for k, v := range want {
				if vs := q[k]; len(vs) != 1 || vs[0] != v {
					t.Errorf("query %s = %v, want [%s]", k, vs, v)
				}
			}
			if len(q) != len(want) {
				t.Errorf("query = %v, want only %v", q, want)
			}
		})
	}
}

func TestDefaultURLGetsClientIdentityQuery(t *testing.T) {
	got, err := withClientIdentity(DefaultURL)
	if err != nil {
		t.Fatal(err)
	}
	want := "wss://api.telloai.io/sdk?protocol=" + ProtocolVersion + "&sdk=go&version=" + Version
	if got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
}
