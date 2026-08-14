package chat_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zenion/mmokit/pkg/ops"
	"github.com/zenion/mmokit/pkg/services/auth"
	"github.com/zenion/mmokit/pkg/services/auth/authtest"
	"github.com/zenion/mmokit/pkg/services/chat"
	"github.com/zenion/mmokit/pkg/services/chat/chattest"
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

// --- HandleBanFromChannel ---

func TestHandleBanFromChannel_HappyPath(t *testing.T) {
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

	resp, err := svc.HandleBanFromChannel(&ops.OpContext{ConnID: 101}, &chat.ChatBanRequest{
		ChannelID:  chid.String(),
		UserID:     bobUID.String(),
		DurationMs: int64(time.Hour / time.Millisecond),
		Reason:     "abuse",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}

	// Ban active in BanCheck.
	active, _ := svc.BanCheck(bobUID, chid)
	if !active {
		t.Fatal("expected ban to be active")
	}

	// Bob's send to channel returns Banned.
	sendResp, err := svc.HandleSend(&ops.OpContext{ConnID: 102}, &chat.ChatSendRequest{
		ChannelID: chid.String(), Body: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sendResp.ErrorCode != uint32(chat.ChatErrorBanned) {
		t.Fatalf("got code=%d want Banned", sendResp.ErrorCode)
	}

	// Bob receives ChatBannedEvent; gm + carol receive ChatMemberLeftEvent.
	gotBanned := false
	for _, r := range sent {
		ev, ok := r.Event.(*chat.ChatBannedEvent)
		if !ok {
			continue
		}
		if r.ConnID != 102 {
			t.Fatalf("ChatBannedEvent should go to bob's conn=102, got %d", r.ConnID)
		}
		if ev.UntilMs == 0 {
			t.Fatal("UntilMs should be non-zero")
		}
		if ev.Reason != "abuse" {
			t.Fatalf("Reason=%q want abuse", ev.Reason)
		}
		gotBanned = true
	}
	if !gotBanned {
		t.Fatal("expected ChatBannedEvent to bob")
	}

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
			t.Fatal("bob's own conn should not receive Left event")
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

func TestHandleBanFromChannel_BannedUserCannotRejoin(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, nil)

	// Create a custom channel so HandleJoin's custom-only check passes.
	chid, err := svc.CreateChannelDirect("cool", chat.ChannelKindCustom)
	if err != nil {
		t.Fatal(err)
	}

	gmUID := svc.MustOnlineFakeUser(101, "gm")
	svc.MustAddMember(chid, gmUID, "admin")
	bobUID := svc.MustOnlineFakeUser(102, "bob")
	svc.MustAddMember(chid, bobUID, "member")
	svc.MustSubscribe(chid, 101)
	svc.MustSubscribe(chid, 102)

	resp, err := svc.HandleBanFromChannel(&ops.OpContext{ConnID: 101}, &chat.ChatBanRequest{
		ChannelID:  chid.String(),
		UserID:     bobUID.String(),
		DurationMs: int64(time.Hour / time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("ban failed: %s", resp.ErrorMessage)
	}

	// Bob attempts to re-join → ChatErrorBanned.
	joinResp, err := svc.HandleJoin(&ops.OpContext{ConnID: 102}, &chat.ChatJoinRequest{
		Slug: "cool",
	})
	if err != nil {
		t.Fatal(err)
	}
	if joinResp.ErrorCode != uint32(chat.ChatErrorBanned) {
		t.Fatalf("got code=%d want Banned on rejoin attempt", joinResp.ErrorCode)
	}
}

func TestHandleBanFromChannel_PermissionDeniedForNonAdmin(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "council", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("council")

	_ = svc.MustOnlineFakeUser(101, "alice") // not admin
	bobUID := uuid.New()

	resp, err := svc.HandleBanFromChannel(&ops.OpContext{ConnID: 101}, &chat.ChatBanRequest{
		ChannelID:  chid.String(),
		UserID:     bobUID.String(),
		DurationMs: 60_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorPermissionDenied) {
		t.Fatalf("got code=%d want PermissionDenied", resp.ErrorCode)
	}
}

// --- HandleUnbanFromChannel ---

func TestHandleUnbanFromChannel_HappyPath_AllowsRejoinAndSend(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, nil)

	chid, err := svc.CreateChannelDirect("cool", chat.ChannelKindCustom)
	if err != nil {
		t.Fatal(err)
	}

	gmUID := svc.MustOnlineFakeUser(101, "gm")
	svc.MustAddMember(chid, gmUID, "admin")
	bobUID := svc.MustOnlineFakeUser(102, "bob")
	svc.MustAddMember(chid, bobUID, "member")
	svc.MustSubscribe(chid, 101)
	svc.MustSubscribe(chid, 102)

	// Ban bob.
	if _, err := svc.HandleBanFromChannel(&ops.OpContext{ConnID: 101}, &chat.ChatBanRequest{
		ChannelID:  chid.String(),
		UserID:     bobUID.String(),
		DurationMs: int64(time.Hour / time.Millisecond),
	}); err != nil {
		t.Fatal(err)
	}

	// Unban bob.
	resp, err := svc.HandleUnbanFromChannel(&ops.OpContext{ConnID: 101}, &chat.ChatUnbanRequest{
		ChannelID: chid.String(),
		UserID:    bobUID.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}

	active, _ := svc.BanCheck(bobUID, chid)
	if active {
		t.Fatal("expected ban cleared")
	}

	// Bob can rejoin.
	joinResp, err := svc.HandleJoin(&ops.OpContext{ConnID: 102}, &chat.ChatJoinRequest{
		Slug: "cool",
	})
	if err != nil {
		t.Fatal(err)
	}
	if joinResp.ErrorCode != 0 {
		t.Fatalf("rejoin failed: %s", joinResp.ErrorMessage)
	}

	// Bob can send.
	sendResp, err := svc.HandleSend(&ops.OpContext{ConnID: 102}, &chat.ChatSendRequest{
		ChannelID: chid.String(), Body: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sendResp.ErrorCode != 0 {
		t.Fatalf("send after unban failed: code=%d msg=%s", sendResp.ErrorCode, sendResp.ErrorMessage)
	}
}

func TestHandleUnbanFromChannel_PermissionDeniedForNonAdmin(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "council", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("council")

	_ = svc.MustOnlineFakeUser(101, "alice")
	resp, err := svc.HandleUnbanFromChannel(&ops.OpContext{ConnID: 101}, &chat.ChatUnbanRequest{
		ChannelID: chid.String(),
		UserID:    uuid.NewString(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorPermissionDenied) {
		t.Fatalf("got code=%d want PermissionDenied", resp.ErrorCode)
	}
}

// --- HandleDeleteMessage ---

func TestHandleDeleteMessage_HappyPath_FansOutToMembers(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll},
	})
	chid := svc.MustChannelID("world")

	gmUID := svc.MustOnlineFakeUser(101, "gm")
	svc.MustAddMember(chid, gmUID, "admin")
	bobUID := svc.MustOnlineFakeUser(102, "bob")
	carolUID := svc.MustOnlineFakeUser(103, "carol")
	_ = bobUID
	_ = carolUID

	// Bob sends a message → produces a msg_id we can later delete.
	sendResp, err := svc.HandleSend(&ops.OpContext{ConnID: 102}, &chat.ChatSendRequest{
		ChannelID: chid.String(), Body: "delete me",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sendResp.ErrorCode != 0 {
		t.Fatalf("send: %s", sendResp.ErrorMessage)
	}

	var sent []recordedEvent
	svc.SetSendEventFn(func(c uint32, ev any) { sent = append(sent, recordedEvent{c, ev}) })

	resp, err := svc.HandleDeleteMessage(&ops.OpContext{ConnID: 101}, &chat.ChatDeleteMessageRequest{
		MsgID:     sendResp.MsgID,
		ChannelID: chid.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}

	// All online users (system_all) should receive ChatMessageDeletedEvent.
	count := 0
	for _, r := range sent {
		ev, ok := r.Event.(*chat.ChatMessageDeletedEvent)
		if !ok {
			continue
		}
		if ev.MsgID != sendResp.MsgID {
			t.Fatalf("MsgID=%q want %q", ev.MsgID, sendResp.MsgID)
		}
		if ev.DeletedByUserID != gmUID.String() {
			t.Fatalf("DeletedByUserID=%q want %q", ev.DeletedByUserID, gmUID.String())
		}
		count++
	}
	if count != 3 {
		t.Fatalf("expected 3 fanout sends, got %d", count)
	}
}

func TestHandleDeleteMessage_UnknownMsgID(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll},
	})
	chid := svc.MustChannelID("world")
	gmUID := svc.MustOnlineFakeUser(101, "gm")
	svc.MustAddMember(chid, gmUID, "admin")

	resp, err := svc.HandleDeleteMessage(&ops.OpContext{ConnID: 101}, &chat.ChatDeleteMessageRequest{
		MsgID:     uuid.NewString(), // never-seen
		ChannelID: chid.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorMessageUnknown) {
		t.Fatalf("got code=%d want MessageUnknown", resp.ErrorCode)
	}
}

func TestHandleDeleteMessage_ChannelMismatch(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll},
		{Slug: "help", Kind: chat.ChannelKindSystemAll},
	})
	world := svc.MustChannelID("world")
	help := svc.MustChannelID("help")

	gmUID := svc.MustOnlineFakeUser(101, "gm")
	svc.MustAddMember(world, gmUID, "admin")
	bobUID := svc.MustOnlineFakeUser(102, "bob")
	_ = bobUID

	// Bob sends to "world".
	sendResp, err := svc.HandleSend(&ops.OpContext{ConnID: 102}, &chat.ChatSendRequest{
		ChannelID: world.String(), Body: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}

	// GM tries to delete it claiming the message is from "help".
	resp, err := svc.HandleDeleteMessage(&ops.OpContext{ConnID: 101}, &chat.ChatDeleteMessageRequest{
		MsgID:     sendResp.MsgID,
		ChannelID: help.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorMessageUnknown) {
		t.Fatalf("got code=%d want MessageUnknown for channel mismatch", resp.ErrorCode)
	}
}

func TestHandleDeleteMessage_PermissionDeniedForNonAdmin(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll},
	})
	chid := svc.MustChannelID("world")

	bobUID := svc.MustOnlineFakeUser(102, "bob")
	_ = bobUID
	sendResp, err := svc.HandleSend(&ops.OpContext{ConnID: 102}, &chat.ChatSendRequest{
		ChannelID: chid.String(), Body: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Carol (no admin role) tries to delete bob's message.
	_ = svc.MustOnlineFakeUser(103, "carol")
	resp, err := svc.HandleDeleteMessage(&ops.OpContext{ConnID: 103}, &chat.ChatDeleteMessageRequest{
		MsgID:     sendResp.MsgID,
		ChannelID: chid.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorPermissionDenied) {
		t.Fatalf("got code=%d want PermissionDenied", resp.ErrorCode)
	}
}

// --- HandleBroadcastSystem ---

func TestHandleBroadcastSystem_HappyPath_FansOutWithEmptySender(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll},
	})
	chid := svc.MustChannelID("world")

	gmUID := svc.MustOnlineFakeUser(101, "gm")
	mustGrantChatAdmin(t, authRepo, gmUID)
	_ = svc.MustOnlineFakeUser(102, "bob")
	_ = svc.MustOnlineFakeUser(103, "carol")

	var sent []recordedEvent
	svc.SetSendEventFn(func(c uint32, ev any) { sent = append(sent, recordedEvent{c, ev}) })

	resp, err := svc.HandleBroadcastSystem(&ops.OpContext{ConnID: 101}, &chat.ChatBroadcastRequest{
		ChannelID: chid.String(),
		Body:      "Server going down in 5 minutes",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}

	count := 0
	for _, r := range sent {
		ev, ok := r.Event.(*chat.ChatMessageEvent)
		if !ok {
			continue
		}
		if ev.SenderUserID != "" {
			t.Fatalf("system broadcast must have empty SenderUserID, got %q", ev.SenderUserID)
		}
		if ev.SenderUsername != "" {
			t.Fatalf("system broadcast must have empty SenderUsername, got %q", ev.SenderUsername)
		}
		if ev.Body != "Server going down in 5 minutes" {
			t.Fatalf("Body=%q", ev.Body)
		}
		count++
	}
	if count != 3 {
		t.Fatalf("expected 3 fanout sends, got %d", count)
	}
}

func TestHandleBroadcastSystem_PermissionDeniedWithoutChatAdmin(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll},
	})
	chid := svc.MustChannelID("world")

	// No chat.admin granted.
	_ = svc.MustOnlineFakeUser(101, "alice")

	resp, err := svc.HandleBroadcastSystem(&ops.OpContext{ConnID: 101}, &chat.ChatBroadcastRequest{
		ChannelID: chid.String(),
		Body:      "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorPermissionDenied) {
		t.Fatalf("got code=%d want PermissionDenied", resp.ErrorCode)
	}
}

func TestHandleBroadcastSystem_PermissionDeniedForChannelAdminOnly(t *testing.T) {
	// Channel-admin role does NOT grant the right to system-broadcast —
	// only chat.admin does. Verifies the gate is hasGlobalAdmin, not
	// canModerate.
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "council", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("council")

	caUID := svc.MustOnlineFakeUser(101, "ca")
	svc.MustAddMember(chid, caUID, "admin") // channel admin only

	resp, err := svc.HandleBroadcastSystem(&ops.OpContext{ConnID: 101}, &chat.ChatBroadcastRequest{
		ChannelID: chid.String(),
		Body:      "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorPermissionDenied) {
		t.Fatalf("channel admin should be denied broadcast: got code=%d", resp.ErrorCode)
	}
}

func TestHandleBroadcastSystem_BodyTooLarge(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll},
	})
	chid := svc.MustChannelID("world")

	gmUID := svc.MustOnlineFakeUser(101, "gm")
	mustGrantChatAdmin(t, authRepo, gmUID)

	body := make([]byte, 600) // default MaxMessageLen=500
	for i := range body {
		body[i] = 'x'
	}
	resp, err := svc.HandleBroadcastSystem(&ops.OpContext{ConnID: 101}, &chat.ChatBroadcastRequest{
		ChannelID: chid.String(),
		Body:      string(body),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorPayloadTooLarge) {
		t.Fatalf("got code=%d want PayloadTooLarge", resp.ErrorCode)
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
