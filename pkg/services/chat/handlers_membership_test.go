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

// --- HandleRemoveMember ---

func TestHandleRemoveMember_HappyPath_AdminCallerSucceeds(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "council", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("council")

	aliceUID := svc.MustOnlineFakeUser(101, "alice")
	svc.MustAddMember(chid, aliceUID, "admin")
	bobUID := svc.MustOnlineFakeUser(102, "bob")
	svc.MustAddMember(chid, bobUID, "member")
	svc.MustSubscribe(chid, 101)
	svc.MustSubscribe(chid, 102)

	resp, err := svc.HandleRemoveMember(&ops.OpContext{ConnID: 101}, &chat.ChatRemoveMemberRequest{
		ChannelID: chid.String(),
		UserID:    bobUID.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}

	mems, _ := chatRepo.ListMembers(context.Background(), chid)
	for _, m := range mems {
		if m.UserID == bobUID {
			t.Fatal("bob still in repo member list after RemoveMember")
		}
	}

	// In-memory: bob's conn dropped from subs[chid].
	for _, c := range svc.FanoutTargets(chid) {
		if c == 102 {
			t.Fatal("bob's conn still in subs after RemoveMember")
		}
	}
}

func TestHandleRemoveMember_PermissionDeniedForNonAdmin(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "council", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("council")

	_ = svc.MustOnlineFakeUser(101, "alice") // not an admin
	bobUID := uuid.New()
	svc.MustAddMember(chid, bobUID, "member")

	resp, err := svc.HandleRemoveMember(&ops.OpContext{ConnID: 101}, &chat.ChatRemoveMemberRequest{
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

func TestHandleRemoveMember_FanoutExcludesRemovedUser(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "council", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("council")

	aliceUID := svc.MustOnlineFakeUser(101, "alice")
	svc.MustAddMember(chid, aliceUID, "admin")
	bobUID := svc.MustOnlineFakeUser(102, "bob")
	svc.MustAddMember(chid, bobUID, "member")
	carolUID := svc.MustOnlineFakeUser(103, "carol")
	svc.MustAddMember(chid, carolUID, "member")
	svc.MustSubscribe(chid, 101)
	svc.MustSubscribe(chid, 102)
	svc.MustSubscribe(chid, 103)

	var sent []recordedEvent
	svc.SetSendEventFn(func(c uint32, ev any) { sent = append(sent, recordedEvent{c, ev}) })

	resp, err := svc.HandleRemoveMember(&ops.OpContext{ConnID: 101}, &chat.ChatRemoveMemberRequest{
		ChannelID: chid.String(),
		UserID:    bobUID.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}

	// Bob (the removed user, conn 102) should NOT receive the fanout.
	// Alice (101) + carol (103) should.
	gotAlice := false
	gotCarol := false
	for _, r := range sent {
		ev, ok := r.Event.(*chat.ChatMemberLeftEvent)
		if !ok {
			continue
		}
		if r.ConnID == 102 {
			t.Fatal("removed user's conn should not receive ChatMemberLeftEvent")
		}
		if ev.UserID != bobUID.String() {
			t.Fatalf("event UserID=%s want %s", ev.UserID, bobUID.String())
		}
		if r.ConnID == 101 {
			gotAlice = true
		}
		if r.ConnID == 103 {
			gotCarol = true
		}
	}
	if !gotAlice || !gotCarol {
		t.Fatalf("expected alice+carol got events, alice=%v carol=%v", gotAlice, gotCarol)
	}
}

// --- HandleBulkSetMembers ---

func TestHandleBulkSetMembers_HappyPath_ReplacesMembers(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "council", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("council")

	aliceUID := svc.MustOnlineFakeUser(101, "alice")
	svc.MustAddMember(chid, aliceUID, "admin")
	bobUID := uuid.New()
	svc.MustAddMember(chid, bobUID, "member")

	// New set: alice + carol (bob departs).
	carolUID := uuid.New()
	resp, err := svc.HandleBulkSetMembers(&ops.OpContext{ConnID: 101}, &chat.ChatBulkSetMembersRequest{
		ChannelID: chid.String(),
		UserIDs:   []string{aliceUID.String(), carolUID.String()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}

	mems, _ := chatRepo.ListMembers(context.Background(), chid)
	if len(mems) != 2 {
		t.Fatalf("got %d members, want 2", len(mems))
	}
	gotAlice := false
	gotCarol := false
	for _, m := range mems {
		if m.UserID == aliceUID {
			gotAlice = true
		}
		if m.UserID == carolUID {
			gotCarol = true
		}
		if m.UserID == bobUID {
			t.Fatal("bob should be removed by BulkSetMembers")
		}
	}
	if !gotAlice || !gotCarol {
		t.Fatalf("alice=%v carol=%v in repo", gotAlice, gotCarol)
	}
}

func TestHandleBulkSetMembers_PermissionDeniedForNonAdmin(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "council", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("council")

	_ = svc.MustOnlineFakeUser(101, "alice")
	resp, err := svc.HandleBulkSetMembers(&ops.OpContext{ConnID: 101}, &chat.ChatBulkSetMembersRequest{
		ChannelID: chid.String(),
		UserIDs:   []string{uuid.NewString()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorPermissionDenied) {
		t.Fatalf("got code=%d want PermissionDenied", resp.ErrorCode)
	}
}

func TestHandleBulkSetMembers_FanoutForJoinersAndLeavers(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "council", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("council")

	// Existing: alice (admin, conn 101), bob (member, conn 102).
	aliceUID := svc.MustOnlineFakeUser(101, "alice")
	svc.MustAddMember(chid, aliceUID, "admin")
	bobUID := svc.MustOnlineFakeUser(102, "bob")
	svc.MustAddMember(chid, bobUID, "member")
	svc.MustSubscribe(chid, 101)
	svc.MustSubscribe(chid, 102)

	// New: alice + carol (online conn 103). Bob departs.
	carolUID := svc.MustOnlineFakeUser(103, "carol")

	var sent []recordedEvent
	svc.SetSendEventFn(func(c uint32, ev any) { sent = append(sent, recordedEvent{c, ev}) })

	resp, err := svc.HandleBulkSetMembers(&ops.OpContext{ConnID: 101}, &chat.ChatBulkSetMembersRequest{
		ChannelID: chid.String(),
		UserIDs:   []string{aliceUID.String(), carolUID.String()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}

	// Expect ChatMemberJoinedEvent for carol — recipients = post-set
	// subscribers minus carol's own conn = alice (101).
	gotJoinedForCarolToAlice := false
	for _, r := range sent {
		ev, ok := r.Event.(*chat.ChatMemberJoinedEvent)
		if !ok {
			continue
		}
		if ev.UserID == carolUID.String() {
			if r.ConnID == 103 {
				t.Fatal("joiner should not receive own ChatMemberJoinedEvent")
			}
			if r.ConnID == 101 {
				gotJoinedForCarolToAlice = true
			}
		}
	}
	if !gotJoinedForCarolToAlice {
		t.Fatal("expected ChatMemberJoinedEvent for carol to alice")
	}

	// Expect ChatMemberLeftEvent for bob — recipients = post-set subscribers
	// = alice (101) + carol (103). Bob's own conn (102) is no longer a sub.
	gotLeftToAlice := false
	gotLeftToCarol := false
	for _, r := range sent {
		ev, ok := r.Event.(*chat.ChatMemberLeftEvent)
		if !ok {
			continue
		}
		if ev.UserID != bobUID.String() {
			continue
		}
		if r.ConnID == 102 {
			t.Fatal("removed user's conn (bob) should not receive ChatMemberLeftEvent")
		}
		if r.ConnID == 101 {
			gotLeftToAlice = true
		}
		if r.ConnID == 103 {
			gotLeftToCarol = true
		}
	}
	if !gotLeftToAlice || !gotLeftToCarol {
		t.Fatalf("expected left events to alice+carol, alice=%v carol=%v", gotLeftToAlice, gotLeftToCarol)
	}
}

// --- HandleSetMemberRole ---

func TestHandleSetMemberRole_HappyPath_PromoteToAdmin(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "council", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("council")

	aliceUID := svc.MustOnlineFakeUser(101, "alice")
	svc.MustAddMember(chid, aliceUID, "admin")
	bobUID := svc.MustOnlineFakeUser(102, "bob")
	svc.MustAddMember(chid, bobUID, "member")

	resp, err := svc.HandleSetMemberRole(&ops.OpContext{ConnID: 101}, &chat.ChatSetMemberRoleRequest{
		ChannelID: chid.String(),
		UserID:    bobUID.String(),
		Role:      "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}

	mems, _ := chatRepo.ListMembers(context.Background(), chid)
	for _, m := range mems {
		if m.UserID == bobUID && m.Role != "admin" {
			t.Fatalf("bob role=%s want admin", m.Role)
		}
	}
}

func TestHandleSetMemberRole_PermissionDeniedForNonAdmin(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "council", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("council")

	_ = svc.MustOnlineFakeUser(101, "alice") // not admin
	bobUID := uuid.New()
	svc.MustAddMember(chid, bobUID, "member")

	resp, err := svc.HandleSetMemberRole(&ops.OpContext{ConnID: 101}, &chat.ChatSetMemberRoleRequest{
		ChannelID: chid.String(),
		UserID:    bobUID.String(),
		Role:      "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorPermissionDenied) {
		t.Fatalf("got code=%d want PermissionDenied", resp.ErrorCode)
	}
}

func TestHandleSetMemberRole_FanoutIncludesTarget(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "council", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("council")

	aliceUID := svc.MustOnlineFakeUser(101, "alice")
	svc.MustAddMember(chid, aliceUID, "admin")
	bobUID := svc.MustOnlineFakeUser(102, "bob")
	svc.MustAddMember(chid, bobUID, "member")
	carolUID := svc.MustOnlineFakeUser(103, "carol")
	svc.MustAddMember(chid, carolUID, "member")
	svc.MustSubscribe(chid, 101)
	svc.MustSubscribe(chid, 102)
	svc.MustSubscribe(chid, 103)

	var sent []recordedEvent
	svc.SetSendEventFn(func(c uint32, ev any) { sent = append(sent, recordedEvent{c, ev}) })

	resp, err := svc.HandleSetMemberRole(&ops.OpContext{ConnID: 101}, &chat.ChatSetMemberRoleRequest{
		ChannelID: chid.String(),
		UserID:    bobUID.String(),
		Role:      "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}

	// Bob (target, conn 102) MUST receive the event — they need to know
	// their role changed.
	gotAlice := false
	gotBob := false
	gotCarol := false
	for _, r := range sent {
		ev, ok := r.Event.(*chat.ChatMemberRoleChangedEvent)
		if !ok {
			continue
		}
		if ev.UserID != bobUID.String() {
			t.Fatalf("event UserID=%s want %s", ev.UserID, bobUID.String())
		}
		if ev.Role != "admin" {
			t.Fatalf("event Role=%s want admin", ev.Role)
		}
		switch r.ConnID {
		case 101:
			gotAlice = true
		case 102:
			gotBob = true
		case 103:
			gotCarol = true
		}
	}
	if !gotAlice || !gotBob || !gotCarol {
		t.Fatalf("expected alice+bob+carol all got events, alice=%v bob=%v carol=%v", gotAlice, gotBob, gotCarol)
	}
}

// --- Idempotency Tests ---

func TestHandleAddMember_IdempotentReAddSendsNoEvent(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "council", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("council")

	// Setup: admin caller and existing member.
	adminUID := svc.MustOnlineFakeUser(101, "admin")
	svc.MustAddMember(chid, adminUID, "admin")
	targetUID := svc.MustOnlineFakeUser(102, "target")
	svc.MustAddMember(chid, targetUID, "member")
	svc.MustSubscribe(chid, 101)
	svc.MustSubscribe(chid, 102)

	// Grant admin caller chat.admin capability.
	if err := authRepo.GrantCapability(context.Background(), auth.Capability{
		UserID:     adminUID,
		Capability: "chat.admin",
		GrantedBy:  adminUID,
	}); err != nil {
		t.Fatalf("GrantCapability: %v", err)
	}

	var sent []recordedEvent
	svc.SetSendEventFn(func(c uint32, ev any) { sent = append(sent, recordedEvent{c, ev}) })

	// Re-add the target with the same role.
	resp, err := svc.HandleAddMember(&ops.OpContext{ConnID: 101},
		&chat.ChatAddMemberRequest{ChannelID: chid.String(), UserID: targetUID.String(), Role: "member"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}

	// Assert no ChatMemberJoinedEvent fired.
	for _, r := range sent {
		if _, ok := r.Event.(*chat.ChatMemberJoinedEvent); ok {
			t.Errorf("got spurious ChatMemberJoinedEvent on idempotent re-add")
		}
	}
}

func TestHandleAddMember_RoleChangeEmitsRoleChangedEvent(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "council", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("council")

	// Setup: admin caller and existing member.
	adminUID := svc.MustOnlineFakeUser(101, "admin")
	svc.MustAddMember(chid, adminUID, "admin")
	targetUID := svc.MustOnlineFakeUser(102, "target")
	svc.MustAddMember(chid, targetUID, "member")
	svc.MustSubscribe(chid, 101)
	svc.MustSubscribe(chid, 102)

	// Grant admin caller chat.admin capability.
	if err := authRepo.GrantCapability(context.Background(), auth.Capability{
		UserID:     adminUID,
		Capability: "chat.admin",
		GrantedBy:  adminUID,
	}); err != nil {
		t.Fatalf("GrantCapability: %v", err)
	}

	var sent []recordedEvent
	svc.SetSendEventFn(func(c uint32, ev any) { sent = append(sent, recordedEvent{c, ev}) })

	// Re-add the target with a role change (member → admin).
	resp, err := svc.HandleAddMember(&ops.OpContext{ConnID: 101},
		&chat.ChatAddMemberRequest{ChannelID: chid.String(), UserID: targetUID.String(), Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}

	// Assert ChatMemberRoleChangedEvent fired to all members (including target).
	gotRoleChanged := false
	gotToAdmin := false
	gotToTarget := false
	for _, r := range sent {
		ev, ok := r.Event.(*chat.ChatMemberRoleChangedEvent)
		if !ok {
			continue
		}
		if ev.UserID == targetUID.String() && ev.Role == "admin" {
			gotRoleChanged = true
			if r.ConnID == 101 {
				gotToAdmin = true
			}
			if r.ConnID == 102 {
				gotToTarget = true
			}
		}
	}
	if !gotRoleChanged {
		t.Fatalf("expected ChatMemberRoleChangedEvent for target's role change")
	}
	if !gotToAdmin || !gotToTarget {
		t.Fatalf("expected event to both admin and target, gotToAdmin=%v gotToTarget=%v", gotToAdmin, gotToTarget)
	}
}
