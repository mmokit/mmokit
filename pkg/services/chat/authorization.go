package chat

import (
	"context"

	"github.com/google/uuid"

	"github.com/zenion/mmokit/pkg/ops"
)

// SystemCallerID is the sentinel UUID returned by callerFromOpCtx when
// opCtx.SystemTrusted is set (e.g. ops dispatched from the bare server
// console wrapper). hasGlobalAdmin and canModerate treat it as having
// full chat-admin authority. Persisted into Mute.MutedBy / Ban.BannedBy
// when a trusted console operator issues those actions — distinguishable
// from real users in audit logs by the all-FF byte pattern.
var SystemCallerID = uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")

// canModerate returns true if userID has either:
//   - the global "chat.admin" capability (cached), or
//   - role == "admin" on the specific channel.
//
// Returns true unconditionally for SystemCallerID (trusted console).
// Returns false on any auth-side lookup error (defensive — the right
// thing for an authorization check is to deny on error).
func (s *Service) canModerate(userID, channelID uuid.UUID) bool {
	if userID == SystemCallerID {
		return true
	}
	if s.hasGlobalAdmin(userID) {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	role := s.membership[channelID][userID]
	return role == "admin"
}

// hasGlobalAdmin returns true if userID holds the global "chat.admin"
// capability. Cached via auth.CapabilityCache (30s TTL). Returns true
// unconditionally for SystemCallerID (trusted console). Returns false
// when the auth repository is not wired (unit-test default) or any
// error is returned by the repo.
func (s *Service) hasGlobalAdmin(userID uuid.UUID) bool {
	if userID == SystemCallerID {
		return true
	}
	if s.authCap == nil {
		return false
	}
	has, err := s.authCap.HasCapability(context.Background(), userID, "chat.admin")
	if err != nil {
		return false
	}
	return has
}

// CanModerate is the exported test-facing form of canModerate.
func (s *Service) CanModerate(userID, channelID uuid.UUID) bool {
	return s.canModerate(userID, channelID)
}

// callerFromOpCtx resolves the caller's user_id from opCtx. When
// opCtx.SystemTrusted is set (cmdsys SourceConsole wrappers), returns
// SystemCallerID — handlers then bypass connIndex-based identity
// resolution and pass the trusted-system auth checks. Otherwise looks
// up the connID in the connIndex map (populated by HandleSessionEnter)
// and returns uuid.Nil if not online — callers must handle that.
func (s *Service) callerFromOpCtx(opCtx *ops.OpContext) uuid.UUID {
	if opCtx == nil {
		return uuid.Nil
	}
	if opCtx.SystemTrusted {
		return SystemCallerID
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.connIndex[opCtx.ConnID]
}
