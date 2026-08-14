package chat

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/zenion/mmokit/pkg/ops"
)

// HandleRegisterChannel registers a new SYSTEM_ALL or SYSTEM_PREDICATE
// channel. CUSTOM channels are created via HandleCreate (caller-owned).
//
// Authorization: requires the global "chat.admin" capability — caller
// cannot use canModerate because there's no pre-existing channel
// membership to check against.
func (s *Service) HandleRegisterChannel(opCtx *ops.OpContext, req *ChatRegisterChannelRequest) (*ChatRegisterChannelResponse, error) {
	if opCtx == nil {
		return errResp[ChatRegisterChannelResponse](ChatErrorInternal, "missing op context", 0)
	}
	callerID := s.callerFromOpCtx(opCtx)
	if callerID == uuid.Nil {
		return errResp[ChatRegisterChannelResponse](ChatErrorPermissionDenied, "not online", 0)
	}
	if !s.hasGlobalAdmin(callerID) {
		return errResp[ChatRegisterChannelResponse](ChatErrorPermissionDenied, "denied", 0)
	}

	// Validate kind — only SYSTEM_ALL and SYSTEM_PREDICATE are
	// register-able; CUSTOM channels go through HandleCreate.
	kindStr := channelKindToString(req.Kind)
	if kindStr != "system_all" && kindStr != "system_predicate" {
		return errResp[ChatRegisterChannelResponse](ChatErrorInternal, "invalid kind for register (use system_all or system_predicate)", 0)
	}

	// Validate slug.
	slug := req.Slug
	if slug == "" {
		return errResp[ChatRegisterChannelResponse](ChatErrorReservedSlug, "empty slug", 0)
	}
	if len(slug) > s.opts.MaxChannelSlugLen {
		return errResp[ChatRegisterChannelResponse](ChatErrorReservedSlug, "slug exceeds max length", 0)
	}
	if !validateSlugFormat(slug) {
		// Allow a single ":" in system slugs (guild:foo etc) — RegisterChannel
		// is the right place for those. Otherwise enforce [a-z0-9_-].
		if !validateSystemSlugFormat(slug) {
			return errResp[ChatRegisterChannelResponse](ChatErrorReservedSlug, "slug must match [a-z0-9_:-]", 0)
		}
	}

	// Validate topic length.
	if len(req.Topic) > s.opts.MaxTopicLen {
		return errResp[ChatRegisterChannelResponse](ChatErrorPayloadTooLarge, "topic exceeds max length", 0)
	}

	// Persist channel.
	c := Channel{
		Slug:            slug,
		Kind:            kindStr,
		Topic:           req.Topic,
		SlowModeSeconds: int(req.SlowModeSeconds),
	}
	stored, err := s.repo.UpsertChannel(context.Background(), c)
	if err != nil {
		if err == ErrChannelSlugInUse {
			return errResp[ChatRegisterChannelResponse](ChatErrorSlugInUse, "slug already in use", 0)
		}
		return errResp[ChatRegisterChannelResponse](ChatErrorInternal, "upsert: "+err.Error(), 0)
	}

	// In-memory bookkeeping. system_predicate gets explicit subs;
	// system_all uses online[] for fanout.
	s.mu.Lock()
	s.channels[stored.ChannelID] = stored
	s.bySlug[stored.Slug] = stored.ChannelID
	if s.subs[stored.ChannelID] == nil {
		s.subs[stored.ChannelID] = map[uint32]struct{}{}
	}
	if stored.Kind == "system_predicate" {
		if s.membership[stored.ChannelID] == nil {
			s.membership[stored.ChannelID] = map[uuid.UUID]string{}
		}
	}
	memberCount := len(s.membership[stored.ChannelID])
	info := channelInfoOfLocked(stored, memberCount)
	s.mu.Unlock()

	if s.ctx != nil && s.ctx.Logger != nil {
		s.ctx.Logger.Log(logCat, "register_channel: channel=%s slug=%s kind=%s caller=%s",
			stored.ChannelID, stored.Slug, stored.Kind, callerID)
	}

	// No fanout — no members yet for system_predicate; system_all will
	// be discovered by clients on next SessionEnter.
	return &ChatRegisterChannelResponse{Channel: info}, nil
}

// HandleUnregisterChannel deletes a channel and cascades all its
// memberships in the repo. In-memory state is cleared, including
// userChans entries pointing at this channel.
//
// Authorization: requires the global "chat.admin" capability.
//
// Fans out ChatChannelGoneEvent to all current subscribers BEFORE
// clearing subs (so the message reaches them).
func (s *Service) HandleUnregisterChannel(opCtx *ops.OpContext, req *ChatUnregisterChannelRequest) (*ChatUnregisterChannelResponse, error) {
	if opCtx == nil {
		return errResp[ChatUnregisterChannelResponse](ChatErrorInternal, "missing op context", 0)
	}
	callerID := s.callerFromOpCtx(opCtx)
	if callerID == uuid.Nil {
		return errResp[ChatUnregisterChannelResponse](ChatErrorPermissionDenied, "not online", 0)
	}
	if !s.hasGlobalAdmin(callerID) {
		return errResp[ChatUnregisterChannelResponse](ChatErrorPermissionDenied, "denied", 0)
	}

	chID, err := uuid.Parse(req.ChannelID)
	if err != nil {
		return errResp[ChatUnregisterChannelResponse](ChatErrorChannelNotFound, "invalid channel_id", 0)
	}

	s.mu.RLock()
	c, ok := s.channels[chID]
	s.mu.RUnlock()
	if !ok {
		return errResp[ChatUnregisterChannelResponse](ChatErrorChannelNotFound, "channel not found", 0)
	}

	// Persist deletion (cascades chat.channel_members rows on the FK).
	if err := s.repo.DeleteChannel(context.Background(), chID); err != nil && err != ErrChannelNotFound {
		return errResp[ChatUnregisterChannelResponse](ChatErrorInternal, "delete: "+err.Error(), 0)
	}

	// Snapshot fanout targets BEFORE clearing subs. For SYSTEM_ALL the
	// fanout target set is the full online[] map (every connected user).
	s.mu.Lock()
	var targets []uint32
	if c.Kind == "system_all" {
		targets = make([]uint32, 0, len(s.online))
		for _, conn := range s.online {
			targets = append(targets, conn)
		}
	} else {
		targets = make([]uint32, 0, len(s.subs[chID]))
		for cid := range s.subs[chID] {
			targets = append(targets, cid)
		}
	}

	// Clear in-memory state.
	delete(s.channels, chID)
	delete(s.bySlug, c.Slug)
	if oldMembers := s.membership[chID]; oldMembers != nil {
		for uid := range oldMembers {
			if uc := s.userChans[uid]; uc != nil {
				delete(uc, chID)
				if len(uc) == 0 {
					delete(s.userChans, uid)
				}
			}
		}
		delete(s.membership, chID)
	}
	delete(s.subs, chID)
	send := s.sendEventFn
	s.mu.Unlock()

	if s.ctx != nil && s.ctx.Logger != nil {
		s.ctx.Logger.Log(logCat, "unregister_channel: channel=%s slug=%s caller=%s",
			chID, c.Slug, callerID)
	}

	if send != nil {
		ev := &ChatChannelGoneEvent{ChannelID: chID.String()}
		for _, cid := range targets {
			send(cid, ev)
		}
	}
	return &ChatUnregisterChannelResponse{}, nil
}

// validateSystemSlugFormat permits a single colon plus the standard
// charset; used for system slugs like "guild:foo" or "party:abc123".
func validateSystemSlugFormat(s string) bool {
	if s == "" {
		return false
	}
	if strings.Count(s, ":") > 1 {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == ':') {
			return false
		}
	}
	return true
}
