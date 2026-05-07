package chat_test

import (
	"reflect"
	"testing"

	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/services/chat"
)

// TestRegisterChatServerEvents_TypeIDsRegistered asserts that, after
// chat.RegisterChatServerEvents() runs, every chat-emitted event type
// is present in the mmokit server-event registry with a non-zero
// typeID.
//
// (The plan referred to a hypothetical mmokit.TypeIDOfPtr helper; this
// codebase exposes mmokit.TypeIDOf(reflect.Type) + LookupServerEventType
// instead. The semantic is identical — go through the public registry
// API to confirm registration.)
func TestRegisterChatServerEvents_TypeIDsRegistered(t *testing.T) {
	chat.RegisterChatServerEvents()

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
