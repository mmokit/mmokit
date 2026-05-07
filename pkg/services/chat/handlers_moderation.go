package chat

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/zenion/mmoserver/pkg/ops"
)

// HandleMuteUser mutes a user either globally (req.ChannelID == "" →
// MuteGlobalChannelID, requires chat.admin) or for a specific channel
// (canModerate(callerID, channelID)).
//
// Persists via repo.UpsertMute, populates s.mutes, fans out a
// ChatMutedEvent to the target's connID if online.
func (s *Service) HandleMuteUser(opCtx *ops.OpContext, req *ChatMuteUserRequest) (*ChatMuteUserResponse, error) {
	if opCtx == nil {
		return errResp[ChatMuteUserResponse](ChatErrorInternal, "missing op context", 0)
	}
	callerID := s.callerFromOpCtx(opCtx)
	if callerID == uuid.Nil {
		return errResp[ChatMuteUserResponse](ChatErrorPermissionDenied, "not online", 0)
	}
	targetID, err := uuid.Parse(req.UserID)
	if err != nil {
		return errResp[ChatMuteUserResponse](ChatErrorInternal, "invalid user_id", 0)
	}
	if req.DurationMs <= 0 {
		return errResp[ChatMuteUserResponse](ChatErrorInternal, "duration must be positive", 0)
	}

	// Resolve channel + auth gate. Empty channel_id ⇒ global mute (chat.admin).
	var chID uuid.UUID
	if req.ChannelID == "" {
		chID = MuteGlobalChannelID
		if !s.hasGlobalAdmin(callerID) {
			return errResp[ChatMuteUserResponse](ChatErrorPermissionDenied, "denied", 0)
		}
	} else {
		parsed, err := uuid.Parse(req.ChannelID)
		if err != nil {
			return errResp[ChatMuteUserResponse](ChatErrorChannelNotFound, "invalid channel_id", 0)
		}
		chID = parsed
		s.mu.RLock()
		_, channelExists := s.channels[chID]
		s.mu.RUnlock()
		if !channelExists {
			return errResp[ChatMuteUserResponse](ChatErrorChannelNotFound, "channel not found", 0)
		}
		if !s.canModerate(callerID, chID) {
			return errResp[ChatMuteUserResponse](ChatErrorPermissionDenied, "denied", 0)
		}
	}

	expires := time.Now().Add(time.Duration(req.DurationMs) * time.Millisecond)
	mute := Mute{
		UserID:    targetID,
		ChannelID: chID,
		ExpiresAt: expires,
		Reason:    req.Reason,
		MutedBy:   callerID,
	}
	if err := s.repo.UpsertMute(context.Background(), mute); err != nil {
		return errResp[ChatMuteUserResponse](ChatErrorInternal, "upsert mute: "+err.Error(), 0)
	}

	s.mu.Lock()
	s.mutes[muteKey{targetID, chID}] = mute
	targetConn, online := s.online[targetID]
	send := s.sendEventFn
	s.mu.Unlock()

	if s.ctx != nil && s.ctx.Logger != nil {
		s.ctx.Logger.Log(logCat, "mute: user=%s channel=%s duration_ms=%d caller=%s reason=%q",
			targetID, chID, req.DurationMs, callerID, req.Reason)
	}

	if online && send != nil {
		send(targetConn, &ChatMutedEvent{
			ChannelID: chIDStringForGlobal(chID),
			UntilMs:   expires.UnixMilli(),
			Reason:    req.Reason,
		})
	}
	return &ChatMuteUserResponse{}, nil
}

// HandleUnmuteUser removes a mute (channel-scoped or global). Same
// dual auth gate as HandleMuteUser: empty channel_id requires chat.admin,
// non-empty requires canModerate.
//
// Idempotent: unmuting a user who isn't currently muted returns success
// (operators don't want unmute-of-already-unmuted to fail).
func (s *Service) HandleUnmuteUser(opCtx *ops.OpContext, req *ChatUnmuteUserRequest) (*ChatUnmuteUserResponse, error) {
	if opCtx == nil {
		return errResp[ChatUnmuteUserResponse](ChatErrorInternal, "missing op context", 0)
	}
	callerID := s.callerFromOpCtx(opCtx)
	if callerID == uuid.Nil {
		return errResp[ChatUnmuteUserResponse](ChatErrorPermissionDenied, "not online", 0)
	}
	targetID, err := uuid.Parse(req.UserID)
	if err != nil {
		return errResp[ChatUnmuteUserResponse](ChatErrorInternal, "invalid user_id", 0)
	}

	var chID uuid.UUID
	if req.ChannelID == "" {
		chID = MuteGlobalChannelID
		if !s.hasGlobalAdmin(callerID) {
			return errResp[ChatUnmuteUserResponse](ChatErrorPermissionDenied, "denied", 0)
		}
	} else {
		parsed, err := uuid.Parse(req.ChannelID)
		if err != nil {
			return errResp[ChatUnmuteUserResponse](ChatErrorChannelNotFound, "invalid channel_id", 0)
		}
		chID = parsed
		s.mu.RLock()
		_, channelExists := s.channels[chID]
		s.mu.RUnlock()
		if !channelExists {
			return errResp[ChatUnmuteUserResponse](ChatErrorChannelNotFound, "channel not found", 0)
		}
		if !s.canModerate(callerID, chID) {
			return errResp[ChatUnmuteUserResponse](ChatErrorPermissionDenied, "denied", 0)
		}
	}

	// Idempotent: ErrMuteNotFound from the repo is mapped to success.
	if err := s.repo.DeleteMute(context.Background(), targetID, chID); err != nil && err != ErrMuteNotFound {
		return errResp[ChatUnmuteUserResponse](ChatErrorInternal, "delete mute: "+err.Error(), 0)
	}

	s.mu.Lock()
	delete(s.mutes, muteKey{targetID, chID})
	targetConn, online := s.online[targetID]
	send := s.sendEventFn
	s.mu.Unlock()

	if s.ctx != nil && s.ctx.Logger != nil {
		s.ctx.Logger.Log(logCat, "unmute: user=%s channel=%s caller=%s",
			targetID, chID, callerID)
	}

	if online && send != nil {
		send(targetConn, &ChatUnmutedEvent{
			ChannelID: chIDStringForGlobal(chID),
		})
	}
	return &ChatUnmuteUserResponse{}, nil
}

// chIDStringForGlobal returns "" when chID is the MuteGlobalChannelID
// sentinel; otherwise the canonical UUID string. Used to translate the
// sentinel back to wire-form "empty = global" semantics.
func chIDStringForGlobal(chID uuid.UUID) string {
	if chID == MuteGlobalChannelID {
		return ""
	}
	return chID.String()
}
