package chat_test

import (
	"context"
	"testing"

	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/service"
	"github.com/zenion/mmoserver/pkg/services/chat"
	"github.com/zenion/mmoserver/pkg/services/chat/chattest"
)

func newTestCtx() *service.Context {
	return &service.Context{
		KindName:   "chat",
		InstanceID: "test",
		Logger:     logger.New(),
		Roles:      map[string]struct{}{"service": {}},
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
