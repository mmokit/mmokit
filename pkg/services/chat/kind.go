package chat

import (
	"embed"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zenion/mmoserver/pkg/service"
	"github.com/zenion/mmoserver/pkg/services/auth"
)

const KindName = "chat"

//go:embed postgres/migrations/*.sql
var pgMigrations embed.FS

func MigrationsFS() fs.FS {
	sub, err := fs.Sub(pgMigrations, "postgres/migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

// DefaultChannelDef declares one channel that the chat service should
// UPSERT at startup. Idempotent: existing rows with the same slug are
// reconciled to the declared values (kind transition is rejected as
// an error logged at warn level).
type DefaultChannelDef struct {
	Slug            string
	Kind            ChannelKind // SYSTEM_ALL or SYSTEM_PREDICATE
	Topic           string
	SlowModeSeconds int
}

// ServiceOpts configures the chat service.
type ServiceOpts struct {
	// Repository is an injected Repository (e.g. an in-memory mock).
	// When non-nil, RepositoryFactory is ignored and RequiresDB is false.
	Repository Repository

	// RepositoryFactory builds the live Repository from a pgx pool.
	// Set by mmokit.RegisterChatService to chat/postgres.New so
	// pkg/services/chat doesn't import pkg/services/chat/postgres.
	RepositoryFactory func(*pgxpool.Pool) Repository

	// AuthRepoProvider returns the live auth.Repository (used for
	// HasCapability calls + UserIDByUsername lookups). Wired by the
	// mmokit facade after auth.OnReady fires.
	AuthRepoProvider func() auth.Repository

	// OnReady fires from Service.Init after the live Repository has
	// resolved and bootstrap has finished. Used by mmokit.RegisterChatService
	// to wire the gateway hook + capture the live *Service for console
	// command handlers.
	OnReady func(svc *Service)

	// Capacity / policy knobs
	UserRateMax                int
	UserRateWindow             time.Duration
	DefaultSlowMode            time.Duration
	MaxMessageLen              int
	MaxTopicLen                int
	MaxChannelSlugLen          int
	MaxChannelsPerUser         int
	MaxMembersPerCustomChannel int
	MinChannelPasswordLen      int
	MuteReapInterval           time.Duration
	MsgIDTTL                   time.Duration
	ReservedSlugPrefixes       []string

	// Channels created at startup; idempotent UPSERT.
	DefaultChannels []DefaultChannelDef
}

func DefaultServiceOpts() ServiceOpts {
	return ServiceOpts{
		UserRateMax:                5,
		UserRateWindow:             5 * time.Second,
		DefaultSlowMode:            0,
		MaxMessageLen:              500,
		MaxTopicLen:                200,
		MaxChannelSlugLen:          32,
		MaxChannelsPerUser:         5,
		MaxMembersPerCustomChannel: 1000,
		MinChannelPasswordLen:      4,
		MuteReapInterval:           time.Minute,
		MsgIDTTL:                   5 * time.Minute,
		ReservedSlugPrefixes:       []string{"guild:", "party:", "alliance:", "raid:", "system:"},
	}
}

// Kind returns the service.Kind descriptor for the chat service.
//
// OpCodes is nil — chat ops live on the typed-op channel
// (mmokit.RegisterOp[Req, Res]) and route by request typeID, not opcode.
func Kind(opts ServiceOpts) service.Kind {
	return service.Kind{
		Name:        KindName,
		OpCodes:     nil,
		Factory:     func(ctx *service.Context) service.Service { return newService(ctx, opts) },
		RequiresDB:  opts.Repository == nil,
		Description: "engine-tier chat service: pure-pubsub messaging, persisted channels/members/mutes, capability-gated moderation",
	}
}
