package chat_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zenion/mmoserver/pkg/ops"
	"github.com/zenion/mmoserver/pkg/services/auth"
	"github.com/zenion/mmoserver/pkg/services/auth/authtest"
	"github.com/zenion/mmoserver/pkg/services/chat"
	"github.com/zenion/mmoserver/pkg/services/chat/chattest"
)

// mustGrantChatAdmin grants the chat.admin global capability to userID
// using the authRepo. Fatals on error.
func mustGrantChatAdmin(t *testing.T, authRepo *authtest.RepoMock, userID uuid.UUID) {
	t.Helper()
	if err := authRepo.GrantCapability(context.Background(), auth.Capability{
		UserID:     userID,
		Capability: "chat.admin",
		GrantedBy:  uuid.Nil,
	}); err != nil {
		t.Fatalf("GrantCapability: %v", err)
	}
}

// --- HandleMuteUser ---

func TestHandleMuteUser_ChannelScoped_HappyPath(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll},
	})
	chid := svc.MustChannelID("world")

	gmUID := svc.MustOnlineFakeUser(101, "gm")
	svc.MustAddMember(chid, gmUID, "admin")
	aliceUID := svc.MustOnlineFakeUser(102, "alice")

	var sent []recordedEvent
	svc.SetSendEventFn(func(c uint32, ev any) { sent = append(sent, recordedEvent{c, ev}) })

	resp, err := svc.HandleMuteUser(&ops.OpContext{ConnID: 101}, &chat.ChatMuteUserRequest{
		UserID:     aliceUID.String(),
		ChannelID:  chid.String(),
		DurationMs: 60_000,
		Reason:     "spam",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}

	// Mute is now active (channel-scoped).
	active, _ := svc.MuteCheck(aliceUID, chid)
	if !active {
		t.Fatal("expected channel-scoped mute to be active")
	}

	// Subsequent send returns Muted.
	sendResp, err := svc.HandleSend(&ops.OpContext{ConnID: 102}, &chat.ChatSendRequest{
		ChannelID: chid.String(), Body: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sendResp.ErrorCode != uint32(chat.ChatErrorMuted) {
		t.Fatalf("got code=%d want Muted", sendResp.ErrorCode)
	}

	// Target received ChatMutedEvent with ChannelID=chid (non-empty).
	gotEvent := false
	for _, r := range sent {
		ev, ok := r.Event.(*chat.ChatMutedEvent)
		if !ok {
			continue
		}
		if r.ConnID != 102 {
			t.Fatalf("ChatMutedEvent should go to target connID=102, got %d", r.ConnID)
		}
		if ev.ChannelID != chid.String() {
			t.Fatalf("event ChannelID=%q want %q", ev.ChannelID, chid.String())
		}
		gotEvent = true
	}
	if !gotEvent {
		t.Fatal("expected ChatMutedEvent to alice")
	}
}

func TestHandleMuteUser_Global_RequiresChatAdmin_Allow(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll},
	})

	gmUID := svc.MustOnlineFakeUser(101, "gm")
	mustGrantChatAdmin(t, authRepo, gmUID)
	aliceUID := svc.MustOnlineFakeUser(102, "alice")

	resp, err := svc.HandleMuteUser(&ops.OpContext{ConnID: 101}, &chat.ChatMuteUserRequest{
		UserID:     aliceUID.String(),
		ChannelID:  "", // global
		DurationMs: 60_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}

	// Subsequent DM send returns Muted (global mute applies to DMs too).
	bobUID := svc.MustOnlineFakeUser(103, "bob")
	sendResp, err := svc.HandleSendDM(&ops.OpContext{ConnID: 102}, &chat.ChatSendDMRequest{
		RecipientUserID: bobUID.String(),
		Body:            "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sendResp.ErrorCode != uint32(chat.ChatErrorMuted) {
		t.Fatalf("got code=%d want Muted", sendResp.ErrorCode)
	}
}

func TestHandleMuteUser_Global_DeniedWithoutChatAdmin(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll},
	})

	_ = svc.MustOnlineFakeUser(101, "alice") // no chat.admin
	bobUID := uuid.New()

	resp, err := svc.HandleMuteUser(&ops.OpContext{ConnID: 101}, &chat.ChatMuteUserRequest{
		UserID:     bobUID.String(),
		ChannelID:  "",
		DurationMs: 60_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorPermissionDenied) {
		t.Fatalf("got code=%d want PermissionDenied", resp.ErrorCode)
	}
}

func TestHandleMuteUser_ChannelScoped_DeniedForNonAdmin(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll},
	})
	chid := svc.MustChannelID("world")

	_ = svc.MustOnlineFakeUser(101, "alice") // not channel admin, no chat.admin
	bobUID := uuid.New()

	resp, err := svc.HandleMuteUser(&ops.OpContext{ConnID: 101}, &chat.ChatMuteUserRequest{
		UserID:     bobUID.String(),
		ChannelID:  chid.String(),
		DurationMs: 60_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorPermissionDenied) {
		t.Fatalf("got code=%d want PermissionDenied", resp.ErrorCode)
	}
}

func TestHandleMuteUser_RejectsZeroOrNegativeDuration(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll},
	})
	chid := svc.MustChannelID("world")
	gmUID := svc.MustOnlineFakeUser(101, "gm")
	svc.MustAddMember(chid, gmUID, "admin")
	aliceUID := uuid.New()

	resp, err := svc.HandleMuteUser(&ops.OpContext{ConnID: 101}, &chat.ChatMuteUserRequest{
		UserID:     aliceUID.String(),
		ChannelID:  chid.String(),
		DurationMs: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode == 0 {
		t.Fatal("expected error for zero duration")
	}
}

// --- HandleUnmuteUser ---

func TestHandleUnmuteUser_ChannelScoped_HappyPath(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll},
	})
	chid := svc.MustChannelID("world")

	gmUID := svc.MustOnlineFakeUser(101, "gm")
	svc.MustAddMember(chid, gmUID, "admin")
	aliceUID := svc.MustOnlineFakeUser(102, "alice")

	// First mute alice via the handler.
	if _, err := svc.HandleMuteUser(&ops.OpContext{ConnID: 101}, &chat.ChatMuteUserRequest{
		UserID:     aliceUID.String(),
		ChannelID:  chid.String(),
		DurationMs: 60_000,
	}); err != nil {
		t.Fatal(err)
	}

	var sent []recordedEvent
	svc.SetSendEventFn(func(c uint32, ev any) { sent = append(sent, recordedEvent{c, ev}) })

	resp, err := svc.HandleUnmuteUser(&ops.OpContext{ConnID: 101}, &chat.ChatUnmuteUserRequest{
		UserID:    aliceUID.String(),
		ChannelID: chid.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}

	// Mute is no longer active.
	active, _ := svc.MuteCheck(aliceUID, chid)
	if active {
		t.Fatal("expected channel-scoped mute to be cleared")
	}
	// Subsequent send works.
	sendResp, err := svc.HandleSend(&ops.OpContext{ConnID: 102}, &chat.ChatSendRequest{
		ChannelID: chid.String(), Body: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sendResp.ErrorCode != 0 {
		t.Fatalf("expected unmuted send to succeed, got code=%d", sendResp.ErrorCode)
	}

	// Target received ChatUnmutedEvent.
	gotEvent := false
	for _, r := range sent {
		ev, ok := r.Event.(*chat.ChatUnmutedEvent)
		if !ok {
			continue
		}
		if r.ConnID != 102 {
			t.Fatalf("ChatUnmutedEvent should go to target connID=102, got %d", r.ConnID)
		}
		if ev.ChannelID != chid.String() {
			t.Fatalf("event ChannelID=%q want %q", ev.ChannelID, chid.String())
		}
		gotEvent = true
	}
	if !gotEvent {
		t.Fatal("expected ChatUnmutedEvent to alice")
	}
}

func TestHandleUnmuteUser_IdempotentSuccessOnNoExistingMute(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll},
	})
	chid := svc.MustChannelID("world")
	gmUID := svc.MustOnlineFakeUser(101, "gm")
	svc.MustAddMember(chid, gmUID, "admin")
	aliceUID := uuid.New()

	resp, err := svc.HandleUnmuteUser(&ops.OpContext{ConnID: 101}, &chat.ChatUnmuteUserRequest{
		UserID:    aliceUID.String(),
		ChannelID: chid.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("expected idempotent success on no-existing-mute, got code=%d msg=%s", resp.ErrorCode, resp.ErrorMessage)
	}
}

func TestHandleUnmuteUser_Global_DeniedWithoutChatAdmin(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll},
	})

	_ = svc.MustOnlineFakeUser(101, "alice") // no chat.admin

	resp, err := svc.HandleUnmuteUser(&ops.OpContext{ConnID: 101}, &chat.ChatUnmuteUserRequest{
		UserID:    uuid.NewString(),
		ChannelID: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorPermissionDenied) {
		t.Fatalf("got code=%d want PermissionDenied", resp.ErrorCode)
	}
}

// --- HandleKickFromChannel ---

func TestHandleKickFromChannel_HappyPath(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "council", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("council")

	gmUID := svc.MustOnlineFakeUser(101, "gm")
	svc.MustAddMember(chid, gmUID, "admin")
	bobUID := svc.MustOnlineFakeUser(102, "bob")
	svc.MustAddMember(chid, bobUID, "member")
	carolUID := svc.MustOnlineFakeUser(103, "carol")
	svc.MustAddMember(chid, carolUID, "member")
	svc.MustSubscribe(chid, 101)
	svc.MustSubscribe(chid, 102)
	svc.MustSubscribe(chid, 103)

	var sent []recordedEvent
	svc.SetSendEventFn(func(c uint32, ev any) { sent = append(sent, recordedEvent{c, ev}) })

	resp, err := svc.HandleKickFromChannel(&ops.OpContext{ConnID: 101}, &chat.ChatKickRequest{
		ChannelID: chid.String(),
		UserID:    bobUID.String(),
		Reason:    "spam",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}

	// Bob's repo membership row gone.
	mems, _ := chatRepo.ListMembers(context.Background(), chid)
	for _, m := range mems {
		if m.UserID == bobUID {
			t.Fatal("bob still in repo after kick")
		}
	}

	// Bob (102) receives ChatKickedEvent.
	gotKicked := false
	for _, r := range sent {
		ev, ok := r.Event.(*chat.ChatKickedEvent)
		if !ok {
			continue
		}
		if r.ConnID != 102 {
			t.Fatalf("ChatKickedEvent should go to bob's conn=102, got %d", r.ConnID)
		}
		if ev.ChannelID != chid.String() {
			t.Fatalf("ChannelID=%q want %q", ev.ChannelID, chid.String())
		}
		if ev.Reason != "spam" {
			t.Fatalf("Reason=%q want spam", ev.Reason)
		}
		gotKicked = true
	}
	if !gotKicked {
		t.Fatal("expected ChatKickedEvent to bob")
	}

	// Remaining (gm 101, carol 103) receive ChatMemberLeftEvent. Bob (102) does not.
	gotGM := false
	gotCarol := false
	for _, r := range sent {
		ev, ok := r.Event.(*chat.ChatMemberLeftEvent)
		if !ok {
			continue
		}
		if ev.UserID != bobUID.String() {
			continue
		}
		if r.ConnID == 102 {
			t.Fatal("bob's own conn should not receive ChatMemberLeftEvent")
		}
		if r.ConnID == 101 {
			gotGM = true
		}
		if r.ConnID == 103 {
			gotCarol = true
		}
	}
	if !gotGM || !gotCarol {
		t.Fatalf("expected gm+carol got Left events, gm=%v carol=%v", gotGM, gotCarol)
	}
}

func TestHandleKickFromChannel_PermissionDeniedForNonAdmin(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "council", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("council")

	_ = svc.MustOnlineFakeUser(101, "alice") // not admin
	bobUID := uuid.New()
	svc.MustAddMember(chid, bobUID, "member")

	resp, err := svc.HandleKickFromChannel(&ops.OpContext{ConnID: 101}, &chat.ChatKickRequest{
		ChannelID: chid.String(),
		UserID:    bobUID.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorPermissionDenied) {
		t.Fatalf("got code=%d want PermissionDenied", resp.ErrorCode)
	}
}

// Ensure that re-reading mutes via the service post-handler, before
// reaper kicks in, sees the row in the repo.
func TestHandleMuteUser_PersistsToRepository(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll},
	})
	chid := svc.MustChannelID("world")

	gmUID := svc.MustOnlineFakeUser(101, "gm")
	svc.MustAddMember(chid, gmUID, "admin")
	aliceUID := svc.MustOnlineFakeUser(102, "alice")

	resp, err := svc.HandleMuteUser(&ops.OpContext{ConnID: 101}, &chat.ChatMuteUserRequest{
		UserID:     aliceUID.String(),
		ChannelID:  chid.String(),
		DurationMs: int64(time.Hour / time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}

	// Re-read via ListActiveMutes.
	mutes, err := chatRepo.ListActiveMutes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range mutes {
		if m.UserID == aliceUID && m.ChannelID == chid {
			found = true
		}
	}
	if !found {
		t.Fatal("mute not found in repo")
	}
}
