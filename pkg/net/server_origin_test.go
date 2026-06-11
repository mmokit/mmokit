package net

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// wsURLFromHTTP converts an httptest "http://127.0.0.1:port" base into a
// "ws://127.0.0.1:port" URL.
func wsURLFromHTTP(httpURL string) string {
	return "ws" + httpURL[len("http"):]
}

func TestHandleWebSocket_RejectsCrossOrigin(t *testing.T) {
	cm := NewConnManager()
	srv := httptest.NewServer(http.HandlerFunc(cm.HandleWebSocket))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, wsURLFromHTTP(srv.URL), &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"http://evil.example.com"}},
	})
	if err == nil {
		c.Close(websocket.StatusNormalClosure, "")
		t.Fatal("expected cross-origin WS dial to be rejected, got success")
	}
}

func TestHandleWebSocket_AllowsSameOrigin(t *testing.T) {
	cm := NewConnManager()
	srv := httptest.NewServer(http.HandlerFunc(cm.HandleWebSocket))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Origin host == request Host (both the httptest server's addr) -> same-origin.
	c, _, err := websocket.Dial(ctx, wsURLFromHTTP(srv.URL), &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{srv.URL}},
	})
	if err != nil {
		t.Fatalf("expected same-origin WS dial to succeed: %v", err)
	}
	c.Close(websocket.StatusNormalClosure, "")
}

func TestHandleWebSocket_AllowsNoOriginHeader(t *testing.T) {
	// Native (non-browser) clients send no Origin header and must be allowed.
	cm := NewConnManager()
	srv := httptest.NewServer(http.HandlerFunc(cm.HandleWebSocket))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, wsURLFromHTTP(srv.URL), nil)
	if err != nil {
		t.Fatalf("expected no-Origin WS dial to succeed: %v", err)
	}
	c.Close(websocket.StatusNormalClosure, "")
}

func TestHandleWebSocket_AllowsListedOrigin(t *testing.T) {
	cm := NewConnManager()
	cm.AllowedOrigins = []string{"trusted.example.com"}
	srv := httptest.NewServer(http.HandlerFunc(cm.HandleWebSocket))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, wsURLFromHTTP(srv.URL), &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"http://trusted.example.com"}},
	})
	if err != nil {
		t.Fatalf("expected allowlisted origin to succeed: %v", err)
	}
	c.Close(websocket.StatusNormalClosure, "")
}
