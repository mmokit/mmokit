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

// HandleRemoveMember removes a target user from a channel.
// Capability-gated: caller must be a channel admin OR hold the global
// "chat.admin" capability.
//
// Fans out ChatMemberLeftEvent to remaining members (excluding the
// removed user — they will, in practice, also be unsubscribed and so
// not in the fanout target set).
func (s *Service) HandleRemoveMember(opCtx *ops.OpContext, req *ChatRemoveMemberRequest) (*ChatRemoveMemberResponse, error) {
	if opCtx == nil {
		return errResp[ChatRemoveMemberResponse](ChatErrorInternal, "missing op context", 0)
	}
	callerID := s.callerFromOpCtx(opCtx)
	if callerID == uuid.Nil {
		return errResp[ChatRemoveMemberResponse](ChatErrorPermissionDenied, "not online", 0)
	}
	chID, err := uuid.Parse(req.ChannelID)
	if err != nil {
		return errResp[ChatRemoveMemberResponse](ChatErrorChannelNotFound, "invalid channel_id", 0)
	}
	targetID, err := uuid.Parse(req.UserID)
	if err != nil {
		return errResp[ChatRemoveMemberResponse](ChatErrorInternal, "invalid user_id", 0)
	}
	if !s.canModerate(callerID, chID) {
		return errResp[ChatRemoveMemberResponse](ChatErrorPermissionDenied, "denied", 0)
	}

	s.mu.RLock()
	c, ok := s.channels[chID]
	s.mu.RUnlock()
	if !ok {
		return errResp[ChatRemoveMemberResponse](ChatErrorChannelNotFound, "channel not found", 0)
	}
	if c.Kind == "system_all" {
		return errResp[ChatRemoveMemberResponse](ChatErrorPermissionDenied, "cannot mutate system_all members", 0)
	}

	if err := s.repo.RemoveMember(context.Background(), chID, targetID); err != nil && err != ErrMemberNotFound {
		return errResp[ChatRemoveMemberResponse](ChatErrorInternal, "remove member: "+err.Error(), 0)
	}

	// In-memory bookkeeping: drop from membership + userChans + subs.
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
	// If target was online, drop their conn from subs[chid].
	if targetConn, online := s.online[targetID]; online {
		if subs := s.subs[chID]; subs != nil {
			delete(subs, targetConn)
		}
	}
	// Snapshot fanout targets — remaining subscribers (target's conn
	// already removed above).
	targets := make([]uint32, 0, len(s.subs[chID]))
	for cid := range s.subs[chID] {
		targets = append(targets, cid)
	}
	send := s.sendEventFn
	s.mu.Unlock()

	if s.ctx != nil && s.ctx.Logger != nil {
		s.ctx.Logger.Log(logCat, "remove_member: channel=%s user=%s caller=%s",
			chID, targetID, callerID)
	}

	if send != nil {
		ev := &ChatMemberLeftEvent{
			ChannelID: chID.String(),
			UserID:    targetID.String(),
		}
		for _, cid := range targets {
			send(cid, ev)
		}
	}
	return &ChatRemoveMemberResponse{}, nil
}

// HandleBulkSetMembers wholesale-replaces the member set of a channel.
// Caller must canModerate. Existing members not in the new set are
// removed; new members are added with role "member".
//
// Fanout:
//   - ChatMemberJoinedEvent for each newly-added user, sent to the
//     post-mutation subscriber set EXCLUDING the new user's conn.
//   - ChatMemberLeftEvent for each departed user, sent to the
//     post-mutation subscriber set (departed user already unsubscribed).
//
// Existing members (present in both old + new) get nothing.
func (s *Service) HandleBulkSetMembers(opCtx *ops.OpContext, req *ChatBulkSetMembersRequest) (*ChatBulkSetMembersResponse, error) {
	if opCtx == nil {
		return errResp[ChatBulkSetMembersResponse](ChatErrorInternal, "missing op context", 0)
	}
	callerID := s.callerFromOpCtx(opCtx)
	if callerID == uuid.Nil {
		return errResp[ChatBulkSetMembersResponse](ChatErrorPermissionDenied, "not online", 0)
	}
	chID, err := uuid.Parse(req.ChannelID)
	if err != nil {
		return errResp[ChatBulkSetMembersResponse](ChatErrorChannelNotFound, "invalid channel_id", 0)
	}
	if !s.canModerate(callerID, chID) {
		return errResp[ChatBulkSetMembersResponse](ChatErrorPermissionDenied, "denied", 0)
	}

	// Parse user IDs.
	newIDs := make([]uuid.UUID, 0, len(req.UserIDs))
	for _, raw := range req.UserIDs {
		uid, err := uuid.Parse(raw)
		if err != nil {
			return errResp[ChatBulkSetMembersResponse](ChatErrorInternal, "invalid user_id: "+raw, 0)
		}
		newIDs = append(newIDs, uid)
	}

	s.mu.RLock()
	c, ok := s.channels[chID]
	s.mu.RUnlock()
	if !ok {
		return errResp[ChatBulkSetMembersResponse](ChatErrorChannelNotFound, "channel not found", 0)
	}
	if c.Kind == "system_all" {
		return errResp[ChatBulkSetMembersResponse](ChatErrorPermissionDenied, "cannot mutate system_all members", 0)
	}

	// Persist via wholesale replace.
	if err := s.repo.BulkSetMembers(context.Background(), chID, newIDs, "member"); err != nil {
		return errResp[ChatBulkSetMembersResponse](ChatErrorInternal, "bulk set: "+err.Error(), 0)
	}

	// Snapshot old membership, compute diff, replace, rebuild subs from
	// online intersect.
	s.mu.Lock()
	oldMembers := s.membership[chID]
	if oldMembers == nil {
		oldMembers = map[uuid.UUID]string{}
	}
	newSet := make(map[uuid.UUID]struct{}, len(newIDs))
	for _, uid := range newIDs {
		newSet[uid] = struct{}{}
	}

	// Compute departed (in old, not in new) and added (in new, not in old).
	var departed []uuid.UUID
	for uid := range oldMembers {
		if _, stays := newSet[uid]; !stays {
			departed = append(departed, uid)
		}
	}
	var added []uuid.UUID
	for _, uid := range newIDs {
		if _, was := oldMembers[uid]; !was {
			added = append(added, uid)
		}
	}

	// Wholesale-replace membership[chid] (preserving any prior admin role
	// for users still present is intentionally NOT done here — BulkSetMembers
	// resets all to "member" per the repo contract).
	s.membership[chID] = map[uuid.UUID]string{}
	for _, uid := range newIDs {
		s.membership[chID][uid] = "member"
	}

	// Drop chID from departed users' userChans.
	for _, uid := range departed {
		if uc := s.userChans[uid]; uc != nil {
			delete(uc, chID)
			if len(uc) == 0 {
				delete(s.userChans, uid)
			}
		}
	}
	// Add chID to added users' userChans.
	for _, uid := range added {
		if s.userChans[uid] == nil {
			s.userChans[uid] = map[uuid.UUID]struct{}{}
		}
		s.userChans[uid][chID] = struct{}{}
	}

	// Rebuild subs[chid] from online intersect.
	s.subs[chID] = map[uint32]struct{}{}
	for _, uid := range newIDs {
		if conn, online := s.online[uid]; online {
			s.subs[chID][conn] = struct{}{}
		}
	}

	// Snapshot fanout state. For each added user, snapshot the "post-add
	// minus this user" subscriber set; departing users' conns are not in
	// the new subs[chID] (already gone).
	postSubs := make([]uint32, 0, len(s.subs[chID]))
	for cid := range s.subs[chID] {
		postSubs = append(postSubs, cid)
	}
	addedConns := make(map[uuid.UUID]uint32, len(added))
	addedUsernames := make(map[uuid.UUID]string, len(added))
	for _, uid := range added {
		if conn, online := s.online[uid]; online {
			addedConns[uid] = conn
		}
		addedUsernames[uid] = s.usernames[uid]
	}
	send := s.sendEventFn
	s.mu.Unlock()

	if s.ctx != nil && s.ctx.Logger != nil {
		s.ctx.Logger.Log(logCat, "bulk_set_members: channel=%s caller=%s added=%d departed=%d total=%d",
			chID, callerID, len(added), len(departed), len(newIDs))
	}

	if send == nil {
		return &ChatBulkSetMembersResponse{}, nil
	}

	// Joined events (one per newly-added user; recipients = postSubs minus
	// the new user's own conn).
	for _, uid := range added {
		ev := &ChatMemberJoinedEvent{
			ChannelID: chID.String(),
			UserID:    uid.String(),
			Username:  addedUsernames[uid],
		}
		excludeConn, hasConn := addedConns[uid]
		for _, cid := range postSubs {
			if hasConn && cid == excludeConn {
				continue
			}
			send(cid, ev)
		}
	}
	// Left events (one per departed user; recipients = postSubs).
	for _, uid := range departed {
		ev := &ChatMemberLeftEvent{
			ChannelID: chID.String(),
			UserID:    uid.String(),
		}
		for _, cid := range postSubs {
			send(cid, ev)
		}
	}
	return &ChatBulkSetMembersResponse{}, nil
}
