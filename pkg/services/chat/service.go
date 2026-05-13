package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/zenion/mmoserver/pkg/ops"
	"github.com/zenion/mmoserver/pkg/service"
	"github.com/zenion/mmoserver/pkg/services/auth"
)

const logCat = "services:chat"

// Service is the running chat service instance.
type Service struct {
	ctx      *service.Context
	opts     ServiceOpts
	repo     Repository
	authRepo auth.Repository
	authCap  *auth.CapabilityCache

	mu          sync.RWMutex
	channels    map[uuid.UUID]Channel
	bySlug      map[string]uuid.UUID
	membership  map[uuid.UUID]map[uuid.UUID]string // channelID → userID → role
	userChans   map[uuid.UUID]map[uuid.UUID]struct{}
	mutes       map[muteKey]Mute
	bans        map[banKey]banEntry               // {channelID,userID} → ban info; hydrated from chat.channel_members at Init
	online      map[uuid.UUID]uint32              // userID → connID
	connIndex   map[uint32]uuid.UUID              // connID → userID
	usernames   map[uuid.UUID]string              // userID → username (cached at session-enter)
	gatewayConn map[string]map[uint32]struct{}    // gatewayID → connIDs
	subs        map[uuid.UUID]map[uint32]struct{} // channelID → connIDs

	rateBuckets map[uuid.UUID]*tokenBucket
	slowMode    map[slowModeKey]time.Time

	msgIDIndex *msgIDTTLMap

	// sendEventFn is set by the mmokit facade to the gateway's per-conn
	// typed-event sender (e.g. process.SendTypedEvent). Lifted from the
	// gateway after Init via OnReady. nil in unit tests.
	sendEventFn func(connID uint32, event any)

	reapCh chan struct{}
	reapWG sync.WaitGroup
}

type muteKey struct{ UserID, ChannelID uuid.UUID }
type slowModeKey struct{ ChannelID, UserID uuid.UUID }

// banKey identifies a per-channel ban. Hydrated from chat.channel_members
// rows whose banned_until > NOW at Init. Helpers for ban-check land in
// Phase 9; the underlying state lives here so HandleJoin (Phase 6.2) can
// consult it.
type banKey struct{ ChannelID, UserID uuid.UUID }

type banEntry struct {
	BannedUntil time.Time
	BannedBy    uuid.UUID
	Reason      string
}

func newService(ctx *service.Context, opts ServiceOpts) service.Service {
	return &Service{ctx: ctx, opts: opts}
}

// Repository returns the live Repository for the mmokit facade's console wiring.
func (s *Service) Repository() Repository { return s.repo }

// ChannelIDBySlug returns the channel_id for a slug. Used by console handlers
// that need to translate operator-typed slugs into UUIDs before dispatching
// to Handle* methods. Reads under RLock — safe for concurrent use.
func (s *Service) ChannelIDBySlug(slug string) (uuid.UUID, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.bySlug[slug]
	return id, ok
}

// ChannelByID returns a snapshot of the in-memory Channel row for id, or
// the zero value + false when not found. Used by console handlers (e.g.
// chat.channel.info) for read-only inspection.
func (s *Service) ChannelByID(id uuid.UUID) (Channel, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.channels[id]
	return c, ok
}

// ListChannelsSnapshot returns a snapshot copy of all in-memory channels,
// in unspecified order. Used by chat.channel.list (cluster-wide list).
func (s *Service) ListChannelsSnapshot() []Channel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Channel, 0, len(s.channels))
	for _, c := range s.channels {
		out = append(out, c)
	}
	return out
}

// MemberCountForChannel returns the current explicit-member count for a
// channel (0 for SYSTEM_ALL — implicit membership). Used alongside
// ChannelByID to populate ChannelInfoResult for console handlers.
func (s *Service) MemberCountForChannel(id uuid.UUID) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.membership[id])
}

// OnlineConnIDForUser returns the connID for a user if they are currently
// online, plus a found bool. Used by console handlers to synthesize an
// OpContext targeting an operator who is also a logged-in player.
func (s *Service) OnlineConnIDForUser(userID uuid.UUID) (uint32, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	connID, ok := s.online[userID]
	return connID, ok
}

func (s *Service) Init(ctx *service.Context) error {
	s.ctx = ctx

	// Bus subscriptions for engine-driven session events. Replaces the
	// previous Process.chatHook plumbing — chat now listens for the same
	// transitions on the typed pub/sub bus owned by Process.
	//
	// Bus is always non-nil per service.Context contract.
	service.Subscribe(ctx.Bus, func(ev service.SessionEnterEvent) {
		if _, err := s.HandleSessionEnter(nil, &ChatSessionEnterRequest{
			ConnID:    ev.ConnID,
			UserID:    ev.UserID,
			Username:  ev.Username,
			GatewayID: ev.GatewayID,
		}); err != nil {
			s.ctx.Logger.Log(logCat, "SessionEnter handler failed: conn=%d user=%q gw=%q: %v",
				ev.ConnID, ev.UserID, ev.GatewayID, err)
		}
	})
	service.Subscribe(ctx.Bus, func(ev service.SessionLeaveEvent) {
		if _, err := s.HandleSessionLeave(nil, &ChatSessionLeaveRequest{
			ConnID:    ev.ConnID,
			GatewayID: ev.GatewayID,
		}); err != nil {
			s.ctx.Logger.Log(logCat, "SessionLeave handler failed: conn=%d gw=%q: %v",
				ev.ConnID, ev.GatewayID, err)
		}
	})

	if s.opts.Repository != nil {
		s.repo = s.opts.Repository
	} else {
		if ctx.DB == nil {
			return errors.New("chat.Init: DB required (RequiresDB=true should have caught this)")
		}
		if s.opts.RepositoryFactory == nil {
			return errors.New("chat.Init: RepositoryFactory must be set when no Repository is injected")
		}
		s.repo = s.opts.RepositoryFactory(ctx.DB.Pool())
	}

	// Auth repo for capability checks. Optional in tests; required in production.
	if s.opts.AuthRepoProvider != nil {
		s.authRepo = s.opts.AuthRepoProvider()
		if s.authRepo != nil {
			s.authCap = auth.NewCapabilityCache(s.authRepo, 30*time.Second)
		}
	}

	// Initialize in-memory maps
	s.channels = map[uuid.UUID]Channel{}
	s.bySlug = map[string]uuid.UUID{}
	s.membership = map[uuid.UUID]map[uuid.UUID]string{}
	s.userChans = map[uuid.UUID]map[uuid.UUID]struct{}{}
	s.mutes = map[muteKey]Mute{}
	s.bans = map[banKey]banEntry{}
	s.online = map[uuid.UUID]uint32{}
	s.connIndex = map[uint32]uuid.UUID{}
	s.usernames = map[uuid.UUID]string{}
	s.gatewayConn = map[string]map[uint32]struct{}{}
	s.subs = map[uuid.UUID]map[uint32]struct{}{}
	s.rateBuckets = map[uuid.UUID]*tokenBucket{}
	s.slowMode = map[slowModeKey]time.Time{}
	s.msgIDIndex = newMsgIDTTLMap(s.opts.MsgIDTTL)

	// 1. Bootstrap DefaultChannels via UPSERT (idempotent)
	if err := s.bootstrapDefaultChannels(context.Background()); err != nil {
		return fmt.Errorf("bootstrap default channels: %w", err)
	}

	// 2. Hydrate channels
	chans, err := s.repo.ListAllChannels(context.Background())
	if err != nil {
		return fmt.Errorf("hydrate channels: %w", err)
	}
	for _, c := range chans {
		s.channels[c.ChannelID] = c
		s.bySlug[c.Slug] = c.ChannelID
		s.subs[c.ChannelID] = map[uint32]struct{}{}
		if c.Kind != "system_all" {
			s.membership[c.ChannelID] = map[uuid.UUID]string{}
		}
	}

	// 3. Hydrate members (load all rows; ban check is per-op at runtime)
	mems, err := s.repo.ListAllMembers(context.Background())
	if err != nil {
		return fmt.Errorf("hydrate members: %w", err)
	}
	now := time.Now()
	for _, m := range mems {
		if s.membership[m.ChannelID] == nil {
			continue // SYSTEM_ALL doesn't track explicit members
		}
		s.membership[m.ChannelID][m.UserID] = m.Role
		if s.userChans[m.UserID] == nil {
			s.userChans[m.UserID] = map[uuid.UUID]struct{}{}
		}
		s.userChans[m.UserID][m.ChannelID] = struct{}{}
		// Active ban hydration: rows whose banned_until is in the future
		// snapshot into s.bans for fast HandleJoin gating. The reaper
		// clears expired bans both in DB and (Phase 9) in-memory.
		if !m.BannedUntil.IsZero() && m.BannedUntil.After(now) {
			s.bans[banKey{m.ChannelID, m.UserID}] = banEntry{
				BannedUntil: m.BannedUntil,
				BannedBy:    m.BannedBy,
				Reason:      m.BannedReason,
			}
		}
	}

	// 4. Hydrate mutes
	mutes, err := s.repo.ListActiveMutes(context.Background())
	if err != nil {
		return fmt.Errorf("hydrate mutes: %w", err)
	}
	for _, mu := range mutes {
		s.mutes[muteKey{mu.UserID, mu.ChannelID}] = mu
	}

	// 5. Start reaper goroutine
	s.reapCh = make(chan struct{})
	s.reapWG.Add(1)
	go s.reapLoop()

	// Server-event types are registered by mmokit.RegisterChatService
	// at facade time (before Build) — see pkg/mmokit/chat.go's
	// registerChatServerEvents helper. By the time Init runs, the
	// codec already knows every chat-emitted event type.

	if s.opts.OnReady != nil {
		s.opts.OnReady(s)
	}
	ctx.Logger.Log(logCat, "chat service initialized: instance=%s channels=%d members=%d mutes=%d",
		ctx.InstanceID, len(s.channels), len(mems), len(s.mutes))
	return nil
}

func (s *Service) bootstrapDefaultChannels(ctx context.Context) error {
	for _, def := range s.opts.DefaultChannels {
		kindStr := channelKindToString(def.Kind)
		if kindStr == "" {
			s.ctx.Logger.Log(logCat, "bootstrap: skipping invalid kind for slug=%s", def.Slug)
			continue
		}
		existing, err := s.repo.GetChannelBySlug(ctx, def.Slug)
		if err != nil && !errors.Is(err, ErrChannelNotFound) {
			return err
		}
		if err == nil && existing.Kind != kindStr {
			s.ctx.Logger.Log(logCat, "bootstrap: ERROR slug=%s kind transition %s→%s rejected; delete + recreate",
				def.Slug, existing.Kind, kindStr)
			continue
		}
		c := Channel{
			Slug:            def.Slug,
			Kind:            kindStr,
			Topic:           def.Topic,
			SlowModeSeconds: def.SlowModeSeconds,
		}
		if existing.ChannelID != uuid.Nil {
			c.ChannelID = existing.ChannelID
		}
		if _, err := s.repo.UpsertChannel(ctx, c); err != nil {
			return fmt.Errorf("upsert %s: %w", def.Slug, err)
		}
	}
	return nil
}

func channelKindToString(k ChannelKind) string {
	switch k {
	case ChannelKindSystemAll:
		return "system_all"
	case ChannelKindSystemPredicate:
		return "system_predicate"
	case ChannelKindCustom:
		return "custom"
	default:
		return ""
	}
}

func channelKindFromString(s string) ChannelKind {
	switch strings.ToLower(s) {
	case "system_all":
		return ChannelKindSystemAll
	case "system_predicate":
		return ChannelKindSystemPredicate
	case "custom":
		return ChannelKindCustom
	default:
		return ChannelKindUnspecified
	}
}

// RegisterOps is a no-op for the chat service. Chat handlers live on the
// typed-op channel; the mmokit facade registers them against the typed-op
// router rather than the per-kind opcode router. Returning nil here is
// intentional — Kind.OpCodes is also nil.
func (s *Service) RegisterOps(_ *ops.Router) error { return nil }

func (s *Service) Shutdown(ctx context.Context) error {
	if s.reapCh != nil {
		close(s.reapCh)
	}
	done := make(chan struct{})
	go func() { s.reapWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	if s.ctx != nil && s.ctx.Logger != nil {
		s.ctx.Logger.Log(logCat, "chat service shutdown: instance=%s", s.ctx.InstanceID)
	}
	return nil
}

func (s *Service) reapLoop() {
	defer s.reapWG.Done()
	t := time.NewTicker(s.opts.MuteReapInterval)
	defer t.Stop()
	for {
		select {
		case <-s.reapCh:
			return
		case <-t.C:
			s.reapOnce()
		}
	}
}

func (s *Service) reapOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if n, err := s.repo.DeleteExpiredMutes(ctx); err == nil && n > 0 {
		s.ctx.Logger.Log(logCat, "reaper: removed %d expired mutes", n)
	}
	if n, err := s.repo.ClearExpiredBans(ctx); err == nil && n > 0 {
		s.ctx.Logger.Log(logCat, "reaper: cleared %d expired bans", n)
	}
	if n := s.msgIDIndex.Sweep(time.Now()); n > 0 {
		s.ctx.Logger.Log(logCat, "reaper: evicted %d expired msg_id index entries", n)
	}
	// In-memory mute eviction
	s.mu.Lock()
	now := time.Now()
	for k, v := range s.mutes {
		if v.ExpiresAt.Before(now) {
			delete(s.mutes, k)
		}
	}
	s.mu.Unlock()
}

// SetSendEventFn wires the per-conn typed-event sender. Called by
// mmokit.RegisterChatService from OnReady.
func (s *Service) SetSendEventFn(fn func(connID uint32, event any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sendEventFn = fn
}

var _ service.Service = (*Service)(nil)
