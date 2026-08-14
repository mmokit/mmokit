package chat_test

import (
	"reflect"
	"testing"

	"github.com/mmokit/mmokit/pkg/mmokit"
	"github.com/mmokit/mmokit/pkg/services/chat"
)

// TestRegisterChatServerEvents_TypeIDsRegistered asserts that, after
// the mmokit chat facade registers every chat-emitted server event,
// each event type is present in the mmokit server-event registry with
// a non-zero typeID. The mmokit facade's registerChatServerEvents is
// invoked by RegisterChatService; for this isolated test we exercise
// the public API mmokit.RegisterChatServerEvents which is a thin
// re-export that the chat package itself can no longer provide
// without forming an import cycle.
func TestRegisterChatServerEvents_TypeIDsRegistered(t *testing.T) {
	mmokit.RegisterChatServerEvents()

	for _, sample := range []any{
		(*chat.ChatMessageEvent)(nil),
		(*chat.ChatDMEvent)(nil),
		(*chat.ChatMemberJoinedEvent)(nil),
		(*chat.ChatMemberLeftEvent)(nil),
		(*chat.ChatMessageDeletedEvent)(nil),
		(*chat.ChatMutedEvent)(nil),
		(*chat.ChatUnmutedEvent)(nil),
		(*chat.ChatKickedEvent)(nil),
		(*chat.ChatBannedEvent)(nil),
		(*chat.ChatChannelUpdatedEvent)(nil),
		(*chat.ChatChannelGoneEvent)(nil),
		(*chat.ChatMemberRoleChangedEvent)(nil),
		(*chat.ChatRateLimitedEvent)(nil),
		(*chat.ChatChannelsHydratedEvent)(nil),
	} {
		elem := reflect.TypeOf(sample).Elem()
		id := mmokit.TypeIDOf(elem)
		if id == 0 {
			t.Errorf("typeID for %s is zero", elem.String())
			continue
		}
		got, ok := mmokit.LookupServerEventType(id)
		if !ok {
			t.Errorf("typeID %#x for %s not found in server-event registry", id, elem.String())
			continue
		}
		if got != elem {
			t.Errorf("typeID %#x maps to %s, want %s", id, got.String(), elem.String())
		}
	}
}
