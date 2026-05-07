package chat

import (
	"context"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/zenion/mmoserver/pkg/ops"
)

// HandleSend handles CHAT_OP_SEND_MESSAGE. Fans out a ChatMessageEvent
// to every member of the channel.
//
// v1 first-cut: no rate limit, no mute, no ban check, no auth gate —
// those land in subsequent phases. Validation is just (channel exists,
// payload size).
//
// Sender identity is resolved from opCtx.ConnID via the connIndex map
// populated by HandleSessionEnter (which the gateway calls after auth).
// The OpContext type itself does not carry a UserID field; chat owns
// the connID→userID mapping.
func (s *Service) HandleSend(opCtx *ops.OpContext, req *ChatSendRequest) (*ChatSendResponse, error) {
	if len(req.Body) > s.opts.MaxMessageLen {
		return errResp[ChatSendResponse](ChatErrorPayloadTooLarge, "body exceeds max length", 0)
	}
	chID, err := uuid.Parse(req.ChannelID)
	if err != nil {
		return errResp[ChatSendResponse](ChatErrorChannelNotFound, "invalid channel_id", 0)
	}
	s.mu.RLock()
	c, ok := s.channels[chID]
	if !ok {
		s.mu.RUnlock()
		return errResp[ChatSendResponse](ChatErrorChannelNotFound, "channel not found", 0)
	}
	var senderID uuid.UUID
	if opCtx != nil {
		senderID = s.connIndex[opCtx.ConnID]
	}
	senderUsername := s.usernameForUserLocked(senderID)
	s.mu.RUnlock()

	// Static checks first (mute, ban) — no state mutation. Then the
	// consume-on-success checks (rate-limit, slow-mode). Order matters:
	// muted users must not consume rate-limit tokens.
	if active, retry := s.muteCheck(senderID, chID); active {
		return errResp[ChatSendResponse](ChatErrorMuted, "muted", retry.Milliseconds())
	}
	if active, retry := s.banCheck(senderID, chID); active {
		return errResp[ChatSendResponse](ChatErrorBanned, "banned", retry.Milliseconds())
	}
	if ok, retry := s.rateLimitTake(senderID); !ok {
		return errResp[ChatSendResponse](ChatErrorRateLimited, "rate limited", retry.Milliseconds())
	}
	if ok, retry := s.slowModeCheck(chID, senderID); !ok {
		return errResp[ChatSendResponse](ChatErrorSlowMode, "slow mode active", retry.Milliseconds())
	}

	msgID, err := uuid.NewV7()
	if err != nil {
		return errResp[ChatSendResponse](ChatErrorInternal, "msg_id: "+err.Error(), 0)
	}
	s.msgIDIndex.Put(msgID.String(), chID)

	now := timeNowMs()
	ev := &ChatMessageEvent{
		MsgID:          msgID.String(),
		ChannelID:      chID.String(),
		SenderUserID:   senderID.String(),
		SenderUsername: senderUsername,
		Body:           req.Body,
		SentAtMs:       now,
	}
	s.fanoutEvent(chID, ev)
	_ = c // future: per-channel logging

	return &ChatSendResponse{MsgID: msgID.String(), SentAtMs: now}, nil
}

// usernameForUserLocked returns the in-memory cached username for a
// user (snapshotted at session-enter). Returns empty string when not
// online — callers can fall back to a repo lookup if needed.
//
// Caller MUST hold s.mu (R or W).
func (s *Service) usernameForUserLocked(userID uuid.UUID) string {
	return s.usernames[userID]
}

// HandleCreate registers a new user-owned custom channel. Caller becomes
// the channel admin and is auto-subscribed.
//
// Sender identity is resolved from opCtx.ConnID via connIndex; the caller
// must therefore have a live ChatSessionEnter (i.e. be online) before
// invoking this op.
func (s *Service) HandleCreate(opCtx *ops.OpContext, req *ChatCreateRequest) (*ChatCreateResponse, error) {
	// Resolve caller from connID
	if opCtx == nil {
		return errResp[ChatCreateResponse](ChatErrorInternal, "missing op context", 0)
	}
	s.mu.RLock()
	callerID, online := s.connIndex[opCtx.ConnID]
	callerConnID := opCtx.ConnID
	s.mu.RUnlock()
	if !online || callerID == uuid.Nil {
		return errResp[ChatCreateResponse](ChatErrorPermissionDenied, "not online", 0)
	}

	// Validate slug
	slug := req.Slug
	if slug == "" {
		return errResp[ChatCreateResponse](ChatErrorReservedSlug, "empty slug", 0)
	}
	if len(slug) > s.opts.MaxChannelSlugLen {
		return errResp[ChatCreateResponse](ChatErrorReservedSlug, "slug exceeds max length", 0)
	}
	if !validateSlugFormat(slug) {
		return errResp[ChatCreateResponse](ChatErrorReservedSlug, "slug must match [a-z0-9_-]", 0)
	}
	for _, prefix := range s.opts.ReservedSlugPrefixes {
		if strings.HasPrefix(slug, prefix) {
			return errResp[ChatCreateResponse](ChatErrorReservedSlug, "slug uses reserved prefix", 0)
		}
	}

	// Validate password length (when present)
	if req.Password != "" && len(req.Password) < s.opts.MinChannelPasswordLen {
		return errResp[ChatCreateResponse](ChatErrorInvalidPassword, "password too short", 0)
	}

	// Enforce MaxChannelsPerUser (count current custom channels owned by caller)
	s.mu.RLock()
	owned := 0
	for _, c := range s.channels {
		if c.Kind == "custom" && c.OwnerUserID == callerID {
			owned++
		}
	}
	s.mu.RUnlock()
	if owned >= s.opts.MaxChannelsPerUser {
		return errResp[ChatCreateResponse](ChatErrorMaxChannelsReached, "max channels reached", 0)
	}

	// Hash password (if present)
	var passwordHash string
	if req.Password != "" {
		h, err := HashChannelPassword(req.Password)
		if err != nil {
			return errResp[ChatCreateResponse](ChatErrorInternal, "hash: "+err.Error(), 0)
		}
		passwordHash = h
	}

	// Persist channel
	c := Channel{
		Slug:         slug,
		Kind:         "custom",
		Topic:        req.Topic,
		PasswordHash: passwordHash,
		OwnerUserID:  callerID,
	}
	stored, err := s.repo.UpsertChannel(context.Background(), c)
	if err != nil {
		if err == ErrChannelSlugInUse {
			return errResp[ChatCreateResponse](ChatErrorSlugInUse, "slug already in use", 0)
		}
		return errResp[ChatCreateResponse](ChatErrorInternal, "upsert: "+err.Error(), 0)
	}

	// Persist creator as admin member
	if err := s.repo.AddOrUpdateMember(context.Background(), ChannelMember{
		ChannelID: stored.ChannelID,
		UserID:    callerID,
		Role:      "admin",
	}); err != nil {
		return errResp[ChatCreateResponse](ChatErrorInternal, "add member: "+err.Error(), 0)
	}

	// In-memory bookkeeping
	s.mu.Lock()
	s.channels[stored.ChannelID] = stored
	s.bySlug[stored.Slug] = stored.ChannelID
	if s.membership[stored.ChannelID] == nil {
		s.membership[stored.ChannelID] = map[uuid.UUID]string{}
	}
	s.membership[stored.ChannelID][callerID] = "admin"
	if s.userChans[callerID] == nil {
		s.userChans[callerID] = map[uuid.UUID]struct{}{}
	}
	s.userChans[callerID][stored.ChannelID] = struct{}{}
	if s.subs[stored.ChannelID] == nil {
		s.subs[stored.ChannelID] = map[uint32]struct{}{}
	}
	s.subs[stored.ChannelID][callerConnID] = struct{}{}
	memberCount := len(s.membership[stored.ChannelID])
	info := channelInfoOfLocked(stored, memberCount)
	s.mu.Unlock()

	if s.ctx != nil && s.ctx.Logger != nil {
		s.ctx.Logger.Log(logCat, "create: channel=%s slug=%s owner=%s has_password=%v",
			stored.ChannelID, stored.Slug, callerID, passwordHash != "")
	}

	// Creator is the only member at creation time → no fanout to others.
	return &ChatCreateResponse{Channel: info}, nil
}

// HandleJoin adds the caller as a member of the named channel.
//
// Refuses on:
//   - non-custom channel kind (system_all/system_predicate)
//   - wrong password (when channel is password-protected)
//   - active ban
//   - MaxMembersPerCustomChannel reached
func (s *Service) HandleJoin(opCtx *ops.OpContext, req *ChatJoinRequest) (*ChatJoinResponse, error) {
	if opCtx == nil {
		return errResp[ChatJoinResponse](ChatErrorInternal, "missing op context", 0)
	}
	s.mu.RLock()
	callerID, online := s.connIndex[opCtx.ConnID]
	s.mu.RUnlock()
	if !online || callerID == uuid.Nil {
		return errResp[ChatJoinResponse](ChatErrorPermissionDenied, "not online", 0)
	}
	connID := opCtx.ConnID

	// Resolve channel by slug
	s.mu.RLock()
	chid, ok := s.bySlug[req.Slug]
	if !ok {
		s.mu.RUnlock()
		return errResp[ChatJoinResponse](ChatErrorChannelNotFound, "channel not found", 0)
	}
	c, ok := s.channels[chid]
	if !ok {
		s.mu.RUnlock()
		return errResp[ChatJoinResponse](ChatErrorChannelNotFound, "channel not found", 0)
	}
	// Custom-only — system channels are joined implicitly via SessionEnter.
	if c.Kind != "custom" {
		s.mu.RUnlock()
		return errResp[ChatJoinResponse](ChatErrorPermissionDenied, "cannot join system channel", 0)
	}
	storedHash := c.PasswordHash
	// Ban check (snapshotted under lock)
	ban, banned := s.bans[banKey{chid, callerID}]
	memberCount := len(s.membership[chid])
	s.mu.RUnlock()

	if banned && ban.BannedUntil.After(time.Now()) {
		return errResp[ChatJoinResponse](ChatErrorBanned, "user is banned", 0)
	}

	// Password check (lock-free; argon2 is expensive)
	if storedHash != "" {
		ok, err := VerifyChannelPassword(req.Password, storedHash)
		if err != nil || !ok {
			return errResp[ChatJoinResponse](ChatErrorInvalidPassword, "invalid password", 0)
		}
	}

	// Capacity check
	if memberCount >= s.opts.MaxMembersPerCustomChannel {
		return errResp[ChatJoinResponse](ChatErrorMaxMembersReached, "channel full", 0)
	}

	// Persist membership
	if err := s.repo.AddOrUpdateMember(context.Background(), ChannelMember{
		ChannelID: chid,
		UserID:    callerID,
		Role:      "member",
	}); err != nil {
		return errResp[ChatJoinResponse](ChatErrorInternal, "add member: "+err.Error(), 0)
	}

	// In-memory bookkeeping
	s.mu.Lock()
	if s.membership[chid] == nil {
		s.membership[chid] = map[uuid.UUID]string{}
	}
	s.membership[chid][callerID] = "member"
	if s.userChans[callerID] == nil {
		s.userChans[callerID] = map[uuid.UUID]struct{}{}
	}
	s.userChans[callerID][chid] = struct{}{}
	if s.subs[chid] == nil {
		s.subs[chid] = map[uint32]struct{}{}
	}
	s.subs[chid][connID] = struct{}{}
	username := s.usernames[callerID]
	count := len(s.membership[chid])
	info := channelInfoOfLocked(c, count)
	// Snapshot fanout targets, excluding the joiner.
	targets := make([]uint32, 0, len(s.subs[chid]))
	for cid := range s.subs[chid] {
		if cid != connID {
			targets = append(targets, cid)
		}
	}
	send := s.sendEventFn
	s.mu.Unlock()

	if s.ctx != nil && s.ctx.Logger != nil {
		s.ctx.Logger.Log(logCat, "join: channel=%s user=%s username=%s", chid, callerID, username)
	}

	if send != nil {
		ev := &ChatMemberJoinedEvent{
			ChannelID: chid.String(),
			UserID:    callerID.String(),
			Username:  username,
		}
		for _, cid := range targets {
			send(cid, ev)
		}
	}

	return &ChatJoinResponse{Channel: info}, nil
}

// HandleLeave removes the caller's membership from a custom or
// system_predicate channel. SYSTEM_ALL channels reject because
// per spec §15.3 they are mandatory in v1.
func (s *Service) HandleLeave(opCtx *ops.OpContext, req *ChatLeaveRequest) (*ChatLeaveResponse, error) {
	if opCtx == nil {
		return errResp[ChatLeaveResponse](ChatErrorInternal, "missing op context", 0)
	}
	chid, err := uuid.Parse(req.ChannelID)
	if err != nil {
		return errResp[ChatLeaveResponse](ChatErrorChannelNotFound, "invalid channel_id", 0)
	}
	s.mu.RLock()
	callerID, online := s.connIndex[opCtx.ConnID]
	s.mu.RUnlock()
	if !online || callerID == uuid.Nil {
		return errResp[ChatLeaveResponse](ChatErrorPermissionDenied, "not online", 0)
	}
	connID := opCtx.ConnID

	// Lookup channel + membership under a single RLock snapshot.
	s.mu.RLock()
	c, ok := s.channels[chid]
	if !ok {
		s.mu.RUnlock()
		return errResp[ChatLeaveResponse](ChatErrorChannelNotFound, "channel not found", 0)
	}
	if c.Kind == "system_all" {
		s.mu.RUnlock()
		return errResp[ChatLeaveResponse](ChatErrorPermissionDenied, "cannot leave system channel", 0)
	}
	role := s.membership[chid][callerID]
	s.mu.RUnlock()
	if role == "" {
		return errResp[ChatLeaveResponse](ChatErrorNotAMember, "not a member", 0)
	}

	// Persist removal
	if err := s.repo.RemoveMember(context.Background(), chid, callerID); err != nil && err != ErrMemberNotFound {
		return errResp[ChatLeaveResponse](ChatErrorInternal, "remove member: "+err.Error(), 0)
	}

	// In-memory bookkeeping + snapshot fanout targets (excluding leaver).
	s.mu.Lock()
	if m := s.membership[chid]; m != nil {
		delete(m, callerID)
	}
	if uc := s.userChans[callerID]; uc != nil {
		delete(uc, chid)
		if len(uc) == 0 {
			delete(s.userChans, callerID)
		}
	}
	if subs := s.subs[chid]; subs != nil {
		delete(subs, connID)
	}
	targets := make([]uint32, 0, len(s.subs[chid]))
	for cid := range s.subs[chid] {
		if cid != connID {
			targets = append(targets, cid)
		}
	}
	send := s.sendEventFn
	s.mu.Unlock()

	if s.ctx != nil && s.ctx.Logger != nil {
		s.ctx.Logger.Log(logCat, "leave: channel=%s user=%s", chid, callerID)
	}

	if send != nil {
		ev := &ChatMemberLeftEvent{
			ChannelID: chid.String(),
			UserID:    callerID.String(),
		}
		for _, cid := range targets {
			send(cid, ev)
		}
	}
	return &ChatLeaveResponse{}, nil
}

// HandleSendDM delivers a DM to recipient_user_id. If recipient is offline,
// the message is dropped silently per spec §8 (success returned with the
// allocated msg_id, but no fanout — not even sender echo, since there is
// no recipient to deliver to).
//
// When recipient is online, the DM event is sent to the recipient AND
// echoed back to the sender (so multi-window clients see their own sends).
func (s *Service) HandleSendDM(opCtx *ops.OpContext, req *ChatSendDMRequest) (*ChatSendDMResponse, error) {
	if opCtx == nil {
		return errResp[ChatSendDMResponse](ChatErrorInternal, "missing op context", 0)
	}
	if len(req.Body) > s.opts.MaxMessageLen {
		return errResp[ChatSendDMResponse](ChatErrorPayloadTooLarge, "body exceeds max length", 0)
	}
	recipientID, err := uuid.Parse(req.RecipientUserID)
	if err != nil {
		return errResp[ChatSendDMResponse](ChatErrorInternal, "invalid recipient_user_id", 0)
	}

	// Snapshot sender + recipient state under one RLock.
	s.mu.RLock()
	senderID, senderOnline := s.connIndex[opCtx.ConnID]
	senderConnID := opCtx.ConnID
	senderUsername := s.usernames[senderID]
	recipientConnID, recipientOnline := s.online[recipientID]
	send := s.sendEventFn
	s.mu.RUnlock()

	if !senderOnline || senderID == uuid.Nil {
		return errResp[ChatSendDMResponse](ChatErrorPermissionDenied, "not online", 0)
	}

	// Global mute applies to DMs (channel-scoped mutes do not — DMs
	// have no channel). Pass MuteGlobalChannelID for both args; the
	// helper reads the global slot first.
	if active, retry := s.muteCheck(senderID, MuteGlobalChannelID); active {
		return errResp[ChatSendDMResponse](ChatErrorMuted, "muted", retry.Milliseconds())
	}
	// Rate limit (shared per-user bucket with channel sends).
	if ok, retry := s.rateLimitTake(senderID); !ok {
		return errResp[ChatSendDMResponse](ChatErrorRateLimited, "rate limited", retry.Milliseconds())
	}

	msgID, err := uuid.NewV7()
	if err != nil {
		return errResp[ChatSendDMResponse](ChatErrorInternal, "msg_id: "+err.Error(), 0)
	}
	now := timeNowMs()

	if s.ctx != nil && s.ctx.Logger != nil {
		s.ctx.Logger.Log(logCat, "dm: sender=%s recipient=%s online=%v body_len=%d",
			senderID, recipientID, recipientOnline, len(req.Body))
	}

	// Spec §8: if recipient is offline, drop silently — no fanout (not
	// even echo). Still allocate + return msg_id for symmetry.
	if !recipientOnline {
		return &ChatSendDMResponse{MsgID: msgID.String(), SentAtMs: now}, nil
	}

	if send != nil {
		ev := &ChatDMEvent{
			MsgID:          msgID.String(),
			SenderUserID:   senderID.String(),
			SenderUsername: senderUsername,
			Body:           req.Body,
			SentAtMs:       now,
		}
		send(recipientConnID, ev)
		send(senderConnID, ev) // echo to sender so multi-window clients see own sends
	}
	return &ChatSendDMResponse{MsgID: msgID.String(), SentAtMs: now}, nil
}

// validateSlugFormat enforces [a-z0-9_-] only. Empty string is rejected
// at the call site.
func validateSlugFormat(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// HandleSessionEnter is invoked by the gateway after successful auth.
// Builds presence + subscription state for the connID; sends back a
// ChatChannelsHydratedEvent so the client knows its full channel list.
//
// opCtx may be nil — this op is invoked in-process from the gateway,
// not through the typed-op router for client traffic. Capability is
// implicit (in-process bypass per spec §15.6).
func (s *Service) HandleSessionEnter(_ *ops.OpContext, req *ChatSessionEnterRequest) (*ChatSessionEnterResponse, error) {
	uid, err := uuid.Parse(req.UserID)
	if err != nil {
		return errResp[ChatSessionEnterResponse](ChatErrorInternal, "invalid user_id", 0)
	}
	s.mu.Lock()
	s.online[uid] = req.ConnID
	s.connIndex[req.ConnID] = uid
	if s.usernames == nil {
		s.usernames = map[uuid.UUID]string{}
	}
	s.usernames[uid] = req.Username
	if req.GatewayID != "" {
		if s.gatewayConn[req.GatewayID] == nil {
			s.gatewayConn[req.GatewayID] = map[uint32]struct{}{}
		}
		s.gatewayConn[req.GatewayID][req.ConnID] = struct{}{}
	}
	// Subscribe connID to every explicit-membership channel the user belongs to
	if userChs := s.userChans[uid]; userChs != nil {
		for chid := range userChs {
			if s.subs[chid] == nil {
				s.subs[chid] = map[uint32]struct{}{}
			}
			s.subs[chid][req.ConnID] = struct{}{}
		}
	}
	// Build hydration payload: explicit-membership channels + every SYSTEM_ALL
	hydration := make([]ChannelInfo, 0, len(s.channels))
	for _, c := range s.channels {
		if c.Kind == "system_all" || s.membership[c.ChannelID][uid] != "" {
			hydration = append(hydration, channelInfoOfLocked(c, len(s.membership[c.ChannelID])))
		}
	}
	s.mu.Unlock()

	// Fanout SE_CHAT_MEMBER_JOINED to other members of explicit channels
	// (deferred to membership-aware tests in Phase 7; simple single-conn
	// hydration alone is sufficient for v1 SYSTEM_ALL).

	if s.ctx != nil && s.ctx.Logger != nil {
		s.ctx.Logger.Log(logCat, "session enter: conn=%d user=%s username=%s gateway=%s hydration=%d (channels=%d sendFn=%v)",
			req.ConnID, uid, req.Username, req.GatewayID, len(hydration), len(s.channels), s.sendEventFn != nil)
	}
	s.fanoutToOne(req.ConnID, &ChatChannelsHydratedEvent{Channels: hydration})
	return &ChatSessionEnterResponse{}, nil
}

// HandleSessionLeave drops presence + subscription state for a conn.
// Idempotent — calling with an unknown connID returns success.
func (s *Service) HandleSessionLeave(_ *ops.OpContext, req *ChatSessionLeaveRequest) (*ChatSessionLeaveResponse, error) {
	s.mu.Lock()
	uid, ok := s.connIndex[req.ConnID]
	if !ok {
		s.mu.Unlock()
		return &ChatSessionLeaveResponse{}, nil // idempotent
	}
	delete(s.connIndex, req.ConnID)
	delete(s.online, uid)
	for chid, members := range s.subs {
		if _, present := members[req.ConnID]; present {
			delete(members, req.ConnID)
			// fanout MEMBER_LEFT to remaining members of explicit channels (Phase 7)
			_ = chid
		}
	}
	if g := s.gatewayConn[req.GatewayID]; g != nil {
		delete(g, req.ConnID)
		if len(g) == 0 {
			delete(s.gatewayConn, req.GatewayID)
		}
	}
	s.mu.Unlock()
	return &ChatSessionLeaveResponse{}, nil
}

// channelInfoOfLocked builds a ChannelInfo for the wire from an internal
// Channel row + memberCount. Caller MUST hold s.mu (R or W).
func channelInfoOfLocked(c Channel, memberCount int) ChannelInfo {
	owner := ""
	if c.OwnerUserID != uuid.Nil {
		owner = c.OwnerUserID.String()
	}
	return ChannelInfo{
		ChannelID:       c.ChannelID.String(),
		Slug:            c.Slug,
		Kind:            channelKindFromString(c.Kind),
		Topic:           c.Topic,
		SlowModeSeconds: int32(c.SlowModeSeconds),
		OwnerUserID:     owner,
		MemberCount:     int32(memberCount),
		HasPassword:     c.PasswordHash != "",
	}
}

// errResp builds a typed error response of type R with the given fields.
// The generic helper avoids repeating boilerplate for every handler.
//
// Uses reflection (once per error path) to set the embedded ErrorBlock
// fields. Every Response struct in typed_messages.go embeds ErrorBlock,
// so the lookup is reliable.
func errResp[R any](code ChatError, msg string, retryAfterMs int64) (*R, error) {
	r := new(R)
	v := reflect.ValueOf(r).Elem()
	eb := v.FieldByName("ErrorBlock")
	if !eb.IsValid() {
		panic("chat: response type missing embedded ErrorBlock field")
	}
	eb.FieldByName("ErrorCode").SetUint(uint64(code))
	eb.FieldByName("ErrorMessage").SetString(msg)
	eb.FieldByName("RetryAfterMs").SetInt(retryAfterMs)
	return r, nil
}
