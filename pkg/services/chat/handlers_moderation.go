package chat

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/mmokit/mmokit/pkg/ops"
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

// HandleKickFromChannel removes a member from a channel and notifies
// both the kicked target (ChatKickedEvent) and remaining members
// (ChatMemberLeftEvent). canModerate gated.
//
// Differs from HandleBanFromChannel: kick fully removes the membership
// row (no tombstone). To reapply with a duration, use HandleBanFromChannel.
func (s *Service) HandleKickFromChannel(opCtx *ops.OpContext, req *ChatKickRequest) (*ChatKickResponse, error) {
	if opCtx == nil {
		return errResp[ChatKickResponse](ChatErrorInternal, "missing op context", 0)
	}
	callerID := s.callerFromOpCtx(opCtx)
	if callerID == uuid.Nil {
		return errResp[ChatKickResponse](ChatErrorPermissionDenied, "not online", 0)
	}
	chID, err := uuid.Parse(req.ChannelID)
	if err != nil {
		return errResp[ChatKickResponse](ChatErrorChannelNotFound, "invalid channel_id", 0)
	}
	targetID, err := uuid.Parse(req.UserID)
	if err != nil {
		return errResp[ChatKickResponse](ChatErrorInternal, "invalid user_id", 0)
	}
	if !s.canModerate(callerID, chID) {
		return errResp[ChatKickResponse](ChatErrorPermissionDenied, "denied", 0)
	}

	s.mu.RLock()
	c, ok := s.channels[chID]
	s.mu.RUnlock()
	if !ok {
		return errResp[ChatKickResponse](ChatErrorChannelNotFound, "channel not found", 0)
	}
	if c.Kind == "system_all" {
		return errResp[ChatKickResponse](ChatErrorPermissionDenied, "cannot kick from system_all channel", 0)
	}

	if err := s.repo.RemoveMember(context.Background(), chID, targetID); err != nil && err != ErrMemberNotFound {
		return errResp[ChatKickResponse](ChatErrorInternal, "remove member: "+err.Error(), 0)
	}

	// In-memory bookkeeping + snapshot fanout state.
	s.mu.Lock()
	if m := s.membership[chID]; m != nil {
		delete(m, targetID)
	}
	if uc := s.userChans[targetID]; uc != nil {
		delete(uc, chID)
		if len(uc) == 0 {
			delete(s.userChans, targetID)
		}
	}
	targetConn, online := s.online[targetID]
	if online {
		if subs := s.subs[chID]; subs != nil {
			delete(subs, targetConn)
		}
	}
	// Remaining recipients of MemberLeftEvent (target's conn already removed).
	leftTargets := make([]uint32, 0, len(s.subs[chID]))
	for cid := range s.subs[chID] {
		leftTargets = append(leftTargets, cid)
	}
	send := s.sendEventFn
	s.mu.Unlock()

	if s.ctx != nil && s.ctx.Logger != nil {
		s.ctx.Logger.Log(logCat, "kick: channel=%s user=%s caller=%s reason=%q",
			chID, targetID, callerID, req.Reason)
	}

	if send != nil {
		if online {
			send(targetConn, &ChatKickedEvent{
				ChannelID: chID.String(),
				ByUserID:  callerID.String(),
				Reason:    req.Reason,
			})
		}
		leftEv := &ChatMemberLeftEvent{
			ChannelID: chID.String(),
			UserID:    targetID.String(),
		}
		for _, cid := range leftTargets {
			send(cid, leftEv)
		}
	}
	return &ChatKickResponse{}, nil
}

// HandleBanFromChannel sets a tombstoned-membership ban on a target
// for a given duration. canModerate gated.
//
// Differs from kick: ban PRESERVES the membership row but stamps
// banned_until/by/reason; the row is consulted on subsequent join/send
// attempts. The target's conn is unsubscribed from subs while banned
// so they stop receiving channel traffic. Fans out ChatBannedEvent to
// the target + ChatMemberLeftEvent to remaining members.
func (s *Service) HandleBanFromChannel(opCtx *ops.OpContext, req *ChatBanRequest) (*ChatBanResponse, error) {
	if opCtx == nil {
		return errResp[ChatBanResponse](ChatErrorInternal, "missing op context", 0)
	}
	callerID := s.callerFromOpCtx(opCtx)
	if callerID == uuid.Nil {
		return errResp[ChatBanResponse](ChatErrorPermissionDenied, "not online", 0)
	}
	chID, err := uuid.Parse(req.ChannelID)
	if err != nil {
		return errResp[ChatBanResponse](ChatErrorChannelNotFound, "invalid channel_id", 0)
	}
	targetID, err := uuid.Parse(req.UserID)
	if err != nil {
		return errResp[ChatBanResponse](ChatErrorInternal, "invalid user_id", 0)
	}
	if req.DurationMs <= 0 {
		return errResp[ChatBanResponse](ChatErrorInternal, "duration must be positive", 0)
	}
	if !s.canModerate(callerID, chID) {
		return errResp[ChatBanResponse](ChatErrorPermissionDenied, "denied", 0)
	}

	s.mu.RLock()
	c, ok := s.channels[chID]
	s.mu.RUnlock()
	if !ok {
		return errResp[ChatBanResponse](ChatErrorChannelNotFound, "channel not found", 0)
	}
	if c.Kind == "system_all" {
		return errResp[ChatBanResponse](ChatErrorPermissionDenied, "cannot ban from system_all channel", 0)
	}

	until := time.Now().Add(time.Duration(req.DurationMs) * time.Millisecond)
	if err := s.repo.SetMemberBan(context.Background(), chID, targetID, callerID, until, req.Reason); err != nil {
		return errResp[ChatBanResponse](ChatErrorInternal, "set ban: "+err.Error(), 0)
	}

	// In-memory bookkeeping: install ban entry; drop target's conn from
	// subs so the banned user stops receiving traffic. Membership row is
	// left in place (tombstoned) per spec.
	s.mu.Lock()
	s.bans[banKey{chID, targetID}] = banEntry{
		BannedUntil: until,
		BannedBy:    callerID,
		Reason:      req.Reason,
	}
	targetConn, online := s.online[targetID]
	if online {
		if subs := s.subs[chID]; subs != nil {
			delete(subs, targetConn)
		}
	}
	leftTargets := make([]uint32, 0, len(s.subs[chID]))
	for cid := range s.subs[chID] {
		leftTargets = append(leftTargets, cid)
	}
	send := s.sendEventFn
	s.mu.Unlock()

	if s.ctx != nil && s.ctx.Logger != nil {
		s.ctx.Logger.Log(logCat, "ban: channel=%s user=%s caller=%s duration_ms=%d reason=%q",
			chID, targetID, callerID, req.DurationMs, req.Reason)
	}

	if send != nil {
		if online {
			send(targetConn, &ChatBannedEvent{
				ChannelID: chID.String(),
				ByUserID:  callerID.String(),
				UntilMs:   until.UnixMilli(),
				Reason:    req.Reason,
			})
		}
		leftEv := &ChatMemberLeftEvent{
			ChannelID: chID.String(),
			UserID:    targetID.String(),
		}
		for _, cid := range leftTargets {
			send(cid, leftEv)
		}
	}
	return &ChatBanResponse{}, nil
}

// HandleUnbanFromChannel clears a channel-scoped ban. canModerate gated.
//
// No fanout per the plan — unbanned users may rejoin via HandleJoin
// when ready. Idempotent: ErrMemberNotFound from repo.ClearMemberBan
// (e.g. row never existed) is mapped to success.
func (s *Service) HandleUnbanFromChannel(opCtx *ops.OpContext, req *ChatUnbanRequest) (*ChatUnbanResponse, error) {
	if opCtx == nil {
		return errResp[ChatUnbanResponse](ChatErrorInternal, "missing op context", 0)
	}
	callerID := s.callerFromOpCtx(opCtx)
	if callerID == uuid.Nil {
		return errResp[ChatUnbanResponse](ChatErrorPermissionDenied, "not online", 0)
	}
	chID, err := uuid.Parse(req.ChannelID)
	if err != nil {
		return errResp[ChatUnbanResponse](ChatErrorChannelNotFound, "invalid channel_id", 0)
	}
	targetID, err := uuid.Parse(req.UserID)
	if err != nil {
		return errResp[ChatUnbanResponse](ChatErrorInternal, "invalid user_id", 0)
	}
	if !s.canModerate(callerID, chID) {
		return errResp[ChatUnbanResponse](ChatErrorPermissionDenied, "denied", 0)
	}

	s.mu.RLock()
	_, channelExists := s.channels[chID]
	s.mu.RUnlock()
	if !channelExists {
		return errResp[ChatUnbanResponse](ChatErrorChannelNotFound, "channel not found", 0)
	}

	if err := s.repo.ClearMemberBan(context.Background(), chID, targetID); err != nil && err != ErrMemberNotFound {
		return errResp[ChatUnbanResponse](ChatErrorInternal, "clear ban: "+err.Error(), 0)
	}

	s.mu.Lock()
	delete(s.bans, banKey{chID, targetID})
	s.mu.Unlock()

	if s.ctx != nil && s.ctx.Logger != nil {
		s.ctx.Logger.Log(logCat, "unban: channel=%s user=%s caller=%s",
			chID, targetID, callerID)
	}
	return &ChatUnbanResponse{}, nil
}

// HandleDeleteMessage deletes a previously-sent channel message.
// canModerate-gated; the channel is resolved via the msg_id index.
//
// RAM-only — no DB write. Sends ChatMessageDeletedEvent to all current
// channel members (lets clients clear the message from their view).
//
// Returns ChatErrorMessageUnknown for an expired/unknown msg_id, or for
// a request whose channel_id doesn't match the indexed channel for that
// msg_id (defends against forging deletes by guessing msg_ids).
func (s *Service) HandleDeleteMessage(opCtx *ops.OpContext, req *ChatDeleteMessageRequest) (*ChatDeleteMessageResponse, error) {
	if opCtx == nil {
		return errResp[ChatDeleteMessageResponse](ChatErrorInternal, "missing op context", 0)
	}
	callerID := s.callerFromOpCtx(opCtx)
	if callerID == uuid.Nil {
		return errResp[ChatDeleteMessageResponse](ChatErrorPermissionDenied, "not online", 0)
	}

	expectedChID, ok := s.msgIDIndex.Get(req.MsgID)
	if !ok {
		return errResp[ChatDeleteMessageResponse](ChatErrorMessageUnknown, "message id unknown or expired", 0)
	}
	parsedChID, err := uuid.Parse(req.ChannelID)
	if err != nil || parsedChID != expectedChID {
		return errResp[ChatDeleteMessageResponse](ChatErrorMessageUnknown, "channel mismatch", 0)
	}
	if !s.canModerate(callerID, parsedChID) {
		return errResp[ChatDeleteMessageResponse](ChatErrorPermissionDenied, "denied", 0)
	}

	if s.ctx != nil && s.ctx.Logger != nil {
		s.ctx.Logger.Log(logCat, "delete_message: msg_id=%s channel=%s caller=%s",
			req.MsgID, parsedChID, callerID)
	}

	s.fanoutEvent(parsedChID, &ChatMessageDeletedEvent{
		MsgID:           req.MsgID,
		ChannelID:       parsedChID.String(),
		DeletedByUserID: callerID.String(),
	})
	return &ChatDeleteMessageResponse{}, nil
}

// HandleBroadcastSystem fans out a system message to a channel as a
// ChatMessageEvent with SenderUserID="" (the convention for system
// broadcasts). Requires global chat.admin (NOT canModerate) — channel
// admins can't broadcast as the system.
func (s *Service) HandleBroadcastSystem(opCtx *ops.OpContext, req *ChatBroadcastRequest) (*ChatBroadcastResponse, error) {
	if opCtx == nil {
		return errResp[ChatBroadcastResponse](ChatErrorInternal, "missing op context", 0)
	}
	callerID := s.callerFromOpCtx(opCtx)
	if callerID == uuid.Nil {
		return errResp[ChatBroadcastResponse](ChatErrorPermissionDenied, "not online", 0)
	}
	if !s.hasGlobalAdmin(callerID) {
		return errResp[ChatBroadcastResponse](ChatErrorPermissionDenied, "denied", 0)
	}

	chID, err := uuid.Parse(req.ChannelID)
	if err != nil {
		return errResp[ChatBroadcastResponse](ChatErrorChannelNotFound, "invalid channel_id", 0)
	}
	s.mu.RLock()
	_, exists := s.channels[chID]
	s.mu.RUnlock()
	if !exists {
		return errResp[ChatBroadcastResponse](ChatErrorChannelNotFound, "channel not found", 0)
	}
	if len(req.Body) > s.opts.MaxMessageLen {
		return errResp[ChatBroadcastResponse](ChatErrorPayloadTooLarge, "body exceeds max length", 0)
	}

	msgID, err := uuid.NewV7()
	if err != nil {
		return errResp[ChatBroadcastResponse](ChatErrorInternal, "msg_id: "+err.Error(), 0)
	}
	s.msgIDIndex.Put(msgID.String(), chID)

	if s.ctx != nil && s.ctx.Logger != nil {
		s.ctx.Logger.Log(logCat, "broadcast: channel=%s caller=%s body_len=%d",
			chID, callerID, len(req.Body))
	}

	s.fanoutEvent(chID, &ChatMessageEvent{
		MsgID:          msgID.String(),
		ChannelID:      chID.String(),
		SenderUserID:   "", // empty = system broadcast
		SenderUsername: "",
		Body:           req.Body,
		SentAtMs:       timeNowMs(),
	})
	return &ChatBroadcastResponse{}, nil
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
