package main

import (
	"encoding/binary"
	"math"
	"math/rand"

	"github.com/mlange-42/ark/ecs"
	slitherpb "github.com/zenion/mmoserver/gen/go/slitherpb"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/universe"
)

// SlitherSessionData holds per-session game data stored in PlayerSession.Data.
type SlitherSessionData struct {
	SkinID uint8
}

// SlitherWorld is the game world for a single node in the slither.io example.
type SlitherWorld struct {
	*mmokit.WorldBase

	Cfg SlitherConfig

	// Game-specific component mappers
	SnakeBodyMap  *ecs.Map1[SnakeBody]
	SnakeStateMap *ecs.Map1[SnakeState]
	SnakeInputMap *ecs.Map1[SnakeInput]
	FoodMap       *ecs.Map1[Food]
	BotMap        *ecs.Map1[Bot]
	RotationMap   *ecs.Map1[mmokit.Rotation]

	// Entity mappers (per-node, since each node has its own ecs.World)
	snakeMappers *snakeMappers
	foodMappers  *foodMappers

	// Spatial grid for AoI and collision queries
	Spatial *mmokit.HashGrid

	// Per-tick state
	PendingKills []KillInfo
	KillFeed     []KillFeedEntry
	Leaderboard  []LeaderEntry
	FoodCount    int

	// TickQueue for pending events
	Queue *mmokit.TickQueue
}

// NewSlitherWorld creates a new SlitherWorld for a node.
func NewSlitherWorld(base *mmokit.WorldBase, cfg SlitherConfig) *SlitherWorld {
	w := base.ECSWorld()
	return &SlitherWorld{
		WorldBase:     base,
		Cfg:           cfg,
		SnakeBodyMap:  ecs.NewMap1[SnakeBody](w),
		SnakeStateMap: ecs.NewMap1[SnakeState](w),
		SnakeInputMap: ecs.NewMap1[SnakeInput](w),
		FoodMap:       ecs.NewMap1[Food](w),
		BotMap:        ecs.NewMap1[Bot](w),
		RotationMap:   ecs.NewMap1[mmokit.Rotation](w),
		snakeMappers:  newSnakeMappers(w),
		foodMappers:   newFoodMappers(w),
		Spatial:       base.SpatialGrid(),
		Queue:         mmokit.NewTickQueue(),
	}
}

// StateDead is the player state after death (awaiting respawn).
var StateDead mmokit.PlayerState

// Init is called after all nodes are created and bridges are wired, before game loops start.
func (gw *SlitherWorld) Init() {
	// Configure PlayerManager callbacks.
	pm := gw.Engine().Players

	// Register custom death state and transitions
	StateDead = pm.RegisterState("dead")
	pm.AddTransitions([]mmokit.StateTransition{
		{From: mmokit.StateActive, To: StateDead},
		{From: StateDead, To: mmokit.StateActive},
		{From: StateDead, To: mmokit.StateDisconnected},
		{From: mmokit.StateDisconnected, To: StateDead},
	})
	pm.OnState(mmokit.StateActive, mmokit.StateCallbacks{
		OnEnter: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
			data, _ := s.Data.(*SlitherSessionData)
			skinID := uint8(0)
			if data != nil {
				skinID = data.SkinID
			}
			s.Entity = gw.SpawnPlayerSnake(s.ConnID, s.Username, skinID)
		},
		OnExit: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
			if s.Entity != (ecs.Entity{}) {
				gw.MarkForRemoval(s.Entity)
				s.Entity = ecs.Entity{}
			}
		},
	})

	// Register component replicators for snake data across node boundaries.
	// Note: the registry is only consumed by TransferFrame component
	// serialization today; border replication uses BorderDispatcher which
	// encodes a fixed 18-byte per-entity payload directly. Extending
	// border frames with registry-driven per-component data is part of
	// the roadmap #12 follow-up.
	reg := mmokit.NewReplicationRegistry()

	// SnakeBody: custom Scan/Apply/Add (needs entity position for relative encoding)
	reg.Register(mmokit.ComponentReplicator{
		Scan: func(e ecs.Entity) []byte {
			if !gw.SnakeBodyMap.HasAll(e) || !gw.PositionMap().HasAll(e) {
				return nil
			}
			pos := gw.PositionMap().Get(e)
			body := gw.SnakeBodyMap.Get(e)
			return marshalSnakeBodyRelative(body, pos.X, pos.Y)
		},
		Apply: func(e ecs.Entity, data []byte) {
			if !gw.SnakeBodyMap.HasAll(e) || !gw.PositionMap().HasAll(e) {
				return
			}
			pos := gw.PositionMap().Get(e)
			unmarshalSnakeBodyRelativeInto(data, gw.SnakeBodyMap.Get(e), pos.X, pos.Y)
		},
		Add: func(e ecs.Entity, data []byte) {
			var body SnakeBody
			var ox, oy float32
			if gw.PositionMap().HasAll(e) {
				p := gw.PositionMap().Get(e)
				ox, oy = p.X, p.Y
			}
			unmarshalSnakeBodyRelativeInto(data, &body, ox, oy)
			gw.SnakeBodyMap.Add(e, &body)
		},
	})

	// Auto-marshaled via reflection
	universe.RegisterComponent(reg, gw.SnakeStateMap)
	universe.RegisterComponent(reg, gw.BotMap)
	universe.RegisterComponent(reg, gw.FoodMap)

	gw.SetReplicationRegistry(reg)

	// Shift body segments alongside head during cross-cell transfers.
	// BoundarySystem normalizes the head position by (dx, dy); body segments
	// in the ring buffer are absolute and need the same offset.
	shiftBody := func(entity ecs.Entity, dx, dy float32) {
		if !gw.SnakeBodyMap.HasAll(entity) {
			return
		}
		body := gw.SnakeBodyMap.Get(entity)
		for i := 0; i < body.Length; i++ {
			idx := (body.Head - i + MaxSegments) % MaxSegments
			body.Segments[idx].X += dx
			body.Segments[idx].Y += dy
		}
	}
	gw.SetPreSerialize(shiftBody)
	gw.SetPostSerialize(shiftBody)

	// Post-transfer hook: add SnakeInput for snake entities (not transferred, but required).
	// Component auto-fill doesn't cover this because SnakeInput is only needed on snakes,
	// not all entity kinds.
	gw.SetOnTransferReceived(func(entity ecs.Entity, frame *mmokit.TransferFrame) {
		if gw.SnakeStateMap.HasAll(entity) && !gw.SnakeInputMap.HasAll(entity) {
			gw.SnakeInputMap.Add(entity, &SnakeInput{})
		}
	})

	// Initial spawns — no more PendingAdminCmds hack!
	gw.SpawnInitialFood()
	cellSize := mmokit.CellSize()
	for i := 0; i < gw.Cfg.BotsPerNode; i++ {
		x := rand.Float32()*cellSize*0.6 + cellSize*0.2
		y := rand.Float32()*cellSize*0.6 + cellSize*0.2
		gw.SpawnBotSnake(x, y)
	}
}

// ServerEvents returns the typed server-event registry declared in main.go's
// cfg.Protocol.
func (gw *SlitherWorld) ServerEvents() *mmokit.ServerEvents {
	return mmokit.ServerEventsOf(gw.Process())
}

// ---------------------------------------------------------------------------
// GameWorld interface overrides
// ---------------------------------------------------------------------------

func (gw *SlitherWorld) Hooks() engine.Hooks {
	return engine.Hooks{
		ClearTickState: func() {
			gw.PendingKills = gw.PendingKills[:0]
			gw.KillFeed = gw.KillFeed[:0]
		},
	}
}

// HandleCrossCellAction processes actions from other nodes.
func (gw *SlitherWorld) HandleCrossCellAction(action *mmokit.CrossCellAction) *mmokit.ActionResult {
	switch mmokit.ActionType(action.Type) {
	case mmokit.ActionType(ActionEat):
		return gw.handleEatAction(action)
	case mmokit.ActionType(ActionSpawnFood):
		gw.handleSpawnFoodAction(action)
		return nil
	}
	return nil
}

func (gw *SlitherWorld) handleEatAction(action *mmokit.CrossCellAction) *mmokit.ActionResult {
	// Find the food entity by netID
	filter := ecs.NewFilter2[mmokit.NetworkID, Food](gw.ECSWorld())
	query := filter.Query()
	for query.Next() {
		nid, food := query.Get()
		if nid.ID == action.TargetNetID {
			entity := query.Entity()
			query.Close()
			value := food.Value
			gw.MarkForRemoval(entity)
			gw.FoodCount--

			payload := make([]byte, 4)
			binary.LittleEndian.PutUint32(payload, math.Float32bits(value))
			return &mmokit.ActionResult{
				Type:        action.Type,
				TargetNetID: action.TargetNetID,
				SourceNetID: action.SourceNetID,
				Success:     true,
				Payload:     payload,
			}
		}
	}
	return &mmokit.ActionResult{
		Type:    action.Type,
		Success: false,
	}
}

func (gw *SlitherWorld) handleSpawnFoodAction(action *mmokit.CrossCellAction) {
	// Decode food spawn requests from payload
	data := action.Payload
	if len(data) < 4 {
		return
	}
	count := int(binary.LittleEndian.Uint32(data[:4]))
	off := 4
	for i := 0; i < count && off+12 <= len(data); i++ {
		x := math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
		off += 4
		y := math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
		off += 4
		value := math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
		off += 4
		gw.SpawnFood(x, y, value, uint8(i%8))
	}
}

// HandleActionResult applies results from cross-cell actions.
func (gw *SlitherWorld) HandleActionResult(result *mmokit.ActionResult) {
	if !result.Success {
		return
	}
	switch mmokit.ActionType(result.Type) {
	case mmokit.ActionType(ActionEat):
		// Find the snake that ate and add mass
		filter := ecs.NewFilter2[mmokit.NetworkID, SnakeState](gw.ECSWorld())
		query := filter.Query()
		for query.Next() {
			nid, state := query.Get()
			if nid.ID == result.SourceNetID {
				query.Close()
				value := math.Float32frombits(binary.LittleEndian.Uint32(result.Payload))
				state.Mass += value
				return
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Input handlers
// ---------------------------------------------------------------------------

func setupInputHandlers(router *mmokit.InputRouter, gw *SlitherWorld) {
	eng := gw.Engine()

	// Player input (movement)
	mmokit.Handle(router,
		slitherpb.SlitherClientEventCode_SCE_PLAYER_INPUT,
		mmokit.States(mmokit.StateActive),
		func(ctx *mmokit.InputContext, msg *slitherpb.SlitherInputMsg) {
			if !gw.SnakeInputMap.HasAll(ctx.Entity) {
				return
			}
			input := gw.SnakeInputMap.Get(ctx.Entity)
			input.TargetAngle = msg.TargetAngle
			input.Boost = msg.Boost
			input.Sequence = msg.Sequence
		})

	// Respawn
	router.Handle(uint32(slitherpb.SlitherClientEventCode_SCE_RESPAWN),
		mmokit.States(StateDead),
		func(ctx *mmokit.InputContext, data []byte) {
			_ = eng.Players.Transition(ctx.Session, mmokit.StateActive)
		})

}

// ---------------------------------------------------------------------------
// Serialization helpers
// ---------------------------------------------------------------------------

// marshalSnakeBodyRelative serializes body segments as offsets from (ox, oy).
// Used for replication so segments are coordinate-space independent.
func marshalSnakeBodyRelative(b *SnakeBody, ox, oy float32) []byte {
	buf := make([]byte, 8+b.Length*8)
	binary.LittleEndian.PutUint32(buf[0:], uint32(b.Head))
	binary.LittleEndian.PutUint32(buf[4:], uint32(b.Length))
	off := 8
	for i := 0; i < b.Length; i++ {
		seg := b.GetSegment(i)
		binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(seg.X-ox))
		off += 4
		binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(seg.Y-oy))
		off += 4
	}
	return buf
}

// unmarshalSnakeBodyRelativeInto deserializes relative segments and adds (ox, oy)
// to reconstruct absolute positions in the receiver's coordinate space.
func unmarshalSnakeBodyRelativeInto(data []byte, b *SnakeBody, ox, oy float32) {
	if len(data) < 8 {
		return
	}
	b.Head = int(binary.LittleEndian.Uint32(data[0:]))
	b.Length = int(binary.LittleEndian.Uint32(data[4:]))
	off := 8
	for i := 0; i < b.Length && off+8 <= len(data); i++ {
		rx := math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
		off += 4
		ry := math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
		off += 4
		idx := (b.Head - i + MaxSegments) % MaxSegments
		b.Segments[idx] = Segment{X: rx + ox, Y: ry + oy}
	}
}
