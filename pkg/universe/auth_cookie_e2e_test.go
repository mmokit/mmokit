package universe_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/mmokit/mmokit/pkg/mmokit"
	"github.com/mmokit/mmokit/pkg/services/auth/authtest"
)

// TestAuthCookieFullFlow verifies: HTTPS register sets cookie → fresh
// WS upgrade carries the cookie → gateway binds the session at
// upgrade time → no op-channel auth round-trip needed.
//
// Skipped pending a cluster fixture helper that exposes the HTTP
// listener address. The shape of the assertion is fixed and recorded
// here so a future fixture extension can flip the t.Skip and run.
func TestAuthCookieFullFlow(t *testing.T) {
	t.Skip("integration fixture: TODO — extend cluster_fixture to expose HTTP listener address")

	repo := authtest.NewMock()
	addr := startTestProcess(t, func(p *mmokit.Process) {
		_ = mmokit.RegisterAuthServiceWithMock(p, repo)
	})

	jar, _ := cookiejar.New(nil)
	httpClient := &http.Client{Jar: jar}

	// 1. Register via HTTPS. Cookie jar captures the Set-Cookie.
	regBody, _ := json.Marshal(map[string]any{"username": "alice", "password": "hunter22"})
	req, _ := http.NewRequest("POST", "http://"+addr+"/auth/register", bytes.NewReader(regBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("register status: %d", resp.StatusCode)
	}

	// 2. WS upgrade — cookie rides automatically via the http.Client jar.
	wsURL := "ws://" + addr + "/ws"
	u, _ := url.Parse(wsURL)
	cookies := jar.Cookies(u)
	if len(cookies) == 0 {
		t.Fatal("cookie jar empty after register")
	}
	hdr := http.Header{}
	for _, c := range cookies {
		hdr.Add("Cookie", c.Name+"="+c.Value)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()

	// 3. The connection should be authenticated WITHOUT any op-channel
	//    auth call. Indirect assertion: first frame received is on the
	//    event channel (0x00) — the gateway dispatches PlayerAssignment
	//    on successful upgrade and the cell sends an event. If the
	//    upgrade had stayed unauthenticated, no spawn would happen and
	//    we'd time out.
	_, msg, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if msg[0] != 0x00 {
		t.Fatalf("expected event channel 0x00 first, got 0x%02x", msg[0])
	}
}

// startTestProcess + assertion helpers depend on a cluster fixture
// helper that doesn't exist yet. Stub here.
func startTestProcess(t *testing.T, configure func(*mmokit.Process)) string {
	t.Helper()
	t.Skip("startTestProcess not yet implemented — wire to cluster_fixture")
	return ""
}
