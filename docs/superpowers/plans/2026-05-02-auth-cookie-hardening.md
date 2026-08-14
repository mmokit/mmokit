# Auth Cookie Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the v1 auth session token out of `localStorage` and into an `httpOnly Secure SameSite=Strict` cookie issued by new HTTPS `/auth/*` endpoints; gateway validates the cookie at WebSocket-upgrade time. Web client never touches the token from JavaScript.

**Architecture:** Additive HTTPS endpoints alongside the existing op-channel auth ops (which keep working for non-browser callers). The gateway's WS-upgrade handler reads the cookie, validates via the existing `auth.Service` repo path, and binds the connID before any op traffic. Dev mode opts out of `Secure` via a `--dev-insecure-cookie` flag plumbed into both `cmd/server` and `examples/4node-basic`.

**Tech Stack:** Go 1.23+, Go's `net/http` for endpoints, the existing `pkg/auth/` service handlers (no business-logic duplication), TypeScript fetch API on the client. Reference spec: [docs/superpowers/specs/2026-05-02-auth-cookie-hardening-design.md](../specs/2026-05-02-auth-cookie-hardening-design.md).

---

## File-structure overview

**New:**
- `pkg/auth/http.go` — `RegisterHTTP(mux, opts)` plus six handler funcs (login/register/logout/refresh/me/change-password)
- `pkg/auth/http_test.go` — unit tests using `httptest.NewRecorder`
- `pkg/auth/cookie.go` — `setAuthCookie` / `clearAuthCookie` helpers + `HTTPOpts` type
- `pkg/auth/resolver.go` — `Resolver` interface and `Service.Resolve(token)` adapter (in-process, no op-channel hop)

**Modified:**
- `pkg/auth/kind.go` — add `HTTPOpts` field to `ServiceOpts`
- `pkg/auth/service.go` — implement `Resolver` interface on `*Service`; expose via `OnReady` so the gateway can call it
- `pkg/mmokit/auth.go` — wire `RegisterHTTP` into `Config.HTTPRoutes` hook; expose `HTTPOpts` via the facade; expose `Resolver` to gateway via process-level slot
- `pkg/universe/coordinator.go` — add `cfg.AuthResolver` slot; small accessor for the gateway
- `pkg/universe/gateway.go` — WS-upgrade handler reads cookie + validates + binds session
- `cmd/server/main.go` — `--dev-insecure-cookie` flag + pass to `AuthOpts.HTTPOpts.CookieSecure`
- `examples/4node-basic/main.go` — same flag + `RegisterAuthService` call (currently doesn't have it!)
- `justfile` — `dev` recipe passes `--dev-insecure-cookie`
- `examples/4node-basic/justfile` — `dev` recipe passes `--dev-insecure-cookie`
- `web-pixi/src/auth.ts` — replace op-channel calls with `fetch('/auth/*')`; drop `localStorage` token
- `web-pixi/src/ui/login.ts` — `fetch('/auth/me')` instead of op-channel `authValidateToken`
- `web-pixi/src/main.ts` — logout button calls `fetch('/auth/logout')`
- `examples/4node-basic/web/...` — only if 4node-basic also has a login flow (verify; likely no-op since 4node-basic uses LoginHandler-less mode currently)

**No proto changes.** The cookie value is the same opaque base64url token the auth service already issues.

---

## Phase A — HTTPS endpoint scaffold (additive, no consumer yet)

### Task 1: `HTTPOpts` + cookie helpers

**Files:**
- Create: `pkg/auth/cookie.go`

- [ ] **Step 1: Create `pkg/auth/cookie.go`**

```go
package auth

import (
	"net/http"
	"time"
)

// HTTPOpts configures the cookie shape and HTTP endpoint behavior.
// Defaults match the spec: HttpOnly=true, Secure=true, SameSite=Strict,
// Path="/", Max-Age=SessionTTL.
type HTTPOpts struct {
	// CookieName is the cookie key. Defaults to "mmokit-session".
	CookieName string
	// CookiePath is the cookie path scope. Defaults to "/".
	// Must include both the auth endpoints and the WS upgrade path.
	CookiePath string
	// CookieDomain is the cookie domain. Empty = single-origin
	// (Domain attribute omitted, scoped to the request host).
	CookieDomain string
	// CookieSecure controls the Secure flag. Default true; flip to
	// false only for plain-HTTP local dev (--dev-insecure-cookie).
	CookieSecure bool
	// SameSite is the SameSite flag. Defaults to http.SameSiteStrictMode.
	SameSite http.SameSite
}

// DefaultHTTPOpts returns production-safe cookie defaults.
func DefaultHTTPOpts() HTTPOpts {
	return HTTPOpts{
		CookieName:   "mmokit-session",
		CookiePath:   "/",
		CookieSecure: true,
		SameSite:     http.SameSiteStrictMode,
	}
}

// effective returns o with empty fields filled from DefaultHTTPOpts.
// Used so callers can override only the fields they care about.
func (o HTTPOpts) effective() HTTPOpts {
	d := DefaultHTTPOpts()
	if o.CookieName == "" {
		o.CookieName = d.CookieName
	}
	if o.CookiePath == "" {
		o.CookiePath = d.CookiePath
	}
	if o.SameSite == 0 {
		o.SameSite = d.SameSite
	}
	// CookieSecure false is a meaningful override (dev mode), so we
	// don't auto-fill from default. Callers must set it explicitly
	// or rely on the zero value (false). The facade's
	// RegisterAuthService fills it from DefaultHTTPOpts when the
	// caller didn't touch HTTPOpts at all.
	return o
}

// setAuthCookie writes a Set-Cookie header carrying the session token.
// ttl mirrors auth.ServiceOpts.SessionTTL.
func setAuthCookie(w http.ResponseWriter, opts HTTPOpts, token string, ttl time.Duration) {
	o := opts.effective()
	http.SetCookie(w, &http.Cookie{
		Name:     o.CookieName,
		Value:    token,
		Path:     o.CookiePath,
		Domain:   o.CookieDomain,
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   o.CookieSecure,
		SameSite: o.SameSite,
	})
}

// clearAuthCookie writes a Set-Cookie header that immediately expires
// the session cookie on the client.
func clearAuthCookie(w http.ResponseWriter, opts HTTPOpts) {
	o := opts.effective()
	http.SetCookie(w, &http.Cookie{
		Name:     o.CookieName,
		Value:    "",
		Path:     o.CookiePath,
		Domain:   o.CookieDomain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   o.CookieSecure,
		SameSite: o.SameSite,
	})
}

// readAuthCookie returns the session token from the request, or "" if
// the cookie is absent. Used by the gateway's WS-upgrade handler.
func readAuthCookie(r *http.Request, opts HTTPOpts) string {
	o := opts.effective()
	c, err := r.Cookie(o.CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go vet ./pkg/auth/...`
Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add pkg/auth/cookie.go
git commit -m "feat(auth): HTTPOpts + cookie helpers (set/clear/read)

Cookie shape per spec: HttpOnly + Secure + SameSite=Strict + Path=/ +
Max-Age=SessionTTL. CookieSecure defaults to true; only the facade
fills defaults when HTTPOpts is fully zero — callers passing partial
opts keep the explicit false (dev mode) intact."
```

---

### Task 2: `Resolver` interface + `Service.Resolve`

**Files:**
- Create: `pkg/auth/resolver.go`
- Modify: `pkg/auth/service.go`

The gateway needs to validate cookies at WS-upgrade time without going through the op channel. We expose the same code path that `handleValidateToken` uses (token hash → DB lookup → slide expiry) as a typed Go method.

- [ ] **Step 1: Create `pkg/auth/resolver.go`**

```go
package auth

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ResolvedSession is the result of a successful Resolve call.
type ResolvedSession struct {
	UserID    uuid.UUID
	Username  string
	ExpiresAt time.Time
}

// Resolver is the typed Go path for validating a session token outside
// the op-channel handler flow. The gateway uses it on WS upgrade.
type Resolver interface {
	Resolve(ctx context.Context, token string) (*ResolvedSession, error)
}

// ErrTokenInvalid is returned when the token is unknown / revoked /
// expired. Callers should clear the cookie and treat the connection as
// unauthenticated.
var ErrTokenInvalid = errors.New("auth: token invalid")
```

- [ ] **Step 2: Implement `Service.Resolve`**

Append to `pkg/auth/service.go`:

```go
// Resolve validates a session token and slides its expiry. Mirrors
// handleValidateToken's logic minus the op-channel envelope; called by
// the gateway at WS-upgrade time. Returns ErrTokenInvalid for any
// recoverable failure (unknown / revoked / expired); any other error
// is an infra failure that should bubble.
func (s *Service) Resolve(ctx context.Context, token string) (*ResolvedSession, error) {
	if token == "" {
		return nil, ErrTokenInvalid
	}
	h := HashToken(token)
	sess, err := s.repo.GetSession(ctx, h)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return nil, ErrTokenInvalid
		}
		return nil, err
	}
	if !sess.RevokedAt.IsZero() {
		return nil, ErrTokenInvalid
	}
	if time.Now().After(sess.ExpiresAt) {
		return nil, ErrTokenInvalid
	}
	user, err := s.repo.GetUserByID(ctx, sess.UserID)
	if err != nil {
		return nil, err
	}
	if user.Status == "disabled" || (!user.LockedUntil.IsZero() && user.LockedUntil.After(time.Now())) {
		return nil, ErrTokenInvalid
	}
	newExp := time.Now().Add(s.opts.SessionTTL)
	if err := s.repo.SlideSession(ctx, h, newExp); err != nil {
		return nil, err
	}
	return &ResolvedSession{
		UserID:    user.UserID,
		Username:  user.Username,
		ExpiresAt: newExp,
	}, nil
}
```

Add at top of file alongside other interface assertions:

```go
var _ Resolver = (*Service)(nil)
```

- [ ] **Step 3: Verify**

Run: `go vet ./pkg/auth/...`
Expected: clean.

Run: `go test ./pkg/auth/ -count=1`
Expected: all existing tests still PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/auth/resolver.go pkg/auth/service.go
git commit -m "feat(auth): Resolver interface + Service.Resolve for in-process cookie validation

Same DB path as handleValidateToken (GetSession → revocation/expiry/status
checks → SlideSession) without the op-channel envelope. Gateway will use
this at WS-upgrade time before the op channel is even live. ErrTokenInvalid
is the typed sentinel for 'clear the cookie and treat as unauthenticated'."
```

---

### Task 3: HTTP handler infrastructure + `POST /auth/login`

**Files:**
- Create: `pkg/auth/http.go`

- [ ] **Step 1: Create `pkg/auth/http.go` with handler scaffolding + login**

```go
package auth

import (
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"

	enginepb "github.com/zenion/mmokit/gen/go/enginepb"
)

// RegisterHTTP mounts the auth service's HTTPS endpoints on the given
// mux. Must be called after Service.Init has resolved the live
// Repository (typically via ServiceOpts.OnReady on a process that
// hosts the auth kind).
//
// Routes mounted (Path is opts.CookiePath stripped; default mounts
// directly under /auth):
//
//	POST /auth/login
//	POST /auth/register
//	POST /auth/logout
//	POST /auth/refresh
//	POST /auth/change-password
//	GET  /auth/me
//
// Request bodies are JSON; response bodies are JSON. session_token is
// NEVER in any HTTP response body — it rides only in the Set-Cookie
// header.
func (s *Service) RegisterHTTP(mux *http.ServeMux, opts HTTPOpts) {
	mux.Handle("POST /auth/login", s.httpHandle(opts, s.httpLogin))
	mux.Handle("POST /auth/register", s.httpHandle(opts, s.httpRegister))
	mux.Handle("POST /auth/logout", s.httpHandle(opts, s.httpLogout))
	mux.Handle("POST /auth/refresh", s.httpHandle(opts, s.httpRefresh))
	mux.Handle("POST /auth/change-password", s.httpHandle(opts, s.httpChangePassword))
	mux.Handle("GET /auth/me", s.httpHandle(opts, s.httpMe))
}

// httpHandle wraps an inner handler with shared logging + recovery.
// Inner handlers return (status, body, err); err is logged and a 500
// is sent to the client.
func (s *Service) httpHandle(opts HTTPOpts, fn func(w http.ResponseWriter, r *http.Request, opts HTTPOpts) (int, any, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, body, err := fn(w, r, opts)
		if err != nil {
			s.ctx.Logger.Log(logCat, "http handler %s %s: %v", r.Method, r.URL.Path, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if body != nil {
			_ = json.NewEncoder(w).Encode(body)
		}
	})
}

// httpClientIP extracts a netip.Addr from r.RemoteAddr (or
// X-Forwarded-For when TrustedProxyHeader is set).
func httpClientIP(r *http.Request, trustProxy bool) netip.Addr {
	if trustProxy {
		if h := r.Header.Get("X-Forwarded-For"); h != "" {
			parts := strings.Split(h, ",")
			if a, err := netip.ParseAddr(strings.TrimSpace(parts[0])); err == nil {
				return a
			}
		}
	}
	if r.RemoteAddr == "" {
		return netip.Addr{}
	}
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr()
	}
	if a, err := netip.ParseAddr(r.RemoteAddr); err == nil {
		return a
	}
	return netip.Addr{}
}

// authErrorJSON is the shape of an error response body. The
// HTTP-friendly error code is included alongside the AuthError enum
// value so clients have a stable mapping.
type authErrorJSON struct {
	Code    enginepb.AuthError `json:"code"`
	Message string             `json:"message"`
	// RetryAfterMs is non-zero on RATE_LIMITED and ACCOUNT_LOCKED.
	RetryAfterMs int64 `json:"retry_after_ms,omitempty"`
}

// loginResponseJSON is what /auth/login + /auth/register return on
// success. session_token is intentionally absent — the cookie carries
// it.
type loginResponseJSON struct {
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	ExpiresAtMs int64  `json:"expires_at_ms"`
}

// loginRequestJSON mirrors AuthLoginRequest.
type loginRequestJSON struct {
	Username string `json:"username"`
	Password string `json:"password"`
	MFACode  string `json:"mfa_code,omitempty"`
}

func (s *Service) httpLogin(w http.ResponseWriter, r *http.Request, opts HTTPOpts) (int, any, error) {
	var req loginRequestJSON
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return http.StatusBadRequest, authErrorJSON{
			Code: enginepb.AuthError_AUTH_ERROR_INTERNAL, Message: "bad request body",
		}, nil
	}
	// Build an op-context-shaped struct so we can reuse handleLogin's
	// internal helpers. ConnID is irrelevant for HTTP path; ClientIP +
	// (later) Bag are what matter.
	opCtx := newHTTPOpCtx(r, s.opts.TrustedProxyHeader)
	resp, err := s.handleLogin(opCtx, &enginepb.AuthLoginRequest{
		Username: req.Username, Password: req.Password, MfaCode: req.MFACode,
	})
	if err != nil {
		return httpStatusFromAuthError(err), authJSONFromError(err), nil
	}
	setAuthCookie(w, opts, resp.SessionToken, s.opts.SessionTTL)
	return http.StatusOK, loginResponseJSON{
		UserID: resp.UserId, Username: resp.Username, ExpiresAtMs: resp.ExpiresAtMs,
	}, nil
}

// (Other handlers land in subsequent tasks.)
```

- [ ] **Step 2: Add helpers `newHTTPOpCtx`, `httpStatusFromAuthError`, `authJSONFromError`**

Append to `pkg/auth/http.go`:

```go
import (
	"github.com/zenion/mmokit/pkg/ops"
)

// newHTTPOpCtx builds an OpContext for the HTTP path. ConnID is 0 (no
// WS connection); ClientIP is populated from the request; Bag is left
// empty (logout/change-password get the bound session token from the
// cookie via httpSessionToken instead).
func newHTTPOpCtx(r *http.Request, trustProxy bool) *ops.OpContext {
	return &ops.OpContext{
		ConnID:   0,
		ClientIP: httpClientIP(r, trustProxy),
	}
}

// httpStatusFromAuthError maps an authError code to an HTTP status.
// Any non-authError gets 500.
func httpStatusFromAuthError(err error) int {
	ae, ok := err.(*authError)
	if !ok {
		return http.StatusInternalServerError
	}
	switch ae.Code {
	case enginepb.AuthError_AUTH_ERROR_INVALID_CREDENTIALS,
		enginepb.AuthError_AUTH_ERROR_USERNAME_INVALID,
		enginepb.AuthError_AUTH_ERROR_PASSWORD_TOO_WEAK:
		return http.StatusBadRequest
	case enginepb.AuthError_AUTH_ERROR_USERNAME_TAKEN:
		return http.StatusConflict
	case enginepb.AuthError_AUTH_ERROR_ACCOUNT_LOCKED,
		enginepb.AuthError_AUTH_ERROR_RATE_LIMITED:
		return http.StatusTooManyRequests
	case enginepb.AuthError_AUTH_ERROR_TOKEN_INVALID,
		enginepb.AuthError_AUTH_ERROR_TOKEN_EXPIRED,
		enginepb.AuthError_AUTH_ERROR_NOT_AUTHENTICATED:
		return http.StatusUnauthorized
	case enginepb.AuthError_AUTH_ERROR_MFA_REQUIRED,
		enginepb.AuthError_AUTH_ERROR_MFA_INVALID:
		return http.StatusUnauthorized
	default:
		return http.StatusInternalServerError
	}
}

func authJSONFromError(err error) authErrorJSON {
	ae, ok := err.(*authError)
	if !ok {
		return authErrorJSON{
			Code: enginepb.AuthError_AUTH_ERROR_INTERNAL, Message: err.Error(),
		}
	}
	out := authErrorJSON{Code: ae.Code, Message: ae.Msg}
	if ae.Metadata != nil {
		out.RetryAfterMs = ae.Metadata.RetryAfterMs
	}
	return out
}
```

- [ ] **Step 3: Verify compile**

Run: `go vet ./pkg/auth/...`
Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add pkg/auth/http.go
git commit -m "feat(auth): RegisterHTTP scaffold + POST /auth/login

Login handler reuses handleLogin's existing logic — JSON envelope,
HTTP status code mapped from AuthError, session_token rides only in
Set-Cookie (never in body). Other endpoints land in following tasks."
```

---

### Task 4: `POST /auth/register`

**Files:**
- Modify: `pkg/auth/http.go`

- [ ] **Step 1: Append `httpRegister`**

```go
// registerRequestJSON mirrors AuthRegisterRequest.
type registerRequestJSON struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Email    string `json:"email,omitempty"`
}

func (s *Service) httpRegister(w http.ResponseWriter, r *http.Request, opts HTTPOpts) (int, any, error) {
	var req registerRequestJSON
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return http.StatusBadRequest, authErrorJSON{
			Code: enginepb.AuthError_AUTH_ERROR_INTERNAL, Message: "bad request body",
		}, nil
	}
	opCtx := newHTTPOpCtx(r, s.opts.TrustedProxyHeader)
	resp, err := s.handleRegister(opCtx, &enginepb.AuthRegisterRequest{
		Username: req.Username, Password: req.Password, Email: req.Email,
	})
	if err != nil {
		return httpStatusFromAuthError(err), authJSONFromError(err), nil
	}
	setAuthCookie(w, opts, resp.SessionToken, s.opts.SessionTTL)
	return http.StatusOK, loginResponseJSON{
		UserID: resp.UserId, Username: resp.Username, ExpiresAtMs: resp.ExpiresAtMs,
	}, nil
}
```

- [ ] **Step 2: Compile + commit**

Run: `go vet ./pkg/auth/...`
Expected: clean.

```bash
git add pkg/auth/http.go
git commit -m "feat(auth): POST /auth/register

Same shape as /auth/login: reuses handleRegister, sets cookie on
success, response carries user_id+username+expires_at_ms (no token)."
```

---

### Task 5: `POST /auth/logout`

**Files:**
- Modify: `pkg/auth/http.go`

- [ ] **Step 1: Append `httpLogout`**

```go
// httpSessionToken extracts the session token from the request cookie.
// Used by handlers that need the bound session (logout, change-password).
func httpSessionToken(r *http.Request, opts HTTPOpts) string {
	return readAuthCookie(r, opts)
}

func (s *Service) httpLogout(w http.ResponseWriter, r *http.Request, opts HTTPOpts) (int, any, error) {
	tok := httpSessionToken(r, opts)
	if tok == "" {
		// No cookie — already logged out as far as the client is
		// concerned. Clear any stale cookie just in case and return 200.
		clearAuthCookie(w, opts)
		return http.StatusOK, struct{}{}, nil
	}
	opCtx := newHTTPOpCtx(r, s.opts.TrustedProxyHeader)
	WithSessionToken(opCtx, tok)
	if _, err := s.handleLogout(opCtx, &enginepb.AuthLogoutRequest{}); err != nil {
		// Even on error we clear the cookie so the client returns to
		// login. The audit row (or its absence) tracks the server
		// state.
		clearAuthCookie(w, opts)
		return httpStatusFromAuthError(err), authJSONFromError(err), nil
	}
	clearAuthCookie(w, opts)
	return http.StatusOK, struct{}{}, nil
}
```

- [ ] **Step 2: Compile + commit**

Run: `go vet ./pkg/auth/...`
Expected: clean.

```bash
git add pkg/auth/http.go
git commit -m "feat(auth): POST /auth/logout

Reads the session token from the cookie, calls handleLogout (which
revokes server-side), then writes Set-Cookie with Max-Age=-1 to clear
the client cookie. Idempotent: no cookie → 200, clear anyway."
```

---

### Task 6: `POST /auth/refresh`

**Files:**
- Modify: `pkg/auth/http.go`

`/auth/refresh` validates the current cookie via `Resolve` (which already slides expiry) and rewrites the cookie with the new max-age. No body in the request.

- [ ] **Step 1: Append `httpRefresh`**

```go
func (s *Service) httpRefresh(w http.ResponseWriter, r *http.Request, opts HTTPOpts) (int, any, error) {
	tok := httpSessionToken(r, opts)
	if tok == "" {
		return http.StatusUnauthorized, authErrorJSON{
			Code: enginepb.AuthError_AUTH_ERROR_NOT_AUTHENTICATED,
			Message: "no session cookie",
		}, nil
	}
	resolved, err := s.Resolve(r.Context(), tok)
	if err != nil {
		clearAuthCookie(w, opts)
		return http.StatusUnauthorized, authErrorJSON{
			Code: enginepb.AuthError_AUTH_ERROR_TOKEN_INVALID,
			Message: "invalid session",
		}, nil
	}
	// Resolve already slid expires_at on the server side; rewrite the
	// cookie max-age to match so the client sees the same expiry.
	setAuthCookie(w, opts, tok, s.opts.SessionTTL)
	return http.StatusOK, loginResponseJSON{
		UserID:      resolved.UserID.String(),
		Username:    resolved.Username,
		ExpiresAtMs: resolved.ExpiresAt.UnixMilli(),
	}, nil
}
```

- [ ] **Step 2: Compile + commit**

Run: `go vet ./pkg/auth/...`
Expected: clean.

```bash
git add pkg/auth/http.go
git commit -m "feat(auth): POST /auth/refresh

Validates the cookie via Resolver (slides expiry server-side) and
rewrites the cookie with the new max-age. Returns user_id/username
in the body so the client can refresh display state without an
extra /auth/me round-trip."
```

---

### Task 7: `GET /auth/me`

**Files:**
- Modify: `pkg/auth/http.go`

- [ ] **Step 1: Append `httpMe`**

```go
func (s *Service) httpMe(w http.ResponseWriter, r *http.Request, opts HTTPOpts) (int, any, error) {
	tok := httpSessionToken(r, opts)
	if tok == "" {
		return http.StatusUnauthorized, authErrorJSON{
			Code: enginepb.AuthError_AUTH_ERROR_NOT_AUTHENTICATED,
			Message: "no session cookie",
		}, nil
	}
	resolved, err := s.Resolve(r.Context(), tok)
	if err != nil {
		clearAuthCookie(w, opts)
		return http.StatusUnauthorized, authErrorJSON{
			Code: enginepb.AuthError_AUTH_ERROR_TOKEN_INVALID,
			Message: "invalid session",
		}, nil
	}
	// Resolve already slid expiry; rewrite the cookie max-age too so
	// idle tab refreshes effectively keep the session alive (matches
	// the behavior the web client had with localStorage).
	setAuthCookie(w, opts, tok, s.opts.SessionTTL)
	return http.StatusOK, loginResponseJSON{
		UserID:      resolved.UserID.String(),
		Username:    resolved.Username,
		ExpiresAtMs: resolved.ExpiresAt.UnixMilli(),
	}, nil
}
```

- [ ] **Step 2: Compile + commit**

Run: `go vet ./pkg/auth/...`
Expected: clean.

```bash
git add pkg/auth/http.go
git commit -m "feat(auth): GET /auth/me

Returns the resolved user identity if the cookie is valid; otherwise
401 + cookie clear. Slides expiry as a side effect — page loads
double as session-keepalive, matching the localStorage-era behavior."
```

---

### Task 8: `POST /auth/change-password`

**Files:**
- Modify: `pkg/auth/http.go`

- [ ] **Step 1: Append `httpChangePassword`**

```go
type changePasswordRequestJSON struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (s *Service) httpChangePassword(w http.ResponseWriter, r *http.Request, opts HTTPOpts) (int, any, error) {
	tok := httpSessionToken(r, opts)
	if tok == "" {
		return http.StatusUnauthorized, authErrorJSON{
			Code: enginepb.AuthError_AUTH_ERROR_NOT_AUTHENTICATED,
			Message: "no session cookie",
		}, nil
	}
	var req changePasswordRequestJSON
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return http.StatusBadRequest, authErrorJSON{
			Code: enginepb.AuthError_AUTH_ERROR_INTERNAL, Message: "bad request body",
		}, nil
	}
	opCtx := newHTTPOpCtx(r, s.opts.TrustedProxyHeader)
	WithSessionToken(opCtx, tok)
	if _, err := s.handleChangePassword(opCtx, &enginepb.AuthChangePasswordRequest{
		CurrentPassword: req.CurrentPassword, NewPassword: req.NewPassword,
	}); err != nil {
		return httpStatusFromAuthError(err), authJSONFromError(err), nil
	}
	// Password change revokes all OTHER sessions for the user; the
	// current cookie remains valid. Don't touch the cookie here.
	return http.StatusOK, struct{}{}, nil
}
```

- [ ] **Step 2: Compile + commit**

Run: `go vet ./pkg/auth/...`
Expected: clean.

```bash
git add pkg/auth/http.go
git commit -m "feat(auth): POST /auth/change-password

Reuses handleChangePassword (revokes other sessions, keeps current).
Cookie stays untouched — the user's current tab remains logged in."
```

---

### Task 9: HTTP handler unit tests

**Files:**
- Create: `pkg/auth/http_test.go`

- [ ] **Step 1: Write the tests**

```go
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zenion/mmokit/pkg/auth/authtest"
	"github.com/zenion/mmokit/pkg/logger"
	"github.com/zenion/mmokit/pkg/service"
)

func newHTTPTestService(t *testing.T) (*Service, *authtest.RepoMock, *http.ServeMux, HTTPOpts) {
	t.Helper()
	repo := authtest.NewMock()
	opts := DefaultServiceOpts()
	opts.Repository = repo
	s := newService(&service.Context{
		KindName: "auth", InstanceID: "test-0",
		Logger: logger.New(), Roles: map[string]struct{}{"service": {}},
	}, opts).(*Service)
	if err := s.Init(s.ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	mux := http.NewServeMux()
	httpOpts := DefaultHTTPOpts()
	httpOpts.CookieSecure = false // local test, no TLS
	s.RegisterHTTP(mux, httpOpts)
	return s, repo, mux, httpOpts
}

func TestHTTPRegisterSetsCookieAndOmitsTokenFromBody(t *testing.T) {
	_, _, mux, opts := newHTTPTestService(t)
	body, _ := json.Marshal(registerRequestJSON{Username: "alice", Password: "hunter22"})
	req := httptest.NewRequest("POST", "/auth/register", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, body=%s", w.Code, w.Body.String())
	}
	// Cookie present
	cookies := w.Result().Cookies()
	var sess *http.Cookie
	for _, c := range cookies {
		if c.Name == opts.CookieName {
			sess = c
		}
	}
	if sess == nil {
		t.Fatalf("Set-Cookie %q missing; got %v", opts.CookieName, cookies)
	}
	if !sess.HttpOnly {
		t.Errorf("HttpOnly flag not set")
	}
	if sess.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite: got %v, want Strict", sess.SameSite)
	}
	if sess.Value == "" {
		t.Error("cookie value empty")
	}
	// Body has no session_token
	if bytes.Contains(w.Body.Bytes(), []byte("session_token")) {
		t.Errorf("body leaked session_token: %s", w.Body.String())
	}
}

func TestHTTPLoginThenMeRoundTrip(t *testing.T) {
	_, _, mux, opts := newHTTPTestService(t)
	regBody, _ := json.Marshal(registerRequestJSON{Username: "bob", Password: "hunter22"})
	regReq := httptest.NewRequest("POST", "/auth/register", bytes.NewReader(regBody))
	regW := httptest.NewRecorder()
	mux.ServeHTTP(regW, regReq)
	cookies := regW.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("no cookie from register")
	}

	meReq := httptest.NewRequest("GET", "/auth/me", nil)
	for _, c := range cookies {
		meReq.AddCookie(c)
	}
	meW := httptest.NewRecorder()
	mux.ServeHTTP(meW, meReq)
	if meW.Code != http.StatusOK {
		t.Fatalf("/auth/me status: got %d, body=%s", meW.Code, meW.Body.String())
	}
	var resp loginResponseJSON
	if err := json.NewDecoder(meW.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Username != "bob" {
		t.Fatalf("username mismatch: %s", resp.Username)
	}
	_ = opts
}

func TestHTTPMeWithNoCookieReturns401(t *testing.T) {
	_, _, mux, _ := newHTTPTestService(t)
	req := httptest.NewRequest("GET", "/auth/me", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d", w.Code)
	}
}

func TestHTTPLogoutClearsCookie(t *testing.T) {
	_, _, mux, opts := newHTTPTestService(t)
	regBody, _ := json.Marshal(registerRequestJSON{Username: "carol", Password: "hunter22"})
	regReq := httptest.NewRequest("POST", "/auth/register", bytes.NewReader(regBody))
	regW := httptest.NewRecorder()
	mux.ServeHTTP(regW, regReq)
	cookies := regW.Result().Cookies()

	logoutReq := httptest.NewRequest("POST", "/auth/logout", nil)
	for _, c := range cookies {
		logoutReq.AddCookie(c)
	}
	logoutW := httptest.NewRecorder()
	mux.ServeHTTP(logoutW, logoutReq)
	if logoutW.Code != http.StatusOK {
		t.Fatalf("logout status: %d", logoutW.Code)
	}
	cleared := false
	for _, c := range logoutW.Result().Cookies() {
		if c.Name == opts.CookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("logout did not clear the cookie")
	}

	// Subsequent /auth/me with the original cookie should now 401
	// (server-side session was revoked).
	meReq := httptest.NewRequest("GET", "/auth/me", nil)
	for _, c := range cookies {
		meReq.AddCookie(c)
	}
	meW := httptest.NewRecorder()
	mux.ServeHTTP(meW, meReq)
	if meW.Code != http.StatusUnauthorized {
		t.Fatalf("/auth/me after logout: got %d", meW.Code)
	}
}

func TestHTTPLoginInvalidCredentialsReturns400(t *testing.T) {
	_, _, mux, _ := newHTTPTestService(t)
	body, _ := json.Marshal(loginRequestJSON{Username: "ghost", Password: "x"})
	req := httptest.NewRequest("POST", "/auth/login", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d", w.Code)
	}
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./pkg/auth/ -count=1 -run TestHTTP`
Expected: all 5 tests PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/auth/http_test.go
git commit -m "test(auth): HTTP endpoint coverage — register, login, /me, logout, error mapping

Verifies cookie shape (HttpOnly + SameSite=Strict), absence of
session_token from response bodies, /auth/me round-trip with cookie
jar, logout clears cookie + revokes server-side session, invalid
credentials map to 400."
```

---

## Phase B — Facade + dev flag

### Task 10: Wire `RegisterHTTP` through the facade

**Files:**
- Modify: `pkg/auth/kind.go` — add `HTTPOpts` to `ServiceOpts`
- Modify: `pkg/auth/service.go` — expose `Resolver` interface for the gateway
- Modify: `pkg/mmokit/auth.go` — register HTTP routes via `Config.HTTPRoutes` hook
- Modify: `pkg/universe/coordinator.go` — add `Config.AuthResolver` accessor

The HTTP routes mount on the engine's `*http.Server` via the existing `Config.HTTPRoutes` hook. The gateway gets the `Resolver` via a new process-level slot that `RegisterAuthService` populates from the auth service's `OnReady` callback.

- [ ] **Step 1: Add `HTTPOpts` to `ServiceOpts` + seed default**

In `pkg/auth/kind.go`, append to the `ServiceOpts` struct (just before the existing `OnReady` field):

```go
	// HTTPOpts controls the HTTPS endpoint cookie shape and routes.
	// Honoured when RegisterAuthService is the entry-point. Zero
	// value falls through to effective() which fills DefaultHTTPOpts.
	HTTPOpts HTTPOpts
```

Also seed `DefaultHTTPOpts()` in `DefaultServiceOpts()`:

```go
func DefaultServiceOpts() ServiceOpts {
	return ServiceOpts{
		SessionTTL:        30 * 24 * time.Hour,
		PasswordMinLen:    8,
		Argon2id:          DefaultArgonParams(),
		IPRateLimitMax:    10,
		IPRateLimitWindow: 60 * time.Second,
		IPLockoutDuration: 5 * time.Minute,
		LockoutThreshold:  5,
		LockoutDuration:   15 * time.Minute,
		AuditRetention:    90 * 24 * time.Hour,
		ReapInterval:      time.Hour,
		HTTPOpts:          DefaultHTTPOpts(),
	}
}
```

- [ ] **Step 2: Add `AuthResolver` slot on `Config`**

In `pkg/universe/coordinator.go` Config struct (alongside the other auth-related slots), add:

```go
	// AuthResolver validates a cookie/session token at WS-upgrade time
	// without touching the op channel. Set by mmokit.RegisterAuthService
	// after the auth Service finishes Init. Read by the gateway's
	// upgrade handler. Nil disables cookie-based auth (the gateway
	// will treat every connection as unauthenticated).
	AuthResolver auth.Resolver
```

(Add `"github.com/zenion/mmokit/pkg/auth"` to imports if not already there.)

- [ ] **Step 3: Update `mmokit.RegisterAuthService` to wire HTTP + Resolver**

In `pkg/mmokit/auth.go`, modify the existing `RegisterAuthService`:

```go
func RegisterAuthService(p *universe.Process, opts AuthOpts) error {
	if opts.SessionTTL == 0 {
		base := DefaultAuthOpts()
		base.Repository = opts.Repository
		base.RepositoryFactory = opts.RepositoryFactory
		base.HTTPOpts = opts.HTTPOpts
		opts = base
	}
	if opts.Repository == nil && opts.RepositoryFactory == nil {
		opts.RepositoryFactory = func(pool *pgxpool.Pool) auth.Repository {
			return authpg.New(pool)
		}
	}
	// Cookie defaults: only flip Secure off when caller explicitly
	// passed CookieSecure=false in HTTPOpts. (The HTTPOpts struct
	// has effective() to fill the rest.)
	if opts.HTTPOpts.CookieName == "" {
		opts.HTTPOpts = auth.DefaultHTTPOpts()
		// Preserve any explicit user opt-out of Secure (dev mode):
		// the original opts had no CookieName so the zero-value
		// CookieSecure=false would have been the user's deliberate
		// choice if they touched HTTPOpts at all. Detect this with
		// a sentinel: if the original CookieSecure was already true
		// (uncommon — defaults are zero values), keep that.
		// Simpler: caller passes a fully-populated HTTPOpts via
		// DefaultHTTPOpts() then mutates fields. This branch only
		// fires when the caller didn't touch HTTPOpts at all, in
		// which case Secure=true is correct.
	}

	setRepo, getRepo := auth.NewRepoSlot()
	if opts.Repository != nil {
		setRepo(opts.Repository)
	}
	prev := opts.OnReady
	// Capture the live Service via a slot so the HTTP routes can call
	// Resolve. The auth Service implements Resolver.
	var liveService *auth.Service
	opts.OnReady = func(repo auth.Repository) {
		setRepo(repo)
		// liveService is set by a separate hook: the kind Factory
		// (auth.kindFor → newService) constructs a *Service and
		// stashes it via a side channel — but the simpler option
		// is to capture from the FIRST Resolver consumer at runtime.
		// See note below.
		if prev != nil {
			prev(repo)
		}
	}

	kind := auth.Kind(opts)
	if err := p.RegisterService(kind); err != nil {
		return fmt.Errorf("RegisterAuthService: %w", err)
	}
	p.AddGatewayAuthHook(kind.OpCodes)
	p.AppendExtraMigrations(auth.MigrationsFS())

	// Auto-include "auth" so default boots wire the kind.
	cfg := p.Config()
	already := false
	for _, k := range cfg.ServiceKinds {
		if k == auth.KindName {
			already = true
			break
		}
	}
	if !already {
		cfg.ServiceKinds = append(cfg.ServiceKinds, auth.KindName)
	}

	// Wire HTTP routes via the existing Config.HTTPRoutes hook.
	// The hook fires when the engine's HTTP listener mounts user routes.
	prevHTTP := cfg.HTTPRoutes
	cfg.HTTPRoutes = func(mux *http.ServeMux) {
		if prevHTTP != nil {
			prevHTTP(mux)
		}
		if liveService != nil {
			liveService.RegisterHTTP(mux, opts.HTTPOpts)
		}
	}

	if reg := p.CmdRegistry(); reg != nil {
		if err := auth.RegisterConsoleCommands(reg, getRepo); err != nil {
			return fmt.Errorf("RegisterAuthService: console commands: %w", err)
		}
	}
	_ = liveService // silence until wired
	return nil
}
```

The `liveService` capture is awkward as written. Cleaner: change the `OnReady` callback signature to `func(repo auth.Repository, svc *auth.Service)` so the facade gets the live service pointer. Smaller blast radius:

- [ ] **Step 4: Extend `OnReady` signature to include `*Service`**

In `pkg/auth/kind.go`, change the comment + signature on `OnReady`:

```go
	// OnReady fires from Service.Init exactly once, after the live
	// Repository has been resolved and just before reapLoop starts.
	// repo is the bound Repository; svc is the running *Service (used
	// by the mmokit facade to capture a Resolver pointer for the
	// gateway).
	OnReady func(svc *Service)
```

In `pkg/auth/service.go`, change the call site in `Init`:

```go
	if s.opts.OnReady != nil {
		s.opts.OnReady(s)
	}
```

(Drop the `repo` argument from the callback. Callers that need the repo can read `s.repo` if needed — but for the facade we want the `*Service` to call `Resolve`.)

Now `RegisterAuthService` becomes:

```go
	var liveService *auth.Service
	prev := opts.OnReady
	opts.OnReady = func(svc *auth.Service) {
		liveService = svc
		setRepo(svc.RepoForTesting())  // see below
		// Stash the resolver on the process Config so the gateway
		// can pick it up at WS-upgrade time.
		p.Config().AuthResolver = svc
		if prev != nil {
			prev(svc)
		}
	}
```

`RepoForTesting` is overkill for callers — instead expose a `Repository()` accessor on `*Service`:

In `pkg/auth/service.go` add:

```go
// Repository returns the live Repository. Used by the mmokit facade to
// wire the console-command repo provider after Init.
func (s *Service) Repository() Repository { return s.repo }
```

Then in `RegisterAuthService`:

```go
	opts.OnReady = func(svc *auth.Service) {
		liveService = svc
		setRepo(svc.Repository())
		p.Config().AuthResolver = svc
		if prev != nil {
			prev(svc)
		}
	}
```

- [ ] **Step 5: Fix the `console.go` slot wiring**

`auth.NewRepoSlot` was designed around the old `OnReady(Repository)` signature. Look at `pkg/auth/console.go` — `NewRepoSlot` returns `(set, get)`. The set was wired to the OnReady. After the signature change we can either keep the set-by-Repository pattern (call `setRepo(svc.Repository())`) or rework console.go to take a `*Service` getter. Keep the simpler change: setRepo(svc.Repository()). Console.go is unchanged.

- [ ] **Step 6: Add `net/http` import to mmokit/auth.go**

Add `"net/http"` to the imports of `pkg/mmokit/auth.go`.

- [ ] **Step 7: Compile**

Run: `go vet ./pkg/...`
Expected: clean.

- [ ] **Step 8: Run all auth tests**

Run: `go test ./pkg/auth/ ./pkg/mmokit/ -count=1`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add pkg/auth/kind.go pkg/auth/service.go pkg/mmokit/auth.go pkg/universe/coordinator.go
git commit -m "feat(auth): wire HTTP routes + Resolver via facade

OnReady callback signature changes to OnReady(*Service) so the facade
can capture the live service pointer. Service exposes Repository() and
already implements Resolver. mmokit.RegisterAuthService stamps:
  - cfg.HTTPRoutes ← liveService.RegisterHTTP(mux, HTTPOpts)
  - cfg.AuthResolver ← liveService
The gateway picks AuthResolver up at WS-upgrade time (next task)."
```

---

### Task 11: Gateway WS-upgrade cookie validation

**Files:**
- Modify: `pkg/universe/gateway.go`

The gateway's WS-upgrade handler currently accepts every connection unauthenticated. New behavior: read the cookie, call `cfg.AuthResolver.Resolve`, bind the connID at upgrade time on success, dispatch `PlayerAssignment`. On failure, clear the cookie and let the connection upgrade unauthenticated (web client will redirect to login form).

- [ ] **Step 1: Locate the WS upgrade handler**

Run: `grep -n "websocket\.Accept\|Upgrade\|handleWS\|/ws" pkg/universe/gateway.go pkg/net/*.go | head -10`

Most likely the upgrade is in `pkg/net/server.go`'s `HandleWebSocket` (the connMgr path). The gateway intercepts via the `ConnManager.OnConnect` event. We add the cookie-read + resolver-call between the WS upgrade and the gateway recording the connection.

- [ ] **Step 2: Add `OnUpgrade` hook on `ConnManager` (if not present)**

If `ConnManager` has an existing `OnConnect` callback that fires *after* upgrade, we attach the cookie validation there. Look for the field; the connection record likely already has `RemoteAddr` (added in Task 18 of the original auth-service plan). If there's no per-upgrade hook, add one:

```go
// In pkg/net/server.go, on ConnManager:
type UpgradeContext struct {
	ConnID  uint32
	Request *http.Request  // the original HTTP request — exposes cookies
}

// OnUpgrade fires synchronously during HandleWebSocket, after the WS
// upgrade succeeds and the connection is recorded but before the
// connection's read loop starts. Set by the gateway to read cookies.
OnUpgrade func(UpgradeContext)
```

In `HandleWebSocket`, after the connection record is added:

```go
if m.OnUpgrade != nil {
	m.OnUpgrade(UpgradeContext{ConnID: connID, Request: r})
}
```

If a similar hook already exists, use it instead.

- [ ] **Step 3: Wire the gateway to consume `OnUpgrade`**

In `pkg/universe/gateway.go`, in the gateway's setup (`installAuthHook` or the constructor — find where the gateway has access to `g.connMgr`):

```go
// Attach the WS-upgrade cookie reader. Fires once per accepted
// connection before any frames flow.
g.connMgr.OnUpgrade = g.onWSUpgrade
```

Then add the handler:

```go
func (g *Gateway) onWSUpgrade(uc net.UpgradeContext) {
	if g.coord == nil || g.coord.cfg.AuthResolver == nil {
		return
	}
	opts := g.coord.cfg.authOptsForCookie() // small accessor, see step 4
	cookie, err := uc.Request.Cookie(opts.CookieName)
	if err != nil || cookie.Value == "" {
		return // no cookie — upgrade stays unauthenticated
	}
	resolved, err := g.coord.cfg.AuthResolver.Resolve(uc.Request.Context(), cookie.Value)
	if err != nil {
		// Token unknown / revoked / expired. We can't write
		// Set-Cookie post-upgrade through the WS — the cookie stays
		// stale until the next HTTPS roundtrip. The web client's
		// /auth/me call will clear it.
		g.log.Log(CatNetConn, "ws upgrade: cookie validate failed conn=%d: %v", uc.ConnID, err)
		return
	}
	// Bind the session at upgrade time, same shape as
	// onAuthSuccess does post-op-channel.
	g.onAuthSuccess(uc.ConnID, resolved.UserID, resolved.Username, cookie.Value, resolved.ExpiresAt.UnixMilli())
}
```

- [ ] **Step 4: Expose `authOptsForCookie` on Config (or pass through)**

Simplest: add a `Config.AuthHTTPOpts auth.HTTPOpts` field stamped by `RegisterAuthService` alongside `AuthResolver`. The gateway reads the cookie name from there.

In `pkg/universe/coordinator.go` Config:

```go
	// AuthHTTPOpts is the HTTPOpts used by the auth service. The
	// gateway uses CookieName at WS-upgrade time to read the session
	// cookie. Stamped by mmokit.RegisterAuthService.
	AuthHTTPOpts auth.HTTPOpts
```

In `pkg/mmokit/auth.go` RegisterAuthService, after the OnReady wiring, also stamp:

```go
	p.Config().AuthHTTPOpts = opts.HTTPOpts
```

In gateway.onWSUpgrade replace `opts := g.coord.cfg.authOptsForCookie()` with `opts := g.coord.cfg.AuthHTTPOpts`.

- [ ] **Step 5: Compile + run all tests**

Run: `go vet ./pkg/...`
Expected: clean.

Run: `go test ./pkg/auth/ ./pkg/universe/ ./pkg/mmokit/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/net/server.go pkg/universe/gateway.go pkg/universe/coordinator.go pkg/mmokit/auth.go
git commit -m "feat(gateway): WS-upgrade cookie validation + bind

ConnManager.OnUpgrade fires synchronously after each WS upgrade with
the original *http.Request still in scope (exposes cookies). Gateway
reads cfg.AuthHTTPOpts.CookieName, calls cfg.AuthResolver.Resolve,
and routes to the existing onAuthSuccess path on hit. On miss the
connection upgrades unauthenticated — the web client's /auth/me
or /auth/login round-trip clears any stale cookie."
```

---

### Task 12: Integration test — register → cookie set → WS upgrade authenticated

**Files:**
- Create: `pkg/universe/auth_cookie_e2e_test.go`

- [ ] **Step 1: Write the test**

```go
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

	"github.com/zenion/mmokit/pkg/auth/authtest"
	"github.com/zenion/mmokit/pkg/mmokit"
)

// TestAuthCookieFullFlow verifies: HTTPS register sets cookie → fresh
// WS upgrade carries the cookie → gateway binds the session at
// upgrade time → no op-channel auth round-trip needed.
func TestAuthCookieFullFlow(t *testing.T) {
	// Build a minimal in-process cluster with auth + mock repo, on an
	// ephemeral port. Reuse whatever fixture pkg/universe tests use
	// for HTTP-listening processes; if no fixture exists, build a
	// throwaway here using mmokit.New + RegisterAuthServiceWithMock.
	// The actual fixture API is in cluster_fixture_*_test.go — adapt
	// to that.
	t.Skip("integration fixture: TODO — build once the fixture API supports HTTP listener + cookie jar")

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
	//    auth call. We assert this indirectly: send a dummy non-auth
	//    op and expect it NOT to come back as NOT_AUTHENTICATED.
	//    The exact assertion depends on what cell-side ops are
	//    registered; the cleanest is to wait for SE_PLAYER_SPAWNED
	//    on the event channel since the gateway dispatches
	//    PlayerAssignment on successful upgrade.
	deadline := time.Now().Add(2 * time.Second)
	c.SetReadDeadline(deadline) // adjust to actual API
	_, msg, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if msg[0] != 0x00 {
		t.Fatalf("expected event channel 0x00 first, got 0x%02x", msg[0])
	}
}

// startTestProcess + assertion helpers depend on the cluster fixture.
// Stub here.
func startTestProcess(t *testing.T, configure func(*mmokit.Process)) string {
	t.Helper()
	t.Skip("startTestProcess not yet implemented — wire to cluster_fixture")
	return ""
}
```

This test is gated by `t.Skip` because the cluster fixture doesn't yet have an HTTP-listener helper. The implementer should land the test as-is (skipping) and follow up by extending the fixture, OR implement it inline if comfortable.

- [ ] **Step 2: Verify the file compiles**

Run: `go vet ./pkg/universe/...`
Expected: clean (the t.Skip lines protect against the unimplemented helpers).

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/auth_cookie_e2e_test.go
git commit -m "test(auth): cookie+WS upgrade e2e test scaffold

Skipped pending a cluster fixture helper that exposes the HTTP
listener address. The shape of the assertion is fixed: HTTPS register
→ cookie in jar → WS upgrade carries cookie → gateway binds session
at upgrade time → first frame is on event channel (post-spawn), no
op-channel auth round-trip."
```

---

## Phase C — Web client migration

### Task 13: Replace web-pixi auth.ts op-channel calls with `fetch('/auth/*')`

**Files:**
- Modify: `web-pixi/src/auth.ts`

- [ ] **Step 1: Rewrite `auth.ts`**

```typescript
// HTTPS-based auth. Replaces the previous op-channel + localStorage
// pattern. The session cookie is HttpOnly and managed by the browser;
// JS never sees the token.

export interface MeResponse {
  userId: string;
  username: string;
  expiresAtMs: number;
}

export interface AuthError extends Error {
  code: number; // enginepb.AuthError enum value
  retryAfterMs?: number;
}

async function postJSON<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(path, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    const err = await parseError(res);
    throw err;
  }
  if (res.status === 200 && res.headers.get("Content-Length") !== "0") {
    return (await res.json()) as T;
  }
  return {} as T;
}

async function parseError(res: Response): Promise<AuthError> {
  let body: { code?: number; message?: string; retry_after_ms?: number } = {};
  try {
    body = await res.json();
  } catch {
    /* non-JSON response */
  }
  const e = new Error(body.message || `HTTP ${res.status}`) as AuthError;
  e.code = body.code ?? 0;
  if (body.retry_after_ms) e.retryAfterMs = body.retry_after_ms;
  return e;
}

export function authLogin(username: string, password: string): Promise<MeResponse> {
  return postJSON<MeResponse>("/auth/login", { username, password });
}

export function authRegister(username: string, password: string, email?: string): Promise<MeResponse> {
  return postJSON<MeResponse>("/auth/register", { username, password, email });
}

export function authLogout(): Promise<void> {
  return postJSON<void>("/auth/logout", {});
}

export async function authMe(): Promise<MeResponse | null> {
  const res = await fetch("/auth/me", { credentials: "same-origin" });
  if (res.status === 401) return null;
  if (!res.ok) throw await parseError(res);
  return (await res.json()) as MeResponse;
}

export async function authChangePassword(current: string, next: string): Promise<void> {
  await postJSON<void>("/auth/change-password", {
    current_password: current,
    new_password: next,
  });
}
```

- [ ] **Step 2: Verify TypeScript compiles**

Run: `cd web-pixi && bun run build`
Expected: build succeeds. (Will fail on TS errors in callers — those are fixed in tasks 14-15.)

If the build complains about unused imports in auth.ts, no further action needed in this task; carry forward.

- [ ] **Step 3: Don't commit yet — entangled with Tasks 14-15**

Hold the commit until login.ts and main.ts are updated to consume the new API.

---

### Task 14: Update `login.ts` to use `fetch('/auth/me')` for resume

**Files:**
- Modify: `web-pixi/src/ui/login.ts`

- [ ] **Step 1: Rewrite `login.ts`**

```typescript
import { authLogin, authMe, authRegister } from "../auth.js";

export interface LoginResult {
  userId: string;
  username: string;
}

/**
 * setupLogin returns a Promise that resolves with LoginResult once
 * auth completes. Tries cookie-based resume via /auth/me first; on
 * 401 falls through to the credential form.
 */
export async function setupLogin(): Promise<LoginResult> {
  const overlay = document.getElementById("login-overlay")!;
  const spinner = document.getElementById("login-spinner")!;
  const panel = document.getElementById("login-panel")!;
  const usernameEl = document.getElementById("login-username") as HTMLInputElement;
  const passwordEl = document.getElementById("login-password") as HTMLInputElement;
  const submitBtn = document.getElementById("login-submit") as HTMLButtonElement;
  const registerBtn = document.getElementById("login-register-toggle") as HTMLButtonElement;
  const hint = document.getElementById("login-hint")!;

  overlay.style.display = "flex";

  // Try cookie-based resume first.
  spinner.style.display = "flex";
  panel.style.display = "none";
  try {
    const me = await authMe();
    if (me) {
      overlay.style.display = "none";
      spinner.style.display = "none";
      return { userId: me.userId, username: me.username };
    }
  } catch (e) {
    // Network error or 5xx — fall through to form. The user can
    // retry; if the cookie is bad, the next login attempt issues a
    // fresh one.
    console.warn("authMe failed:", e);
  }
  spinner.style.display = "none";
  panel.style.display = "block";

  usernameEl.value = (localStorage.getItem("username") || "").toLowerCase();
  usernameEl.focus();

  return new Promise((resolve) => {
    let mode: "login" | "register" = "login";

    function setHint(msg: string, cls: string) {
      hint.textContent = msg;
      hint.className = "hint" + (cls ? " " + cls : "");
    }

    async function submit() {
      const username = usernameEl.value.trim().toLowerCase();
      const password = passwordEl.value;
      if (!username || !password) {
        setHint("Username and password required", "error");
        return;
      }
      setHint(mode === "login" ? "Logging in..." : "Registering...", "");
      submitBtn.disabled = true;
      registerBtn.disabled = true;
      try {
        const me = mode === "login"
          ? await authLogin(username, password)
          : await authRegister(username, password);
        localStorage.setItem("username", me.username);
        overlay.style.display = "none";
        resolve({ userId: me.userId, username: me.username });
      } catch (e: unknown) {
        const msg = e instanceof Error ? e.message : String(e);
        setHint(msg.slice(0, 80), "error");
      } finally {
        submitBtn.disabled = false;
        registerBtn.disabled = false;
      }
    }

    submitBtn.addEventListener("click", () => {
      mode = "login";
      submit();
    });

    registerBtn.addEventListener("click", () => {
      if (mode !== "register") {
        mode = "register";
        setHint("Pick a unique callsign + password (8+ chars)", "");
        registerBtn.textContent = "Submit";
        submitBtn.style.display = "none";
        return;
      }
      submit();
    });

    function onKey(e: KeyboardEvent) {
      e.stopPropagation();
      if (e.key === "Enter") {
        submit();
      }
    }
    usernameEl.addEventListener("keydown", onKey);
    passwordEl.addEventListener("keydown", onKey);
  });
}

export function showLogin(error?: string): void {
  const overlay = document.getElementById("login-overlay")!;
  const hint = document.getElementById("login-hint")!;
  overlay.style.display = "flex";
  if (error) {
    hint.textContent = error;
    hint.className = "hint error";
  }
  const usernameEl = document.getElementById("login-username") as HTMLInputElement | null;
  usernameEl?.focus();
}
```

Note the signature change: `setupLogin()` no longer takes a `SpaceClient` parameter. The web-pixi `LoginResult` no longer includes `sessionToken`.

- [ ] **Step 2: Don't commit yet**

Hold for Task 15.

---

### Task 15: Update `main.ts` logout button + drop SpaceClient dependency

**Files:**
- Modify: `web-pixi/src/main.ts`

- [ ] **Step 1: Update imports and usage**

```typescript
// Replace the prior:
//   import { authLogout, TOKEN_KEY } from "./auth";
//   import { setupLogin, showLogin, type LoginResult } from "./ui/login";
// with:
import { authLogout } from "./auth";
import { setupLogin, showLogin, type LoginResult } from "./ui/login";
```

In the logout button handler, replace:

```typescript
logoutBtn?.addEventListener("click", async () => {
  if (!state.client || !state.loggedIn) return;
  logoutBtn.disabled = true;
  try {
    await authLogout(state.client);
  } catch (e) {
    console.warn("logout failed:", e);
  }
  localStorage.removeItem(TOKEN_KEY);
  window.location.reload();
});
```

with:

```typescript
logoutBtn?.addEventListener("click", async () => {
  if (!state.loggedIn) return;
  logoutBtn.disabled = true;
  try {
    await authLogout();
  } catch (e) {
    console.warn("logout failed:", e);
  }
  window.location.reload();
});
```

In the connect callback, replace:

```typescript
loginResult = await setupLogin(state.client!);
```

with:

```typescript
loginResult = await setupLogin();
```

- [ ] **Step 2: Build the web client**

Run: `cd web-pixi && bun run build`
Expected: clean build, no TS errors.

- [ ] **Step 3: Commit Tasks 13+14+15 together**

```bash
git add web-pixi/src/auth.ts web-pixi/src/ui/login.ts web-pixi/src/main.ts
git commit -m "feat(web-pixi): cookie-based auth — drop op-channel calls + localStorage token

auth.ts: switch to fetch('/auth/*') endpoints with credentials:'same-origin'.
JS no longer sees the session token; the browser cookie jar handles
automatic Set-Cookie / Cookie round-trips.

login.ts: setupLogin() drops the SpaceClient parameter. Resume via
GET /auth/me; on 401 show form, on 200 skip to game.

main.ts: logout button calls fetch('/auth/logout') (server clears
cookie + revokes session) then reloads. localStorage TOKEN_KEY is
gone — verify with localStorage.getItem('mmokit-auth-token') === null
on every page load post-deploy."
```

---

## Phase D — Dev mode flag + 4node-basic

### Task 16: `--dev-insecure-cookie` flag

**Files:**
- Modify: `cmd/server/main.go` — flag definition
- Modify: `pkg/universe/coordinator.go` — `Config.DevInsecureCookie bool` field + flag binding (so 4node-basic gets it for free)

The flag flips `AuthOpts.HTTPOpts.CookieSecure` from true → false at boot. Production deployments leave it off.

- [ ] **Step 1: Add `Config.DevInsecureCookie` + flag binding**

In `pkg/universe/coordinator.go` Config:

```go
	// DevInsecureCookie disables the Secure flag on the auth session
	// cookie. Default false (production-safe). Flip via the
	// --dev-insecure-cookie CLI flag for plain-HTTP local dev.
	DevInsecureCookie bool
```

In `pkg/universe/bootstrap.go` `BindFlags`:

```go
	flag.BoolVar(&c.DevInsecureCookie, "dev-insecure-cookie", c.DevInsecureCookie,
		"disable Secure flag on the auth session cookie (plain-HTTP local dev only)")
```

- [ ] **Step 2: Honor the flag in `RegisterAuthService`**

In `pkg/mmokit/auth.go` `RegisterAuthService`, after the HTTPOpts default fill:

```go
	// --dev-insecure-cookie overrides Secure regardless of caller intent.
	if p.Config().DevInsecureCookie {
		opts.HTTPOpts.CookieSecure = false
	}
```

- [ ] **Step 3: Compile + test**

Run: `go vet ./pkg/...`
Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/coordinator.go pkg/universe/bootstrap.go pkg/mmokit/auth.go
git commit -m "feat: --dev-insecure-cookie flag

Engine-level Config.DevInsecureCookie + CLI flag plumbed through
RegisterAuthService. When set, AuthOpts.HTTPOpts.CookieSecure flips
to false so plain-HTTP localhost works. Default false everywhere
else; production deployments leave it off."
```

---

### Task 17: Update justfiles

**Files:**
- Modify: `justfile`
- Modify: `examples/4node-basic/justfile`

- [ ] **Step 1: Update root justfile `dev` recipe**

```bash
grep -n "^dev:" justfile
```

Find the existing `dev: build` recipe. Change `./bin/server` to `./bin/server --dev-insecure-cookie`.

After:

```
dev: build
    #!/usr/bin/env bash
    set -euo pipefail
    tmux kill-session -t space-vite 2>/dev/null || true
    tmux new-session -d -s space-vite -c "{{ justfile_directory() }}/web-pixi" 'bun run dev'
    trap 'tmux kill-session -t space-vite 2>/dev/null' INT TERM EXIT
    ./bin/server --dev-insecure-cookie
```

- [ ] **Step 2: Update `examples/4node-basic/justfile` `dev` recipe**

Change the last line from `./bin/4node-basic --web-dir=disabled --postgres-url={{postgres_url}} {{ARGS}}` to include `--dev-insecure-cookie`:

```
cd {{root}} && ./bin/4node-basic --web-dir=disabled --postgres-url={{postgres_url}} --dev-insecure-cookie {{ARGS}}
```

- [ ] **Step 3: Update the `distributed-space` recipe (if present) to pass `--dev-insecure-cookie` to all auth-handling processes**

```bash
grep -n "distributed-space\|distributed:" justfile examples/4node-basic/justfile
```

For any recipe that runs the binary on plain HTTP localhost (any of the dev tmux sessions), append `--dev-insecure-cookie` to the same place where other dev flags are passed.

- [ ] **Step 4: Commit**

```bash
git add justfile examples/4node-basic/justfile
git commit -m "build: pass --dev-insecure-cookie in dev/distributed justfile recipes

Plain-HTTP localhost dev needs Secure off. Production setups (any
TLS-fronted deploy) leave the flag off so cookies stay secure."
```

---

### Task 18: Wire `RegisterAuthService` into 4node-basic

**Files:**
- Modify: `examples/4node-basic/main.go`

The original auth service plan added `RegisterAuthService` to `cmd/server/main.go` but 4node-basic was wired to use the auth service in a different commit (8296d35). Re-verify:

- [ ] **Step 1: Verify current state**

Run: `grep -n "RegisterAuthService\|RegisterAuthServiceWithMock" examples/4node-basic/main.go`

If `RegisterAuthService` is already there, this task is a no-op — confirm and move on. If not, add it after the `mmokit.New(cfg)` call:

```go
if err := mmokit.RegisterAuthService(coord, mmokit.DefaultAuthOpts()); err != nil {
    log.Fatalf("4node-basic: RegisterAuthService: %v", err)
}
```

- [ ] **Step 2: Confirm 4node-basic boots clean with cookie auth**

Run: `cd examples/4node-basic && just build-go && ./bin/4node-basic --mode=all --headless --dev-insecure-cookie --postgres-url=postgres://mmo:mmo@localhost:5432/mmo?sslmode=disable &`
Expected: starts without panic, listens on default port.

Kill the process after a few seconds: `pkill -INT -f bin/4node-basic`.

- [ ] **Step 3: Commit (if any change was needed)**

```bash
git add examples/4node-basic/main.go
git commit -m "feat(4node-basic): RegisterAuthService wiring (if missing)

4node-basic now uses the same auth surface as cmd/server. Skip if
already wired by an earlier commit."
```

If no change was needed, skip the commit.

---

## Phase E — Smoke verification

### Task 19: Browser smoke test (manual)

- [ ] **Step 1: `just build && just dev`**

Open `http://localhost:8080` in a fresh browser (or fresh incognito window).

- [ ] **Step 2: Register a fresh user**

Click Register, pick a fresh username + password. Ship spawns.

- [ ] **Step 3: Verify the cookie shape in dev tools**

Open dev tools → Application → Cookies → `http://localhost:8080`. There should be a `mmokit-session` cookie with:
- `HttpOnly`: ✓
- `Secure`: ✗ (dev mode)
- `SameSite`: `Strict`
- `Path`: `/`

Open dev tools → Application → Local Storage → `http://localhost:8080`. **`mmokit-auth-token` should NOT exist.**

- [ ] **Step 4: Verify JS cannot read the cookie**

In dev console, run: `document.cookie`
Expected: empty string (or only non-HttpOnly cookies). The `mmokit-session` cookie is invisible to JS.

- [ ] **Step 5: Reload the page**

Spinner shows briefly. Ship reattaches without showing the login form.

- [ ] **Step 6: Click LOGOUT**

Page reloads → login form appears → cookie cleared (verify via dev tools Application → Cookies).

- [ ] **Step 7: Try with a stale cookie**

Register a new user, copy the `mmokit-session` cookie value. Use server console: `auth.user.kick <username>` to revoke. In the browser: reload. The spinner should appear, /auth/me returns 401, the cookie is cleared, the form appears.

- [ ] **Step 8: Verify the WS upgrade carries the cookie**

In dev tools → Network → WS, find the `/ws` upgrade request. Click it, view headers. The request should include `Cookie: mmokit-session=...` in the upgrade.

- [ ] **Step 9: Commit any final tweaks**

If any small issues surfaced (CSS, copy, ordering), commit them with: `fix(auth): smoke-pass adjustments`.

---

## Plan complete

Spec reference: [docs/superpowers/specs/2026-05-02-auth-cookie-hardening-design.md](../specs/2026-05-02-auth-cookie-hardening-design.md)

**Estimated total commits:** ~17 (one per task plus a smoke-pass tweak commit).
**Estimated total LOC:** ~700 (Go) + ~150 (TS).

Memory note: per `feedback_security_best_practices`, this is the kind of work that wants explicit threat-model framing — the spec's §3 lays out the in-scope threats and how each is mitigated. Cross-reference when adding follow-up auth work.
