package chat

import (
	"context"

	"github.com/google/uuid"

	"github.com/zenion/mmoserver/pkg/ops"
)

// canModerate returns true if userID has either:
//   - the global "chat.admin" capability (cached), or
//   - role == "admin" on the specific channel.
//
// Returns false on any auth-side lookup error (defensive — the right
// thing for an authorization check is to deny on error).
func (s *Service) canModerate(userID, channelID uuid.UUID) bool {
	if s.hasGlobalAdmin(userID) {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	role := s.membership[channelID][userID]
	return role == "admin"
}

// hasGlobalAdmin returns true if userID holds the global "chat.admin"
// capability. Cached via auth.CapabilityCache (30s TTL). Returns false
// when the auth repository is not wired (unit-test default) or any
// error is returned by the repo.
func (s *Service) hasGlobalAdmin(userID uuid.UUID) bool {
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

// callerFromOpCtx resolves the caller's user_id from opCtx via the
// connIndex map (populated by HandleSessionEnter). Returns uuid.Nil
// when opCtx is nil or the connID is not online — callers must handle
// both failure modes.
func (s *Service) callerFromOpCtx(opCtx *ops.OpContext) uuid.UUID {
	if opCtx == nil {
		return uuid.Nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.connIndex[opCtx.ConnID]
}
