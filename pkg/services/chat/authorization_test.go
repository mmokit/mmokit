package chat_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/mmokit/mmokit/pkg/services/auth"
	"github.com/mmokit/mmokit/pkg/services/auth/authtest"
	"github.com/mmokit/mmokit/pkg/services/chat"
	"github.com/mmokit/mmokit/pkg/services/chat/chattest"
)

func TestCanModerate_GlobalAdminAlwaysWins(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll},
	})
	chid := svc.MustChannelID("world")

	admin := uuid.New()
	if err := authRepo.GrantCapability(context.Background(), auth.Capability{
		UserID:     admin,
		Capability: "chat.admin",
		GrantedBy:  uuid.Nil,
	}); err != nil {
		t.Fatalf("GrantCapability: %v", err)
	}

	if !svc.CanModerate(admin, chid) {
		t.Fatal("global chat.admin should pass canModerate even with no per-channel role")
	}
}

func TestCanModerate_ChannelAdminPasses(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "council", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("council")

	uid := uuid.New()
	svc.MustAddMember(chid, uid, "admin")

	if !svc.CanModerate(uid, chid) {
		t.Fatal("channel admin role should pass canModerate")
	}
}

func TestCanModerate_PlainMemberFails(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "council", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("council")

	uid := uuid.New()
	svc.MustAddMember(chid, uid, "member")

	if svc.CanModerate(uid, chid) {
		t.Fatal("plain member should not pass canModerate")
	}
}
