package main

import (
	"math/rand"
	"strings"

	"github.com/mlange-42/ark/ecs"
	basicpb "github.com/zenion/mmoserver/gen/go/basicpb"
	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"google.golang.org/protobuf/proto"
)

// BasicWorld is the game world for a single node in the 4-node basic example.
type BasicWorld struct {
	mmokit.WorldBase

	Spatial        *mmokit.HashGrid
	ConnMap        *ecs.Map1[mmokit.PlayerConn]
	NameMap        *ecs.Map1[PlayerName]
	DebugInfoMap   *ecs.Map1[DebugInfo]
	MoveTargetMap  *ecs.Map1[mmokit.MoveTarget]
}

// NewBasicWorld creates a BasicWorld for a node.
func NewBasicWorld(base *mmokit.WorldBase) *BasicWorld {
	w := base.ECSWorld()

	gw := &BasicWorld{
		WorldBase:     *base,
		Spatial:        base.SpatialGrid(),
		ConnMap:        ecs.NewMap1[mmokit.PlayerConn](w),
		NameMap:        ecs.NewMap1[PlayerName](w),
		DebugInfoMap:   ecs.NewMap1[DebugInfo](w),
		MoveTargetMap:  ecs.NewMap1[mmokit.MoveTarget](w),
	}

	// --- Replication registry (for cross-node entity transfers) ---
	reg := mmokit.NewReplicationRegistry()
	mmokit.RegisterComponent(reg, 1, gw.VelocityMap())
	mmokit.RegisterComponent(reg, 2, gw.NameMap)
	mmokit.RegisterComponent(reg, 3, gw.MoveTargetMap)
	mmokit.RegisterComponent(reg, 4, gw.DebugInfoMap)
	gw.SetReplicationRegistry(reg)

	// --- Login handler ---
	pm := base.Engine().Players
	pm.SetLoginHandler(func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) error {
		msgs := pm.Engine().ConnMgr.DrainInput(s.ConnID)
		for _, data := range msgs {
			var evt enginepb.ClientEvent
			if err := proto.Unmarshal(data, &evt); err != nil {
				continue
			}
			if evt.Code == uint32(basicpb.BasicClientEventCode_BCE_LOGIN) {
				var login basicpb.BasicLoginMsg
				if err := proto.Unmarshal(evt.Data, &login); err != nil {
					continue
				}
				name := strings.ToLower(strings.TrimSpace(login.Name))
				if name == "" || len(name) > 20 {
					continue
				}
				s.Username = name
				return nil
			}
		}
		return mmokit.ErrLoginPending
	})

	// --- State callbacks ---
	pm.OnState(mmokit.StateActive, mmokit.StateCallbacks{
		OnEnter: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
			s.Entity = gw.spawnPlayer(s.ConnID, s.Username)
			gw.sendSpawnedMsg(s.ConnID, s.Entity)
		},
		OnExit: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
			if s.Entity != (ecs.Entity{}) && gw.ECSWorld().Alive(s.Entity) {
				// Don't remove if entity is a ghost (being transferred).
				// The ghost lingers for visual continuity until the replica arrives.
				if gw.GhostMap().HasAll(s.Entity) {
					s.Entity = ecs.Entity{}
					return
				}
				gw.MarkForRemoval(s.Entity)
				s.Entity = ecs.Entity{}
			}
		},
	})

	// --- Transfer hooks ---
	gw.SetOnTransferReceived(func(entity ecs.Entity, frame *mmokit.TransferFrame) {
		if !gw.MoveTargetMap.HasAll(entity) {
			gw.MoveTargetMap.Add(entity, &mmokit.MoveTarget{})
		}
		if !gw.DebugInfoMap.HasAll(entity) {
			gw.DebugInfoMap.Add(entity, &DebugInfo{})
		}
		if frame.ConnID != 0 && !gw.ConnMap.HasAll(entity) {
			gw.ConnMap.Add(entity, &mmokit.PlayerConn{ConnID: frame.ConnID})
		}
	})

	gw.SetOnPlayerTransferReceived(func(entity ecs.Entity, frame *mmokit.TransferFrame) {
		if s := gw.Engine().Players.ByConnID(frame.ConnID); s != nil {
			s.Entity = entity
		}
		gw.sendSpawnedMsg(frame.ConnID, entity)
	})

	return gw
}

func (gw *BasicWorld) Hooks() engine.Hooks {
	return engine.Hooks{}
}

// spawnPlayer creates a player circle entity at a random position within the cell.
func (gw *BasicWorld) spawnPlayer(connID uint32, username string) ecs.Entity {
	cellSize := mmokit.CellSize()
	x := cellSize*0.2 + rand.Float32()*cellSize*0.6
	y := cellSize*0.2 + rand.Float32()*cellSize*0.6

	entity := gw.SpawnEntity(
		mmokit.Position{X: x, Y: y},
		mmokit.WithCollider(PlayerRadius),
		mmokit.WithEntityKind(KindPlayer),
	)

	gw.ConnMap.Add(entity, &mmokit.PlayerConn{ConnID: connID})
	gw.NameMap.Add(entity, &PlayerName{Name: username})
	gw.DebugInfoMap.Add(entity, &DebugInfo{})
	gw.MoveTargetMap.Add(entity, &mmokit.MoveTarget{})

	gw.Spatial.Register(mmokit.SpatialEntry{
		Entity: entity,
		X:      x,
		Y:      y,
		Radius: PlayerRadius,
	})

	return entity
}

// sendSpawnedMsg tells the client its entity ID and grid metadata.
func (gw *BasicWorld) sendSpawnedMsg(connID uint32, entity ecs.Entity) {
	cell := gw.Cell()
	netID := uint32(0)
	if gw.NetworkIDMap().HasAll(entity) {
		netID = gw.NetworkIDMap().Get(entity).ID
	}
	msg := &basicpb.BasicSpawnedMsg{
		EntityNetId: netID,
		CellX:       int32(cell.CellX),
		CellY:       int32(cell.CellY),
		CellSize:    mmokit.CellSize(),
		GridW:       int32(MeshCellsX),
		GridH:       int32(MeshCellsY),
		AoiRadius:   AoIRadius,
	}
	frame := mmokit.MakeEvent(uint32(enginepb.ServerEventCode_SE_PLAYER_SPAWNED), msg)
	gw.Engine().ConnMgr.Send(connID, frame)
}

