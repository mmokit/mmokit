package chat

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/service"
)

// TestService wraps *Service with helpers for unit tests. Not part of
// the production public API; available because tests in chat_test live
// in a different package and need exported entry points.
type TestService struct{ *Service }

// NewTestService builds a chat Service against the supplied (in-memory)
// repository + the supplied default channels. Caller does not need to
// call Shutdown — t.Cleanup is registered automatically.
//
// The repo arg is injected to avoid an import cycle (chat → chattest →
// chat). Tests typically pass `chattest.NewMock()`.
func NewTestService(t *testing.T, repo Repository, defaults []DefaultChannelDef) *TestService {
	t.Helper()
	opts := DefaultServiceOpts()
	opts.Repository = repo
	opts.DefaultChannels = defaults
	ctx := &service.Context{
		KindName:   "chat",
		InstanceID: "test",
		Logger:     logger.New(),
		Roles:      map[string]struct{}{"service": {}},
	}
	svc := &Service{ctx: ctx, opts: opts}
	if err := svc.Init(ctx); err != nil {
		t.Fatalf("svc.Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
	return &TestService{Service: svc}
}

// MustChannelID resolves a slug or panics.
func (t *TestService) MustChannelID(slug string) uuid.UUID {
	t.mu.RLock()
	defer t.mu.RUnlock()
	id, ok := t.bySlug[slug]
	if !ok {
		panic("test: unknown slug " + slug)
	}
	return id
}

// MustOnlineFakeUser inserts a fake user into the presence + subs maps.
// Used by tests that don't go through ChatSessionEnter.
func (t *TestService) MustOnlineFakeUser(connID uint32, username string) uuid.UUID {
	uid := uuid.New()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.online[uid] = connID
	t.connIndex[connID] = uid
	t.usernames[uid] = username
	// SYSTEM_ALL channels: presence in `online[]` is sufficient; explicit
	// subs[] entries are only used for non-SYSTEM_ALL channels.
	return uid
}

// OnlineCount returns the number of currently-online users (size of
// the online map).
func (t *TestService) OnlineCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.online)
}

// FanoutTargets returns the connIDs that would receive a fanout to channelID.
func (t *TestService) FanoutTargets(channelID uuid.UUID) []uint32 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	c, ok := t.channels[channelID]
	if !ok {
		return nil
	}
	if c.Kind == "system_all" {
		out := make([]uint32, 0, len(t.online))
		for _, c := range t.online {
			out = append(out, c)
		}
		return out
	}
	subs := t.subs[channelID]
	out := make([]uint32, 0, len(subs))
	for c := range subs {
		out = append(out, c)
	}
	return out
}
