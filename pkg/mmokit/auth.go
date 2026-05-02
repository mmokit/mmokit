package mmokit

import (
	"fmt"
	"net/http"

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
		base := DefaultAuthOpts()
		base.Repository = opts.Repository
		base.RepositoryFactory = opts.RepositoryFactory
		opts = base
	}
	if opts.Repository == nil && opts.RepositoryFactory == nil {
		opts.RepositoryFactory = func(pool *pgxpool.Pool) auth.Repository {
			return authpg.New(pool)
		}
	}

	// Console commands are registered at facade time (pre-Build) but their
	// handlers need the live Repository which only exists after Service.Init.
	// The slot bridges the gap: OnReady fills it; getRepo reads it.
	setRepo, getRepo := auth.NewRepoSlot()
	if opts.Repository != nil {
		// Mock injection — repo is live now; populate immediately so commands
		// invoked before Service.Init still work.
		setRepo(opts.Repository)
	}
	// --dev-insecure-cookie overrides Secure regardless of caller intent.
	if p.Config().DevInsecureCookie {
		opts.HTTPOpts.CookieSecure = false
	}

	var liveService *auth.Service
	prev := opts.OnReady
	opts.OnReady = func(svc *auth.Service) {
		liveService = svc
		setRepo(svc.Repository())
		p.Config().AuthResolver = svc
		p.Config().AuthHTTPOpts = opts.HTTPOpts
		if prev != nil {
			prev(svc)
		}
	}

	kind := auth.Kind(opts)
	if err := p.RegisterService(kind); err != nil {
		return fmt.Errorf("RegisterAuthService: %w", err)
	}
	p.AddGatewayAuthHook(kind.OpCodes)
	p.AppendExtraMigrations(auth.MigrationsFS())

	// Auto-include "auth" in cfg.ServiceKinds so the kind is actually
	// instantiated by startServices at Build. Without this, an operator
	// running the binary with default flags (no --services=) would see
	// the kind registered but never started, and clients would get
	// "unknown operation code" on AUTH_OPCODE_LOGIN/REGISTER. Idempotent.
	cfg := p.Config()
	already := false
	for _, k := range cfg.ServiceKinds {
		if k == auth.KindName {
			already = true
			break
		}
	}
	if !already {
		cfg.ServiceKinds = append(cfg.ServiceKinds, auth.KindName)
	}

	prevHTTP := cfg.HTTPRoutes
	cfg.HTTPRoutes = func(mux *http.ServeMux) {
		if prevHTTP != nil {
			prevHTTP(mux)
		}
		if liveService != nil {
			liveService.RegisterHTTP(mux, opts.HTTPOpts)
		}
	}

	if reg := p.CmdRegistry(); reg != nil {
		if err := auth.RegisterConsoleCommands(reg, getRepo, p.DisconnectActiveUser); err != nil {
			return fmt.Errorf("RegisterAuthService: console commands: %w", err)
		}
	}
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
