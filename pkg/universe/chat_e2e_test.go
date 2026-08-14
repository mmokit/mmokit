package universe_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/mmokit/mmokit/pkg/mmokit"
	"github.com/mmokit/mmokit/pkg/service"
	"github.com/mmokit/mmokit/pkg/services/auth/authtest"
	"github.com/mmokit/mmokit/pkg/services/chat"
	"github.com/mmokit/mmokit/pkg/services/chat/chattest"
)

// TestChat_RegisterChatService_RegistersTypedEvents verifies that
// RegisterChatServiceWithMock populates the typed-event codec registry —
// the side effect that confirms the codec-side wiring is in place. Pick
// one representative event; if any are missing the bulk RegisterEvent in
// the facade short-circuited.
//
// The Process.chatHook side-effect that the prior test asserted was
// removed in Phase 1 of the services-event-bus refactor — chat now
// listens on Process.bus instead. Bus injection is covered by
// TestProcess_BusPresentInServiceContext in coordinator_test.go;
// bus-driven presence is covered by TestChat_BusDrivesPresence below.
func TestChat_RegisterChatService_RegistersTypedEvents(t *testing.T) {
	p := mmokit.New(mmokit.Config{Mode: "all", HTTPPort: -1})

	repo := chattest.NewMock()
	if err := mmokit.RegisterChatServiceWithMock(p, repo, nil); err != nil {
		t.Fatalf("RegisterChatServiceWithMock: %v", err)
	}

	id := mmokit.TypeIDOf(reflect.TypeOf(chat.ChatMessageEvent{}))
	if got, ok := mmokit.LookupServerEventType(id); !ok || got == nil {
		t.Errorf("chat ChatMessageEvent not registered after RegisterChatServiceWithMock")
	}
}

// TestChat_ServiceLifecycleEndToEnd verifies that registering BOTH the
// auth and chat services through the mmokit facade against in-memory
// mocks completes without error and the chat-emitted typed-event
// registry is populated. Equivalent to the wiring step on a fresh
// dev-server start (RegisterAuthService then RegisterChatService),
// short of running startServices — the cluster fixture for that path
// is t.Skip'd until the gateway test harness lands; see
// auth_cookie_e2e_test.go's startTestProcess.
func TestChat_ServiceLifecycleEndToEnd(t *testing.T) {
	p := mmokit.New(mmokit.Config{Mode: "all", HTTPPort: -1})

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

// TestChat_BusDrivesPresence verifies the bus-driven session-presence
// contract: publishing service.SessionEnterEvent / SessionLeaveEvent on
// the per-process bus drives the same presence transitions on the live
// *chat.Service that the legacy Process.chatHook path used to. This is
// the contract Task 6 wired (chat subscribes at Init) and Task 7 wired
// (gateway publishes on auth-success / disconnect).
//
// The test drives chat.NewTestServiceWithBus directly because the
// Process-level startServices path (which would Init the service against
// Process.bus) is gated on the cluster fixture that's still pending. The
// shape is identical to production: chat.Service.Init subscribes to the
// bus passed in via service.Context, and Publish is a synchronous
// fan-out.
func TestChat_BusDrivesPresence(t *testing.T) {
	bus := service.NewBus("test-proc")
	chatRepo := chattest.NewMock()

	// Bootstrap the chat service against the supplied bus. Init runs
	// synchronously and registers Subscribers for SessionEnterEvent /
	// SessionLeaveEvent before this returns.
	svc := chat.NewTestServiceWithBus(t, chatRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll, Topic: "World"},
	}, bus)

	// Drive a login transition by publishing on the bus — the same shape
	// the gateway's onAuthSuccess uses in production.
	uid := uuid.New().String()
	service.Publish(bus, service.SessionEnterEvent{
		ConnID:    42,
		UserID:    uid,
		Username:  "alice",
		GatewayID: "gw-1",
	})

	if got := svc.OnlineCount(); got != 1 {
		t.Fatalf("OnlineCount=%d after SessionEnterEvent, want 1", got)
	}

	// Drive a logout — confirm it tears down presence.
	service.Publish(bus, service.SessionLeaveEvent{
		ConnID:    42,
		UserID:    uid,
		GatewayID: "gw-1",
	})

	if got := svc.OnlineCount(); got != 0 {
		t.Fatalf("OnlineCount=%d after SessionLeaveEvent, want 0", got)
	}
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
