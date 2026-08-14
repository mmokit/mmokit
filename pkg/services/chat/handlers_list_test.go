package chat_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/zenion/mmokit/pkg/ops"
	"github.com/zenion/mmokit/pkg/services/auth/authtest"
	"github.com/zenion/mmokit/pkg/services/chat"
	"github.com/zenion/mmokit/pkg/services/chat/chattest"
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

// --- HandleListMembers ---

func TestHandleListMembers_HappyPath_SystemAllReturnsOnline(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll},
	})
	chid := svc.MustChannelID("world")

	_ = svc.MustOnlineFakeUser(101, "alice")
	_ = svc.MustOnlineFakeUser(102, "bob")
	_ = svc.MustOnlineFakeUser(103, "carol")

	resp, err := svc.HandleListMembers(&ops.OpContext{ConnID: 101}, &chat.ChatListMembersRequest{
		ChannelID: chid.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}
	if len(resp.Members) != 3 {
		t.Fatalf("got %d members want 3: %#v", len(resp.Members), resp.Members)
	}
	gotAlice := false
	gotBob := false
	gotCarol := false
	for _, m := range resp.Members {
		if m.Role != "member" {
			t.Fatalf("system_all member role=%q want member", m.Role)
		}
		switch m.Username {
		case "alice":
			gotAlice = true
		case "bob":
			gotBob = true
		case "carol":
			gotCarol = true
		}
	}
	if !gotAlice || !gotBob || !gotCarol {
		t.Fatalf("expected alice+bob+carol, got alice=%v bob=%v carol=%v", gotAlice, gotBob, gotCarol)
	}
}

func TestHandleListMembers_HappyPath_CustomChannelReturnsFullList(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "guild:alpha", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("guild:alpha")

	aliceUID := svc.MustOnlineFakeUser(101, "alice")
	svc.MustAddMember(chid, aliceUID, "admin")
	bobUID := svc.MustOnlineFakeUser(102, "bob")
	svc.MustAddMember(chid, bobUID, "member")
	// carol added but offline (no MustOnlineFakeUser).
	carolUID := uuid.New()
	svc.MustAddMember(chid, carolUID, "member")

	resp, err := svc.HandleListMembers(&ops.OpContext{ConnID: 101}, &chat.ChatListMembersRequest{
		ChannelID: chid.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}
	if len(resp.Members) != 3 {
		t.Fatalf("got %d members want 3: %#v", len(resp.Members), resp.Members)
	}
	gotAlice := false
	gotBob := false
	gotCarol := false
	for _, m := range resp.Members {
		switch m.UserID {
		case aliceUID.String():
			gotAlice = true
			if m.Role != "admin" {
				t.Fatalf("alice role=%q want admin", m.Role)
			}
			if m.Username != "alice" {
				t.Fatalf("alice username=%q want alice", m.Username)
			}
		case bobUID.String():
			gotBob = true
			if m.Role != "member" {
				t.Fatalf("bob role=%q want member", m.Role)
			}
			if m.Username != "bob" {
				t.Fatalf("bob username=%q want bob", m.Username)
			}
		case carolUID.String():
			gotCarol = true
			if m.Role != "member" {
				t.Fatalf("carol role=%q want member", m.Role)
			}
			// carol is offline — username is empty (acceptable for v1).
			if m.Username != "" {
				t.Fatalf("offline carol username=%q want empty", m.Username)
			}
		}
	}
	if !gotAlice || !gotBob || !gotCarol {
		t.Fatalf("expected alice+bob+carol, got alice=%v bob=%v carol=%v", gotAlice, gotBob, gotCarol)
	}
}

func TestHandleListMembers_NotAMemberOnCustom(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "guild:alpha", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("guild:alpha")

	// alice is online but NOT a member of guild:alpha.
	_ = svc.MustOnlineFakeUser(101, "alice")

	resp, err := svc.HandleListMembers(&ops.OpContext{ConnID: 101}, &chat.ChatListMembersRequest{
		ChannelID: chid.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorNotAMember) {
		t.Fatalf("got code=%d want NotAMember", resp.ErrorCode)
	}
}
