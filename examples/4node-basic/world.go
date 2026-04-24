package main

import (
	"github.com/mlange-42/ark/ecs"
	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/coords"
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

// Hooks returns empty hooks — no custom pre/post-tick behavior needed for this example.
func (gw *World) Hooks() engine.Hooks {
	return engine.Hooks{}
}

// ServerEvents returns the server-event registry for this world's coordinator.
func (gw *World) ServerEvents() *mmokit.ServerEvents {
	return mmokit.ServerEventsOf(gw.Process())
}

// sendCellTopology builds an SE_CELL_TOPOLOGY frame from the cluster's
// known cells and sends it to a single client via the engine's ConnSender.
// Replaces the deleted engine-side coord.SendCellTopology helper —
// topology distribution is now a game concern. Called from OnEnter on
// player spawn; can be reused by a dynamic-cells OnTopologyChanged
// callback to rebroadcast on cell split/merge.
func (gw *World) sendCellTopology(connID uint32) {
	cells := gw.ClusterCells()
	if len(cells) == 0 {
		return
	}
	msg := &enginepb.CellTopologyMsg{
		GridW:        int32(CellsX),
		GridH:        int32(CellsY),
		BaseCellSize: coords.CellSize,
	}
	for _, c := range cells {
		size := c.Cell.Size(coords.CellSize)
		ox, oy := c.Cell.WorldOrigin(coords.CellSize)
		msg.Cells = append(msg.Cells, &enginepb.CellInfo{
			CellX:   c.Cell.X,
			CellY:   c.Cell.Y,
			Depth:   uint32(c.Cell.Depth),
			Size:    size,
			OriginX: ox,
			OriginY: oy,
			NodeId:  c.HostID,
		})
	}
	gw.ServerEvents().Send(gw.Engine().ConnMgr, connID, uint32(enginepb.ServerEventCode_SE_CELL_TOPOLOGY), msg)
}
