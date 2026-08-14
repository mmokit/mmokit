package auth

import (
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"

	"github.com/mmokit/mmokit/pkg/ops"
)

// RegisterHTTP mounts the auth service's HTTPS endpoints on the given
// mux. Must be called after Service.Init has resolved the live
// Repository (typically via ServiceOpts.OnReady on a process that
// hosts the auth kind).
//
// Routes mounted:
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
	case AuthErrorInvalidCredentials,
		AuthErrorUsernameInvalid,
		AuthErrorPasswordTooWeak:
		return http.StatusBadRequest
	case AuthErrorUsernameTaken:
		return http.StatusConflict
	case AuthErrorAccountLocked,
		AuthErrorRateLimited:
		return http.StatusTooManyRequests
	case AuthErrorTokenInvalid,
		AuthErrorTokenExpired,
		AuthErrorNotAuthenticated,
		AuthErrorMFARequired,
		AuthErrorMFAInvalid:
		return http.StatusUnauthorized
	default:
		return http.StatusInternalServerError
	}
}

// authErrorJSON is the shape of an error response body. The
// HTTP-friendly error code is included alongside the AuthError enum
// value so clients have a stable mapping.
type authErrorJSON struct {
	Code    AuthError `json:"code"`
	Message string    `json:"message"`
	// RetryAfterMs is non-zero on RATE_LIMITED and ACCOUNT_LOCKED.
	RetryAfterMs int64 `json:"retry_after_ms,omitempty"`
}

func authJSONFromError(err error) authErrorJSON {
	ae, ok := err.(*authError)
	if !ok {
		return authErrorJSON{
			Code: AuthErrorInternal, Message: err.Error(),
		}
	}
	out := authErrorJSON{Code: ae.Code, Message: ae.Msg}
	if ae.Metadata != nil {
		out.RetryAfterMs = ae.Metadata.RetryAfterMs
	}
	return out
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
			Code: AuthErrorInternal, Message: "bad request body",
		}, nil
	}
	opCtx := newHTTPOpCtx(r, s.opts.TrustedProxyHeader)
	resp, err := s.handleLogin(opCtx, &AuthLoginRequest{
		Username: req.Username, Password: req.Password, MFACode: req.MFACode,
	})
	if err != nil {
		return httpStatusFromAuthError(err), authJSONFromError(err), nil
	}
	setAuthCookie(w, opts, resp.SessionToken, s.opts.SessionTTL)
	return http.StatusOK, loginResponseJSON{
		UserID: resp.UserID, Username: resp.Username, ExpiresAtMs: resp.ExpiresAtMs,
	}, nil
}

// ----- /auth/register -----

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
			Code: AuthErrorInternal, Message: "bad request body",
		}, nil
	}
	opCtx := newHTTPOpCtx(r, s.opts.TrustedProxyHeader)
	resp, err := s.handleRegister(opCtx, &AuthRegisterRequest{
		Username: req.Username, Password: req.Password, Email: req.Email,
	})
	if err != nil {
		return httpStatusFromAuthError(err), authJSONFromError(err), nil
	}
	setAuthCookie(w, opts, resp.SessionToken, s.opts.SessionTTL)
	return http.StatusOK, loginResponseJSON{
		UserID: resp.UserID, Username: resp.Username, ExpiresAtMs: resp.ExpiresAtMs,
	}, nil
}

// ----- /auth/logout -----

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
	if _, err := s.handleLogout(opCtx, &AuthLogoutRequest{}); err != nil {
		// Even on error we clear the cookie so the client returns to
		// login. The audit row (or its absence) tracks the server
		// state.
		clearAuthCookie(w, opts)
		return httpStatusFromAuthError(err), authJSONFromError(err), nil
	}
	clearAuthCookie(w, opts)
	return http.StatusOK, struct{}{}, nil
}

// ----- /auth/refresh -----

func (s *Service) httpRefresh(w http.ResponseWriter, r *http.Request, opts HTTPOpts) (int, any, error) {
	tok := httpSessionToken(r, opts)
	if tok == "" {
		return http.StatusUnauthorized, authErrorJSON{
			Code:    AuthErrorNotAuthenticated,
			Message: "no session cookie",
		}, nil
	}
	resolved, err := s.Resolve(r.Context(), tok)
	if err != nil {
		clearAuthCookie(w, opts)
		return http.StatusUnauthorized, authErrorJSON{
			Code:    AuthErrorTokenInvalid,
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

// ----- /auth/me -----

func (s *Service) httpMe(w http.ResponseWriter, r *http.Request, opts HTTPOpts) (int, any, error) {
	tok := httpSessionToken(r, opts)
	if tok == "" {
		return http.StatusUnauthorized, authErrorJSON{
			Code:    AuthErrorNotAuthenticated,
			Message: "no session cookie",
		}, nil
	}
	resolved, err := s.Resolve(r.Context(), tok)
	if err != nil {
		clearAuthCookie(w, opts)
		return http.StatusUnauthorized, authErrorJSON{
			Code:    AuthErrorTokenInvalid,
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

// ----- /auth/change-password -----

type changePasswordRequestJSON struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (s *Service) httpChangePassword(w http.ResponseWriter, r *http.Request, opts HTTPOpts) (int, any, error) {
	tok := httpSessionToken(r, opts)
	if tok == "" {
		return http.StatusUnauthorized, authErrorJSON{
			Code:    AuthErrorNotAuthenticated,
			Message: "no session cookie",
		}, nil
	}
	var req changePasswordRequestJSON
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return http.StatusBadRequest, authErrorJSON{
			Code: AuthErrorInternal, Message: "bad request body",
		}, nil
	}
	opCtx := newHTTPOpCtx(r, s.opts.TrustedProxyHeader)
	WithSessionToken(opCtx, tok)
	if _, err := s.handleChangePassword(opCtx, &AuthChangePasswordRequest{
		CurrentPassword: req.CurrentPassword, NewPassword: req.NewPassword,
	}); err != nil {
		return httpStatusFromAuthError(err), authJSONFromError(err), nil
	}
	// Password change revokes all OTHER sessions for the user; the
	// current cookie remains valid. Don't touch the cookie here.
	return http.StatusOK, struct{}{}, nil
}
