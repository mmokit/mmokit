package chat_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/zenion/mmoserver/pkg/ops"
	"github.com/zenion/mmoserver/pkg/services/auth"
	"github.com/zenion/mmoserver/pkg/services/auth/authtest"
	"github.com/zenion/mmoserver/pkg/services/chat"
	"github.com/zenion/mmoserver/pkg/services/chat/chattest"
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
