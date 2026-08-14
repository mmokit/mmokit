package chat_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mmokit/mmokit/pkg/cmdsys"
	"github.com/mmokit/mmokit/pkg/services/auth"
	"github.com/mmokit/mmokit/pkg/services/auth/authtest"
	"github.com/mmokit/mmokit/pkg/services/chat"
	"github.com/mmokit/mmokit/pkg/services/chat/chattest"
)

// consoleFixture wires together a chat service, an auth repo, an operator
// (logged-in admin user with chat.admin), and a cmdsys dispatcher with
// chat console commands registered. Returned values:
//   - svc: the live chat service (with the operator's online presence set)
//   - authRepo: in-memory auth repo (operator already created)
//   - disp: cmdsys dispatcher pointed at the chat command registry
//   - operatorID: operator's auth.User.UserID (matches the cmdsys Caller.ID)
//   - operatorConnID: connID under which the operator is "online" in chat
//   - operatorCaller: pre-built cmdsys.Caller with global wildcard grants
type consoleFixture struct {
	svc            *chat.TestService
	authRepo       *authtest.RepoMock
	disp           *cmdsys.Dispatcher
	operatorID     uuid.UUID
	operatorConnID uint32
	operatorCaller cmdsys.Caller
}

func newConsoleFixture(t *testing.T) *consoleFixture {
	t.Helper()
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, nil)

	// Create the operator user in the auth repo.
	const operatorName = "operator"
	user, err := authRepo.CreateUser(context.Background(), auth.User{
		Username: operatorName,
		Email:    "operator@test.local",
	}, "ignored-hash")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := authRepo.GrantCapability(context.Background(), auth.Capability{
		UserID: user.UserID, Capability: "chat.admin", GrantedBy: uuid.Nil,
	}); err != nil {
		t.Fatalf("GrantCapability: %v", err)
	}
	// Place the operator online in chat under a known connID. The console
	// handler resolves the connID via Service.OnlineConnIDForUser (keyed by
	// operator user_id), so we have to stamp that mapping ourselves —
	// MustOnlineFakeUser returns a fresh UUID, so we instead use the
	// already-created auth user_id and inject presence directly.
	const opConnID uint32 = 9000
	svc.MustOnlineExistingUser(opConnID, user.UserID, operatorName)

	reg := cmdsys.NewRegistry()
	getSvc := func() *chat.Service { return svc.Service }
	getAuth := func() auth.Repository { return authRepo }
	if err := chat.RegisterConsoleCommands(reg, getSvc, getAuth); err != nil {
		t.Fatalf("RegisterConsoleCommands: %v", err)
	}
	disp := cmdsys.NewDispatcher(cmdsys.DispatcherConfig{
		Registry: reg,
		Audit:    cmdsys.NoopAuditSink{},
	})
	t.Cleanup(func() { _ = disp.Close() })

	caller := cmdsys.NewOperatorIdentity("test-op")
	caller.ID = user.UserID.String() // operator's chat-side identity

	return &consoleFixture{
		svc:            svc,
		authRepo:       authRepo,
		disp:           disp,
		operatorID:     user.UserID,
		operatorConnID: opConnID,
		operatorCaller: caller,
	}
}

func deadlineCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// invokeOK invokes verb and asserts a single-target success result, returning
// the first PerTarget result for caller-side assertions.
func (f *consoleFixture) invokeOK(t *testing.T, verb string, args any) cmdsys.TargetResult {
	t.Helper()
	res, err := f.disp.Invoke(deadlineCtx(t), f.operatorCaller, verb, args)
	if err != nil {
		t.Fatalf("Invoke %s: %v", verb, err)
	}
	if len(res.PerTarget) != 1 {
		t.Fatalf("Invoke %s: PerTarget len=%d want 1", verb, len(res.PerTarget))
	}
	tr := res.PerTarget[0]
	if !tr.OK {
		t.Fatalf("Invoke %s: target not OK: %s", verb, tr.Error)
	}
	return tr
}

// invokeFail invokes verb expecting target-level failure (handler returned
// an error). Returns the formatted error message.
func (f *consoleFixture) invokeFail(t *testing.T, verb string, args any) string {
	t.Helper()
	res, err := f.disp.Invoke(deadlineCtx(t), f.operatorCaller, verb, args)
	if err != nil {
		// Whole-dispatch error counts as fail too.
		return err.Error()
	}
	if len(res.PerTarget) != 1 {
		t.Fatalf("Invoke %s: PerTarget len=%d want 1", verb, len(res.PerTarget))
	}
	tr := res.PerTarget[0]
	if tr.OK {
		t.Fatalf("Invoke %s: expected failure, got OK", verb)
	}
	return tr.Error
}

// --- Read-only commands ---

func TestConsole_ChannelList_Empty(t *testing.T) {
	f := newConsoleFixture(t)
	tr := f.invokeOK(t, "chat.channel.list", struct{}{})
	got, ok := tr.Result.(chat.ChannelListResult)
	if !ok {
		t.Fatalf("result type %T", tr.Result)
	}
	if len(got.Channels) != 0 {
		t.Fatalf("expected empty channel list, got %d", len(got.Channels))
	}
}

func TestConsole_ChannelInfo_NotFound(t *testing.T) {
	f := newConsoleFixture(t)
	msg := f.invokeFail(t, "chat.channel.info", chat.ChannelSlugArgs{Slug: "noexist"})
	if !strings.Contains(msg, "not found") {
		t.Fatalf("expected 'not found' error, got %q", msg)
	}
}

// --- Channel-mutation commands ---

func TestConsole_ChannelCreate_AndInfo(t *testing.T) {
	f := newConsoleFixture(t)
	f.invokeOK(t, "chat.channel.create", chat.ChannelCreateArgs{
		Slug:  "world",
		Kind:  "system_all",
		Topic: "global chat",
	})
	tr := f.invokeOK(t, "chat.channel.info", chat.ChannelSlugArgs{Slug: "world"})
	info, ok := tr.Result.(chat.ChannelInfoResult)
	if !ok {
		t.Fatalf("result type %T", tr.Result)
	}
	if info.Slug != "world" {
		t.Fatalf("slug=%q", info.Slug)
	}
	if info.Kind != "system_all" {
		t.Fatalf("kind=%q", info.Kind)
	}
	if info.Topic != "global chat" {
		t.Fatalf("topic=%q", info.Topic)
	}
}

func TestConsole_ChannelCreate_RejectsCustom(t *testing.T) {
	f := newConsoleFixture(t)
	msg := f.invokeFail(t, "chat.channel.create", chat.ChannelCreateArgs{
		Slug: "private", Kind: "custom",
	})
	if !strings.Contains(msg, "user-owned") && !strings.Contains(msg, "custom") {
		t.Fatalf("expected refusal for custom kind, got %q", msg)
	}
}

func TestConsole_ChannelRename(t *testing.T) {
	f := newConsoleFixture(t)
	f.invokeOK(t, "chat.channel.create", chat.ChannelCreateArgs{
		Slug: "guild:alpha", Kind: "system_predicate",
	})
	f.invokeOK(t, "chat.channel.rename", chat.ChannelRenameArgs{
		Slug: "guild:alpha", NewSlug: "guild:beta",
	})
	if _, ok := f.svc.ChannelIDBySlug("guild:beta"); !ok {
		t.Fatal("expected guild:beta after rename")
	}
	if _, ok := f.svc.ChannelIDBySlug("guild:alpha"); ok {
		t.Fatal("expected guild:alpha gone after rename")
	}
}

func TestConsole_ChannelTopic_AndSlowMode(t *testing.T) {
	f := newConsoleFixture(t)
	f.invokeOK(t, "chat.channel.create", chat.ChannelCreateArgs{
		Slug: "trade", Kind: "system_predicate",
	})
	f.invokeOK(t, "chat.channel.topic", chat.ChannelTopicArgs{
		Slug: "trade", Topic: "buy/sell only",
	})
	f.invokeOK(t, "chat.channel.slowmode", chat.ChannelSlowModeArgs{
		Slug: "trade", Seconds: 5,
	})
	id, _ := f.svc.ChannelIDBySlug("trade")
	c, _ := f.svc.ChannelByID(id)
	if c.Topic != "buy/sell only" {
		t.Fatalf("topic=%q", c.Topic)
	}
	if c.SlowModeSeconds != 5 {
		t.Fatalf("slowmode=%d", c.SlowModeSeconds)
	}
}

func TestConsole_ChannelDelete(t *testing.T) {
	f := newConsoleFixture(t)
	f.invokeOK(t, "chat.channel.create", chat.ChannelCreateArgs{
		Slug: "ephemeral", Kind: "system_predicate",
	})
	f.invokeOK(t, "chat.channel.delete", chat.ChannelSlugArgs{Slug: "ephemeral"})
	if _, ok := f.svc.ChannelIDBySlug("ephemeral"); ok {
		t.Fatal("expected channel gone after delete")
	}
}

// --- Membership-mutation commands ---

func TestConsole_AddRemoveMember(t *testing.T) {
	f := newConsoleFixture(t)
	f.invokeOK(t, "chat.channel.create", chat.ChannelCreateArgs{
		Slug: "guild:alpha", Kind: "system_predicate",
	})
	// Create the target user in auth.
	target, err := f.authRepo.CreateUser(context.Background(), auth.User{
		Username: "alice",
	}, "h")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	f.invokeOK(t, "chat.channel.addmember", chat.ChannelMemberArgs{
		Slug: "guild:alpha", Username: "alice",
	})
	// Verify membership in the in-memory state.
	chID, _ := f.svc.ChannelIDBySlug("guild:alpha")
	if !f.svc.HasMember(chID, target.UserID) {
		t.Fatal("alice not a member after addmember")
	}
	f.invokeOK(t, "chat.channel.removemember", chat.ChannelMemberRemoveArgs{
		Slug: "guild:alpha", Username: "alice",
	})
	if f.svc.HasMember(chID, target.UserID) {
		t.Fatal("alice still member after removemember")
	}
}

func TestConsole_AddMember_UnknownUser(t *testing.T) {
	f := newConsoleFixture(t)
	f.invokeOK(t, "chat.channel.create", chat.ChannelCreateArgs{
		Slug: "guild:alpha", Kind: "system_predicate",
	})
	msg := f.invokeFail(t, "chat.channel.addmember", chat.ChannelMemberArgs{
		Slug: "guild:alpha", Username: "ghost",
	})
	if !strings.Contains(msg, "ghost") {
		t.Fatalf("expected error mentioning ghost, got %q", msg)
	}
}

// --- Per-user moderation commands ---

func TestConsole_UserMute_Global(t *testing.T) {
	f := newConsoleFixture(t)
	target, err := f.authRepo.CreateUser(context.Background(), auth.User{
		Username: "alice",
	}, "h")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	f.invokeOK(t, "chat.user.mute", chat.UserMuteArgs{
		Username: "alice", Duration: "10m",
	})
	active, _ := f.svc.MuteCheck(target.UserID, chat.MuteGlobalChannelID)
	if !active {
		t.Fatal("alice should be globally muted")
	}
	f.invokeOK(t, "chat.user.unmute", chat.UserUnmuteArgs{Username: "alice"})
	active, _ = f.svc.MuteCheck(target.UserID, chat.MuteGlobalChannelID)
	if active {
		t.Fatal("alice should be unmuted")
	}
}

func TestConsole_UserMute_BadDuration(t *testing.T) {
	f := newConsoleFixture(t)
	_, err := f.authRepo.CreateUser(context.Background(), auth.User{
		Username: "alice",
	}, "h")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	msg := f.invokeFail(t, "chat.user.mute", chat.UserMuteArgs{
		Username: "alice", Duration: "not-a-duration",
	})
	if !strings.Contains(msg, "duration") {
		t.Fatalf("expected duration error, got %q", msg)
	}
}

func TestConsole_UserKickAndBan(t *testing.T) {
	f := newConsoleFixture(t)
	f.invokeOK(t, "chat.channel.create", chat.ChannelCreateArgs{
		Slug: "guild:alpha", Kind: "system_predicate",
	})
	target, err := f.authRepo.CreateUser(context.Background(), auth.User{
		Username: "bob",
	}, "h")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	f.invokeOK(t, "chat.channel.addmember", chat.ChannelMemberArgs{
		Slug: "guild:alpha", Username: "bob",
	})
	chID, _ := f.svc.ChannelIDBySlug("guild:alpha")

	// Kick — verify removed.
	f.invokeOK(t, "chat.user.kick", chat.UserKickArgs{
		Username: "bob", Channel: "guild:alpha",
	})
	if f.svc.HasMember(chID, target.UserID) {
		t.Fatal("bob still a member after kick")
	}

	// Re-add then ban for 1h.
	f.invokeOK(t, "chat.channel.addmember", chat.ChannelMemberArgs{
		Slug: "guild:alpha", Username: "bob",
	})
	f.invokeOK(t, "chat.user.ban", chat.UserBanArgs{
		Username: "bob", Channel: "guild:alpha", Duration: "1h",
	})
	active, _ := f.svc.BanCheck(target.UserID, chID)
	if !active {
		t.Fatal("bob should be banned after ban command")
	}

	// Unban.
	f.invokeOK(t, "chat.user.unban", chat.UserUnbanArgs{
		Username: "bob", Channel: "guild:alpha",
	})
	active, _ = f.svc.BanCheck(target.UserID, chID)
	if active {
		t.Fatal("bob should be unbanned after unban command")
	}
}

// --- Broadcast / msg.delete ---

func TestConsole_Broadcast(t *testing.T) {
	f := newConsoleFixture(t)
	f.invokeOK(t, "chat.channel.create", chat.ChannelCreateArgs{
		Slug: "world", Kind: "system_all",
	})
	f.invokeOK(t, "chat.broadcast", chat.BroadcastArgs{
		Channel: "world", Body: "server restarting in 5 minutes",
	})
	// No assertion on fanout (no event sink wired in tests); the absence
	// of an error is the contract — the handler accepted the message and
	// dispatched without service-side validation tripping.
}

// TestConsole_BareConsole_Broadcast covers the bare interactive coord
// console path: caller ID is the sentinel "console" (not a UUID), Source
// is SourceConsole, and no operator is online in chat. operatorOpContext
// must build a SystemTrusted opCtx so HandleBroadcastSystem bypasses
// connIndex auth and broadcast succeeds. Regression for the bug where
// `chat broadcast` from coord console errored "invalid operator user_id
// 'console': invalid UUID length: 7".
func TestConsole_BareConsole_Broadcast(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, nil)

	reg := cmdsys.NewRegistry()
	if err := chat.RegisterConsoleCommands(reg,
		func() *chat.Service { return svc.Service },
		func() auth.Repository { return authRepo },
	); err != nil {
		t.Fatalf("RegisterConsoleCommands: %v", err)
	}
	disp := cmdsys.NewDispatcher(cmdsys.DispatcherConfig{
		Registry: reg,
		Audit:    cmdsys.NoopAuditSink{},
	})
	t.Cleanup(func() { _ = disp.Close() })

	// Mirror engine.operatorCaller: ID is the sentinel string, not a UUID.
	caller := cmdsys.NewOperatorIdentity("console")

	// Create a channel via the same trusted path.
	if _, err := disp.Invoke(deadlineCtx(t), caller, "chat.channel.create",
		chat.ChannelCreateArgs{Slug: "world", Kind: "system_all"}); err != nil {
		t.Fatalf("Invoke channel.create: %v", err)
	}
	// Broadcast must succeed with no operator logged in.
	res, err := disp.Invoke(deadlineCtx(t), caller, "chat.broadcast",
		chat.BroadcastArgs{Channel: "world", Body: "hello i am god"})
	if err != nil {
		t.Fatalf("Invoke chat.broadcast: %v", err)
	}
	if len(res.PerTarget) != 1 || !res.PerTarget[0].OK {
		t.Fatalf("expected broadcast OK, got %+v", res.PerTarget)
	}
}

func TestConsole_MsgDelete_Unknown(t *testing.T) {
	f := newConsoleFixture(t)
	f.invokeOK(t, "chat.channel.create", chat.ChannelCreateArgs{
		Slug: "world", Kind: "system_all",
	})
	// Attempt to delete a msg_id that was never indexed → unknown.
	msg := f.invokeFail(t, "chat.msg.delete", chat.MsgDeleteArgs{
		MsgID: "0190abcd-0000-7000-8000-000000000000", Channel: "world",
	})
	if !strings.Contains(strings.ToLower(msg), "unknown") &&
		!strings.Contains(strings.ToLower(msg), "expired") {
		t.Fatalf("expected unknown-message error, got %q", msg)
	}
}

// --- Operator-online enforcement (required for mutation commands) ---

func TestConsole_RequiresOperatorOnline(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, nil)

	// Create the operator but DON'T mark them online in chat.
	user, err := authRepo.CreateUser(context.Background(), auth.User{
		Username: "operator",
	}, "h")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := authRepo.GrantCapability(context.Background(), auth.Capability{
		UserID: user.UserID, Capability: "chat.admin", GrantedBy: uuid.Nil,
	}); err != nil {
		t.Fatalf("GrantCapability: %v", err)
	}

	reg := cmdsys.NewRegistry()
	if err := chat.RegisterConsoleCommands(reg,
		func() *chat.Service { return svc.Service },
		func() auth.Repository { return authRepo },
	); err != nil {
		t.Fatalf("RegisterConsoleCommands: %v", err)
	}
	disp := cmdsys.NewDispatcher(cmdsys.DispatcherConfig{
		Registry: reg,
		Audit:    cmdsys.NoopAuditSink{},
	})
	t.Cleanup(func() { _ = disp.Close() })

	caller := cmdsys.NewOperatorIdentity("offline-op")
	caller.ID = user.UserID.String()

	res, err := disp.Invoke(deadlineCtx(t), caller, "chat.channel.create",
		chat.ChannelCreateArgs{Slug: "world", Kind: "system_all"})
	if err == nil {
		t.Fatal("expected handler error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "logged into") &&
		!strings.Contains(strings.ToLower(err.Error()), "online") {
		t.Fatalf("expected operator-online error, got %q", err.Error())
	}
	if len(res.PerTarget) != 1 || res.PerTarget[0].OK {
		t.Fatal("expected failure when operator is not online in chat")
	}
}

// --- Read-only commands work without the operator being online ---

func TestConsole_ReadOnly_NoOperatorOnlineRequired(t *testing.T) {
	// Variant of operator-online tests in later commits: list/info should
	// succeed even if the operator isn't online in chat. Validates the
	// read-only path doesn't go through resolveOperatorConn.
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, nil)

	user, err := authRepo.CreateUser(context.Background(), auth.User{
		Username: "operator",
	}, "h")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_ = authRepo.GrantCapability(context.Background(), auth.Capability{
		UserID: user.UserID, Capability: "chat.admin",
	})

	reg := cmdsys.NewRegistry()
	if err := chat.RegisterConsoleCommands(reg,
		func() *chat.Service { return svc.Service },
		func() auth.Repository { return authRepo },
	); err != nil {
		t.Fatalf("RegisterConsoleCommands: %v", err)
	}
	disp := cmdsys.NewDispatcher(cmdsys.DispatcherConfig{
		Registry: reg, Audit: cmdsys.NoopAuditSink{},
	})
	t.Cleanup(func() { _ = disp.Close() })

	caller := cmdsys.NewOperatorIdentity("offline-op")
	caller.ID = user.UserID.String()
	res, err := disp.Invoke(deadlineCtx(t), caller, "chat.channel.list", struct{}{})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if len(res.PerTarget) != 1 || !res.PerTarget[0].OK {
		t.Fatalf("expected list to succeed; result=%+v", res)
	}
}
