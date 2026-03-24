package system

import (
	"github.com/mlange-42/ark/ecs"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/internal/netutil"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// SectorBoundarySystem normalizes entity positions into [0, SectorSize)
// and bumps their SectorCoord when they cross a boundary.
// Runs after PhysicsSystem, before SpatialSystem.
type SectorBoundarySystem struct {
	gw     *game.GameWorld
	filter *ecs.Filter2[mmokit.Position, mmokit.SectorCoord]
}

func NewSectorBoundarySystem(gw *game.GameWorld) *SectorBoundarySystem {
	return &SectorBoundarySystem{gw: gw}
}

func (s *SectorBoundarySystem) Name() string { return "SectorBoundary" }

func (s *SectorBoundarySystem) Update(dt float32) {
	if s.filter == nil {
		s.filter = ecs.NewFilter2[mmokit.Position, mmokit.SectorCoord](s.gw.ECS).Without(ecs.C[mmokit.Ghost](), ecs.C[mmokit.Replica](), ecs.C[mmokit.TransferCooldown]())
	}

	// Collect cross-node transfers (cannot modify entities during iteration)
	type pendingTransfer struct {
		entity     ecs.Entity
		newSector  coords.SectorCoord
		destNodeID string
	}
	var transfers []pendingTransfer

	query := s.filter.Query()
	for query.Next() {
		pos, sec := query.Get()

		oldSX, oldSY := sec.SX, sec.SY

		// Compute what the new sector would be
		newSX, newSY := sec.SX, sec.SY
		newX, newY := pos.X, pos.Y

		for newX >= coords.SectorSize {
			newX -= coords.SectorSize
			newSX++
		}
		for newX < 0 {
			newX += coords.SectorSize
			newSX--
		}
		for newY >= coords.SectorSize {
			newY -= coords.SectorSize
			newSY++
		}
		for newY < 0 {
			newY += coords.SectorSize
			newSY--
		}

		// No sector change — nothing to do
		if newSX == oldSX && newSY == oldSY {
			continue
		}

		// Check if the new sector belongs to a different node
		destSector := coords.SectorCoord{SX: newSX, SY: newSY}
		if destNodeID := s.gw.Bridge.SectorOwner(destSector); destNodeID != "" && destNodeID != s.gw.NodeID {
			// Cross-node transfer: DON'T normalize, collect for post-iteration
			entity := query.Entity()
			transfers = append(transfers, pendingTransfer{
				entity:     entity,
				newSector:  destSector,
				destNodeID: destNodeID,
			})
			continue
		}

		// Same-node sector change: normalize position
		pos.X = newX
		pos.Y = newY
		sec.SX = newSX
		sec.SY = newSY

		// If this is a player entity, notify the client
		entity := query.Entity()
		if s.gw.C.PlayerConn.HasAll(entity) {
			connID := s.gw.C.PlayerConn.Get(entity).ConnID
			username := s.gw.Players.Usernames[connID]
			s.gw.Log.Log(game.CatMap, "sector change: player=%s from=(%d,%d) to=(%d,%d)", username, oldSX, oldSY, sec.SX, sec.SY)
			frame := netutil.MakeEvent(uint32(enginepb.ServerEventCode_SE_SECTOR_CHANGE), &enginepb.SectorChangeMsg{
				SectorX: sec.SX,
				SectorY: sec.SY,
			})
			if frame != nil {
				s.gw.ConnMgr.SendReliable(connID, frame)
			}
			// Send updated map data for the new sector
			mapFrame := netutil.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_MAP_DATA), &gamepb.MapDataMsg{
				Stations: s.gw.CollectStationMapData(),
			})
			if mapFrame != nil {
				s.gw.ConnMgr.SendReliable(connID, mapFrame)
			}
		}
	}

	// Process cross-node transfers (outside of query iteration)
	for _, t := range transfers {
		if !s.gw.ECS.Alive(t.entity) {
			continue
		}

		// Update position and sector on the payload to reflect the destination
		pos := s.gw.C.Position.Get(t.entity)
		sec := s.gw.C.SectorCoord.Get(t.entity)

		// Normalize position for the destination sector
		newX, newY := pos.X, pos.Y
		for newX >= coords.SectorSize {
			newX -= coords.SectorSize
		}
		for newX < 0 {
			newX += coords.SectorSize
		}
		for newY >= coords.SectorSize {
			newY -= coords.SectorSize
		}
		for newY < 0 {
			newY += coords.SectorSize
		}

		// Serialize entity, then override position/sector for destination
		payload := s.gw.SerializeEntity(t.entity)
		payload.Position.X = newX
		payload.Position.Y = newY
		payload.Sector = mmokit.SectorCoord{SX: t.newSector.SX, SY: t.newSector.SY}

		isPlayer := s.gw.C.PlayerConn.HasAll(t.entity)
		var connID uint32
		var username string
		if isPlayer {
			connID = s.gw.C.PlayerConn.Get(t.entity).ConnID
			username = s.gw.Players.Usernames[connID]
		}

		s.gw.Log.Log(game.CatTransfer, "cross-node transfer: netID=%d type=%d dest=%s sector=(%d,%d) player=%s",
			payload.NetworkID, payload.EntityType, t.destNodeID, t.newSector.SX, t.newSector.SY, username)

		// Marshal to bytes for the generic bridge
		transferBytes, err := game.MarshalTransferPayload(payload)
		if err != nil {
			s.gw.Log.Log(game.CatTransfer, "failed to marshal transfer: %v", err)
			continue
		}

		// Convert to ghost (keep visible on source for visual continuity)
		s.gw.C.Ghost.Add(t.entity, &mmokit.Ghost{
			TTL:        10,
			DestNodeID: t.destNodeID,
		})

		// Send serialized transfer payload to destination node BEFORE routing
		// the connection. This ensures the entity arrives in the dest node's
		// inbox before the dest node starts sending world updates to this client.
		s.gw.Bridge.SendTransfer(t.destNodeID, transferBytes, payload.NetworkID)

		// Remove from active player tracking on this node (ghost is not playable)
		if isPlayer {
			delete(s.gw.Players.Entities, connID)
			delete(s.gw.Players.Usernames, connID)

			// Revert position so the ghost stays at the boundary
			// (don't normalize — ghost keeps its current position)
			_ = sec // sector stays unchanged for ghost

			// Notify coordinator of player routing change (routes connection to dest node)
			s.gw.Bridge.OnPlayerTransfer(connID, t.destNodeID)
		}
	}
}
