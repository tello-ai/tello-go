package tello

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestClientConnectSendsClientIdentityQuery(t *testing.T) {
	ident := "sdk=go&version=" + Version + "&protocol=" + ProtocolVersion
	cases := []struct {
		name       string
		suffix     string
		wantTarget string
	}{
		{"no query", "/sdk", "/sdk?" + ident},
		{"user query kept", "/sdk?region=kr&trace=1", "/sdk?region=kr&trace=1&" + ident},
		{"raw pairs kept verbatim", "/sdk?token=a;b&x=%zz&z=1", "/sdk?token=a;b&x=%zz&z=1&" + ident},
		{"order, encoding and bare flag kept", "/sdk?z=1&a=b%20c&flag&q=x+y", "/sdk?z=1&a=b%20c&flag&q=x+y&" + ident},
		{"same keys removed", "/sdk?sdk=custom&version=9&keep=1&protocol=0.1", "/sdk?keep=1&" + ident},
		{"encoded keys removed", "/sdk?%73dk=custom&ver%73ion=9&a=1", "/sdk?a=1&" + ident},
		{"empty pieces dropped", "/sdk?&a=1&&", "/sdk?a=1&" + ident},
		{"nested path", "/edge/v2/sdk", "/edge/v2/sdk?" + ident},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := make(chan string, 1)
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got <- r.RequestURI
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				readAuth(t, conn)
				sendAuthOK(t, conn)
				_, _, _ = conn.ReadMessage()
			}))
			defer server.Close()

			client, err := NewClient("key-1", WithURL("ws"+server.URL[len("http"):]+tc.suffix))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := client.Connect(ctx); err != nil {
				t.Fatal(err)
			}
			defer client.Close()

			select {
			case target := <-got:
				if target != tc.wantTarget {
					t.Fatalf("request target = %q, want %q", target, tc.wantTarget)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("server never received the upgrade request")
			}
		})
	}
}

func TestDefaultURLGetsClientIdentityQuery(t *testing.T) {
	got, err := withClientIdentity(DefaultURL)
	if err != nil {
		t.Fatal(err)
	}
	want := "wss://api.telloai.io/sdk?sdk=go&version=" + Version + "&protocol=" + ProtocolVersion
	if got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
}
