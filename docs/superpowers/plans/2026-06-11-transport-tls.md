# Transport TLS (Unit 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Serve the client-facing HTTP/WebSocket listeners over optional in-process TLS, and close the cross-site WebSocket hijacking hole, so the session cookie and all client traffic can be encrypted in transit without breaking local dev or proxy-terminated deployments.

**Architecture:** Add three TLS config fields + one WS-origin field to `universe.Config`. A small pure helper (`resolveTLSConfig` / `generateDevCert` / `isLoopbackBind`) decides between files-TLS, in-memory self-signed TLS, or plaintext. Both HTTP listeners (`startHTTPListener`, `startAdminHTTPListener`) share one memoized `*tls.Config` and switch to `ListenAndServeTLS` when it's non-nil; a non-loopback plaintext bind logs a warning. Separately, `ConnManager.HandleWebSocket` drops `InsecureSkipVerify` and checks the WS `Origin` against an allowlist (default: same-origin only).

**Tech Stack:** Go stdlib (`crypto/tls`, `crypto/ecdsa`, `crypto/x509`, `net/http`), `github.com/coder/websocket` v1.8.14. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-06-11-transport-tls-design.md`

---

## File Structure

- **Create** `pkg/universe/tls_config.go` — pure TLS resolution + dev-cert generation + loopback predicate + the memoizing `Process` getter. One responsibility: deciding the TLS posture.
- **Create** `pkg/universe/tls_config_test.go` — unit tests for the above.
- **Modify** `pkg/universe/coordinator.go` — add `TLSCertFile`, `TLSKeyFile`, `TLSMode`, `AllowedWSOrigins` to `Config`; add memoization fields to `Process`.
- **Modify** `pkg/universe/bootstrap.go` — flag wiring; both listeners consume the shared TLS config; set `ConnMgr.AllowedOrigins`; insecure-bind warning.
- **Modify** `pkg/net/server.go` — add `ConnManager.AllowedOrigins`; replace `InsecureSkipVerify: true` with `OriginPatterns`.
- **Create** `pkg/net/server_origin_test.go` — CSWSH accept/reject tests.

---

## Task 1: Config fields + flag wiring

**Files:**
- Modify: `pkg/universe/coordinator.go` (Config struct, after `AdminListen` ~line 130)
- Modify: `pkg/universe/bootstrap.go` (flag section ~line 75-90)

- [ ] **Step 1: Add Config fields**

In `pkg/universe/coordinator.go`, immediately after the `AdminListen string` field (~line 130), add:

```go
	// TLSCertFile and TLSKeyFile, when both non-empty, enable in-process TLS
	// on the client and admin HTTP listeners (production self-hosted). When
	// both empty, the listeners serve plaintext (localhost dev or behind a
	// TLS-terminating reverse proxy). Set via --tls-cert / --tls-key.
	TLSCertFile string
	TLSKeyFile  string

	// TLSMode is an opt-in escape hatch for local TLS testing. When set to
	// "self-signed" (and no cert/key files are provided), the listeners serve
	// TLS using an in-memory self-signed cert (SANs: localhost, 127.0.0.1,
	// ::1). Cert/key files always take precedence. Set via --tls.
	TLSMode string

	// AllowedWSOrigins is the WebSocket Origin allowlist for /ws upgrades.
	// Empty (default) means same-origin only. Browser pages served from a
	// different origin than the WS endpoint must be listed here. Native /
	// non-browser clients (no Origin header) are always allowed. Set via
	// --ws-allowed-origins (comma-separated).
	AllowedWSOrigins []string
```

- [ ] **Step 2: Wire the flags**

In `pkg/universe/bootstrap.go`, after the `admin-listen` `stringFlag(...)` block (~line 78), add:

```go
	stringFlag("tls-cert",
		"path to TLS certificate file for the client/admin HTTP listeners (requires --tls-key; empty = plaintext or proxy-terminated)",
		"", &c.TLSCertFile)
	stringFlag("tls-key",
		"path to TLS private key file (requires --tls-cert)",
		"", &c.TLSKeyFile)
	stringFlag("tls",
		"TLS mode when no cert/key files are set: \"\" (plaintext) or \"self-signed\" (in-memory dev cert)",
		"", &c.TLSMode)
	flag.Func("ws-allowed-origins",
		"comma-separated WebSocket Origin allowlist (empty = same-origin only)",
		func(s string) error {
			parts := strings.Split(s, ",")
			out := make([]string, 0, len(parts))
			for _, p := range parts {
				if p = strings.TrimSpace(p); p != "" {
					out = append(out, p)
				}
			}
			c.AllowedWSOrigins = out
			return nil
		})
```

(`flag` and `strings` are already imported in this file — the `services` flag uses the identical pattern.)

- [ ] **Step 3: Verify it compiles**

Run: `go vet ./pkg/universe/`
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/coordinator.go pkg/universe/bootstrap.go
git commit -m "feat(tls): add TLS + WS-origin config fields and flags"
```

---

## Task 2: TLS resolution helper (TDD)

**Files:**
- Create: `pkg/universe/tls_config.go`
- Test: `pkg/universe/tls_config_test.go`

- [ ] **Step 1: Write the failing tests**

Create `pkg/universe/tls_config_test.go`:

```go
package universe

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func writeTempKeyPair(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	cert, err := generateDevCert()
	if err != nil {
		t.Fatalf("generateDevCert: %v", err)
	}
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	certOut, err := os.Create(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}); err != nil {
		t.Fatal(err)
	}
	certOut.Close()
	keyBytes, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyOut, err := os.Create(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes}); err != nil {
		t.Fatal(err)
	}
	keyOut.Close()
	return certPath, keyPath
}

func TestGenerateDevCert(t *testing.T) {
	cert, err := generateDevCert()
	if err != nil {
		t.Fatalf("generateDevCert: %v", err)
	}
	if len(cert.Certificate) == 0 {
		t.Fatal("no certificate bytes")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	found := false
	for _, d := range leaf.DNSNames {
		if d == "localhost" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected localhost in DNSNames, got %v", leaf.DNSNames)
	}
	if len(leaf.IPAddresses) == 0 {
		t.Error("expected loopback IP SANs")
	}
}

func TestResolveTLSConfig_Plaintext(t *testing.T) {
	cfg, self, err := resolveTLSConfig("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		t.Error("expected nil tls.Config for plaintext")
	}
	if self {
		t.Error("expected selfSigned=false")
	}
}

func TestResolveTLSConfig_SelfSigned(t *testing.T) {
	cfg, self, err := resolveTLSConfig("", "", "self-signed")
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil || len(cfg.Certificates) == 0 {
		t.Fatal("expected a certificate")
	}
	if !self {
		t.Error("expected selfSigned=true")
	}
}

func TestResolveTLSConfig_Files(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeTempKeyPair(t, dir)
	cfg, self, err := resolveTLSConfig(certPath, keyPath, "")
	if err != nil {
		t.Fatalf("load files: %v", err)
	}
	if cfg == nil || len(cfg.Certificates) == 0 {
		t.Fatal("expected a certificate")
	}
	if self {
		t.Error("expected selfSigned=false for explicit files")
	}
}

func TestResolveTLSConfig_FilesWinOverMode(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeTempKeyPair(t, dir)
	_, self, err := resolveTLSConfig(certPath, keyPath, "self-signed")
	if err != nil {
		t.Fatal(err)
	}
	if self {
		t.Error("explicit files must win over self-signed mode")
	}
}

func TestIsLoopbackBind(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8080":   true,
		"localhost:8080":   true,
		"[::1]:8080":       true,
		":8080":            false,
		"0.0.0.0:8080":     false,
		"192.168.1.5:9101": false,
	}
	for addr, want := range cases {
		if got := isLoopbackBind(addr); got != want {
			t.Errorf("isLoopbackBind(%q)=%v want %v", addr, got, want)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./pkg/universe/ -run 'TestGenerateDevCert|TestResolveTLSConfig|TestIsLoopbackBind' -v`
Expected: FAIL — `undefined: generateDevCert`, `undefined: resolveTLSConfig`, `undefined: isLoopbackBind`.

- [ ] **Step 3: Write the implementation**

Create `pkg/universe/tls_config.go`:

```go
package universe

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"time"
)

// resolveTLSConfig decides the TLS posture for the client-facing HTTP
// listeners. Precedence:
//  1. Explicit cert/key files (both non-empty) -> load them.
//  2. mode == "self-signed" -> generate an in-memory dev cert.
//  3. otherwise -> nil (serve plaintext).
//
// The bool return reports whether the returned config uses a self-signed dev
// cert, so the caller can log the "not for production" banner.
func resolveTLSConfig(certFile, keyFile, mode string) (*tls.Config, bool, error) {
	if certFile != "" && keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, false, fmt.Errorf("load TLS keypair: %w", err)
		}
		return &tls.Config{Certificates: []tls.Certificate{cert}}, false, nil
	}
	if mode == "self-signed" {
		cert, err := generateDevCert()
		if err != nil {
			return nil, false, fmt.Errorf("generate dev cert: %w", err)
		}
		return &tls.Config{Certificates: []tls.Certificate{cert}}, true, nil
	}
	return nil, false, nil
}

// generateDevCert builds an in-memory self-signed certificate valid for
// localhost / 127.0.0.1 / ::1. Never written to disk. Dev/testing only.
func generateDevCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "mmoserver-dev"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// isLoopbackBind reports whether addr binds only the loopback interface.
// An empty host (":8080") binds all interfaces, so it is NOT loopback-only.
func isLoopbackBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	switch host {
	case "":
		return false
	case "localhost":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// httpTLSConfig resolves the shared TLS config once and memoizes it, so both
// HTTP listeners present the same certificate. A configuration error is logged
// and falls back to plaintext (which then triggers the non-loopback warning).
func (c *Process) httpTLSConfig() (*tls.Config, bool) {
	c.tlsOnce.Do(func() {
		cfg, selfSigned, err := resolveTLSConfig(c.cfg.TLSCertFile, c.cfg.TLSKeyFile, c.cfg.TLSMode)
		if err != nil {
			c.Log.Log(CatMeshCell, "tls: configuration error (serving plaintext): %v", err)
		}
		c.tlsConfig = cfg
		c.tlsSelfSigned = selfSigned
	})
	return c.tlsConfig, c.tlsSelfSigned
}
```

- [ ] **Step 4: Add the memoization fields to Process**

In `pkg/universe/coordinator.go`, near the `httpServer *http.Server` field (~line 574), add:

```go
	tlsOnce       sync.Once
	tlsConfig     *tls.Config
	tlsSelfSigned bool
```

Ensure `sync` and `crypto/tls` are imported in `coordinator.go` (add to the import block if missing).

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./pkg/universe/ -run 'TestGenerateDevCert|TestResolveTLSConfig|TestIsLoopbackBind' -v`
Expected: PASS (all subtests).

- [ ] **Step 6: Commit**

```bash
git add pkg/universe/tls_config.go pkg/universe/tls_config_test.go pkg/universe/coordinator.go
git commit -m "feat(tls): TLS resolution helper + dev cert generation"
```

---

## Task 3: Wire the client HTTP listener to TLS + insecure-bind warning

**Files:**
- Modify: `pkg/universe/bootstrap.go` (`startHTTPListener`, ~line 179 and ~line 194-207)

- [ ] **Step 1: Set the WS origin allowlist before the /ws handler is registered**

In `startHTTPListener`, find the line registering the WS handler (~line 179):

```go
	mux.HandleFunc("/ws", c.ConnMgr.HandleWebSocket)
```

Immediately **before** it, add:

```go
	c.ConnMgr.AllowedOrigins = c.cfg.AllowedWSOrigins
```

(The `ConnManager.AllowedOrigins` field is added in Task 5. If implementing strictly in order, this line will not compile until Task 5 — do Task 5's struct-field step first, or accept a transient build break resolved by Task 5. Recommended: complete Task 5 Step 3's field addition before running the build in this task.)

- [ ] **Step 2: Switch the listener to optional TLS with the warning**

Replace the block (~line 194-207) that currently reads:

```go
	addr := fmt.Sprintf(":%d", c.cfg.HTTPPort)
	c.httpServer = &http.Server{Addr: addr, Handler: corsMiddleware(c.cfg.CORSOrigins, mux)}
	if c.cfg.CORSOrigins != "" {
		c.Log.Log(CatMeshCell, "http: CORS enabled for origins: %s", c.cfg.CORSOrigins)
	}
	c.Log.Log(CatMeshCell, "http: listening on %s (roles=%s)", addr, c.roles)

	go func() {
		err := c.httpServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.Log.Log(CatMeshCell, "http: listener error: %v", err)
		}
	}()
```

with:

```go
	addr := fmt.Sprintf(":%d", c.cfg.HTTPPort)
	tlsCfg, selfSigned := c.httpTLSConfig()
	c.httpServer = &http.Server{
		Addr:      addr,
		Handler:   corsMiddleware(c.cfg.CORSOrigins, mux),
		TLSConfig: tlsCfg,
	}
	if c.cfg.CORSOrigins != "" {
		c.Log.Log(CatMeshCell, "http: CORS enabled for origins: %s", c.cfg.CORSOrigins)
	}
	if tlsCfg == nil && !isLoopbackBind(addr) {
		c.Log.Log(CatMeshCell, "http: WARNING serving plaintext on non-loopback address %s — session cookie and client traffic are unencrypted; set --tls-cert/--tls-key or terminate TLS at a reverse proxy", addr)
	}
	if selfSigned {
		c.Log.Log(CatMeshCell, "http: WARNING using in-memory self-signed TLS cert (--tls=self-signed) — DO NOT use in production")
	}
	c.Log.Log(CatMeshCell, "http: listening on %s (roles=%s, tls=%v)", addr, c.roles, tlsCfg != nil)

	go func() {
		var err error
		if tlsCfg != nil {
			err = c.httpServer.ListenAndServeTLS("", "")
		} else {
			err = c.httpServer.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.Log.Log(CatMeshCell, "http: listener error: %v", err)
		}
	}()
```

(`ListenAndServeTLS("", "")` uses the certs already placed in `TLSConfig`; the empty path args are correct.)

- [ ] **Step 3: Verify it compiles**

Run: `go vet ./pkg/universe/`
Expected: no errors (requires Task 5's `AllowedOrigins` field to exist).

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/bootstrap.go
git commit -m "feat(tls): client HTTP listener serves optional TLS + non-loopback plaintext warning"
```

---

## Task 4: Wire the admin HTTP listener to the same TLS config

**Files:**
- Modify: `pkg/universe/bootstrap.go` (`startAdminHTTPListener`, ~line 255-261)

- [ ] **Step 1: Switch the admin listener to optional TLS**

In `startAdminHTTPListener`, replace the block (~line 255-261) that currently reads:

```go
	c.adminHTTPServer = &http.Server{Addr: c.cfg.AdminListen, Handler: mux}
	c.Log.Log(CatMeshCell, "admin-http: listening on %s (roles=%s)", c.cfg.AdminListen, c.roles)

	go func() {
		err := c.adminHTTPServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
```

with:

```go
	tlsCfg, _ := c.httpTLSConfig()
	c.adminHTTPServer = &http.Server{Addr: c.cfg.AdminListen, Handler: mux, TLSConfig: tlsCfg}
	if tlsCfg == nil && !isLoopbackBind(c.cfg.AdminListen) {
		c.Log.Log(CatMeshCell, "admin-http: WARNING serving plaintext on non-loopback address %s — admin session cookie is unencrypted; set --tls-cert/--tls-key or terminate TLS at a reverse proxy", c.cfg.AdminListen)
	}
	c.Log.Log(CatMeshCell, "admin-http: listening on %s (roles=%s, tls=%v)", c.cfg.AdminListen, c.roles, tlsCfg != nil)

	go func() {
		var err error
		if tlsCfg != nil {
			err = c.adminHTTPServer.ListenAndServeTLS("", "")
		} else {
			err = c.adminHTTPServer.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
```

(Leave the closing lines of the goroutine — the `c.Log.Log(... "admin-http: listener error" ...)` and braces — unchanged.)

- [ ] **Step 2: Verify it compiles**

Run: `go vet ./pkg/universe/`
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/bootstrap.go
git commit -m "feat(tls): admin HTTP listener shares the client TLS config"
```

---

## Task 5: CSWSH fix — WS origin allowlist (TDD)

**Files:**
- Modify: `pkg/net/server.go` (`ConnManager` struct ~line 35-49; `HandleWebSocket` ~line 220-223)
- Test: `pkg/net/server_origin_test.go`

- [ ] **Step 1: Write the failing tests**

Create `pkg/net/server_origin_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./pkg/net/ -run TestHandleWebSocket -v`
Expected: FAIL — `cm.AllowedOrigins undefined` (compile error). After the field is added but before the accept change, `TestHandleWebSocket_RejectsCrossOrigin` would fail because `InsecureSkipVerify: true` still accepts everything.

- [ ] **Step 3: Add the field and fix the accept path**

In `pkg/net/server.go`, add to the `ConnManager` struct (after the `OnUpgrade` field, ~line 48):

```go
	// AllowedOrigins is the WebSocket Origin allowlist passed to
	// websocket.Accept's OriginPatterns. Empty = same-origin only. Requests
	// with no Origin header (native/non-browser clients) are always allowed.
	AllowedOrigins []string
```

Then replace `HandleWebSocket`'s accept call (~line 221-223):

```go
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // Allow any origin for development
	})
```

with:

```go
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: cm.AllowedOrigins,
	})
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/net/ -run TestHandleWebSocket -v`
Expected: PASS — cross-origin rejected; same-origin, no-Origin, and allowlisted origin accepted.

- [ ] **Step 5: Commit**

```bash
git add pkg/net/server.go pkg/net/server_origin_test.go
git commit -m "fix(net): enforce WebSocket Origin allowlist (drop InsecureSkipVerify)"
```

---

## Task 6: Verify the web client TLS scheme derivation (no code change expected)

**Files:**
- Inspect: `examples/4node-basic/web/src/config.ts:63`, `web-pixi/src/config.ts:56`, `web-pixi/src/ui/dev-overlay.ts:247`

- [ ] **Step 1: Confirm scheme is derived from page protocol**

Run: `grep -rn "location.protocol" examples/4node-basic/web/src/config.ts web-pixi/src/config.ts web-pixi/src/ui/dev-overlay.ts`
Expected: each builds the WS scheme as `window.location.protocol === "https:" ? "wss:" : "ws:"`. No hardcoded `ws://`.

If any connection point hardcodes `ws://`, change it to the protocol-derived form shown above and commit. Otherwise this task is a no-op confirmation — the web clients already emit `wss://` when served over HTTPS. The vite dev proxy (`examples/4node-basic/web/vite.config.ts`) stays on `ws://localhost:8080` (loopback plaintext, per the default).

- [ ] **Step 2: Commit (only if a change was needed)**

```bash
git add <changed file>
git commit -m "fix(web): derive WebSocket scheme from page protocol"
```

---

## Task 7: Final verification

- [ ] **Step 1: Run the full new test set**

Run: `go test ./pkg/universe/ ./pkg/net/ -run 'TestGenerateDevCert|TestResolveTLSConfig|TestIsLoopbackBind|TestHandleWebSocket' -v`
Expected: all PASS.

- [ ] **Step 2: Vet the whole tree**

Run: `go vet ./...`
Expected: no errors. (Do NOT use `go build ./...` — per CLAUDE.md it drops binaries in package dirs.)

- [ ] **Step 3: Full build**

Run: `just build`
Expected: builds `bin/server` and regenerates SDKs cleanly, no errors.

- [ ] **Step 4: Manual smoke (optional, run locally — not in this session)**

Self-signed TLS path:
```bash
./bin/server --tls=self-signed
```
Expected log lines: `http: WARNING using in-memory self-signed TLS cert ...` and `http: listening on :8080 (roles=..., tls=true)`. A `curl -k https://localhost:8080/...` completes a TLS handshake.

Plaintext non-loopback warning:
```bash
./bin/server   # default plaintext; if bound to a non-loopback addr, expect the WARNING line
```

---

## Self-Review Notes

- **Spec coverage:** config model (Task 1), `buildTLSConfig`/`generateDevCert`/`isLoopbackBind` (Task 2), both listeners (Tasks 3-4), non-loopback warning (Tasks 3-4), CSWSH/`InsecureSkipVerify` removal + origin allowlist (Task 5), web client `wss://` (Task 6). All spec sections mapped.
- **Cross-task type consistency:** `ConnManager.AllowedOrigins []string` (Task 5) is consumed in Task 3; `httpTLSConfig()` and `isLoopbackBind` (Task 2) are consumed in Tasks 3-4; `resolveTLSConfig` returns `(*tls.Config, bool, error)` consistently. Note the Task 3↔Task 5 ordering dependency is called out explicitly in Task 3 Step 1.
- **Out of scope (per spec):** ACME, UDP (Unit 2), mesh TLS (Unit 3), HSTS/cipher hardening.
