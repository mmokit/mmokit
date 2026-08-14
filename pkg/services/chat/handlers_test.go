package chat_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/zenion/mmokit/pkg/ops"
	"github.com/zenion/mmokit/pkg/services/chat"
	"github.com/zenion/mmokit/pkg/services/chat/chattest"
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

func TestHandleSessionEnter_AddsToOnlineAndSendsHydration(t *testing.T) {
	svc := chat.NewTestService(t, chattest.NewMock(), []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll, Topic: "World"},
		{Slug: "help", Kind: chat.ChannelKindSystemAll, Topic: "Help"},
	})

	var recv []recordedEvent
	svc.SetSendEventFn(func(connID uint32, ev any) {
		recv = append(recv, recordedEvent{ConnID: connID, Event: ev})
	})

	resp, err := svc.HandleSessionEnter(nil, &chat.ChatSessionEnterRequest{
		ConnID: 200, UserID: uuid.NewString(), Username: "alice", GatewayID: "gw-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}
	if got := svc.OnlineCount(); got != 1 {
		t.Fatalf("online=%d, want 1", got)
	}
	if len(recv) != 1 {
		t.Fatalf("expected 1 hydration event, got %d", len(recv))
	}
	if _, ok := recv[0].Event.(*chat.ChatChannelsHydratedEvent); !ok {
		t.Fatalf("expected ChatChannelsHydratedEvent, got %T", recv[0].Event)
	}
}

func TestHandleSessionLeave_RemovesFromOnline(t *testing.T) {
	svc := chat.NewTestService(t, chattest.NewMock(), []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll, Topic: ""},
	})
	uid := uuid.NewString()
	_, _ = svc.HandleSessionEnter(nil, &chat.ChatSessionEnterRequest{ConnID: 200, UserID: uid, Username: "alice", GatewayID: "gw-a"})
	resp, err := svc.HandleSessionLeave(nil, &chat.ChatSessionLeaveRequest{ConnID: 200, GatewayID: "gw-a"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}
	if got := svc.OnlineCount(); got != 0 {
		t.Fatalf("online=%d, want 0", got)
	}
}

// Smoke that uuid still imports cleanly even if no test references it directly.
var _ = uuid.Nil
var _ = context.TODO
