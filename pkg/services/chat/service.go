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
	online      map[uuid.UUID]uint32              // userID → connID
	connIndex   map[uint32]uuid.UUID              // connID → userID
	gatewayConn map[string]map[uint32]struct{}    // gatewayID → connIDs
	subs        map[uuid.UUID]map[uint32]struct{} // channelID → connIDs

	rateBuckets map[uuid.UUID]*tokenBucket
	slowMode    map[slowModeKey]time.Time

	msgIDIndex *msgIDTTLMap

	reapCh chan struct{}
	reapWG sync.WaitGroup
}

type muteKey struct{ UserID, ChannelID uuid.UUID }
type slowModeKey struct{ ChannelID, UserID uuid.UUID }

func newService(ctx *service.Context, opts ServiceOpts) service.Service {
	return &Service{ctx: ctx, opts: opts}
}

// Repository returns the live Repository for the mmokit facade's console wiring.
func (s *Service) Repository() Repository { return s.repo }

func (s *Service) Init(ctx *service.Context) error {
	s.ctx = ctx
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
	s.online = map[uuid.UUID]uint32{}
	s.connIndex = map[uint32]uuid.UUID{}
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
	for _, m := range mems {
		if s.membership[m.ChannelID] == nil {
			continue // SYSTEM_ALL doesn't track explicit members
		}
		s.membership[m.ChannelID][m.UserID] = m.Role
		if s.userChans[m.UserID] == nil {
			s.userChans[m.UserID] = map[uuid.UUID]struct{}{}
		}
		s.userChans[m.UserID][m.ChannelID] = struct{}{}
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

	// 6. Register typed server-event types so the codec knows them
	RegisterChatServerEvents()

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

var _ service.Service = (*Service)(nil)
