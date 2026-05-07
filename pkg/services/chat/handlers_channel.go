package chat

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/zenion/mmoserver/pkg/ops"
)

// HandleRenameChannel changes a channel's slug. canModerate gated.
//
// Slug validation depends on channel kind:
//   - custom channels use validateSlugFormat (no ":" allowed)
//   - system_predicate channels use validateSystemSlugFormat (allows ":")
//   - system_all channels are bedrock and cannot be renamed
//
// On success, updates bySlug (delete old → insert new) and fans out
// ChatChannelUpdatedEvent with the full updated ChannelInfo.
func (s *Service) HandleRenameChannel(opCtx *ops.OpContext, req *ChatRenameChannelRequest) (*ChatRenameChannelResponse, error) {
	if opCtx == nil {
		return errResp[ChatRenameChannelResponse](ChatErrorInternal, "missing op context", 0)
	}
	callerID := s.callerFromOpCtx(opCtx)
	if callerID == uuid.Nil {
		return errResp[ChatRenameChannelResponse](ChatErrorPermissionDenied, "not online", 0)
	}
	chID, err := uuid.Parse(req.ChannelID)
	if err != nil {
		return errResp[ChatRenameChannelResponse](ChatErrorChannelNotFound, "invalid channel_id", 0)
	}

	s.mu.RLock()
	c, ok := s.channels[chID]
	s.mu.RUnlock()
	if !ok {
		return errResp[ChatRenameChannelResponse](ChatErrorChannelNotFound, "channel not found", 0)
	}
	if !s.canModerate(callerID, chID) {
		return errResp[ChatRenameChannelResponse](ChatErrorPermissionDenied, "denied", 0)
	}
	if c.Kind == "system_all" {
		return errResp[ChatRenameChannelResponse](ChatErrorPermissionDenied, "cannot rename system_all channel", 0)
	}

	newSlug := req.NewSlug
	if newSlug == "" {
		return errResp[ChatRenameChannelResponse](ChatErrorReservedSlug, "empty slug", 0)
	}
	if len(newSlug) > s.opts.MaxChannelSlugLen {
		return errResp[ChatRenameChannelResponse](ChatErrorReservedSlug, "slug exceeds max length", 0)
	}
	switch c.Kind {
	case "custom":
		if !validateSlugFormat(newSlug) {
			return errResp[ChatRenameChannelResponse](ChatErrorReservedSlug, "slug must match [a-z0-9_-]", 0)
		}
		// Reserved-prefix check applies to custom channels only.
		for _, prefix := range s.opts.ReservedSlugPrefixes {
			if strings.HasPrefix(newSlug, prefix) {
				return errResp[ChatRenameChannelResponse](ChatErrorReservedSlug, "slug uses reserved prefix", 0)
			}
		}
	case "system_predicate":
		if !validateSystemSlugFormat(newSlug) {
			return errResp[ChatRenameChannelResponse](ChatErrorReservedSlug, "slug must match [a-z0-9_:-]", 0)
		}
	}

	// Persist rename. Repo translates Postgres unique-violation to
	// ErrChannelSlugInUse → map to ChatErrorSlugInUse.
	updated := c
	updated.Slug = newSlug
	if err := s.repo.UpdateChannel(context.Background(), updated); err != nil {
		if err == ErrChannelSlugInUse {
			return errResp[ChatRenameChannelResponse](ChatErrorSlugInUse, "slug already in use", 0)
		}
		if err == ErrChannelNotFound {
			return errResp[ChatRenameChannelResponse](ChatErrorChannelNotFound, "channel not found", 0)
		}
		return errResp[ChatRenameChannelResponse](ChatErrorInternal, "update: "+err.Error(), 0)
	}

	// In-memory bookkeeping: update channels[chid] + bySlug.
	s.mu.Lock()
	delete(s.bySlug, c.Slug)
	s.bySlug[newSlug] = chID
	s.channels[chID] = updated
	memberCount := len(s.membership[chID])
	info := channelInfoOfLocked(updated, memberCount)
	s.mu.Unlock()

	if s.ctx != nil && s.ctx.Logger != nil {
		s.ctx.Logger.Log(logCat, "rename_channel: channel=%s old_slug=%s new_slug=%s caller=%s",
			chID, c.Slug, newSlug, callerID)
	}

	s.fanoutEvent(chID, &ChatChannelUpdatedEvent{Channel: info})
	return &ChatRenameChannelResponse{Channel: info}, nil
}

// HandleSetTopic updates a channel's topic. canModerate gated.
// Topic length is capped at MaxTopicLen. Fans out ChatChannelUpdatedEvent
// with the full updated ChannelInfo.
func (s *Service) HandleSetTopic(opCtx *ops.OpContext, req *ChatSetTopicRequest) (*ChatSetTopicResponse, error) {
	if opCtx == nil {
		return errResp[ChatSetTopicResponse](ChatErrorInternal, "missing op context", 0)
	}
	callerID := s.callerFromOpCtx(opCtx)
	if callerID == uuid.Nil {
		return errResp[ChatSetTopicResponse](ChatErrorPermissionDenied, "not online", 0)
	}
	chID, err := uuid.Parse(req.ChannelID)
	if err != nil {
		return errResp[ChatSetTopicResponse](ChatErrorChannelNotFound, "invalid channel_id", 0)
	}

	s.mu.RLock()
	c, ok := s.channels[chID]
	s.mu.RUnlock()
	if !ok {
		return errResp[ChatSetTopicResponse](ChatErrorChannelNotFound, "channel not found", 0)
	}
	if !s.canModerate(callerID, chID) {
		return errResp[ChatSetTopicResponse](ChatErrorPermissionDenied, "denied", 0)
	}
	if len(req.Topic) > s.opts.MaxTopicLen {
		return errResp[ChatSetTopicResponse](ChatErrorPayloadTooLarge, "topic exceeds max length", 0)
	}

	updated := c
	updated.Topic = req.Topic
	if err := s.repo.UpdateChannel(context.Background(), updated); err != nil {
		if err == ErrChannelNotFound {
			return errResp[ChatSetTopicResponse](ChatErrorChannelNotFound, "channel not found", 0)
		}
		return errResp[ChatSetTopicResponse](ChatErrorInternal, "update: "+err.Error(), 0)
	}

	s.mu.Lock()
	s.channels[chID] = updated
	memberCount := len(s.membership[chID])
	info := channelInfoOfLocked(updated, memberCount)
	s.mu.Unlock()

	if s.ctx != nil && s.ctx.Logger != nil {
		s.ctx.Logger.Log(logCat, "set_topic: channel=%s caller=%s topic_len=%d",
			chID, callerID, len(req.Topic))
	}

	s.fanoutEvent(chID, &ChatChannelUpdatedEvent{Channel: info})
	return &ChatSetTopicResponse{}, nil
}

// HandleSetSlowMode updates a channel's slow-mode duration. canModerate
// gated. Seconds is clamped to [0, 3600]. Fans out ChatChannelUpdatedEvent
// with the full updated ChannelInfo.
func (s *Service) HandleSetSlowMode(opCtx *ops.OpContext, req *ChatSetSlowModeRequest) (*ChatSetSlowModeResponse, error) {
	if opCtx == nil {
		return errResp[ChatSetSlowModeResponse](ChatErrorInternal, "missing op context", 0)
	}
	callerID := s.callerFromOpCtx(opCtx)
	if callerID == uuid.Nil {
		return errResp[ChatSetSlowModeResponse](ChatErrorPermissionDenied, "not online", 0)
	}
	chID, err := uuid.Parse(req.ChannelID)
	if err != nil {
		return errResp[ChatSetSlowModeResponse](ChatErrorChannelNotFound, "invalid channel_id", 0)
	}

	s.mu.RLock()
	c, ok := s.channels[chID]
	s.mu.RUnlock()
	if !ok {
		return errResp[ChatSetSlowModeResponse](ChatErrorChannelNotFound, "channel not found", 0)
	}
	if !s.canModerate(callerID, chID) {
		return errResp[ChatSetSlowModeResponse](ChatErrorPermissionDenied, "denied", 0)
	}

	// Clamp seconds to [0, 3600].
	seconds := int(req.Seconds)
	if seconds < 0 {
		seconds = 0
	}
	if seconds > 3600 {
		seconds = 3600
	}

	updated := c
	updated.SlowModeSeconds = seconds
	if err := s.repo.UpdateChannel(context.Background(), updated); err != nil {
		if err == ErrChannelNotFound {
			return errResp[ChatSetSlowModeResponse](ChatErrorChannelNotFound, "channel not found", 0)
		}
		return errResp[ChatSetSlowModeResponse](ChatErrorInternal, "update: "+err.Error(), 0)
	}

	s.mu.Lock()
	s.channels[chID] = updated
	memberCount := len(s.membership[chID])
	info := channelInfoOfLocked(updated, memberCount)
	s.mu.Unlock()

	if s.ctx != nil && s.ctx.Logger != nil {
		s.ctx.Logger.Log(logCat, "set_slow_mode: channel=%s caller=%s seconds=%d",
			chID, callerID, seconds)
	}

	s.fanoutEvent(chID, &ChatChannelUpdatedEvent{Channel: info})
	return &ChatSetSlowModeResponse{}, nil
}
