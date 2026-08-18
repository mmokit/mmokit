// Package echo is the v1 demo service for the pluggable services
// framework. It exists primarily to validate the framework end-to-end:
//   - PING returns the handling instance ID (visualizes routing affinity)
//   - PERSIST writes a (key, value, ts) row to demo_echo
//   - FETCH reads it back
//
// PERSIST + FETCH together validate cross-instance DB consistency: a
// PERSIST handled by instance A and a FETCH handled by instance B
// still find each other's writes because state lives in Postgres.
package echo

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mmokit/mmokit"
)

const logCat = "services:echo"

//go:embed migrations/*.sql
var migrationsFS embed.FS

// liveService stores the running echo instance for the typed-op handler
// closures. The handlers are registered at package-init via RegisterOp;
// they call .Load() each invocation to pick up the live service pointer.
// Nil until Service.Init runs (typed-op response is OperationError until
// then).
var liveService atomic.Pointer[Service]

// Service is the runtime instance of the echo demo service.
type Service struct {
	instanceID string
	ctx        *mmokit.ServiceContext
}

// Kind is the registration descriptor passed to coord.RegisterService.
//
// OpCodes is empty: the three echo ops live on the typed-op channel
// (mmokit.RegisterOp[Req, Res]) and route by request typeID rather than
// per-kind code claims.
var Kind = mmokit.ServiceKind{
	Name:    "echo",
	OpCodes: nil,
	Factory: func(ctx *mmokit.ServiceContext) mmokit.Service {
		return &Service{
			instanceID: ctx.InstanceID,
			ctx:        ctx,
		}
	},
	RequiresDB:     true,
	Description:    "demo: ping returns instanceID; persist/fetch round-trip a row through Postgres",
	Migrations:     migrationsFS,
	MigrationsRoot: "migrations",
}

// RegisterTypedOps registers the three echo typed-op handlers on p. The
// handler closures resolve the live *Service pointer at invocation time via
// liveService.Load() — nil until Service.Init populates it.
//
// Call this from main UNCONDITIONALLY, before Build, so --dump-schema sees the
// three ops. It used to be a package init(), which is why it needed no caller
// and no Process; gating the call on a role would silently drop three ops from
// the emitted schema and break the SDK byte gate on a build that has the code
// linked in.
func RegisterTypedOps(p *mmokit.Process) {
	mmokit.RegisterOp[EchoPingRequest, EchoPingResponse](p, mmokit.RouteGatewayLocal,
		func(opCtx *mmokit.OpContext, req *EchoPingRequest) (*EchoPingResponse, error) {
			s := liveService.Load()
			if s == nil {
				return nil, errors.New("echo service not initialized")
			}
			s.ctx.Logger.Log(logCat, "ping: user=%s msg=%q", opCtx.Username, req.Msg)
			return &EchoPingResponse{
				Msg:        fmt.Sprintf("Hello, %s! This is instance %s. You said: %s", opCtx.Username, s.instanceID, req.Msg),
				InstanceID: s.instanceID,
			}, nil
		})

	mmokit.RegisterOp[EchoPersistRequest, EchoPersistResponse](p, mmokit.RouteGatewayLocal,
		func(opCtx *mmokit.OpContext, req *EchoPersistRequest) (*EchoPersistResponse, error) {
			s := liveService.Load()
			if s == nil {
				return nil, errors.New("echo service not initialized")
			}
			pool := s.ctx.DB.Pool()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := pool.Exec(ctx,
				`INSERT INTO demo_echo (key, value, updated_at) VALUES ($1, $2, NOW())
				 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`,
				req.Key, req.Value)
			if err != nil {
				s.ctx.Logger.Log(logCat, "persist failed: key=%s err=%v", req.Key, err)
				return nil, fmt.Errorf("echo persist: %w", err)
			}
			s.ctx.Logger.Log(logCat, "persist: user=%s key=%s value_len=%d",
				opCtx.Username, req.Key, len(req.Value))
			return &EchoPersistResponse{OK: true, InstanceID: s.instanceID}, nil
		})

	mmokit.RegisterOp[EchoFetchRequest, EchoFetchResponse](p, mmokit.RouteGatewayLocal,
		func(opCtx *mmokit.OpContext, req *EchoFetchRequest) (*EchoFetchResponse, error) {
			s := liveService.Load()
			if s == nil {
				return nil, errors.New("echo service not initialized")
			}
			pool := s.ctx.DB.Pool()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			row := pool.QueryRow(ctx,
				`SELECT value, updated_at FROM demo_echo WHERE key = $1`, req.Key)
			var value string
			var updatedAt time.Time
			if err := row.Scan(&value, &updatedAt); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					s.ctx.Logger.Log(logCat, "fetch miss: user=%s key=%s", opCtx.Username, req.Key)
					return &EchoFetchResponse{InstanceID: s.instanceID}, nil
				}
				return nil, fmt.Errorf("echo fetch: %w", err)
			}
			s.ctx.Logger.Log(logCat, "fetch: user=%s key=%s", opCtx.Username, req.Key)
			return &EchoFetchResponse{
				Value:      value,
				FoundAtMs:  updatedAt.UnixMilli(),
				InstanceID: s.instanceID,
			}, nil
		})
}

// Init validates dependencies and publishes the live service pointer
// for the typed-op handlers (registered at package init).
func (s *Service) Init(ctx *mmokit.ServiceContext) error {
	if ctx.DB == nil {
		return errors.New("echo.Init: DB required (RequiresDB=true should have caught this)")
	}
	liveService.Store(s)
	s.ctx.Logger.Log(logCat, "echo service initialized: instance=%s", s.instanceID)
	return nil
}

// Shutdown clears the live service pointer and lets handler invocations
// fail fast with "not initialized" if any are still in flight.
func (s *Service) Shutdown(_ context.Context) error {
	liveService.CompareAndSwap(s, nil)
	s.ctx.Logger.Log(logCat, "echo service shutting down: instance=%s", s.instanceID)
	return nil
}
