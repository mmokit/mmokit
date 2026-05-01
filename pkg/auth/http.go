package auth

import (
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/ops"
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
		enginepb.AuthError_AUTH_ERROR_NOT_AUTHENTICATED,
		enginepb.AuthError_AUTH_ERROR_MFA_REQUIRED,
		enginepb.AuthError_AUTH_ERROR_MFA_INVALID:
		return http.StatusUnauthorized
	default:
		return http.StatusInternalServerError
	}
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
