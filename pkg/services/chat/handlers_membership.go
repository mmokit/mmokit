package chat

import (
	"context"

	"github.com/google/uuid"

	"github.com/zenion/mmoserver/pkg/ops"
)

// HandleAddMember adds (or upserts) a user as a member of a channel.
// Capability-gated: caller must be a channel admin OR hold the global
// "chat.admin" capability.
//
// Fans out ChatMemberJoinedEvent to existing members (excluding the
// new one — they don't need to "discover" themselves).
func (s *Service) HandleAddMember(opCtx *ops.OpContext, req *ChatAddMemberRequest) (*ChatAddMemberResponse, error) {
	if opCtx == nil {
		return errResp[ChatAddMemberResponse](ChatErrorInternal, "missing op context", 0)
	}
	callerID := s.callerFromOpCtx(opCtx)
	if callerID == uuid.Nil {
		return errResp[ChatAddMemberResponse](ChatErrorPermissionDenied, "not online", 0)
	}
	chID, err := uuid.Parse(req.ChannelID)
	if err != nil {
		return errResp[ChatAddMemberResponse](ChatErrorChannelNotFound, "invalid channel_id", 0)
	}
	targetID, err := uuid.Parse(req.UserID)
	if err != nil {
		return errResp[ChatAddMemberResponse](ChatErrorInternal, "invalid user_id", 0)
	}
	if !s.canModerate(callerID, chID) {
		return errResp[ChatAddMemberResponse](ChatErrorPermissionDenied, "denied", 0)
	}

	// Channel-existence check + role normalization.
	role := req.Role
	if role == "" {
		role = "member"
	}
	if role != "member" && role != "admin" {
		return errResp[ChatAddMemberResponse](ChatErrorInternal, "invalid role", 0)
	}

	s.mu.RLock()
	c, ok := s.channels[chID]
	s.mu.RUnlock()
	if !ok {
		return errResp[ChatAddMemberResponse](ChatErrorChannelNotFound, "channel not found", 0)
	}
	if c.Kind == "system_all" {
		return errResp[ChatAddMemberResponse](ChatErrorPermissionDenied, "cannot mutate system_all members", 0)
	}

	// Persist membership.
	if err := s.repo.AddOrUpdateMember(context.Background(), ChannelMember{
		ChannelID: chID,
		UserID:    targetID,
		Role:      role,
	}); err != nil {
		return errResp[ChatAddMemberResponse](ChatErrorInternal, "add member: "+err.Error(), 0)
	}

	// In-memory bookkeeping + snapshot fanout targets (existing members,
	// excluding the new one).
	s.mu.Lock()
	if s.membership[chID] == nil {
		s.membership[chID] = map[uuid.UUID]string{}
	}
	s.membership[chID][targetID] = role
	if s.userChans[targetID] == nil {
		s.userChans[targetID] = map[uuid.UUID]struct{}{}
	}
	s.userChans[targetID][chID] = struct{}{}
	if s.subs[chID] == nil {
		s.subs[chID] = map[uint32]struct{}{}
	}
	// If target is online, subscribe their conn.
	if targetConn, online := s.online[targetID]; online {
		s.subs[chID][targetConn] = struct{}{}
	}
	username := s.usernames[targetID]
	targetConnForExclude, _ := s.online[targetID]
	targets := make([]uint32, 0, len(s.subs[chID]))
	for cid := range s.subs[chID] {
		if cid != targetConnForExclude {
			targets = append(targets, cid)
		}
	}
	send := s.sendEventFn
	s.mu.Unlock()

	if s.ctx != nil && s.ctx.Logger != nil {
		s.ctx.Logger.Log(logCat, "add_member: channel=%s user=%s role=%s caller=%s",
			chID, targetID, role, callerID)
	}

	if send != nil {
		ev := &ChatMemberJoinedEvent{
			ChannelID: chID.String(),
			UserID:    targetID.String(),
			Username:  username,
		}
		for _, cid := range targets {
			send(cid, ev)
		}
	}
	return &ChatAddMemberResponse{}, nil
}
