package chat

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/zenion/mmoserver/pkg/ops"
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
