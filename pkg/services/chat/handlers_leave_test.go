package chat_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/mmokit/mmokit/pkg/ops"
	"github.com/mmokit/mmokit/pkg/services/chat"
	"github.com/mmokit/mmokit/pkg/services/chat/chattest"
)

func TestHandleLeave_HappyPath(t *testing.T) {
	repo := chattest.NewMock()
	svc := chat.NewTestService(t, repo, nil)
	_ = svc.MustOnlineFakeUser(101, "alice")
	bobID := svc.MustOnlineFakeUser(102, "bob")

	// Alice creates "cool"; Bob joins.
	createResp, err := svc.HandleCreate(&ops.OpContext{ConnID: 101},
		&chat.ChatCreateRequest{Slug: "cool"})
	if err != nil || createResp.ErrorCode != 0 {
		t.Fatalf("create: %s", createResp.ErrorMessage)
	}
	chid := uuid.MustParse(createResp.Channel.ChannelID)
	joinResp, err := svc.HandleJoin(&ops.OpContext{ConnID: 102}, &chat.ChatJoinRequest{Slug: "cool"})
	if err != nil || joinResp.ErrorCode != 0 {
		t.Fatalf("join: %s", joinResp.ErrorMessage)
	}

	// Recorder for fanout (post-join, so we don't catch the join event).
	var sent []recordedEvent
	svc.SetSendEventFn(func(c uint32, ev any) { sent = append(sent, recordedEvent{c, ev}) })

	// Bob leaves.
	resp, err := svc.HandleLeave(&ops.OpContext{ConnID: 102},
		&chat.ChatLeaveRequest{ChannelID: chid.String()})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("leave: %s", resp.ErrorMessage)
	}

	// Repo: Bob is no longer a member.
	mems, _ := repo.ListMembers(context.Background(), chid)
	for _, m := range mems {
		if m.UserID == bobID {
			t.Fatal("bob still in member list after leave")
		}
	}

	// In-memory: Bob removed from subs[chid] (use FanoutTargets to see remaining subs).
	targets := svc.FanoutTargets(chid)
	for _, c := range targets {
		if c == 102 {
			t.Fatal("bob conn still in subs after leave")
		}
	}

	// Alice received ChatMemberLeftEvent; Bob did NOT.
	var sawLeft bool
	for _, r := range sent {
		ev, ok := r.Event.(*chat.ChatMemberLeftEvent)
		if !ok {
			continue
		}
		if r.ConnID == 102 {
			t.Fatal("leaver should not receive own ChatMemberLeftEvent")
		}
		if r.ConnID == 101 && ev.UserID == bobID.String() {
			sawLeft = true
		}
	}
	if !sawLeft {
		t.Fatal("expected ChatMemberLeftEvent fanout to remaining members")
	}
}

func TestHandleLeave_NonMemberRejected(t *testing.T) {
	svc := chat.NewTestService(t, chattest.NewMock(), nil)
	_ = svc.MustOnlineFakeUser(101, "alice")
	_ = svc.MustOnlineFakeUser(102, "bob")
	createResp, err := svc.HandleCreate(&ops.OpContext{ConnID: 101},
		&chat.ChatCreateRequest{Slug: "cool"})
	if err != nil || createResp.ErrorCode != 0 {
		t.Fatalf("create: %s", createResp.ErrorMessage)
	}
	chid := uuid.MustParse(createResp.Channel.ChannelID)

	// Bob never joined "cool" — leave should fail with NotAMember.
	resp, err := svc.HandleLeave(&ops.OpContext{ConnID: 102},
		&chat.ChatLeaveRequest{ChannelID: chid.String()})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorNotAMember) {
		t.Fatalf("got code=%d want ChatErrorNotAMember=%d", resp.ErrorCode, chat.ChatErrorNotAMember)
	}
}

func TestHandleLeave_RejectsSystemAll(t *testing.T) {
	svc := chat.NewTestService(t, chattest.NewMock(), []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll},
	})
	_ = svc.MustOnlineFakeUser(101, "alice")
	chid := svc.MustChannelID("world")

	resp, err := svc.HandleLeave(&ops.OpContext{ConnID: 101},
		&chat.ChatLeaveRequest{ChannelID: chid.String()})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorPermissionDenied) {
		t.Fatalf("got code=%d want ChatErrorPermissionDenied=%d", resp.ErrorCode, chat.ChatErrorPermissionDenied)
	}
}

func TestHandleLeave_ChannelNotFound(t *testing.T) {
	svc := chat.NewTestService(t, chattest.NewMock(), nil)
	_ = svc.MustOnlineFakeUser(101, "alice")

	resp, err := svc.HandleLeave(&ops.OpContext{ConnID: 101},
		&chat.ChatLeaveRequest{ChannelID: uuid.New().String()})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorChannelNotFound) {
		t.Fatalf("got code=%d want ChatErrorChannelNotFound=%d", resp.ErrorCode, chat.ChatErrorChannelNotFound)
	}
}
