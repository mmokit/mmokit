package main

import (
	"math/rand"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// World is the game world for a single node in the 4-node basic example.
type World struct {
	*mmokit.WorldBase

	Spatial       *mmokit.HashGrid
	ConnMap       *ecs.Map1[mmokit.PlayerConn]
	NameMap       *ecs.Map1[PlayerName]
	DebugInfoMap  *ecs.Map1[DebugInfo]
	MoveTargetMap *ecs.Map1[mmokit.MoveTarget]
}

// playerKindDef builds the entity kind definition for player entities.
// Shared between NewWorld (runtime) and dumpProtocolSchema (schema export).
func playerKindDef(w *ecs.World) mmokit.EntityKindDef {
	def := mmokit.EntityKindDef{
		Kind:           KindPlayer,
		Name:           "Player",
		EngineBindings: &mmokit.EngineBindingsConfig{VelQuantScale: 2000, SizeQuantScale: 500, IncludeMeshState: true},
	}
	mmokit.KindComponent(&def, ecs.NewMap1[PlayerName](w))
	mmokit.KindComponent(&def, ecs.NewMap1[DebugInfo](w))
	mmokit.KindComponent(&def, ecs.NewMap1[mmokit.MoveTarget](w))
	return def
}

// NewWorld creates a World for a node.
func NewWorld(base *mmokit.WorldBase) mmokit.GameWorld {
	w := base.ECSWorld()
	return &World{
		WorldBase:     base,
		Spatial:       base.SpatialGrid(),
		ConnMap:       ecs.NewMap1[mmokit.PlayerConn](w),
		NameMap:       ecs.NewMap1[PlayerName](w),
		DebugInfoMap:  ecs.NewMap1[DebugInfo](w),
		MoveTargetMap: ecs.NewMap1[mmokit.MoveTarget](w),
	}
}

// Init is called after all nodes are created and bridges are wired.
func (gw *World) Init() {
	w := gw.ECSWorld()

	// Register entity kinds
	gw.RegisterEntityKind(playerKindDef(w))

	// State callbacks
	pm := gw.Engine().Players
	pm.OnState(mmokit.StateActive, mmokit.StateCallbacks{
		OnEnter: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
			s.Entity = gw.spawnPlayer(s.ConnID, s.Username)
			gw.SendSpawnedMsg(s.ConnID, s.Entity)
			gw.SendCellTopology(s.ConnID)
		},
		OnExit: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
			if s.Entity != (ecs.Entity{}) && gw.ECSWorld().Alive(s.Entity) {
				if gw.GhostMap().HasAll(s.Entity) {
					s.Entity = ecs.Entity{}
					return
				}
				gw.MarkForRemoval(s.Entity)
				s.Entity = ecs.Entity{}
			}
		},
	})
}

// Hooks returns empty hooks — no custom pre/post-tick behavior needed for this example.
func (gw *World) Hooks() engine.Hooks {
	return engine.Hooks{}
}

// spawnPlayer creates a player circle entity at a random position within the cell.
func (gw *World) spawnPlayer(connID uint32, username string) ecs.Entity {
	cellSize := mmokit.CellSize()
	x := cellSize*0.2 + rand.Float32()*cellSize*0.6
	y := cellSize*0.2 + rand.Float32()*cellSize*0.6

	entity := gw.SpawnEntity(
		mmokit.Position{X: x, Y: y},
		mmokit.WithCollider(PlayerRadius),
		mmokit.WithEntityKind(KindPlayer),
		mmokit.WithComponents(), // auto-adds PlayerName, DebugInfo, MoveTarget
	)

	gw.ConnMap.Add(entity, &mmokit.PlayerConn{ConnID: connID})
	gw.NameMap.Get(entity).Name = username

	return entity
}
