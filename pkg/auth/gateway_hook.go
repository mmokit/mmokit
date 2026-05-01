package auth

import (
	"google.golang.org/protobuf/proto"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/logger"
)

// GatewayHook lets the gateway extract identity-bind fields from auth
// service responses without depending on enginepb auth proto types
// directly. Auth registers an instance via mmokit.RegisterAuthService
// at startup; the gateway invokes ProcessResponse for any response from
// the "auth" kind before forwarding it to the client.
//
// Separation of concerns: the gateway knows "the auth kind exists and
// may want to inspect responses"; pkg/auth knows the proto shapes.
// Adding ops to auth never requires gateway changes.
type GatewayHook struct {
	Logger    *logger.Logger
	OnSuccess func(connID uint32, userID, username, sessionToken string, expiresAtMs int64)
	OnLogout  func(connID uint32)
}

// ProcessResponse is called by the gateway whenever a response from the
// "auth" kind is about to be forwarded to the client. The implementation
// type-switches on opCode, unmarshals the response, and invokes the
// appropriate callback so the gateway can update its per-connection
// auth state.
func (h *GatewayHook) ProcessResponse(connID uint32, opCode uint32, payload []byte) {
	if h == nil {
		return
	}
	switch enginepb.AuthOpCode(opCode) {
	case enginepb.AuthOpCode_AUTH_OPCODE_LOGIN:
		var m enginepb.AuthLoginResponse
		if err := proto.Unmarshal(payload, &m); err != nil {
			if h.Logger != nil {
				h.Logger.Log(logCat, "gateway hook: bad LoginResponse: %v", err)
			}
			return
		}
		if h.OnSuccess != nil {
			h.OnSuccess(connID, m.UserId, m.Username, m.SessionToken, m.ExpiresAtMs)
		}
	case enginepb.AuthOpCode_AUTH_OPCODE_REGISTER:
		var m enginepb.AuthRegisterResponse
		if err := proto.Unmarshal(payload, &m); err != nil {
			if h.Logger != nil {
				h.Logger.Log(logCat, "gateway hook: bad RegisterResponse: %v", err)
			}
			return
		}
		if h.OnSuccess != nil {
			h.OnSuccess(connID, m.UserId, m.Username, m.SessionToken, m.ExpiresAtMs)
		}
	case enginepb.AuthOpCode_AUTH_OPCODE_VALIDATE_TOKEN:
		var m enginepb.AuthValidateTokenResponse
		if err := proto.Unmarshal(payload, &m); err != nil {
			if h.Logger != nil {
				h.Logger.Log(logCat, "gateway hook: bad ValidateTokenResponse: %v", err)
			}
			return
		}
		if h.OnSuccess != nil {
			// ValidateToken response doesn't carry session_token (client
			// already has it) — gateway preserves the request-side token.
			h.OnSuccess(connID, m.UserId, m.Username, "", m.ExpiresAtMs)
		}
	case enginepb.AuthOpCode_AUTH_OPCODE_LOGOUT:
		if h.OnLogout != nil {
			h.OnLogout(connID)
		}
	}
}
