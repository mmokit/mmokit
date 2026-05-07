package chat_test

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/ops"
	"github.com/zenion/mmoserver/pkg/services/auth/authtest"
	"github.com/zenion/mmoserver/pkg/services/chat"
	"github.com/zenion/mmoserver/pkg/services/chat/chattest"
)

// --- HandleListChannels ---

func TestHandleListChannels_HappyPath(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll},
		{Slug: "help", Kind: chat.ChannelKindSystemAll},
		{Slug: "trade", Kind: chat.ChannelKindSystemAll},
	})

	_ = svc.MustOnlineFakeUser(101, "alice")

	// Create one custom channel — alice is auto-member.
	create, err := svc.HandleCreate(&ops.OpContext{ConnID: 101}, &chat.ChatCreateRequest{Slug: "cool"})
	if err != nil || create.ErrorCode != 0 {
		t.Fatalf("create: %v %s", err, create.ErrorMessage)
	}

	resp, err := svc.HandleListChannels(&ops.OpContext{ConnID: 101}, &chat.ChatListChannelsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}
	// 3 SYSTEM_ALL + 1 custom = 4
	if len(resp.Channels) != 4 {
		t.Fatalf("got %d channels want 4: %#v", len(resp.Channels), resp.Channels)
	}
	gotWorld := false
	gotCustom := false
	for _, c := range resp.Channels {
		if c.Slug == "world" {
			gotWorld = true
		}
		if c.Slug == "cool" {
			gotCustom = true
		}
	}
	if !gotWorld || !gotCustom {
		t.Fatalf("expected world+cool, got world=%v cool=%v", gotWorld, gotCustom)
	}
}

func TestHandleListChannels_IncludesSystemPredicateMembership(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll},
		{Slug: "guild:42", Kind: chat.ChannelKindSystemPredicate},
		{Slug: "guild:99", Kind: chat.ChannelKindSystemPredicate}, // alice NOT a member
	})

	aliceUID := svc.MustOnlineFakeUser(101, "alice")
	chid42 := svc.MustChannelID("guild:42")
	svc.MustAddMember(chid42, aliceUID, "member")

	resp, err := svc.HandleListChannels(&ops.OpContext{ConnID: 101}, &chat.ChatListChannelsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}
	// world (SYSTEM_ALL) + guild:42 (member) = 2; guild:99 excluded.
	if len(resp.Channels) != 2 {
		t.Fatalf("got %d channels want 2: %#v", len(resp.Channels), resp.Channels)
	}
	gotWorld := false
	got42 := false
	for _, c := range resp.Channels {
		if c.Slug == "world" {
			gotWorld = true
		}
		if c.Slug == "guild:42" {
			got42 = true
		}
		if c.Slug == "guild:99" {
			t.Fatal("alice should not see guild:99 — not a member")
		}
	}
	if !gotWorld || !got42 {
		t.Fatalf("expected world+guild:42, got world=%v guild42=%v", gotWorld, got42)
	}
}
