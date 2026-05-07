package chat

import (
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
