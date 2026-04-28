package main

import (
	"context"
	"embed"
	"log"
	"sync"

	"github.com/mlange-42/ark/ecs"
	basicpb "github.com/zenion/mmoserver/gen/go/basicpb"
	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/persist"

	"github.com/zenion/mmoserver/examples/4node-basic/services/echo"
)

//go:embed all:web/dist
var webDist embed.FS

func main() {
	mmo := mmokit.New(mmokit.Config{
		InvariantMode:    mmokit.InvariantPanic,
		StrictNetIDIndex: true,
		CellsX:           CellsX,
		CellsY:           CellsY,
		CellSize:         CellSize,
		TickRate:         TickRate,
		AoIRadius:        AoIRadius,
		StaticFS:         webDist,
		StaticFSPrefix:   "web/dist",
		DefaultSpawn:     mmokit.Location{X: CellSize * 0.85, Y: CellSize * 0.85},
		LoginHandler: mmokit.HandleLogin(
			uint32(basicpb.ClientEventCode_BCE_LOGIN),
			func(m *basicpb.LoginMsg) (string, any, error) {
				name, err := mmokit.ValidateUsername(m.Name, 20)
				return name, nil, err
			},
		),
		OnConsoleReady: func(p *mmokit.Process, console *mmokit.Console) {
			if err := registerBotCommands(p, console.Registry()); err != nil {
				log.Printf("4node-basic: failed to register bot commands: %v", err)
			}
			resolver := &demoDebugResolver{coord: p}
			repo := newInMemoryDebugRepo()
			if err := mmokit.RegisterDebugCommands(console.Registry(), repo, resolver); err != nil {
				log.Printf("4node-basic: failed to register debug commands: %v", err)
			}
		},
		Protocol: mmokit.NewProtocol("basic").
			ClientEvents(func(e *mmokit.ClientEvents) {
				// BCE_LOGIN is handled by LoginHandler (bypasses InputRouter).
				mmokit.RegisterClientEvent[basicpb.LoginMsg](e, basicpb.ClientEventCode_BCE_LOGIN)
			}),
	})

	mmokit.RegisterKind[PlayerComponents](mmo, KindPlayer, "Player")
	mmokit.RegisterKind[BotComponents](mmo, KindBot, "Bot")

	mmo.OnPlayerJoin(func(s *mmokit.PlayerSession, stage *mmokit.Stage) {
		// Demo auto-grant: every player gets the topology overlay by
		// default so the cell-boundary debug rendering Just Works in
		// the example. Production deployments should drop this line
		// and grant per-user via `debug grant <user> topology`.
		s.DebugFlags |= engine.DebugTopology
		stage.SpawnPlayer(s,
			mmokit.WithCollider(PlayerRadius),
			mmokit.WithEntityKind(KindPlayer),
			mmokit.Init(func(c *PlayerComponents) {
				c.Name.Name = s.Username
			}),
		)
	})

	// Register the echo demo service. Engine instantiates it only when
	// the role set includes "service" AND --services= names "echo"; the
	// registration alone is harmless on processes that don't host it.
	if err := mmo.RegisterService(echo.Kind); err != nil {
		log.Fatalf("4node-basic: register echo service: %v", err)
	}

	mmo.AddSystem(mmokit.NewInputSystem(func(router *mmokit.InputRouter, gw *mmokit.Stage) {
		moveTargetMap := ecs.NewMap1[mmokit.MoveTarget](gw.ECSWorld())
		mmokit.Handle(router, basicpb.ClientEventCode_BCE_MOVE_TARGET,
			mmokit.States(mmokit.StateActive),
			func(ctx *mmokit.InputContext, msg *basicpb.MoveTargetMsg) {
				if !moveTargetMap.HasAll(ctx.Entity) {
					return
				}
				mmokit.SetMoveTarget(moveTargetMap.Get(ctx.Entity), msg.TargetX, msg.TargetY)
			})
	}))
	mmo.AddSystem(mmokit.NewClickToMoveSystem())
	mmo.AddSystem(mmokit.NewPhysicsSystem())
	mmo.AddSystem(mmokit.NewSpatialSystem())
	mmo.AddSystem(mmokit.NewDebugBroadcaster())
	mmo.AddSystem(mmokit.NewSystem(&BotSystem{}))
	mmo.AddSystem(mmokit.NewNetworkSystem())

	log.Printf("4node-basic: grid %dx%d cells, cell size %.0f, AoI %.0f", CellsX, CellsY, CellSize, AoIRadius)
	mmo.Start()
}

// demoDebugResolver implements mmokit's debugSessionResolver interface
// for the 4node-basic demo. It walks every cell's per-cell PlayerManager
// to find the active session by username, and sends a sentinel empty
// DebugInfoMsg via the cell's Stage on revoke-to-zero so the client
// clears its overlay.
type demoDebugResolver struct {
	coord *mmokit.Process
}

// SessionByUsername walks every cell looking for an active session with
// the given username. The 4node-basic demo runs single-process so all
// cells share one Process; iteration is O(cells × players).
func (r *demoDebugResolver) SessionByUsername(username string) *engine.PlayerSession {
	for _, cell := range r.coord.Cells {
		if cell.Engine == nil || cell.Engine.Players == nil {
			continue
		}
		if s := cell.Engine.Players.ByUsername(username); s != nil {
			return s
		}
	}
	return nil
}

// SendDebugClear pushes an empty DebugInfoMsg to the given connection so
// the client immediately clears its overlay state. Walks cells to find
// the one that owns the connection's session, then dispatches via that
// cell's Stage.SendEvent.
func (r *demoDebugResolver) SendDebugClear(connID uint32) {
	for _, cell := range r.coord.Cells {
		if cell.Engine == nil || cell.Engine.Players == nil {
			continue
		}
		if cell.Engine.Players.ByConnID(connID) == nil {
			continue
		}
		cell.Stage.SendEvent(connID, uint32(enginepb.ServerEventCode_SE_DEBUG_INFO), &enginepb.DebugInfoMsg{})
		return
	}
}

// inMemoryDebugRepo is a no-op debugRepo for the demo, which doesn't run
// Postgres. Production deployments wire the real persist.PlayerRepository
// (which has LoadDebugFlags / SaveDebugFlags) instead.
type inMemoryDebugRepo struct {
	mu    sync.Mutex
	flags map[string][]string
}

func newInMemoryDebugRepo() *inMemoryDebugRepo {
	return &inMemoryDebugRepo{flags: make(map[string][]string)}
}

func (r *inMemoryDebugRepo) LoadDebugFlags(_ context.Context, username string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, ok := r.flags[username]
	if !ok {
		return nil, persist.ErrNotFound
	}
	out := make([]string, len(cur))
	copy(out, cur)
	return out, nil
}

func (r *inMemoryDebugRepo) SaveDebugFlags(_ context.Context, username string, flags []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if flags == nil {
		r.flags[username] = []string{}
		return nil
	}
	cp := make([]string, len(flags))
	copy(cp, flags)
	r.flags[username] = cp
	return nil
}
