package mmokit

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zenion/mmoserver/pkg/service"
	"github.com/zenion/mmoserver/pkg/services/auth"
	authpg "github.com/zenion/mmoserver/pkg/services/auth/postgres"
	"github.com/zenion/mmoserver/pkg/universe"
)

// AuthOpts is mmokit's facade type for auth.ServiceOpts.
type AuthOpts = auth.ServiceOpts

// AuthRepository is mmokit's facade type for auth.Repository (used by tests
// injecting an in-memory mock).
type AuthRepository = auth.Repository

// DefaultAuthOpts returns sane defaults: 30d sliding session TTL, OWASP-2024
// argon2id parameters, 5-attempt 15-minute account lockout, 90d audit retention.
// See pkg/services/auth/kind.go for the full default set.
func DefaultAuthOpts() AuthOpts { return auth.DefaultServiceOpts() }

// errAuthServiceNotReady is returned by every typed-op auth handler until
// Service.Init runs and OnReady fires. Should never reach a real client —
// auth ops are pre-auth and the handlers are registered at facade time
// (before Build) but only invoked once a client is connected, by which
// point Init has long since completed.
var errAuthServiceNotReady = errors.New("auth service not initialized")

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

	// liveService captures the running *auth.Service after Service.Init.
	// The typed-op handlers below resolve it lazily via the closure so the
	// registry is populated at facade time (pre-Build) without depending on
	// Init order.
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

	// Register the five typed-op auth handlers. All five are RouteGatewayLocal —
	// auth ops never need cell-routed dispatch (no ECS access, no per-player
	// cell affinity). The handlers wrap the live Service.Handle* methods and
	// publish bus events on success.
	RegisterOp[auth.AuthLoginRequest, auth.AuthLoginResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *auth.AuthLoginRequest) (*auth.AuthLoginResponse, error) {
			if liveService == nil {
				return nil, errAuthServiceNotReady
			}
			resp, err := liveService.HandleLogin(opCtx, req)
			if err != nil {
				return nil, err
			}
			// Phase 2: publish AuthLoginSucceededEvent on the per-process
			// bus. The gateway subscribes via subscribeToAuthEvents and
			// populates authStates synchronously inline (Bus is process-local
			// synchronous). The nil-guard tolerates fixture tests that
			// bypass Process.New (Bus is constructed in New, not Build).
			if bus := liveService.Bus(); bus != nil {
				service.Publish(bus, service.AuthLoginSucceededEvent{
					UserID:       resp.UserID,
					Username:     resp.Username,
					SessionToken: resp.SessionToken,
					ExpiresAtMs:  resp.ExpiresAtMs,
					ConnID:       opCtx.ConnID,
					GatewayID:    p.GatewayID(),
				})
			}
			return resp, nil
		})
	RegisterOp[auth.AuthRegisterRequest, auth.AuthRegisterResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *auth.AuthRegisterRequest) (*auth.AuthRegisterResponse, error) {
			if liveService == nil {
				return nil, errAuthServiceNotReady
			}
			resp, err := liveService.HandleRegister(opCtx, req)
			if err != nil {
				return nil, err
			}
			// Phase 2: register also logs the user in, so emit BOTH
			// AuthLoginSucceededEvent (for session-state services like the
			// gateway's authStates map) AND AuthRegisteredEvent (for
			// first-login-only services like welcome packs / achievements).
			if bus := liveService.Bus(); bus != nil {
				service.Publish(bus, service.AuthLoginSucceededEvent{
					UserID:       resp.UserID,
					Username:     resp.Username,
					SessionToken: resp.SessionToken,
					ExpiresAtMs:  resp.ExpiresAtMs,
					ConnID:       opCtx.ConnID,
					GatewayID:    p.GatewayID(),
				})
				service.Publish(bus, service.AuthRegisteredEvent{
					UserID:   resp.UserID,
					Username: resp.Username,
				})
			}
			return resp, nil
		})
	RegisterOp[auth.AuthValidateTokenRequest, auth.AuthValidateTokenResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *auth.AuthValidateTokenRequest) (*auth.AuthValidateTokenResponse, error) {
			if liveService == nil {
				return nil, errAuthServiceNotReady
			}
			resp, err := liveService.HandleValidateToken(opCtx, req)
			if err != nil {
				return nil, err
			}
			// Phase 2: validate-token doesn't mint a new token — the client
			// already has one — so SessionToken is intentionally empty. The
			// gateway subscriber preserves its cached request-side token via
			// the same convention the legacy NotifyValidateTokenSuccess used.
			if bus := liveService.Bus(); bus != nil {
				service.Publish(bus, service.AuthLoginSucceededEvent{
					UserID:      resp.UserID,
					Username:    resp.Username,
					ExpiresAtMs: resp.ExpiresAtMs,
					ConnID:      opCtx.ConnID,
					GatewayID:   p.GatewayID(),
				})
			}
			return resp, nil
		})
	RegisterOp[auth.AuthLogoutRequest, auth.AuthLogoutResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *auth.AuthLogoutRequest) (*auth.AuthLogoutResponse, error) {
			if liveService == nil {
				return nil, errAuthServiceNotReady
			}
			resp, err := liveService.HandleLogout(opCtx, req)
			if err != nil {
				return nil, err
			}
			// Phase 2: AuthLogoutResponse does not carry UserID / Username —
			// the auth handler intentionally omits them (the response is
			// one-shot ack). Subscribers (gateway authStates lookup) resolve
			// identity from their per-conn cache keyed by ConnID + GatewayID.
			if bus := liveService.Bus(); bus != nil {
				service.Publish(bus, service.AuthLogoutEvent{
					ConnID:    opCtx.ConnID,
					GatewayID: p.GatewayID(),
				})
			}
			return resp, nil
		})
	RegisterOp[auth.AuthChangePasswordRequest, auth.AuthChangePasswordResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *auth.AuthChangePasswordRequest) (*auth.AuthChangePasswordResponse, error) {
			if liveService == nil {
				return nil, errAuthServiceNotReady
			}
			return liveService.HandleChangePassword(opCtx, req)
		})

	p.AppendExtraMigrations(auth.MigrationsFS())

	// Auto-include "auth" in cfg.ServiceKinds when this process WILL host
	// services (RoleService is in the mode), so that operators running the
	// binary with default flags don't have to also pass --services=auth.
	// Without this, "unknown typeID" would surface on auth typed-ops for
	// clients connecting to a single-process dev server.
	//
	// In distributed setups (--mode=coordinator, --mode=gateway, --mode=host)
	// every process still calls RegisterAuthService at facade time, but only
	// the process(es) running RoleService instantiate the kind. Auto-appending
	// here without that check would trip the Build-time panic that asserts
	// "ServiceKinds set but RoleService missing".
	cfg := p.Config()
	roles, _ := universe.ParseRoles(cfg.Mode)
	if roles.Has(universe.RoleService) {
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

