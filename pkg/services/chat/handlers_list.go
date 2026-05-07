package chat

import (
	"context"

	"github.com/google/uuid"

	"github.com/zenion/mmoserver/pkg/ops"
)

// HandleListChannels returns ChannelInfo for every channel the caller is
// in: every SYSTEM_ALL channel (implicit membership) plus every channel
// in userChans[caller] (explicit membership for system_predicate / custom).
//
// No authorization gate beyond "must be online" — any logged-in user
// can list their own visible channels. Pure in-memory walk; no DB call.
func (s *Service) HandleListChannels(opCtx *ops.OpContext, _ *ChatListChannelsRequest) (*ChatListChannelsResponse, error) {
	if opCtx == nil {
		return errResp[ChatListChannelsResponse](ChatErrorInternal, "missing op context", 0)
	}
	callerID := s.callerFromOpCtx(opCtx)
	if callerID == uuid.Nil {
		return errResp[ChatListChannelsResponse](ChatErrorPermissionDenied, "not online", 0)
	}

	s.mu.RLock()
	out := make([]ChannelInfo, 0, len(s.channels))
	for _, c := range s.channels {
		switch c.Kind {
		case "system_all":
			out = append(out, channelInfoOfLocked(c, len(s.membership[c.ChannelID])))
		default:
			if _, isMember := s.userChans[callerID][c.ChannelID]; isMember {
				out = append(out, channelInfoOfLocked(c, len(s.membership[c.ChannelID])))
			}
		}
	}
	s.mu.RUnlock()

	return &ChatListChannelsResponse{Channels: out}, nil
}

// HandleListMembers returns MemberInfo for every member of a channel.
//
// Authorization:
//   - SYSTEM_ALL: caller is implicitly a member; returned set is the
//     current online[] map. Role is "member"; JoinedAtMs is 0 (not
//     tracked for system_all).
//   - SYSTEM_PREDICATE / CUSTOM: caller must be a member (NotAMember
//     otherwise). Returned set is repo.ListMembers (so JoinedAt is
//     accurate even for offline users); usernames are enriched from
//     the in-memory s.usernames cache when online.
func (s *Service) HandleListMembers(opCtx *ops.OpContext, req *ChatListMembersRequest) (*ChatListMembersResponse, error) {
	if opCtx == nil {
		return errResp[ChatListMembersResponse](ChatErrorInternal, "missing op context", 0)
	}
	callerID := s.callerFromOpCtx(opCtx)
	if callerID == uuid.Nil {
		return errResp[ChatListMembersResponse](ChatErrorPermissionDenied, "not online", 0)
	}
	chID, err := uuid.Parse(req.ChannelID)
	if err != nil {
		return errResp[ChatListMembersResponse](ChatErrorChannelNotFound, "invalid channel_id", 0)
	}

	s.mu.RLock()
	c, ok := s.channels[chID]
	s.mu.RUnlock()
	if !ok {
		return errResp[ChatListMembersResponse](ChatErrorChannelNotFound, "channel not found", 0)
	}

	if c.Kind == "system_all" {
		s.mu.RLock()
		out := make([]MemberInfo, 0, len(s.online))
		for uid := range s.online {
			out = append(out, MemberInfo{
				UserID:     uid.String(),
				Username:   s.usernames[uid],
				Role:       "member",
				JoinedAtMs: 0,
			})
		}
		s.mu.RUnlock()
		return &ChatListMembersResponse{Members: out}, nil
	}

	// Membership gate for non-SYSTEM_ALL kinds.
	s.mu.RLock()
	role := s.membership[chID][callerID]
	s.mu.RUnlock()
	if role == "" {
		return errResp[ChatListMembersResponse](ChatErrorNotAMember, "not a member", 0)
	}

	// Repo round-trip to get accurate JoinedAt for offline users.
	rows, err := s.repo.ListMembers(context.Background(), chID)
	if err != nil {
		return errResp[ChatListMembersResponse](ChatErrorInternal, "list members: "+err.Error(), 0)
	}

	s.mu.RLock()
	out := make([]MemberInfo, 0, len(rows))
	for _, m := range rows {
		out = append(out, MemberInfo{
			UserID:     m.UserID.String(),
			Username:   s.usernames[m.UserID], // empty for offline users; v1 acceptable
			Role:       m.Role,
			JoinedAtMs: m.JoinedAt.UnixMilli(),
		})
	}
	s.mu.RUnlock()

	return &ChatListMembersResponse{Members: out}, nil
}
