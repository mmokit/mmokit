package auth

import (
	"context"
	"errors"
	"sync"
	"time"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/ops"
	"github.com/zenion/mmoserver/pkg/service"
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
}

func newService(ctx *service.Context, opts ServiceOpts) service.Service {
	return &Service{ctx: ctx, opts: opts}
}

// Repository returns the live Repository. Used by the mmokit facade
// to wire the console-command repo provider after Init.
func (s *Service) Repository() Repository { return s.repo }

func (s *Service) Init(ctx *service.Context) error {
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

// RegisterOps wires the five auth handlers into the process op router.
// ops.Register panics on duplicate code; no error return.
func (s *Service) RegisterOps(router *ops.Router) error {
	ops.Register[enginepb.AuthLoginRequest, enginepb.AuthLoginResponse](
		router, enginepb.AuthOpCode_AUTH_OPCODE_LOGIN, "authLogin", s.handleLogin)
	ops.Register[enginepb.AuthRegisterRequest, enginepb.AuthRegisterResponse](
		router, enginepb.AuthOpCode_AUTH_OPCODE_REGISTER, "authRegister", s.handleRegister)
	ops.Register[enginepb.AuthValidateTokenRequest, enginepb.AuthValidateTokenResponse](
		router, enginepb.AuthOpCode_AUTH_OPCODE_VALIDATE_TOKEN, "authValidateToken", s.handleValidateToken)
	ops.Register[enginepb.AuthLogoutRequest, enginepb.AuthLogoutResponse](
		router, enginepb.AuthOpCode_AUTH_OPCODE_LOGOUT, "authLogout", s.handleLogout)
	ops.Register[enginepb.AuthChangePasswordRequest, enginepb.AuthChangePasswordResponse](
		router, enginepb.AuthOpCode_AUTH_OPCODE_CHANGE_PASSWORD, "authChangePassword", s.handleChangePassword)
	return nil
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
