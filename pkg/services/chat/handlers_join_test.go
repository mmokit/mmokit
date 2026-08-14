package chat_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mmokit/mmokit/pkg/ops"
	"github.com/mmokit/mmokit/pkg/services/chat"
	"github.com/mmokit/mmokit/pkg/services/chat/chattest"
)

func TestHandleJoin_HappyPath(t *testing.T) {
	repo := chattest.NewMock()
	svc := chat.NewTestService(t, repo, nil)
	_ = svc.MustOnlineFakeUser(101, "alice")
	bobID := svc.MustOnlineFakeUser(102, "bob")

	// Alice creates "cool" with no password.
	createResp, err := svc.HandleCreate(&ops.OpContext{ConnID: 101},
		&chat.ChatCreateRequest{Slug: "cool"})
	if err != nil || createResp.ErrorCode != 0 {
		t.Fatalf("create: err=%v code=%d msg=%q", err, createResp.ErrorCode, createResp.ErrorMessage)
	}

	// Recorder for fanout
	var sent []recordedEvent
	svc.SetSendEventFn(func(c uint32, ev any) { sent = append(sent, recordedEvent{c, ev}) })

	// Bob joins.
	joinResp, err := svc.HandleJoin(&ops.OpContext{ConnID: 102},
		&chat.ChatJoinRequest{Slug: "cool"})
	if err != nil {
		t.Fatal(err)
	}
	if joinResp.ErrorCode != 0 {
		t.Fatalf("join: %s", joinResp.ErrorMessage)
	}
	if joinResp.Channel.Slug != "cool" {
		t.Fatalf("got slug=%q want cool", joinResp.Channel.Slug)
	}
	if joinResp.Channel.MemberCount != 2 {
		t.Fatalf("got member_count=%d want 2", joinResp.Channel.MemberCount)
	}

	// Bob is persisted as a member with role=member.
	mems, _ := repo.ListMembers(context.Background(), uuid.MustParse(joinResp.Channel.ChannelID))
	var foundBob bool
	for _, m := range mems {
		if m.UserID == bobID {
			if m.Role != "member" {
				t.Fatalf("bob role=%q want member", m.Role)
			}
			foundBob = true
		}
	}
	if !foundBob {
		t.Fatal("bob not in member list")
	}

	// Alice (other member) received ChatMemberJoinedEvent. Bob does NOT.
	var sawJoined bool
	for _, r := range sent {
		ev, ok := r.Event.(*chat.ChatMemberJoinedEvent)
		if !ok {
			continue
		}
		if r.ConnID == 102 {
			t.Fatal("joiner should not receive its own ChatMemberJoinedEvent")
		}
		if r.ConnID == 101 && ev.UserID == bobID.String() {
			sawJoined = true
		}
	}
	if !sawJoined {
		t.Fatal("expected ChatMemberJoinedEvent fanout to other members")
	}
}

func TestHandleJoin_WrongPasswordRejected(t *testing.T) {
	svc := chat.NewTestService(t, chattest.NewMock(), nil)
	_ = svc.MustOnlineFakeUser(101, "alice")
	_ = svc.MustOnlineFakeUser(102, "bob")
	createResp, err := svc.HandleCreate(&ops.OpContext{ConnID: 101},
		&chat.ChatCreateRequest{Slug: "private", Password: "hunter2"})
	if err != nil || createResp.ErrorCode != 0 {
		t.Fatalf("create: %s", createResp.ErrorMessage)
	}

	resp, err := svc.HandleJoin(&ops.OpContext{ConnID: 102},
		&chat.ChatJoinRequest{Slug: "private", Password: "wrong"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorInvalidPassword) {
		t.Fatalf("got code=%d want ChatErrorInvalidPassword=%d", resp.ErrorCode, chat.ChatErrorInvalidPassword)
	}
}

func TestHandleJoin_CorrectPasswordAccepted(t *testing.T) {
	svc := chat.NewTestService(t, chattest.NewMock(), nil)
	_ = svc.MustOnlineFakeUser(101, "alice")
	_ = svc.MustOnlineFakeUser(102, "bob")
	createResp, err := svc.HandleCreate(&ops.OpContext{ConnID: 101},
		&chat.ChatCreateRequest{Slug: "private", Password: "hunter2"})
	if err != nil || createResp.ErrorCode != 0 {
		t.Fatalf("create: %s", createResp.ErrorMessage)
	}

	resp, err := svc.HandleJoin(&ops.OpContext{ConnID: 102},
		&chat.ChatJoinRequest{Slug: "private", Password: "hunter2"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("got code=%d msg=%q", resp.ErrorCode, resp.ErrorMessage)
	}
}

func TestHandleJoin_SystemAllRejected(t *testing.T) {
	svc := chat.NewTestService(t, chattest.NewMock(), []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll, Topic: ""},
	})
	_ = svc.MustOnlineFakeUser(101, "alice")

	resp, err := svc.HandleJoin(&ops.OpContext{ConnID: 101},
		&chat.ChatJoinRequest{Slug: "world"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorPermissionDenied) {
		t.Fatalf("got code=%d want ChatErrorPermissionDenied=%d", resp.ErrorCode, chat.ChatErrorPermissionDenied)
	}
}

func TestHandleJoin_ChannelNotFound(t *testing.T) {
	svc := chat.NewTestService(t, chattest.NewMock(), nil)
	_ = svc.MustOnlineFakeUser(101, "alice")

	resp, err := svc.HandleJoin(&ops.OpContext{ConnID: 101},
		&chat.ChatJoinRequest{Slug: "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorChannelNotFound) {
		t.Fatalf("got code=%d want ChatErrorChannelNotFound=%d", resp.ErrorCode, chat.ChatErrorChannelNotFound)
	}
}

func TestHandleJoin_BannedUserRejected(t *testing.T) {
	svc := chat.NewTestService(t, chattest.NewMock(), nil)
	_ = svc.MustOnlineFakeUser(101, "alice")
	bobID := svc.MustOnlineFakeUser(102, "bob")

	createResp, err := svc.HandleCreate(&ops.OpContext{ConnID: 101},
		&chat.ChatCreateRequest{Slug: "cool"})
	if err != nil || createResp.ErrorCode != 0 {
		t.Fatalf("create: %s", createResp.ErrorMessage)
	}
	chid := uuid.MustParse(createResp.Channel.ChannelID)

	// Pre-populate s.bans for bob on this channel.
	svc.MustBanUser(chid, bobID, time.Now().Add(time.Hour), "test ban")

	resp, err := svc.HandleJoin(&ops.OpContext{ConnID: 102},
		&chat.ChatJoinRequest{Slug: "cool"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorBanned) {
		t.Fatalf("got code=%d want ChatErrorBanned=%d", resp.ErrorCode, chat.ChatErrorBanned)
	}
}
