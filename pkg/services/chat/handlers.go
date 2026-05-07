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
