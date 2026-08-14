# Auth Cookie Hardening — Design

**Status:** Draft for review
**Author:** Josh Stout (with Claude)
**Date:** 2026-05-02
**Related memories:** `feedback_security_best_practices`, `feedback_no_backward_compat`
**Related specs:** [2026-05-01-auth-service-design.md](2026-05-01-auth-service-design.md)

## 1. Summary

The auth service v1 (commits `4955bf1` → `620d874`) shipped session tokens to the web client via a JSON response field, with the client storing the token in `localStorage`. This is the conventional web-app pattern but exposes the token to any JavaScript on the origin — XSS, malicious browser extensions, compromised npm dependencies. None of those threats land a useful attack on a fresh dev build, but the current shape is below industry standard for credential storage and should be fixed before any real users exist.

This spec replaces `localStorage` with **`httpOnly Secure SameSite=Strict` cookies** issued by the auth service and read by the gateway at WebSocket-upgrade time. The token never enters JS scope. Logout clears the cookie; revocation is server-authoritative.

In-scope threats addressed:
- **XSS / supply-chain JS:** token is unreachable from JS; defense moves from "don't have any XSS" to "don't have any XSS *and* gain `chrome.cookies` host permission" — a much higher bar.
- **CSRF:** `SameSite=Strict` blocks cross-origin auto-send. WebSocket upgrades are exempt from SameSite per spec, but the cookie is still sent only by the user's actual browser session.
- **Credential exfiltration via insecure transport:** `Secure` flag refuses the cookie on plain HTTP.

Out-of-scope (deferred):
- Refresh-token rotation (currently a single sliding 30d session token; refresh-token split is a separate hardening with different trade-offs)
- Browser-extension theft via `chrome.cookies` (requires anomaly detection, not a token-storage fix)
- TLS termination / certificate management (deployment concern)

## 2. Goals & non-goals

### Goals

- Move the v1 session token out of JavaScript reach. After this lands, JS in the web client cannot read or directly send the session token.
- Keep the auth wire protocol simple. The auth service itself does not change — it still issues session tokens on Login/Register/ValidateToken responses. The transport between the client and the gateway is what changes.
- Single source of truth: the cookie *is* the session token. No parallel `localStorage` shadow, no "validated also in body."
- Support transparent reconnect on browser refresh — already works today via the AUTH_VALIDATE_TOKEN op; this spec preserves the property without the client touching the token.
- Reset the v1 baseline cleanly. Per `feedback_no_backward_compat`, no migration path for clients holding `localStorage` tokens — they re-login after deploy. Sole-dev deployment: not a real cost.

### Non-goals

- Refresh-token rotation. Logical follow-up but a separate spec.
- Cookie-based session for native (Unity) clients. Cookies on WS upgrades work in any user-agent, including Unity's `UnityWebRequest` / `ClientWebSocket`, but the Unity client doesn't exist yet — the design just stays compatible.
- Cross-domain auth (auth on `auth.example.com`, game on `play.example.com`). v1 single-origin only; cross-domain needs `SameSite=None Secure` + a `domain=` cookie which is a separate decision.
- Removing the `AUTH_VALIDATE_TOKEN` op-channel call. v2 may make WS-upgrade-time cookie validation the only path; for v1 the op-channel call remains as a fallback so the existing reconnect flow works unchanged.

## 3. Threat model summary

| Threat | Pre-spec (localStorage) | Post-spec (httpOnly cookie) |
|---|---|---|
| XSS exfiltrates session token | Trivial (`localStorage.getItem`) | **Mitigated** — token not in JS |
| Compromised npm dep loads payload | Same as XSS | Same as XSS |
| Malicious browser extension reads token | Trivial (any extension page-script) | **Reduced** — needs `cookies` host permission, audited at install |
| CSRF on auth state changes (logout, change-password) | Custom op-channel — not classic CSRF | `SameSite=Strict` blocks cross-origin auto-send |
| Token leak via TLS-stripping proxy | Plain text in JS console / network tab | **Mitigated** — `Secure` cookie won't transmit on HTTP |
| Stolen-cookie replay from another machine | N/A (token in localStorage stays on origin) | **Unchanged** — server still trusts whichever client presents it; mitigated by audit + future per-IP binding |
| Phishing site captures credentials | Phisher fakes the form — cookie storage doesn't help | Out of scope |

## 4. Architecture

### Wire flow today (post-auth-service v1)

```text
Browser                          Gateway                      Auth-service
  │  WS /ws upgrade                  │                            │
  │ ───────────────────────────────▶ │                            │
  │                                  │  (no auth state yet)       │
  │  AUTH_OPCODE_LOGIN {user, pass}  │                            │
  │ ────────── op channel ─────────▶ │ ── route to "auth" ──────▶ │
  │                                  │ ◀───── AuthLoginResponse  ─│
  │                                  │       {user_id, username,
  │                                  │        session_token,
  │                                  │        expires_at_ms}
  │ ◀──── AuthLoginResponse ─────── ◀│
  │                                  │
  │  localStorage.setItem("...")     │
  │  reload → setupLogin reads       │
  │  localStorage, sends              │
  │  AUTH_VALIDATE_TOKEN              │
```

### Wire flow post-spec

```text
Browser                          Gateway                      Auth-service
  │  WS /ws upgrade                  │                            │
  │  Cookie: mmokit-session=...      │                            │
  │ ───────────────────────────────▶ │                            │
  │                                  │  Read cookie value, stash  │
  │                                  │  on connID auth slot       │
  │                                  │  (token only — no DB hit)  │
  │                                  │                            │
  │  AUTH_OPCODE_LOGIN {user, pass}  │                            │
  │ ────────── op channel ─────────▶ │ ── route to "auth" ──────▶ │
  │                                  │ ◀── AuthLoginResponse ─────│
  │                                  │       {user_id, username,
  │                                  │        expires_at_ms}
  │                                  │       (no session_token!)
  │  Set-Cookie: mmokit-session=...  │
  │           HttpOnly Secure        │
  │           SameSite=Strict        │
  │           Max-Age=2592000        │
  │ ◀── AuthLoginResponse ─────── ◀ │
  │  (cookie never enters JS)        │
```

### Two phases per request

Phase 1 — **WS upgrade**: the browser sends the cookie automatically as part of the HTTP/1.1 → WebSocket upgrade. The gateway's WS upgrade handler reads `Cookie: mmokit-session=...`, parses the value, and stashes it on a per-`connID` slot (no DB hit yet — validation happens lazily on first auth op or first authenticated op). If no cookie is present, the connection starts unauthenticated as today.

Phase 2 — **Auth response**: when the auth service returns `AuthLoginResponse`/`AuthRegisterResponse`/`AuthValidateTokenResponse`, the gateway intercepts the response (already wired via `auth.GatewayHook`), strips `session_token` from the payload before forwarding to the client, and writes the cookie via the gateway's WS-control-frame channel — except WS-control frames don't carry HTTP headers. So the cookie is set via a **separate one-shot HTTP `POST /auth/cookie`** call from the client immediately after the auth response arrives.

Wait — this is awkward. Let me revise.

### Better architecture: pre-WS HTTPS bootstrap

```text
Browser                          Gateway / auth                    
  │  POST /auth/login {user, pass}  HTTP                            
  │ ─────────────────────────────────────▶                          
  │                                  Server validates, issues       
  │                                  cookie via Set-Cookie header   
  │ ◀── 200 OK + Set-Cookie ───────────                             
  │     Cookie now in browser jar                                   
  │                                                                 
  │  WS /ws upgrade                                                 
  │  Cookie: mmokit-session=...      (sent automatically)           
  │ ─────────────────────────────────────▶                          
  │                                  Read cookie at upgrade time;   
  │                                  validate against auth-service; │
  │                                  bind connID. Already auth'd    │
  │                                  before any op channel traffic. │
  │                                                                 
  │  No auth ops on the op channel. Game ops only.                  
```

This collapses three round-trips into two and aligns with the §10 note in the original spec ("UDP future" pre-connect HTTPS login pattern). The op-channel auth ops (`LOGIN/REGISTER/VALIDATE_TOKEN`) become **deprecated** — they keep working for backward compat with any non-browser client, but the web client no longer uses them.

This is the cleaner design. v1's op-channel auth was convenient but mixed credential exchange with the gameplay channel — splitting them is also better security hygiene.

### Final shape (recommended)

| Endpoint | Method | Purpose |
|---|---|---|
| `POST /auth/register` | HTTPS | Body: `{username, password, email?}`. Sets `mmokit-session` cookie on success. Returns `{user_id, username, expires_at_ms}` (no token). |
| `POST /auth/login` | HTTPS | Body: `{username, password, mfa_code?}`. Sets cookie. Returns same shape as register. |
| `POST /auth/logout` | HTTPS | Invalidates server-side session, clears cookie via `Set-Cookie: ...; Max-Age=0`. |
| `POST /auth/refresh` | HTTPS | Validates current cookie, slides expiry, issues new cookie. Used by client to extend session before WS connect. |
| `GET /auth/me` | HTTPS | Returns `{user_id, username, expires_at_ms}` if cookie is valid. Used by web client on page load to decide "show spinner + connect WS" vs "show login form". |
| `POST /auth/change-password` | HTTPS | Body: `{current, new}`. Requires valid cookie. |
| `GET /ws` | WS upgrade | Browser sends cookie automatically. Gateway validates at upgrade time; rejects with HTTP 401 if cookie missing/invalid. |

Op-channel `AUTH_OPCODE_*` codes 50-54 stay registered (don't break any existing callers / non-browser clients). They become "alternate login path." All web-client traffic moves to HTTPS endpoints + the cookie.

## 5. Cookie shape

```
Set-Cookie: mmokit-session=<base64url-token>;
            HttpOnly;
            Secure;
            SameSite=Strict;
            Path=/;
            Max-Age=2592000;        # 30d (matches SessionTTL)
```

| Flag | Setting | Why |
|---|---|---|
| `HttpOnly` | yes | JS cannot read; sole change vs. localStorage |
| `Secure` | yes | refuse on plain HTTP — prevents accidental leak through unencrypted dev URLs |
| `SameSite` | `Strict` | block cross-origin sends; WS upgrades are exempt by spec but cross-origin form posts to `/auth/login` are not. `Lax` would be fine; `Strict` is the conservative default and we have no cross-site flow that needs it relaxed. |
| `Path` | `/` | the cookie has to ride both `/auth/*` HTTPS and `/ws` upgrade |
| `Max-Age` | `2592000` (30d) | matches the auth service's `SessionTTL`. Refreshed by `/auth/refresh` and `/auth/me`. |
| `Domain` | unset | single-origin v1; cross-subdomain is v2 |

The cookie value is the **same opaque base64url token** the auth service issues today (32 bytes from `crypto/rand`, SHA-256 hashed in DB). No format change.

## 6. Gateway WS-upgrade auth

The gateway's WS-upgrade handler currently accepts any connection and starts the connID in the unauthenticated state. New behavior:

```go
// pkg/universe/gateway.go (sketch — actual location is the WS handler)
func (g *Gateway) handleWSUpgrade(w http.ResponseWriter, r *http.Request) {
    var token string
    if c, err := r.Cookie(authCookieName); err == nil {
        token = c.Value
    }

    // Validate via auth service. ValidateToken slides the expiry on
    // success — same path that AUTH_OPCODE_VALIDATE_TOKEN took before.
    var resolved *resolvedSession
    if token != "" {
        if v, err := g.authResolver.Validate(r.Context(), token); err == nil {
            resolved = v
        } else {
            // Token revoked / expired / unknown — clear the cookie so
            // the client falls back to the login form on next request.
            clearAuthCookie(w)
        }
    }

    // Continue with the WS upgrade. resolved is nil for fresh
    // (unauthenticated) connects; the client must call POST /auth/login
    // to acquire a cookie before reconnecting.
    conn, err := g.acceptUpgrade(w, r)
    if err != nil { return }

    if resolved != nil {
        g.bindAuthenticatedSession(conn.ID(), resolved)
        g.dispatchPostAuthAssignment(conn.ID(), resolved.UserID, resolved.Username, token)
    }
}
```

`g.authResolver.Validate` is a thin wrapper around the existing `auth.Service.handleValidateToken` that the auth service exposes via a Go-side interface (no op-channel hop when the auth kind is colocated; same MeshData hop in distributed mode as today's `AUTH_OPCODE_VALIDATE_TOKEN`).

If `resolved` is nil (no cookie / invalid cookie), the WS still upgrades but stays unauthenticated. The web client treats this as "show login form" and POSTs to `/auth/login`, which sets the cookie, after which the client reconnects the WS. We don't try to do auth over the op channel post-connect from the web client — that path is reserved for non-browser clients.

## 7. HTTP endpoints

Each `/auth/*` endpoint is a thin HTTP wrapper around the existing service handler. Implementation lives in a new `pkg/auth/http.go`:

```go
// pkg/auth/http.go (sketch)
func (s *Service) RegisterHTTP(mux *http.ServeMux, opts HTTPOpts) {
    mux.Handle("POST /auth/register",        s.httpHandler(s.handleRegister, opts))
    mux.Handle("POST /auth/login",           s.httpHandler(s.handleLogin, opts))
    mux.Handle("POST /auth/logout",          s.httpHandler(s.handleLogout, opts))
    mux.Handle("POST /auth/refresh",         s.httpHandler(s.handleRefresh, opts))
    mux.Handle("POST /auth/change-password", s.httpHandler(s.handleChangePassword, opts))
    mux.Handle("GET /auth/me",               s.httpHandler(s.handleMe, opts))
}

type HTTPOpts struct {
    CookieName   string         // default "mmokit-session"
    CookieDomain string         // empty for single-origin
    CookiePath   string         // default "/"
    CookieSecure bool           // default true; can be false for local dev
    SameSite     http.SameSite  // default http.SameSiteStrictMode
    SessionTTL   time.Duration  // mirrors ServiceOpts.SessionTTL
}
```

The body shapes match the existing op-channel message shapes via JSON. Each handler:

1. Decodes the JSON body
2. Calls the existing `handleLogin` / `handleRegister` / etc. internally — same code path that the op channel uses
3. On success: writes `Set-Cookie: mmokit-session=<token>; ...` and returns the response JSON (without `session_token`)
4. On failure: returns `{error: "...", code: AUTH_ERROR_*}` with appropriate HTTP status

Mounting: the HTTP listener already exists on `RoleGateway` processes (same port as `/ws`). `pkg/auth.RegisterAuthService` adds one line to mount the `/auth/*` routes when the process has `RoleGateway`.

### CSRF posture

`SameSite=Strict` makes traditional CSRF moot for these endpoints — the cookie won't be sent on cross-origin requests at all, so a malicious site can't cause a logout/password-change as the user. We don't need CSRF tokens.

### `GET /auth/me` rate limiting

This endpoint is public-facing and stateless on the client side, so it could be hammered. Reuse the existing `IPRateLimiter` (currently shared with login attempts). The check is "is the cookie valid" — same DB read as `AUTH_OPCODE_VALIDATE_TOKEN`, including the sliding expiry update. So `GET /auth/me` doubles as session refresh on page load.

## 8. Web client changes (`web-pixi/`)

| File | Change |
|---|---|
| `web-pixi/src/auth.ts` | Replace `authLogin` / `authRegister` / `authValidateToken` / `authLogout` op-channel calls with `fetch('/auth/login', ...)` etc. Drop `TOKEN_KEY` and all `localStorage` references. |
| `web-pixi/src/ui/login.ts` | Replace the "try saved token" branch with `fetch('/auth/me')`. On 200 → spinner + skip to game; on 401 → show form. The form's submit handler POSTs to `/auth/login` or `/auth/register`. |
| `web-pixi/src/main.ts` | Logout button calls `fetch('/auth/logout', {method:'POST'})` then `window.location.reload()`. |
| `web-pixi/src/network.ts` | No change — WS upgrade picks up the cookie automatically. |

After this, the web client has no JS-readable session credential anywhere. `localStorage.getItem('mmokit-auth-token')` returns null on every page load.

## 9. Server-side migration

Per `feedback_no_backward_compat`:

- Delete `auth.AuthLoginResponse.session_token` field from the proto. Op-channel callers that aren't the web client (none today) get a non-empty `expires_at_ms` and the `Set-Cookie` cargo. Wait — the op channel has no Set-Cookie equivalent. Decision: **op-channel `AUTH_OPCODE_*` responses keep `session_token` in the response body** (for non-browser clients that don't have a cookie jar). The HTTPS path is what strips it. Two paths, one auth.
- Or **simpler**: deprecate the op-channel auth ops outright. Mark them `// DEPRECATED — use /auth/* HTTPS endpoints` and have the handlers no-op or return a clear error directing to HTTPS. This is the cleanest answer if there's no non-browser client in flight (there isn't yet — Unity client is hypothetical).

**Recommendation:** keep op-channel ops alive but add a `gateway.disableOpChannelAuth` Config flag, default `true` for browser-client deployments, `false` when a non-cookie client is expected. The web client never hits them; deprecation is just removing the proto messages once the Unity client never exists or implements its own cookie / token-bearer pattern.

Actually the simplest is: keep the op-channel ops working unchanged. The HTTP path is additive. Web client moves to HTTP; non-browser clients keep using op-channel; both paths land in the same handlers. No deprecation, no flag.

## 10. Open questions

### 10.1 Op-channel auth: deprecate or keep?

**Recommendation:** keep, additive. The op-channel path costs nothing once the web client doesn't use it. If a non-browser client (Unity) ever exists, it has a clean choice between (a) use the same `/auth/*` HTTPS endpoints with a custom cookie jar, (b) use the op-channel ops with bearer-token semantics. Option (a) is preferred but (b) is fine. Don't lock either out.

### 10.2 Local dev `Secure` flag

`Secure` cookies refuse on plain HTTP. `just dev` runs on `http://localhost:8080`. Without TLS, the cookie is rejected → web client breaks in dev.

Two options:
- **A.** `HTTPOpts.CookieSecure = false` in dev. Default true; `cmd/server` has a `--dev-insecure-cookie` flag that flips it.
- **B.** Run dev with self-signed TLS. `vite` has built-in HTTPS dev mode, and Go's `net/http` can serve self-signed certs from a local file.

**Recommendation:** A for v1 (frictionless dev). B is the right answer once the project has an environment story (staging, prod). The flag stays in `HTTPOpts` so production is `Secure=true` by default.

### 10.3 SameSite=Strict on the WS upgrade

`SameSite=Strict` cookies are NOT sent on cross-site WebSocket upgrades. Same-site WS upgrades (the user is on `https://play.example.com` and the WS goes to `wss://play.example.com/ws`) are fine. Cross-site embeds (the WS is on a different origin from the page that initiated it) would fail.

The web client and WS server share an origin — `Strict` works. If anyone ever embeds the game in a cross-origin iframe and expects to share a session, this breaks. Defer that concern to v2 with a `SameSite=Lax` opt-in.

### 10.4 Cookie name

`mmokit-session`. Engine-tier; consistent across games. Configurable via `HTTPOpts.CookieName` for games that want a branded name.

### 10.5 What happens to `AUTH_VALIDATE_TOKEN` from the web client?

The web client stops sending it. The gateway's WS-upgrade handler does the equivalent (validate the cookie, slide expiry) at upgrade time. The op-channel op stays registered but receives no traffic from the browser.

## 11. Migration & rollout

- Sole-dev solo project with no real users (per `project_opensource_ready`, `user_solo_developer`). Wipe-and-reset is fine.
- Land HTTP endpoints + cookie wiring + web-client migration in one PR-like commit set.
- After deploy, any browser tab with a stored `localStorage` token gets a 401 on `GET /auth/me`, the form shows, the user logs in, the cookie is set. No data migration.
- `auth_sessions` table is unchanged — the storage format is the same, only the wire transport is new.

## 12. Testing strategy

| Layer | What | How |
|---|---|---|
| Unit | `pkg/auth/http.go` handlers: each endpoint returns the right status, headers, body, and `Set-Cookie` flags | `httptest.NewRecorder` + table tests |
| Unit | Cookie parse on WS upgrade rejects malformed values cleanly | Same |
| Integration | `POST /auth/register` then WS upgrade carries the cookie and binds the session | Existing cluster fixture + Go HTTP client with cookie jar |
| Integration | WS upgrade with no cookie → unauthenticated; with revoked cookie → cookie cleared via `Set-Cookie: ...; Max-Age=0` | Same |
| Integration | `POST /auth/logout` clears cookie + revokes session (next WS upgrade has no cookie) | Same |
| Browser smoke | After deploy, `localStorage.getItem('mmokit-auth-token')` returns null on every reachable page; logout button calls `/auth/logout` + reloads | `just dev` + manual |
| Security smoke | `document.cookie` from JS does NOT contain `mmokit-session` (HttpOnly flag enforced) | Browser dev console |

## 13. Future work (deferred from this spec)

- **Refresh-token rotation** (Option B from the original auth-service brainstorm). Short-lived access cookie + long-lived refresh cookie on a separate path. Compromise containment without breaking transparent reconnect.
- **Per-IP session binding.** Bind the session to the IP of the registering request and reject cookie use from a different IP without a re-auth confirmation. Catches stolen cookies; breaks legitimate IP changes (cellular ↔ wifi). Anomaly detection > hard binding.
- **Audit-driven anomaly detection.** Flag concurrent sessions from geographically distant IPs; alert on token use from a fresh IP without a corresponding re-auth.
- **Token-binding via TLS exporter.** RFC 8471 / channel binding ties the cookie to the TLS session, blocking replay. Standard adoption is poor; defer until needed.
- **CSP + SRI on the web client.** Defense in depth against XSS — once we have a third-party-script story.
- **Session revocation propagation.** Today revocation lands in `auth_sessions.revoked_at`; the gateway re-checks on each WS upgrade. A long-lived WS session still alive when an admin revokes won't get kicked until it disconnects + reconnects. v2: subscribe gateways to revocation events and active-kick the WS. (`auth.user.kick` already does this synchronously via the cmdsys path.)
