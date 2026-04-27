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
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	basicpb "github.com/zenion/mmoserver/gen/go/basicpb"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

const logCat = "services:echo"

// Service is the runtime instance of the echo demo service.
type Service struct {
	instanceID string
	ctx        *mmokit.ServiceContext
}

// Kind is the registration descriptor passed to coord.RegisterService.
var Kind = mmokit.ServiceKind{
	Name: "echo",
	OpCodes: []uint32{
		uint32(basicpb.EchoOpCode_BOP_ECHO_PING),
		uint32(basicpb.EchoOpCode_BOP_ECHO_PERSIST),
		uint32(basicpb.EchoOpCode_BOP_ECHO_FETCH),
	},
	Factory: func(ctx *mmokit.ServiceContext) mmokit.Service {
		return &Service{
			instanceID: ctx.InstanceID,
			ctx:        ctx,
		}
	},
	RequiresDB:  true,
	Description: "demo: ping returns instanceID; persist/fetch round-trip a row through Postgres",
}

// Init validates dependencies. The framework guarantees DB is non-nil
// when RequiresDB=true, but we double-check defensively.
func (s *Service) Init(ctx *mmokit.ServiceContext) error {
	if ctx.DB == nil {
		return errors.New("echo.Init: DB required (RequiresDB=true should have caught this)")
	}
	ctx.Logger.Log(logCat, "echo service initialized: instance=%s", s.instanceID)
	return nil
}

// RegisterOps wires the three handlers — ping, persist, fetch.
func (s *Service) RegisterOps(router *mmokit.OpRouter) error {
	mmokit.RegisterOp(router, basicpb.EchoOpCode_BOP_ECHO_PING, "echoPing",
		func(opCtx *mmokit.OpContext, req *basicpb.EchoPingRequest) (*basicpb.EchoPingResponse, error) {
			s.ctx.Logger.Log(logCat, "ping: user=%s msg=%q", opCtx.Username, req.Msg)
			return &basicpb.EchoPingResponse{
				Msg:        fmt.Sprintf("Hello, %s! This is instance %s. You said: %s", opCtx.Username, s.instanceID, req.Msg),
				InstanceId: s.instanceID,
			}, nil
		},
	)

	mmokit.RegisterOp(router, basicpb.EchoOpCode_BOP_ECHO_PERSIST, "echoPersist",
		func(opCtx *mmokit.OpContext, req *basicpb.EchoPersistRequest) (*basicpb.EchoPersistResponse, error) {
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
			return &basicpb.EchoPersistResponse{Ok: true, InstanceId: s.instanceID}, nil
		},
	)

	mmokit.RegisterOp(router, basicpb.EchoOpCode_BOP_ECHO_FETCH, "echoFetch",
		func(opCtx *mmokit.OpContext, req *basicpb.EchoFetchRequest) (*basicpb.EchoFetchResponse, error) {
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
					return &basicpb.EchoFetchResponse{InstanceId: s.instanceID}, nil
				}
				return nil, fmt.Errorf("echo fetch: %w", err)
			}
			s.ctx.Logger.Log(logCat, "fetch: user=%s key=%s", opCtx.Username, req.Key)
			return &basicpb.EchoFetchResponse{
				Value:      value,
				FoundAtMs:  updatedAt.UnixMilli(),
				InstanceId: s.instanceID,
			}, nil
		},
	)
	return nil
}

// Shutdown is a no-op — handlers are stateless and the underlying
// pgx pool is owned by the engine, not the service.
func (s *Service) Shutdown(_ context.Context) error {
	s.ctx.Logger.Log(logCat, "echo service shutting down: instance=%s", s.instanceID)
	return nil
}
