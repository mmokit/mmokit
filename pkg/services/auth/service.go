package auth

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mmokit/mmokit/pkg/ops"
	"github.com/mmokit/mmokit/pkg/service"
)

const logCat = "services:auth"

// Service is the running auth service instance.
type Service struct {
	ctx    *service.Context
	opts   ServiceOpts
	repo   Repository
	rl     *IPRateLimiter
	reapCh chan struct{}
	reapWG sync.WaitGroup

	// bus is captured from ctx.Bus at Init time. The mmokit facade's
	// typed-op handler wrappers reach it via Bus() to publish typed
	// AuthLoginSucceededEvent / AuthLogoutEvent / AuthRegisteredEvent
	// values after each successful auth handler returns.
	bus *service.Bus
}

func newService(ctx *service.Context, opts ServiceOpts) service.Service {
	return &Service{ctx: ctx, opts: opts}
}

// Repository returns the live Repository. Used by the mmokit facade
// to wire the console-command repo provider after Init.
func (s *Service) Repository() Repository { return s.repo }

// Bus returns the per-process service bus this auth instance was wired
// onto. Used by the mmokit facade to publish typed Auth*Event values
// from typed-op handler wrappers. Returns nil before Init.
func (s *Service) Bus() *service.Bus {
	return s.bus
}

func (s *Service) Init(ctx *service.Context) error {
	s.bus = ctx.Bus
	if s.opts.Repository != nil {
		s.repo = s.opts.Repository
	} else {
		if ctx.DB == nil {
			return errors.New("auth.Init: DB required (RequiresDB=true should have caught this)")
		}
		if s.opts.RepositoryFactory == nil {
			return errors.New("auth.Init: RepositoryFactory must be set when no Repository is injected")
		}
		s.repo = s.opts.RepositoryFactory(ctx.DB.Pool())
	}
	s.rl = NewIPRateLimiter(IPRateLimitConfig{
		Max: s.opts.IPRateLimitMax, Window: s.opts.IPRateLimitWindow, Lockout: s.opts.IPLockoutDuration,
	})
	s.reapCh = make(chan struct{})
	s.reapWG.Add(1)
	go s.reapLoop()
	if s.opts.OnReady != nil {
		s.opts.OnReady(s)
	}
	ctx.Logger.Log(logCat, "auth service initialized: instance=%s", ctx.InstanceID)
	return nil
}

// HandleLogin / HandleRegister / HandleValidateToken / HandleLogout /
// HandleChangePassword expose the typed handlers to the mmokit facade
// (pkg/mmokit/auth.go) so it can wire them into the typed-op registry
// without depending on auth-internal symbol visibility. The handler
// bodies live unchanged in handlers.go.
func (s *Service) HandleLogin(opCtx *ops.OpContext, req *AuthLoginRequest) (*AuthLoginResponse, error) {
	return s.handleLogin(opCtx, req)
}
func (s *Service) HandleRegister(opCtx *ops.OpContext, req *AuthRegisterRequest) (*AuthRegisterResponse, error) {
	return s.handleRegister(opCtx, req)
}
func (s *Service) HandleValidateToken(opCtx *ops.OpContext, req *AuthValidateTokenRequest) (*AuthValidateTokenResponse, error) {
	return s.handleValidateToken(opCtx, req)
}
func (s *Service) HandleLogout(opCtx *ops.OpContext, req *AuthLogoutRequest) (*AuthLogoutResponse, error) {
	return s.handleLogout(opCtx, req)
}
func (s *Service) HandleChangePassword(opCtx *ops.OpContext, req *AuthChangePasswordRequest) (*AuthChangePasswordResponse, error) {
	return s.handleChangePassword(opCtx, req)
}

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
	s.ctx.Logger.Log(logCat, "auth service shutdown: instance=%s", s.ctx.InstanceID)
	return nil
}

func (s *Service) reapLoop() {
	defer s.reapWG.Done()
	t := time.NewTicker(s.opts.ReapInterval)
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
	if n, err := s.repo.DeleteExpiredSessions(ctx, 7*24*time.Hour); err == nil && n > 0 {
		s.ctx.Logger.Log(logCat, "reaper: removed %d expired sessions", n)
	}
	if n, err := s.repo.DeleteOldAuditRows(ctx, s.opts.AuditRetention); err == nil && n > 0 {
		s.ctx.Logger.Log(logCat, "reaper: removed %d old audit rows", n)
	}
	if n := s.rl.Sweep(time.Hour); n > 0 {
		s.ctx.Logger.Log(logCat, "reaper: evicted %d idle ip buckets", n)
	}
}

// Resolve validates a session token and slides its expiry. Mirrors
// handleValidateToken's logic minus the op-channel envelope; called by
// the gateway at WS-upgrade time. Returns ErrTokenInvalid for any
// recoverable failure (unknown / revoked / expired); any other error
// is an infra failure that should bubble.
func (s *Service) Resolve(ctx context.Context, token string) (*ResolvedSession, error) {
	if token == "" {
		return nil, ErrTokenInvalid
	}
	h := HashToken(token)
	sess, err := s.repo.GetSession(ctx, h)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return nil, ErrTokenInvalid
		}
		return nil, err
	}
	if !sess.RevokedAt.IsZero() {
		return nil, ErrTokenInvalid
	}
	if time.Now().After(sess.ExpiresAt) {
		return nil, ErrTokenInvalid
	}
	user, err := s.repo.GetUserByID(ctx, sess.UserID)
	if err != nil {
		return nil, err
	}
	if user.Status == "disabled" || (!user.LockedUntil.IsZero() && user.LockedUntil.After(time.Now())) {
		return nil, ErrTokenInvalid
	}
	newExp := time.Now().Add(s.opts.SessionTTL)
	if err := s.repo.SlideSession(ctx, h, newExp); err != nil {
		return nil, err
	}
	return &ResolvedSession{
		UserID:    user.UserID,
		Username:  user.Username,
		ExpiresAt: newExp,
	}, nil
}

var _ service.Service = (*Service)(nil)
var _ Resolver = (*Service)(nil)
