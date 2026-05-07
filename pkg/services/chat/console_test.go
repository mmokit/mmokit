package chat_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/services/auth"
	"github.com/zenion/mmoserver/pkg/services/auth/authtest"
	"github.com/zenion/mmoserver/pkg/services/chat"
	"github.com/zenion/mmoserver/pkg/services/chat/chattest"
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
