package chat_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/zenion/mmoserver/pkg/ops"
	"github.com/zenion/mmoserver/pkg/services/chat"
	"github.com/zenion/mmoserver/pkg/services/chat/chattest"
)

func TestHandleSend_FansOutToAllOnlineForSystemAll(t *testing.T) {
	svc := chat.NewTestService(t, chattest.NewMock(), []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll, Topic: ""},
	})
	chid := svc.MustChannelID("world")
	_ = svc.MustOnlineFakeUser(101, "alice")
	_ = svc.MustOnlineFakeUser(102, "bob")
	_ = svc.MustOnlineFakeUser(103, "carol")

	var recv []recordedEvent
	svc.SetSendEventFn(func(connID uint32, ev any) {
		recv = append(recv, recordedEvent{ConnID: connID, Event: ev})
	})

	// OpContext.ConnID identifies the sender; chat resolves the user_id
	// internally via connIndex (populated by MustOnlineFakeUser).
	opCtx := &ops.OpContext{ConnID: 101}
	resp, err := svc.HandleSend(opCtx, &chat.ChatSendRequest{ChannelID: chid.String(), Body: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("got error code %d msg=%q", resp.ErrorCode, resp.ErrorMessage)
	}
	if resp.MsgID == "" {
		t.Fatal("expected msg_id")
	}
	if len(recv) != 3 {
		t.Fatalf("expected 3 fanout sends, got %d", len(recv))
	}
	for _, r := range recv {
		ev, ok := r.Event.(*chat.ChatMessageEvent)
		if !ok {
			t.Fatalf("unexpected event type %T", r.Event)
		}
		if ev.Body != "hi" || ev.ChannelID != chid.String() {
			t.Fatalf("unexpected event: %#v", ev)
		}
	}
}

type recordedEvent struct {
	ConnID uint32
	Event  any
}

// Smoke that uuid still imports cleanly even if no test references it directly.
var _ = uuid.Nil
var _ = context.TODO
