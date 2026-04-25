package main

import (
	"github.com/mlange-42/ark/ecs"
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

	def := mmokit.EntityKindDef{
		Kind:           KindPlayer,
		Name:           "Player",
		EngineBindings: &mmokit.EngineBindingsConfig{VelQuantScale: 2000, SizeQuantScale: 500, IncludeMeshState: true},
	}
	mmokit.KindComponent(&def, ecs.NewMap1[PlayerName](w))
	mmokit.KindComponent(&def, ecs.NewMap1[DebugInfo](w))
	mmokit.KindComponent(&def, ecs.NewMap1[mmokit.MoveTarget](w))
	gw.RegisterEntityKind(def)

	// State callbacks
	pm := gw.Engine().Players
	pm.OnState(mmokit.StateActive, mmokit.StateCallbacks{
		OnEnter: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
			s.Entity = gw.SpawnAtLocation(s.SpawnLocation,
				mmokit.WithCollider(PlayerRadius),
				mmokit.WithEntityKind(KindPlayer),
				mmokit.WithComponents(), // auto-adds PlayerName, DebugInfo, MoveTarget
			)
			gw.ConnMap.Add(s.Entity, &mmokit.PlayerConn{ConnID: s.ConnID})
			gw.NameMap.Get(s.Entity).Name = s.Username
			gw.SendSpawnedMsg(s.ConnID, s.Entity)
			// DebugInfoSystem.Update pushes SE_CELL_TOPOLOGY reactively to
			// every active player on change (including first-send to new
			// players), so no per-spawn send is needed here.
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

