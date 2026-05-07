package chat

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/service"
	"github.com/zenion/mmoserver/pkg/services/auth"
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

// NewTestServiceWithAuth builds a chat Service with both an in-memory
// chat repository and an in-memory auth repository wired in. Used by
// authorization-aware tests that need to grant capabilities or check
// the canModerate path. Tests construct chattest.NewMock() and
// authtest.NewMock() in their _test package and pass them in to avoid
// an import cycle.
func NewTestServiceWithAuth(t *testing.T, repo Repository, authRepo auth.Repository, defaults []DefaultChannelDef) *TestService {
	t.Helper()
	opts := DefaultServiceOpts()
	opts.Repository = repo
	opts.AuthRepoProvider = func() auth.Repository { return authRepo }
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

// MustAddMember directly inserts a (channel, user, role) into the
// in-memory membership map + repo. Panics on error. Used by tests that
// don't go through HandleJoin (e.g. authorization tests that need a
// pre-existing admin role on a channel).
func (t *TestService) MustAddMember(channelID, userID uuid.UUID, role string) {
	if err := t.repo.AddOrUpdateMember(context.Background(), ChannelMember{
		ChannelID: channelID,
		UserID:    userID,
		Role:      role,
	}); err != nil {
		panic(err)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.membership[channelID] == nil {
		t.membership[channelID] = map[uuid.UUID]string{}
	}
	t.membership[channelID][userID] = role
	if t.userChans[userID] == nil {
		t.userChans[userID] = map[uuid.UUID]struct{}{}
	}
	t.userChans[userID][channelID] = struct{}{}
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

// MustSubscribe injects a connID into subs[channelID]. Used by tests
// that need an existing member's conn to receive fanout but didn't go
// through HandleSessionEnter.
func (t *TestService) MustSubscribe(channelID uuid.UUID, connID uint32) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.subs[channelID] == nil {
		t.subs[channelID] = map[uint32]struct{}{}
	}
	t.subs[channelID][connID] = struct{}{}
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

// MustBanUser pre-populates the in-memory bans map for tests. Used by
// HandleJoin tests that exercise the ban-check path without going
// through the (Phase 9) HandleBan op.
func (t *TestService) MustBanUser(channelID, userID uuid.UUID, until time.Time, reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.bans[banKey{channelID, userID}] = banEntry{
		BannedUntil: until,
		Reason:      reason,
	}
}

// RateLimitTake invokes the private rateLimitTake helper for tests.
func (t *TestService) RateLimitTake(userID uuid.UUID) (bool, time.Duration) {
	return t.rateLimitTake(userID)
}

// SlowModeCheck invokes the private slowModeCheck helper for tests.
func (t *TestService) SlowModeCheck(channelID, userID uuid.UUID) (bool, time.Duration) {
	return t.slowModeCheck(channelID, userID)
}

// MuteCheck invokes the private muteCheck helper for tests.
func (t *TestService) MuteCheck(userID, channelID uuid.UUID) (bool, time.Duration) {
	return t.muteCheck(userID, channelID)
}

// BanCheck invokes the private banCheck helper for tests.
func (t *TestService) BanCheck(userID, channelID uuid.UUID) (bool, time.Duration) {
	return t.banCheck(userID, channelID)
}

// SetChannelSlowMode mutates the in-memory Channel.SlowModeSeconds for
// tests that need to exercise slow-mode gating without going through
// the SetSlowMode op.
func (t *TestService) SetChannelSlowMode(channelID uuid.UUID, seconds int32) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.channels[channelID]
	if !ok {
		panic("test: unknown channel")
	}
	c.SlowModeSeconds = int(seconds)
	t.channels[channelID] = c
}

// CreateChannelDirect inserts a channel directly into the in-memory
// state (and the repo) without going through HandleCreate. Returns the
// allocated channelID.
func (t *TestService) CreateChannelDirect(slug string, kind ChannelKind) (uuid.UUID, error) {
	c := Channel{Slug: slug, Kind: channelKindToString(kind)}
	stored, err := t.repo.UpsertChannel(context.Background(), c)
	if err != nil {
		return uuid.Nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.channels[stored.ChannelID] = stored
	t.bySlug[stored.Slug] = stored.ChannelID
	if t.subs[stored.ChannelID] == nil {
		t.subs[stored.ChannelID] = map[uint32]struct{}{}
	}
	if stored.Kind != "system_all" && t.membership[stored.ChannelID] == nil {
		t.membership[stored.ChannelID] = map[uuid.UUID]string{}
	}
	return stored.ChannelID, nil
}

// MustMute pre-populates the in-memory mutes map for tests. Pass
// MuteGlobalChannelID for a global mute.
func (t *TestService) MustMute(userID, channelID uuid.UUID, until time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.mutes[muteKey{userID, channelID}] = Mute{
		UserID:    userID,
		ChannelID: channelID,
		ExpiresAt: until,
	}
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
