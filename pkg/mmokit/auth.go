package mmokit

import (
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zenion/mmoserver/pkg/auth"
	authpg "github.com/zenion/mmoserver/pkg/auth/postgres"
	"github.com/zenion/mmoserver/pkg/universe"
)

// AuthOpts is mmokit's facade type for auth.ServiceOpts.
type AuthOpts = auth.ServiceOpts

// AuthRepository is mmokit's facade type for auth.Repository (used by tests
// injecting an in-memory mock).
type AuthRepository = auth.Repository

// DefaultAuthOpts returns sane defaults: 30d sliding session TTL, OWASP-2024
// argon2id parameters, 5-attempt 15-minute account lockout, 90d audit retention.
// See pkg/auth/kind.go for the full default set.
func DefaultAuthOpts() AuthOpts { return auth.DefaultServiceOpts() }

// RegisterAuthService registers the engine-tier auth service kind on the
// coordinator, installs the gateway response-interception hook, and appends
// auth's Postgres migrations to Config.ExtraMigrations.
//
// Must be called BEFORE coord.Build(). The game must include "auth" in
// --services= when running with --mode=...,service for the kind to be
// instantiated on this process.
//
// If opts.SessionTTL is zero, defaults are used (DefaultAuthOpts()).
//
// If opts.Repository is nil and opts.RepositoryFactory is also nil,
// RegisterAuthService injects auth/postgres.New as the factory. Tests that
// want to inject an in-memory mock should set opts.Repository directly
// (see RegisterAuthServiceWithMock).
func RegisterAuthService(p *universe.Process, opts AuthOpts) error {
	if opts.SessionTTL == 0 {
		opts = DefaultAuthOpts()
	}
	if opts.Repository == nil && opts.RepositoryFactory == nil {
		opts.RepositoryFactory = func(pool *pgxpool.Pool) auth.Repository {
			return authpg.New(pool)
		}
	}
	kind := auth.Kind(opts)
	if err := p.RegisterService(kind); err != nil {
		return fmt.Errorf("RegisterAuthService: %w", err)
	}
	p.AddGatewayAuthHook(kind.OpCodes)
	p.AppendExtraMigrations(auth.MigrationsFS())
	return nil
}

// RegisterAuthServiceWithMock is the test-fixture variant that wires auth
// against an in-memory AuthRepository (typically authtest.NewMock()). Skips
// Postgres entirely — the auth kind's RequiresDB flag is automatically
// false because opts.Repository is non-nil.
func RegisterAuthServiceWithMock(p *universe.Process, repo AuthRepository) error {
	opts := DefaultAuthOpts()
	opts.Repository = repo
	return RegisterAuthService(p, opts)
}
