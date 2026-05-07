package auth

import (
	"github.com/zenion/mmoserver/pkg/logger"
)

// GatewayHook lets the gateway extract identity-bind fields from auth
// service responses without depending on auth's typed message layout
// directly. Auth registers an instance via mmokit.RegisterAuthService at
// startup; the typed-op dispatch path invokes the appropriate Notify*
// callback after each successful auth handler returns so the gateway can
// update its per-connection auth state.
//
// Separation of concerns: the gateway knows "the auth kind exists and may
// want to inspect responses"; pkg/services/auth knows the typed message shapes.
// Adding ops to auth never requires gateway changes.
type GatewayHook struct {
	Logger *logger.Logger
	// OnSuccess fires after a successful login / register / validate-token
	// op. token may be empty (for validate-token, which does not mint a
	// new token — the client already has it; the gateway preserves the
	// request-side value).
	OnSuccess func(connID uint32, userID, username, sessionToken string, expiresAtMs int64)
	// OnLogout fires after a successful logout op. The auth service has
	// already revoked the session row; the gateway clears its cached state
	// and tears down the WS.
	OnLogout func(connID uint32)
}

// NotifyLoginSuccess is called after a successful AuthLogin op. The
// typed-op dispatcher invokes this from the response-emit path before the
// frame is shipped to the client. No-op when h is nil or OnSuccess is
// unwired.
func (h *GatewayHook) NotifyLoginSuccess(connID uint32, resp *AuthLoginResponse) {
	if h == nil || h.OnSuccess == nil || resp == nil {
		return
	}
	h.OnSuccess(connID, resp.UserID, resp.Username, resp.SessionToken, resp.ExpiresAtMs)
}

// NotifyRegisterSuccess is called after a successful AuthRegister op.
func (h *GatewayHook) NotifyRegisterSuccess(connID uint32, resp *AuthRegisterResponse) {
	if h == nil || h.OnSuccess == nil || resp == nil {
		return
	}
	h.OnSuccess(connID, resp.UserID, resp.Username, resp.SessionToken, resp.ExpiresAtMs)
}

// NotifyValidateTokenSuccess is called after a successful
// AuthValidateToken op. The response carries no token (the client already
// has it); we pass an empty token so the gateway preserves the
// request-side value via its existing convention.
func (h *GatewayHook) NotifyValidateTokenSuccess(connID uint32, resp *AuthValidateTokenResponse) {
	if h == nil || h.OnSuccess == nil || resp == nil {
		return
	}
	h.OnSuccess(connID, resp.UserID, resp.Username, "", resp.ExpiresAtMs)
}

// NotifyLogoutSuccess is called after a successful AuthLogout op.
func (h *GatewayHook) NotifyLogoutSuccess(connID uint32) {
	if h == nil || h.OnLogout == nil {
		return
	}
	h.OnLogout(connID)
}
