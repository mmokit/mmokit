package chat

import (
	"sync"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

var registerChatServerEventsOnce sync.Once

// RegisterChatServerEvents registers every chat-emitted typed event so
// the server-event codec can encode/decode by typeID and the SDK
// generator can emit typed handlers. Idempotent. Called by Service.Init
// and by tests directly.
func RegisterChatServerEvents() {
	registerChatServerEventsOnce.Do(func() {
		mmokit.RegisterEvent[ChatMessageEvent]()
		mmokit.RegisterEvent[ChatDMEvent]()
		mmokit.RegisterEvent[ChatMemberJoinedEvent]()
		mmokit.RegisterEvent[ChatMemberLeftEvent]()
		mmokit.RegisterEvent[ChatMessageDeletedEvent]()
		mmokit.RegisterEvent[ChatMutedEvent]()
		mmokit.RegisterEvent[ChatUnmutedEvent]()
		mmokit.RegisterEvent[ChatKickedEvent]()
		mmokit.RegisterEvent[ChatBannedEvent]()
		mmokit.RegisterEvent[ChatChannelUpdatedEvent]()
		mmokit.RegisterEvent[ChatChannelGoneEvent]()
		mmokit.RegisterEvent[ChatMemberRoleChangedEvent]()
		mmokit.RegisterEvent[ChatRateLimitedEvent]()
		mmokit.RegisterEvent[ChatChannelsHydratedEvent]()
	})
}
