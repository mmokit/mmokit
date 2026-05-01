package auth

import (
	"embed"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/service"
)

// ServiceOpts is the configuration handed to RegisterAuthService.
type ServiceOpts struct {
	// Repository is an injected Repository implementation (e.g. an in-memory
	// mock for tests). When non-nil, RepositoryFactory is ignored and
	// RequiresDB is false.
	Repository Repository

	// RepositoryFactory constructs the live Repository from a pgxpool.Pool.
	// Set by the caller (mmokit.RegisterAuthService) to postgres.New so that
	// pkg/auth does not need to import pkg/auth/postgres (which imports
	// pkg/auth — a cycle). Ignored when Repository is non-nil.
	RepositoryFactory func(*pgxpool.Pool) Repository

	SessionTTL     time.Duration
	PasswordMinLen int
	Argon2id       ArgonParams

	IPRateLimitMax    int
	IPRateLimitWindow time.Duration
	IPLockoutDuration time.Duration

	LockoutThreshold int
	LockoutDuration  time.Duration

	AuditRetention time.Duration
	ReapInterval   time.Duration

	TrustedProxyHeader bool
}

func DefaultServiceOpts() ServiceOpts {
	return ServiceOpts{
		SessionTTL:        30 * 24 * time.Hour,
		PasswordMinLen:    8,
		Argon2id:          DefaultArgonParams(),
		IPRateLimitMax:    10,
		IPRateLimitWindow: 60 * time.Second,
		IPLockoutDuration: 5 * time.Minute,
		LockoutThreshold:  5,
		LockoutDuration:   15 * time.Minute,
		AuditRetention:    90 * 24 * time.Hour,
		ReapInterval:      time.Hour,
	}
}

// KindName is the engine-reserved name. RegisterAuthService rejects
// games attempting to register a kind named "auth" for other purposes.
const KindName = "auth"

//go:embed postgres/migrations/*.sql
var pgMigrations embed.FS

// MigrationsFS returns the auth-package's Postgres migrations as an
// fs.FS suitable for Config.ExtraMigrations.
func MigrationsFS() fs.FS {
	sub, err := fs.Sub(pgMigrations, "postgres/migrations")
	if err != nil {
		panic(err) // build-time assertion
	}
	return sub
}

// Kind returns the service.Kind descriptor for the auth service. Public
// wrapper around kindFor — used by mmokit.RegisterAuthService.
func Kind(opts ServiceOpts) service.Kind { return kindFor(opts) }

// kindFor returns the service.Kind registration descriptor.
func kindFor(opts ServiceOpts) service.Kind {
	return service.Kind{
		Name: KindName,
		OpCodes: []uint32{
			uint32(enginepb.AuthOpCode_AUTH_OPCODE_LOGIN),
			uint32(enginepb.AuthOpCode_AUTH_OPCODE_REGISTER),
			uint32(enginepb.AuthOpCode_AUTH_OPCODE_VALIDATE_TOKEN),
			uint32(enginepb.AuthOpCode_AUTH_OPCODE_LOGOUT),
			uint32(enginepb.AuthOpCode_AUTH_OPCODE_CHANGE_PASSWORD),
		},
		Factory:     func(ctx *service.Context) service.Service { return newService(ctx, opts) },
		RequiresDB:  opts.Repository == nil, // injected mock skips DB requirement
		Description: "engine-tier identity service: argon2id passwords, opaque sliding-TTL session tokens, OIDC schema seam",
	}
}
