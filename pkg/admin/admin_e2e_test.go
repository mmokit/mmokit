package admin_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/admin"
	"github.com/zenion/mmoserver/pkg/persist"
	"github.com/zenion/mmoserver/pkg/persist/persisttest"
	"github.com/zenion/mmoserver/pkg/services/auth"
	"github.com/zenion/mmoserver/pkg/universe"
)

// newE2EProcess spins up a minimal headless 2x2 universe.Process suitable
// for the admin e2e tests. HTTPPort -1 disables the engine HTTP listener so
// parallel tests don't fight over :8080. When runCells is true, each cell's
// game loop is started — required for tests that invoke cell.split (the
// orchestrator's serialize step needs the cell to drain admin-cmd queue).
func newE2EProcess(t *testing.T, runCells bool) *universe.Process {
	t.Helper()
	cfg := universe.Config{
		Headless: true,
		HTTPPort: -1,
		CellsX:   2,
		CellsY:   2,
	}
	p := universe.New(cfg)
	p.Build()

	if runCells {
		ctx, cancel := context.WithCancel(context.Background())
		for _, cell := range p.Cells {
			go cell.Run(ctx)
		}
		// Give every cell a tick to start draining its admin-cmd queue
		// before the test fires its first command. Mirrors the 20ms
		// settle in newColocatedFixture.
		time.Sleep(20 * time.Millisecond)
		t.Cleanup(func() {
			cancel()
			p.Shutdown()
		})
	} else {
		t.Cleanup(p.Shutdown)
	}
	return p
}

// mountE2EAdmin builds an admin.Server bound to p and mounts it on a fresh
// httptest.Server. Returns the test server and the configured operator's
// password (plain text, for login attempts).
func mountE2EAdmin(t *testing.T, p *universe.Process, username, password string) (*httptest.Server, string) {
	t.Helper()
	hash, err := auth.HashPassword(password, auth.DefaultArgonParams())
	if err != nil {
		t.Fatal(err)
	}

	repo := persisttest.NewAdminOperatorRepoMock()
	if err := repo.Create(context.Background(), &persist.AdminOperator{
		Username:     username,
		PasswordHash: hash,
		Grants:       []string{"*.*"},
	}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv := admin.NewServer(admin.ServerOpts{
		View:         admin.NewLocalClusterView(p),
		Registry:     p.CmdRegistry(),
		Dispatcher:   p.CmdDispatcher(),
		SessionStore: admin.NewMemorySessionStore(),
		Panels:       admin.NewPanelRegistry(),
		Logger:       p.Log,
		Process:      p,
		OperatorRepo: repo,
		Config: admin.Config{
			BindAddr:   "127.0.0.1:0",
			SessionTTL: time.Hour,
		},
	})
	srv.Mount(mux)
	t.Cleanup(srv.Stop)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, hash
}

func TestAdminE2E_LoginAndCluster(t *testing.T) {
	t.Parallel()

	p := newE2EProcess(t, false)
	ts, _ := mountE2EAdmin(t, p, "josh", "secret123")

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second}

	// Login.
	loginBody, _ := json.Marshal(map[string]string{"username": "josh", "password": "secret123"})
	resp, err := client.Post(ts.URL+"/admin/api/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login status=%d body=%s", resp.StatusCode, body)
	}

	// Confirm Set-Cookie header on the login response. (cookiejar.Cookies
	// is path-scoped — /admin cookies aren't returned for /, so checking
	// the jar at the root URL is the wrong assertion. The cluster GET
	// below proves the cookie functions end-to-end.)
	if sc := resp.Header.Get("Set-Cookie"); !strings.Contains(sc, "admin_session=") {
		t.Fatalf("login response missing admin_session cookie: %q", sc)
	}

	// Cluster snapshot via session cookie.
	clusterResp, err := client.Get(ts.URL + "/admin/api/cluster")
	if err != nil {
		t.Fatal(err)
	}
	defer clusterResp.Body.Close()
	if clusterResp.StatusCode != 200 {
		body, _ := io.ReadAll(clusterResp.Body)
		t.Fatalf("cluster status=%d body=%s", clusterResp.StatusCode, body)
	}

	// Lazy validation — body should mention cellCount.
	body2, _ := io.ReadAll(clusterResp.Body)
	if !bytes.Contains(body2, []byte("cellCount")) {
		t.Fatalf("cluster snapshot missing cellCount: %s", body2)
	}
}

func TestAdminE2E_LoginRejected(t *testing.T) {
	t.Parallel()

	p := newE2EProcess(t, false)
	ts, _ := mountE2EAdmin(t, p, "josh", "secret123")

	body, _ := json.Marshal(map[string]string{"username": "josh", "password": "wrong"})
	resp, err := http.Post(ts.URL+"/admin/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestAdminE2E_ClusterRequiresSession(t *testing.T) {
	t.Parallel()

	p := newE2EProcess(t, false)
	ts, _ := mountE2EAdmin(t, p, "josh", "secret123")

	resp, err := http.Get(ts.URL + "/admin/api/cluster")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("expected 401 without session, got %d", resp.StatusCode)
	}
}

// TestAdminE2E_SplitTriggersTopologyEvent exercises the full path: login →
// open SSE stream subscribed to "topology" → invoke cell.split via the
// commands API → observe a topology event arriving on the stream.
func TestAdminE2E_SplitTriggersTopologyEvent(t *testing.T) {
	t.Parallel()

	p := newE2EProcess(t, true)

	// Skip if the cell.split command is not registered in this fixture.
	// At time of writing it is auto-registered by registerCellBuiltins via
	// registerAllBuiltins → New(); the skip is defensive against future
	// re-wiring of the builtin path.
	if _, ok := p.CmdRegistry().Lookup("cell.split"); !ok {
		t.Skip("cell.split command not registered in this fixture")
	}

	ts, _ := mountE2EAdmin(t, p, "josh", "secret123")

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}

	// Login.
	loginBody, _ := json.Marshal(map[string]string{"username": "josh", "password": "secret123"})
	loginResp, err := client.Post(ts.URL+"/admin/api/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatal(err)
	}
	loginResp.Body.Close()
	if loginResp.StatusCode != 200 {
		t.Fatalf("login failed: %d", loginResp.StatusCode)
	}

	// Open the SSE stream subscribed to the topology topic. We use a
	// no-timeout client because the stream is long-lived.
	streamCtx, streamCancel := context.WithCancel(context.Background())
	defer streamCancel()

	streamClient := &http.Client{Jar: jar} // no Timeout — SSE is long-lived
	req, _ := http.NewRequestWithContext(streamCtx, http.MethodGet, ts.URL+"/admin/api/stream?topics=topology,events", nil)
	streamResp, err := streamClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer streamResp.Body.Close()
	if streamResp.StatusCode != 200 {
		body, _ := io.ReadAll(streamResp.Body)
		t.Fatalf("stream status=%d body=%s", streamResp.StatusCode, body)
	}

	// Reader goroutine looking for a topology event.
	gotTopology := make(chan string, 1)
	readErr := make(chan error, 1)
	go func() {
		br := bufio.NewReader(streamResp.Body)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				readErr <- err
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if strings.HasPrefix(line, "event: topology") {
				// next non-empty data line is the payload
				for {
					data, err := br.ReadString('\n')
					if err != nil {
						readErr <- err
						return
					}
					data = strings.TrimRight(data, "\r\n")
					if strings.HasPrefix(data, "data: ") {
						select {
						case gotTopology <- strings.TrimPrefix(data, "data: "):
						default:
						}
						return
					}
					if data == "" {
						break
					}
				}
			}
		}
	}()

	// Give the SSE goroutine a moment to subscribe before firing the split.
	// (If publish fires before Subscribe, TopicBus drops it on the floor.)
	time.Sleep(200 * time.Millisecond)

	// Invoke cell.split via the commands API.
	splitBody, _ := json.Marshal(map[string]string{"CellID": "0_0"})
	splitResp, err := client.Post(ts.URL+"/admin/api/commands/cell.split", "application/json", bytes.NewReader(splitBody))
	if err != nil {
		t.Fatal(err)
	}
	splitBodyResp, _ := io.ReadAll(splitResp.Body)
	splitResp.Body.Close()
	if splitResp.StatusCode != 200 {
		t.Fatalf("cell.split status=%d body=%s", splitResp.StatusCode, splitBodyResp)
	}

	// Wait for the topology event.
	select {
	case payload := <-gotTopology:
		if payload == "" {
			t.Fatalf("got empty topology payload")
		}
		// Smoke check the payload looks like a CommitEvent (mapCommitEvent output).
		var ev map[string]any
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			t.Fatalf("topology payload not JSON: %v\n%s", err, payload)
		}
		if _, ok := ev["kind"]; !ok {
			t.Fatalf("topology payload missing kind field: %s", payload)
		}
	case err := <-readErr:
		t.Fatalf("stream read error before topology event: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for topology event after cell.split")
	}
}
