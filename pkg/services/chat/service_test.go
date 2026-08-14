package chat_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/zenion/mmokit/pkg/logger"
	"github.com/zenion/mmokit/pkg/service"
	"github.com/zenion/mmokit/pkg/services/chat"
	"github.com/zenion/mmokit/pkg/services/chat/chattest"
)

func newTestCtx() *service.Context {
	return &service.Context{
		KindName:   "chat",
		InstanceID: "test",
		Logger:     logger.New(),
		Roles:      map[string]struct{}{"service": {}},
		Bus:        service.NewBus("test"),
	}
}

func TestService_InitWithMockRepo(t *testing.T) {
	opts := chat.DefaultServiceOpts()
	opts.Repository = chattest.NewMock()
	svc := chat.Kind(opts).Factory(newTestCtx())
	if err := svc.Init(newTestCtx()); err != nil {
		t.Fatal(err)
	}
	if err := svc.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestService_InitBootstrapsDefaultChannels(t *testing.T) {
	repo := chattest.NewMock()
	opts := chat.DefaultServiceOpts()
	opts.Repository = repo
	opts.DefaultChannels = []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll, Topic: "World chat"},
		{Slug: "help", Kind: chat.ChannelKindSystemAll, Topic: "Help chat"},
	}
	svc := chat.Kind(opts).Factory(newTestCtx())
	if err := svc.Init(newTestCtx()); err != nil {
		t.Fatal(err)
	}
	defer svc.Shutdown(context.Background())

	all, _ := repo.ListAllChannels(context.Background())
	if len(all) != 2 {
		t.Fatalf("got %d channels, want 2 from DefaultChannels", len(all))
	}
}

// TestService_BusSubscriptionDrivesPresence verifies that chat subscribes
// to service.SessionEnter/Leave events at Init and that publishing those
// events through the bus drives the same presence transitions as the
// older Process.chatHook path. Sets up the new consumer ahead of the
// gateway publisher swap (Task 7).
func TestService_BusSubscriptionDrivesPresence(t *testing.T) {
	bus := service.NewBus("test-proc")
	repo := chattest.NewMock()
	svc := chat.NewTestServiceWithBus(t, repo, nil, bus)

	uid := uuid.NewString()
	service.Publish(bus, service.SessionEnterEvent{
		ConnID:    42,
		UserID:    uid,
		Username:  "alice",
		GatewayID: "gw-1",
	})
	if _, online := svc.OnlineConnIDForUser(uuid.MustParse(uid)); !online {
		t.Fatal("alice not online after SessionEnterEvent")
	}
	service.Publish(bus, service.SessionLeaveEvent{
		ConnID:    42,
		GatewayID: "gw-1",
	})
	if _, online := svc.OnlineConnIDForUser(uuid.MustParse(uid)); online {
		t.Fatal("alice still online after SessionLeaveEvent")
	}
}
