package chat_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/zenion/mmokit/pkg/ops"
	"github.com/zenion/mmokit/pkg/services/auth"
	"github.com/zenion/mmokit/pkg/services/auth/authtest"
	"github.com/zenion/mmokit/pkg/services/chat"
	"github.com/zenion/mmokit/pkg/services/chat/chattest"
)

// --- HandleRegisterChannel ---

func TestHandleRegisterChannel_HappyPath_SystemPredicate(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, nil)

	adminUID := svc.MustOnlineFakeUser(101, "admin")
	if err := authRepo.GrantCapability(context.Background(), auth.Capability{
		UserID: adminUID, Capability: "chat.admin", GrantedBy: uuid.Nil,
	}); err != nil {
		t.Fatalf("GrantCapability: %v", err)
	}

	resp, err := svc.HandleRegisterChannel(&ops.OpContext{ConnID: 101}, &chat.ChatRegisterChannelRequest{
		Slug:  "guild:alpha",
		Kind:  chat.ChannelKindSystemPredicate,
		Topic: "Alpha Guild",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}
	if resp.Channel.Slug != "guild:alpha" {
		t.Fatalf("got slug=%q", resp.Channel.Slug)
	}
	if resp.Channel.Kind != chat.ChannelKindSystemPredicate {
		t.Fatalf("got kind=%v", resp.Channel.Kind)
	}

	stored, err := chatRepo.GetChannelBySlug(context.Background(), "guild:alpha")
	if err != nil {
		t.Fatalf("GetChannelBySlug: %v", err)
	}
	if stored.Kind != "system_predicate" {
		t.Fatalf("stored kind=%s", stored.Kind)
	}
}

func TestHandleRegisterChannel_PermissionDeniedWithoutChatAdmin(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, nil)

	_ = svc.MustOnlineFakeUser(101, "alice") // no chat.admin granted

	resp, err := svc.HandleRegisterChannel(&ops.OpContext{ConnID: 101}, &chat.ChatRegisterChannelRequest{
		Slug: "world",
		Kind: chat.ChannelKindSystemAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorPermissionDenied) {
		t.Fatalf("got code=%d want PermissionDenied", resp.ErrorCode)
	}
}

func TestHandleRegisterChannel_RejectsCustomKind(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, nil)

	adminUID := svc.MustOnlineFakeUser(101, "admin")
	_ = authRepo.GrantCapability(context.Background(), auth.Capability{
		UserID: adminUID, Capability: "chat.admin",
	})

	resp, err := svc.HandleRegisterChannel(&ops.OpContext{ConnID: 101}, &chat.ChatRegisterChannelRequest{
		Slug: "cool",
		Kind: chat.ChannelKindCustom, // not allowed
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode == 0 {
		t.Fatal("expected error for CUSTOM kind on RegisterChannel")
	}
}

// --- HandleUnregisterChannel ---

func TestHandleUnregisterChannel_HappyPath(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "guild:alpha", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("guild:alpha")

	adminUID := svc.MustOnlineFakeUser(101, "admin")
	_ = authRepo.GrantCapability(context.Background(), auth.Capability{
		UserID: adminUID, Capability: "chat.admin",
	})

	resp, err := svc.HandleUnregisterChannel(&ops.OpContext{ConnID: 101}, &chat.ChatUnregisterChannelRequest{
		ChannelID: chid.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}

	if _, err := chatRepo.GetChannelByID(context.Background(), chid); err != chat.ErrChannelNotFound {
		t.Fatalf("expected ErrChannelNotFound, got %v", err)
	}
}

func TestHandleUnregisterChannel_PermissionDenied(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "guild:alpha", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("guild:alpha")

	_ = svc.MustOnlineFakeUser(101, "alice") // no chat.admin

	resp, err := svc.HandleUnregisterChannel(&ops.OpContext{ConnID: 101}, &chat.ChatUnregisterChannelRequest{
		ChannelID: chid.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorPermissionDenied) {
		t.Fatalf("got code=%d want PermissionDenied", resp.ErrorCode)
	}
}

func TestHandleUnregisterChannel_FanoutChannelGoneEventToSubscribers(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "guild:alpha", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("guild:alpha")

	adminUID := svc.MustOnlineFakeUser(101, "admin")
	_ = authRepo.GrantCapability(context.Background(), auth.Capability{
		UserID: adminUID, Capability: "chat.admin",
	})

	bobUID := svc.MustOnlineFakeUser(102, "bob")
	svc.MustAddMember(chid, bobUID, "member")
	svc.MustSubscribe(chid, 102)

	carolUID := svc.MustOnlineFakeUser(103, "carol")
	svc.MustAddMember(chid, carolUID, "member")
	svc.MustSubscribe(chid, 103)

	var sent []recordedEvent
	svc.SetSendEventFn(func(c uint32, ev any) { sent = append(sent, recordedEvent{c, ev}) })

	resp, err := svc.HandleUnregisterChannel(&ops.OpContext{ConnID: 101}, &chat.ChatUnregisterChannelRequest{
		ChannelID: chid.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}

	gotBob := false
	gotCarol := false
	for _, r := range sent {
		ev, ok := r.Event.(*chat.ChatChannelGoneEvent)
		if !ok {
			continue
		}
		if ev.ChannelID != chid.String() {
			t.Fatalf("event channel=%s want %s", ev.ChannelID, chid.String())
		}
		if r.ConnID == 102 {
			gotBob = true
		}
		if r.ConnID == 103 {
			gotCarol = true
		}
	}
	if !gotBob || !gotCarol {
		t.Fatalf("expected bob+carol got ChatChannelGoneEvent, bob=%v carol=%v", gotBob, gotCarol)
	}
}
