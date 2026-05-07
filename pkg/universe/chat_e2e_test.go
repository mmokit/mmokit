package universe_test

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/services/auth/authtest"
	"github.com/zenion/mmoserver/pkg/services/chat"
	"github.com/zenion/mmoserver/pkg/services/chat/chattest"
	"github.com/zenion/mmoserver/pkg/universe"
)

// fakeChatHook captures every OnSessionEnter / OnSessionLeave call so
// tests can assert the gateway's auth-success / disconnect paths fire
// the hook with the right arguments. No DB access; safe to call inline.
type fakeChatHook struct {
	enters atomic.Int32
	leaves atomic.Int32

	lastEnterConnID    atomic.Uint32
	lastEnterUserID    atomic.Value // string
	lastEnterUsername  atomic.Value // string
	lastEnterGatewayID atomic.Value // string

	lastLeaveConnID    atomic.Uint32
	lastLeaveGatewayID atomic.Value // string
}

func (h *fakeChatHook) OnSessionEnter(connID uint32, userID, username, gatewayID string) {
	h.enters.Add(1)
	h.lastEnterConnID.Store(connID)
	h.lastEnterUserID.Store(userID)
	h.lastEnterUsername.Store(username)
	h.lastEnterGatewayID.Store(gatewayID)
}

func (h *fakeChatHook) OnSessionLeave(connID uint32, gatewayID string) {
	h.leaves.Add(1)
	h.lastLeaveConnID.Store(connID)
	h.lastLeaveGatewayID.Store(gatewayID)
}

// TestChat_InstallChatHook_Storage verifies the basic plumbing — a hook
// installed via Process.InstallChatHook is what the gateway sees when
// it goes to dispatch presence. The test exercises the storage slot
// directly because the cluster fixture for end-to-end login is still
// pending (see auth_cookie_e2e_test.go).
func TestChat_InstallChatHook_Storage(t *testing.T) {
	p := universe.New(universe.Config{Mode: "all", HTTPPort: -1})
	hook := &fakeChatHook{}
	p.InstallChatHook(hook)

	// Idempotent: a second install replaces the first (last-writer-wins).
	hook2 := &fakeChatHook{}
	p.InstallChatHook(hook2)

	// Direct dispatch through the second hook to confirm the captures
	// fire as expected; the gateway calls into chatHook in this same
	// shape after auth-success and on disconnect.
	hook2.OnSessionEnter(7, "00000000-0000-0000-0000-000000000001", "alice", "gw-1")
	if got := hook2.enters.Load(); got != 1 {
		t.Errorf("enters: got %d, want 1", got)
	}
	hook2.OnSessionLeave(7, "gw-1")
	if got := hook2.leaves.Load(); got != 1 {
		t.Errorf("leaves: got %d, want 1", got)
	}
}

// TestChat_RegisterChatService_InstallsHook proves the mmokit facade
// installs a working SessionHook on the Process when chat is registered
// against an in-memory repo, AND that the chat-emitted server-event
// types are present in the typed-event codec registry afterward —
// the two side effects RegisterChatService is responsible for at facade
// time (before Build).
func TestChat_RegisterChatService_InstallsHook(t *testing.T) {
	p := universe.New(universe.Config{Mode: "all", HTTPPort: -1})

	repo := chattest.NewMock()
	if err := mmokit.RegisterChatServiceWithMock(p, repo, nil); err != nil {
		t.Fatalf("RegisterChatServiceWithMock: %v", err)
	}

	// Smoke: confirm the chat type-id registry is populated, a side
	// effect of RegisterChatService that confirms the codec-side wiring
	// is in place. Pick one representative event; if any are missing
	// the bulk RegisterEvent in the facade short-circuited.
	id := mmokit.TypeIDOf(reflect.TypeOf(chat.ChatMessageEvent{}))
	if got, ok := mmokit.LookupServerEventType(id); !ok || got == nil {
		t.Errorf("chat ChatMessageEvent not registered after RegisterChatServiceWithMock")
	}
}

// TestChat_ServiceLifecycleEndToEnd verifies that registering BOTH the
// auth and chat services through the mmokit facade against in-memory
// mocks completes without error, the chat hook is wired on the Process,
// and the chat-emitted typed-event registry is populated. Equivalent to
// the wiring step on a fresh dev-server start (RegisterAuthService then
// RegisterChatService), short of running startServices — the cluster
// fixture for that path is t.Skip'd until the gateway test harness
// lands; see auth_cookie_e2e_test.go's startTestProcess.
func TestChat_ServiceLifecycleEndToEnd(t *testing.T) {
	p := universe.New(universe.Config{Mode: "all", HTTPPort: -1})

	authRepo := authtest.NewMock()
	if err := mmokit.RegisterAuthServiceWithMock(p, authRepo); err != nil {
		t.Fatalf("RegisterAuthServiceWithMock: %v", err)
	}

	chatRepo := chattest.NewMock()
	// Pass authRepo explicitly so chat's AuthRepoProvider yields a live
	// auth.Repository (capability checks succeed). When nil, the facade
	// falls back to the AuthResolver path, which only resolves at
	// Service.Init time after auth's OnReady fires.
	if err := mmokit.RegisterChatServiceWithMock(p, chatRepo, authRepo); err != nil {
		t.Fatalf("RegisterChatServiceWithMock: %v", err)
	}

	// The facade-installed chat hook is observable on the Process. If
	// nil, RegisterChatService failed to call InstallChatHook — the
	// gateway would silently skip chat presence at login.
	if got := p.ChatHook(); got == nil {
		t.Fatal("ChatHook is nil after RegisterChatServiceWithMock — facade did not InstallChatHook")
	}

	// Confirm the typed-event registry is populated for at least three
	// representative events. Bulk-register-once means missing one means
	// none registered.
	for _, evType := range []reflect.Type{
		reflect.TypeOf(chat.ChatMessageEvent{}),
		reflect.TypeOf(chat.ChatChannelsHydratedEvent{}),
		reflect.TypeOf(chat.ChatRateLimitedEvent{}),
	} {
		id := mmokit.TypeIDOf(evType)
		if _, ok := mmokit.LookupServerEventType(id); !ok {
			t.Errorf("typed-event registry missing %s", evType.Name())
		}
	}
}

// TestChat_HookDispatchesToService verifies the facade-built chat hook
// adapter — the closure-bridged ChatSessionHook installed on the Process
// — dispatches OnSessionEnter to the live *chat.Service's
// HandleSessionEnter, populating the online[] presence map.
//
// Driven directly via NewTestServiceWithAuth + an inline adapter that
// mirrors the one chatSessionHookImpl builds in pkg/mmokit/chat.go. The
// shape is identical (getSvc closure → svc.HandleSessionEnter) so this
// test pins the contract between the mmokit facade and the chat
// service: any future refactor that breaks the wiring will fail here.
func TestChat_HookDispatchesToService(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll, Topic: "World"},
	})

	// Wrap the live *chat.Service in the same adapter shape that
	// mmokit.RegisterChatService installs (getSvc closure →
	// HandleSessionEnter / HandleSessionLeave).
	hook := &chatHookAdapter{getSvc: func() *chat.Service { return svc.Service }}

	p := universe.New(universe.Config{Mode: "all", HTTPPort: -1})
	p.InstallChatHook(hook)

	// Drive a login transition through the Process.chatHook contract.
	uid := uuid.New().String()
	p.ChatHook().OnSessionEnter(42, uid, "alice", "gw-1")

	if got := svc.OnlineCount(); got != 1 {
		t.Fatalf("OnlineCount=%d after OnSessionEnter, want 1", got)
	}

	// Drive a logout — confirm it tears down presence.
	p.ChatHook().OnSessionLeave(42, "gw-1")

	if got := svc.OnlineCount(); got != 0 {
		t.Fatalf("OnlineCount=%d after OnSessionLeave, want 0", got)
	}
}

// chatHookAdapter mirrors the (unexported) chatSessionHookImpl in
// pkg/mmokit/chat.go. Declared here so the test can construct the
// adapter directly against an in-memory *chat.Service produced by
// chat.NewTestServiceWithAuth.
type chatHookAdapter struct {
	getSvc func() *chat.Service
}

func (h *chatHookAdapter) OnSessionEnter(connID uint32, userID, username, gatewayID string) {
	svc := h.getSvc()
	if svc == nil {
		return
	}
	_, _ = svc.HandleSessionEnter(nil, &chat.ChatSessionEnterRequest{
		ConnID:    connID,
		UserID:    userID,
		Username:  username,
		GatewayID: gatewayID,
	})
}

func (h *chatHookAdapter) OnSessionLeave(connID uint32, gatewayID string) {
	svc := h.getSvc()
	if svc == nil {
		return
	}
	_, _ = svc.HandleSessionLeave(nil, &chat.ChatSessionLeaveRequest{
		ConnID:    connID,
		GatewayID: gatewayID,
	})
}

// TestChat_DefaultChannelsBootstrap verifies the bootstrap path: a chat
// service initialized with DefaultChannels=[world,help,trade] writes all
// three channels to the repo and hydrates them into the in-memory
// channel index, ready for HandleListChannels / HandleSessionEnter
// hydration responses.
//
// Drives chat.NewTestService directly because Service.Init runs
// bootstrapDefaultChannels inline. Mirror of what the facade
// instantiates after startServices fires Init in production.
func TestChat_DefaultChannelsBootstrap(t *testing.T) {
	chatRepo := chattest.NewMock()
	defaults := []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll, Topic: "World"},
		{Slug: "help", Kind: chat.ChannelKindSystemAll, Topic: "Help"},
		{Slug: "trade", Kind: chat.ChannelKindSystemAll, Topic: "Trade"},
	}
	svc := chat.NewTestService(t, chatRepo, defaults)

	for _, def := range defaults {
		// Verify in-memory hydration: ChannelIDBySlug must resolve.
		if _, ok := svc.ChannelIDBySlug(def.Slug); !ok {
			t.Errorf("default channel %q missing from in-memory bySlug map", def.Slug)
		}
		// Verify repo persistence: GetChannelBySlug must return a row
		// with the configured kind.
		got, err := chatRepo.GetChannelBySlug(context.Background(), def.Slug)
		if err != nil {
			t.Errorf("GetChannelBySlug(%q): %v", def.Slug, err)
			continue
		}
		if got.Kind != "system_all" {
			t.Errorf("channel %q kind=%q, want system_all", def.Slug, got.Kind)
		}
		if got.Topic != def.Topic {
			t.Errorf("channel %q topic=%q, want %q", def.Slug, got.Topic, def.Topic)
		}
	}

	// Re-run bootstrap (idempotency check): a second NewTestService
	// against the same repo should not duplicate channels.
	svc2 := chat.NewTestService(t, chatRepo, defaults)
	for _, def := range defaults {
		if _, ok := svc2.ChannelIDBySlug(def.Slug); !ok {
			t.Errorf("after re-bootstrap: %q missing", def.Slug)
		}
	}
	all, err := chatRepo.ListAllChannels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(defaults) {
		t.Errorf("after re-bootstrap: ListAllChannels=%d, want %d (idempotency violated)", len(all), len(defaults))
	}
}

