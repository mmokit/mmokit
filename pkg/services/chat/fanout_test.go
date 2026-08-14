package chat_test

import (
	"testing"

	"github.com/mmokit/mmokit/pkg/services/chat"
	"github.com/mmokit/mmokit/pkg/services/chat/chattest"
)

func TestFanout_RecordsRecipientsForSystemAll(t *testing.T) {
	// We assert at the abstract level: given a service with a SYSTEM_ALL
	// channel and 3 online users, fanoutEvent visits all 3 connIDs.
	// (This is a *unit* test against an exported FanoutTargets helper —
	// the actual send call is wired to the gateway in integration tests.)
	svc := chat.NewTestService(t, chattest.NewMock(), []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll, Topic: ""},
	})
	chid := svc.MustChannelID("world")
	svc.MustOnlineFakeUser(101, "alice")
	svc.MustOnlineFakeUser(102, "bob")
	svc.MustOnlineFakeUser(103, "carol")

	conns := svc.FanoutTargets(chid)
	if len(conns) != 3 {
		t.Fatalf("expected 3 conn targets, got %d", len(conns))
	}
}
