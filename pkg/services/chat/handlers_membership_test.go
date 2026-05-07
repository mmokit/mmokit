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

// --- HandleAddMember ---

func TestHandleAddMember_HappyPath_AdminCallerSucceeds(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "council", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("council")

	// Caller (alice) is online and is the channel admin.
	aliceUID := svc.MustOnlineFakeUser(101, "alice")
	svc.MustAddMember(chid, aliceUID, "admin")

	// Target (bob) is offline — that's allowed for AddMember (it's an
	// op-side membership push, not a join-by-target).
	bobUID := uuid.New()

	resp, err := svc.HandleAddMember(&ops.OpContext{ConnID: 101}, &chat.ChatAddMemberRequest{
		ChannelID: chid.String(),
		UserID:    bobUID.String(),
		Role:      "member",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}

	mems, _ := chatRepo.ListMembers(context.Background(), chid)
	var foundBob bool
	for _, m := range mems {
		if m.UserID == bobUID && m.Role == "member" {
			foundBob = true
		}
	}
	if !foundBob {
		t.Fatal("bob not in member list after AddMember")
	}
}

func TestHandleAddMember_PermissionDeniedForNonAdmin(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "council", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("council")

	// Caller is online, but only a plain member (or no role at all).
	_ = svc.MustOnlineFakeUser(101, "alice")
	bobUID := uuid.New()

	resp, err := svc.HandleAddMember(&ops.OpContext{ConnID: 101}, &chat.ChatAddMemberRequest{
		ChannelID: chid.String(),
		UserID:    bobUID.String(),
		Role:      "member",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorPermissionDenied) {
		t.Fatalf("got code=%d want PermissionDenied=%d", resp.ErrorCode, chat.ChatErrorPermissionDenied)
	}
}

func TestHandleAddMember_GlobalAdminBypassesChannelRole(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "council", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("council")

	// Caller has chat.admin globally but no role on the channel.
	adminUID := svc.MustOnlineFakeUser(101, "admin")
	if err := authRepo.GrantCapability(context.Background(), auth.Capability{
		UserID:     adminUID,
		Capability: "chat.admin",
		GrantedBy:  uuid.Nil,
	}); err != nil {
		t.Fatalf("GrantCapability: %v", err)
	}

	bobUID := uuid.New()
	resp, err := svc.HandleAddMember(&ops.OpContext{ConnID: 101}, &chat.ChatAddMemberRequest{
		ChannelID: chid.String(),
		UserID:    bobUID.String(),
		Role:      "member",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("global admin should succeed: code=%d msg=%q", resp.ErrorCode, resp.ErrorMessage)
	}
}

func TestHandleAddMember_FanoutToExistingMembersExcludingNew(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "council", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("council")

	aliceUID := svc.MustOnlineFakeUser(101, "alice")
	svc.MustAddMember(chid, aliceUID, "admin")
	// Manually subscribe alice's conn (MustAddMember doesn't touch subs[]).
	carolUID := svc.MustOnlineFakeUser(102, "carol")
	svc.MustAddMember(chid, carolUID, "member")
	bobUID := svc.MustOnlineFakeUser(103, "bob")

	// Subscribe alice + carol via session-enter? No — MustAddMember leaves
	// subs untouched. Manipulate via HandleSessionEnter? For simplicity,
	// re-online via direct presence — but subs[chid] is what fanout reads.
	// In production, HandleSessionEnter populates subs[]. Use it here:
	//   We re-fake by calling SessionEnter for alice + carol.
	// Alternative: rebuild via HandleSessionEnter — but it would override
	// connID assignments. Easiest: write a tiny helper that injects subs.
	// For now, exercise the subs-injection path via HandleSessionEnter.
	//
	// Reset connIndex by calling SessionLeave then SessionEnter would
	// undo MustAddMember's userChans setup. So instead: inject subs
	// directly by calling HandleAddMember itself, recursively, for alice +
	// carol. They'd both need to be admins. Skip that — just verify
	// fanout via subs that AddMember itself populates.
	//
	// Actually the cleanest: AddMember puts target's conn into subs only
	// if target is online. Bob is online (connID 103). So this path
	// covers: existing members (alice+carol) won't be in subs[chid] yet
	// because MustAddMember doesn't sub their conn. The fanout will go
	// only to bob's conn (103), which is the new member we want to
	// EXCLUDE.
	//
	// To meaningfully test fanout-excludes-new, we need at least one
	// existing member to be in subs[chid]. Use HandleSessionEnter for
	// alice + carol AFTER MustAddMember (their userChans is already set,
	// so SessionEnter will subscribe their conn).
	//
	// SessionEnter assigns connIDs, so we need to re-assign 101/102 via
	// a fresh username+uid pair. Easiest: call SessionLeave first then
	// SessionEnter with the existing UIDs.

	// Drop alice's conn 101 from connIndex/online so SessionEnter can
	// re-bind UID to a new connID? Actually MustOnlineFakeUser already
	// bound conn 101→aliceUID. SessionEnter would create a new UUID.
	// Skip that — instead, manually inject alice+carol into subs[chid]
	// using a private helper. For tests we'd need to expose it.
	//
	// Simpler approach: add a TestService.MustSubscribe(chid, connID)
	// helper. Since we need it, add it here.

	svc.MustSubscribe(chid, 101) // alice
	svc.MustSubscribe(chid, 102) // carol

	var sent []recordedEvent
	svc.SetSendEventFn(func(c uint32, ev any) { sent = append(sent, recordedEvent{c, ev}) })

	resp, err := svc.HandleAddMember(&ops.OpContext{ConnID: 101}, &chat.ChatAddMemberRequest{
		ChannelID: chid.String(),
		UserID:    bobUID.String(),
		Role:      "member",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}

	// Bob (the new member) should NOT receive the fanout.
	for _, r := range sent {
		ev, ok := r.Event.(*chat.ChatMemberJoinedEvent)
		if !ok {
			continue
		}
		if r.ConnID == 103 {
			t.Fatal("new member's conn should not receive ChatMemberJoinedEvent fanout")
		}
		if ev.UserID != bobUID.String() {
			t.Fatalf("event UserID=%s want %s", ev.UserID, bobUID.String())
		}
	}
	// Alice + carol should receive it.
	gotAlice := false
	gotCarol := false
	for _, r := range sent {
		if _, ok := r.Event.(*chat.ChatMemberJoinedEvent); !ok {
			continue
		}
		if r.ConnID == 101 {
			gotAlice = true
		}
		if r.ConnID == 102 {
			gotCarol = true
		}
	}
	if !gotAlice || !gotCarol {
		t.Fatalf("expected alice+carol both got events, alice=%v carol=%v", gotAlice, gotCarol)
	}
}
