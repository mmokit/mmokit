# Unit 1 — Client Transport TLS (Design)

**Date:** 2026-06-11
**Status:** Approved
**Parent:** `2026-06-11-security-remediation-umbrella-design.md`
**Closes:** Review finding #3 (no transport TLS; cleartext session cookie on
`ws://`) and the bundled CSWSH exposure (`InsecureSkipVerify: true` on WS
accept).

---

## Goal

Let the engine serve client-facing HTTP/WebSocket traffic over TLS so the
session cookie and all wire frames are encrypted in transit, without adding
friction to local development or to deployments that terminate TLS at an edge
proxy. Remove the cross-site WebSocket hijacking hole on the accept path.

This unit covers **only** the two client-facing HTTP listeners. It does not
touch UDP (Unit 2) or the mesh gRPC (Unit 3).

---

## Affected listeners

Two HTTP listeners, both currently `http.Server{...}.ListenAndServe()`:

1. **Client listener** — `Process.startHTTPListener`
   (`pkg/universe/bootstrap.go:195-207`). Serves `/ws`, `/auth/*`, `/metrics`,
   `/commands`, `/events`, game `HTTPRoutes`. Bound on `RoleGateway` processes.
   Address from `Config.HTTPPort`.
2. **Admin listener** — `Process.startAdminHTTPListener`
   (`pkg/universe/bootstrap.go:255-261`). Serves `/admin/*` + `/metrics` etc.
   Bound on `RoleCoordinator` processes. Address from `Config.AdminListen`
   (default `:9101`). The admin cookie is already `Secure`; serving this
   listener over TLS is what makes that flag meaningful off-loopback.

Both honor the **same** TLS configuration, built by one shared helper.

---

## Configuration model

Three mutually exclusive modes, resolved by a single `tls.Config` builder:

| Condition | Behavior |
|-----------|----------|
| `Config.TLSCertFile` **and** `Config.TLSKeyFile` both set | Load the cert/key pair; serve TLS (`ServeTLS`). Production self-hosted. |
| Neither set (default) | Serve plaintext HTTP. Intended for localhost dev **or** a TLS-terminating edge proxy. |
| `Config.TLSMode == "self-signed"` (e.g. `--tls=self-signed`) | Generate an in-memory self-signed cert; serve TLS. Opt-in, for exercising the TLS path locally. |

New `Config` fields (in `pkg/universe/coordinator.go`'s `Config`, flag-wired in
`bootstrap.go` alongside the existing `stringFlag(...)` calls):

- `TLSCertFile string` — `--tls-cert` (default `""`).
- `TLSKeyFile  string` — `--tls-key` (default `""`).
- `TLSMode     string` — `--tls` (default `""`; accepted: `""`/`auto`, `self-signed`).
  When cert/key files are present they win regardless of `TLSMode`.
- `AllowedWSOrigins []string` — `--ws-allowed-origins` (comma-separated; default
  empty = same-origin only). See CSWSH section.

The same `tls.Config` applies to **both** listeners. There is intentionally no
per-listener TLS override in v1 — one cluster, one TLS posture.

### Resolution helper

A single function (location: `pkg/universe/`, e.g. `tls_config.go`):

```
func (c *Process) buildTLSConfig() (*tls.Config, error)
```

- Files present → `tls.LoadX509KeyPair`; return a `*tls.Config` with that cert.
- `TLSMode == "self-signed"` → `generateDevCert()`; return a `*tls.Config` with
  it; log a prominent `DEV self-signed TLS — DO NOT use in production` banner.
- Otherwise → return `nil` (caller serves plaintext).

Each listener's goroutine:

```
tlsCfg, err := c.buildTLSConfig()   // computed once, shared
...
if tlsCfg != nil {
    srv.TLSConfig = tlsCfg
    err = srv.ListenAndServeTLS("", "")   // certs already in TLSConfig
} else {
    err = srv.ListenAndServe()
}
```

### `generateDevCert()`

A small helper (same file): ECDSA P-256 key via `crypto/ecdsa` +
`crypto/rand`; `x509.CreateCertificate` self-signed; SANs `localhost`,
`127.0.0.1`, `::1`; short validity (e.g. 365d is fine — it never persists);
returned as a `tls.Certificate`. In-memory only, never written to disk. No new
dependencies (all stdlib).

---

## Secure-by-default behavior

Per the locked decision: **warn and continue**, never hard-fail.

At listener startup, if the bind address is **non-loopback** (not `127.0.0.1`/
`::1`/`localhost`/empty-host-on-loopback) **and** `tlsCfg == nil`, emit a single
prominent warning:

```
WARNING: serving plaintext on a non-loopback address (<addr>). The session
cookie and all client traffic are unencrypted on the wire. Configure
--tls-cert/--tls-key, or terminate TLS at a reverse proxy in front of this
listener.
```

Then start normally. Loopback binds stay silent (no network path; this is the
zero-friction `just dev` case).

---

## CSWSH fix (bundled)

The WS accept currently passes `InsecureSkipVerify: true`
(`pkg/net/server.go::HandleWebSocket`), which disables the websocket library's
origin check — a cross-site WebSocket hijacking hole that is live precisely
because authentication rides cookies. A malicious page in a logged-in user's
browser can open a WS to the server and act as that user.

Fix:
- Remove `InsecureSkipVerify: true`.
- Drive the accept's `OriginPatterns` from `Config.AllowedWSOrigins`. Default
  (empty) → same-origin only (the websocket library's default-deny for
  cross-origin).
- **`AllowedWSOrigins` falls back to `CORSOrigins` when unset** (resolved by
  `wsAllowedOrigins(cfg)`). Rationale (revised 2026-06-12 after the original
  "keep them separate" choice silently broke the cross-origin `examples/simple`
  client): an origin an operator already trusts for credentialed cross-origin
  HTTP is one they trust to open a WebSocket, so a cross-origin client needs
  only `--cors-origins`. The trust models are strictly consistent — there is no
  real case for trusting an origin for CORS but blocking its WS upgrade — so the
  coupling removes a two-flag papercut rather than introducing surprise.
  `--ws-allowed-origins` still overrides when set explicitly.
- Localhost dev: the vite-proxied examples (`examples/4node-basic`, `web-pixi`)
  are same-origin through the proxy (`Host`==`Origin`), so unaffected. The
  proxy-less `examples/simple` (page on :5174, WS on :8080) is cross-origin and
  works via the CORS fallback since its run recipe already sets
  `--cors-origins=http://localhost:5174`.

`HandleWebSocket` will need access to the allowed origins — thread
`Config.AllowedWSOrigins` (or a resolved `[]string`) into the `ConnManager` /
accept path. Exact plumbing decided during implementation; the accept call is
the single chokepoint.

---

## Web client

- The web client connects `wss://` when the page is served over `https://`, and
  `ws://` over `http://`. Most setups derive the scheme from
  `window.location.protocol` already; verify and fix the SDK transport
  (`web-pixi/sdk/`, `examples/*/web/sdk/`) during implementation so it is not
  hardcoded to `ws://`.
- Local dev is unchanged: the vite dev proxy keeps targeting `ws://localhost:8080`
  (plaintext on loopback, per the default).

---

## Out of scope

- ACME / Let's Encrypt auto-provisioning (cut as YAGNI in the umbrella).
- UDP transport (Unit 2) — note that Unit 2's session-key handoff *depends* on
  this unit, since the key is delivered over the (now-encrypted) WS channel.
- Mesh gRPC TLS (Unit 3).
- HSTS / cert pinning / TLS version & cipher hardening beyond Go's secure
  defaults (Go's `tls.Config` zero value already negotiates TLS 1.2+ with safe
  suites; we do not weaken it).

---

## Verification

- **Unit test:** `buildTLSConfig()` returns a usable cert for the self-signed
  path and for a files path (using a generated temp pair); returns nil when
  unconfigured.
- **Integration/observation:** with `--tls=self-signed` (or a temp cert pair),
  the client listener answers a TLS handshake on `/ws` and `/auth`; with no TLS
  config it answers plaintext and logs the non-loopback warning when bound off
  loopback.
- **CSWSH:** a WS accept from a disallowed `Origin` is rejected; an allowed/same
  origin succeeds. Cover with a test against `HandleWebSocket`.
- **Manual:** confirm (e.g. via a packet capture or browser devtools over a
  non-loopback bind) that the session cookie no longer transits in cleartext
  once TLS is configured.
- `go vet ./...` and `just build` clean.
