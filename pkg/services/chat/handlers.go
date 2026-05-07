package chat

import (
	"reflect"

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
