package auth

import (
	"context"
	"errors"
	"sync"

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
	// reapLoop will be wired in Task 17 (background reaper); for now we don't start it.
	ctx.Logger.Log(logCat, "auth service initialized: instance=%s", ctx.InstanceID)
	return nil
}

// RegisterOps wires the five auth handlers into the process op router.
// Handler implementations land in handlers.go (Tasks 12-16).
func (s *Service) RegisterOps(router *ops.Router) error {
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

var _ service.Service = (*Service)(nil)
