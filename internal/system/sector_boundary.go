package system

import (
	"github.com/mlange-42/ark/ecs"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/internal/netutil"
	"github.com/zenion/mmoserver/pkg/coords"
)

// SectorBoundarySystem normalizes entity positions into [0, SectorSize)
// and bumps their SectorCoord when they cross a boundary.
// Runs after PhysicsSystem, before SpatialSystem.
type SectorBoundarySystem struct {
	gw     *game.GameWorld
	filter *ecs.Filter2[component.Position, component.SectorCoord]
}

func NewSectorBoundarySystem(gw *game.GameWorld) *SectorBoundarySystem {
	return &SectorBoundarySystem{gw: gw}
}

func (s *SectorBoundarySystem) Update(dt float32) {
	if s.filter == nil {
		s.filter = ecs.NewFilter2[component.Position, component.SectorCoord](s.gw.ECS).Without(ecs.C[component.Ghost](), ecs.C[component.Replica](), ecs.C[component.TransferCooldown]())
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
		if s.gw.SectorOwnerFunc != nil {
			destSector := coords.SectorCoord{SX: newSX, SY: newSY}
			destNodeID := s.gw.SectorOwnerFunc(destSector)
			if destNodeID != "" && destNodeID != s.gw.NodeID {
				// Cross-node transfer: DON'T normalize, collect for post-iteration
				entity := query.Entity()
				transfers = append(transfers, pendingTransfer{
					entity:     entity,
					newSector:  destSector,
					destNodeID: destNodeID,
				})
				continue
			}
		}

		// Same-node sector change: normalize position
		pos.X = newX
		pos.Y = newY
		sec.SX = newSX
		sec.SY = newSY

		// If this is a player entity, notify the client
		entity := query.Entity()
		if s.gw.PlayerConnMap.HasAll(entity) {
			connID := s.gw.PlayerConnMap.Get(entity).ConnID
			username := s.gw.ConnToUsername[connID]
			s.gw.Log.Log(game.CatMap, "sector change: player=%s from=(%d,%d) to=(%d,%d)", username, oldSX, oldSY, sec.SX, sec.SY)
			frame := netutil.MakeEvent(uint32(gamepb.ServerEventCode_SE_SECTOR_CHANGE), &gamepb.SectorChangeMsg{
				SectorX: sec.SX,
				SectorY: sec.SY,
			})
			if frame != nil {
				s.gw.ConnMgr.SendReliable(connID, frame)
			}
			// Send updated map data for the new sector
			mapFrame := netutil.MakeEvent(uint32(gamepb.ServerEventCode_SE_MAP_DATA), &gamepb.MapDataMsg{
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
		pos := s.gw.PositionMap.Get(t.entity)
		sec := s.gw.SectorCoordMap.Get(t.entity)

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

		// Serialize before modifying the entity
		payload := s.gw.SerializeEntity(t.entity)
		// Set the normalized position and destination sector on the payload
		payload.Position.X = newX
		payload.Position.Y = newY
		payload.Sector = component.SectorCoord{SX: t.newSector.SX, SY: t.newSector.SY}

		isPlayer := s.gw.PlayerConnMap.HasAll(t.entity)
		var connID uint32
		var username string
		if isPlayer {
			connID = s.gw.PlayerConnMap.Get(t.entity).ConnID
			username = s.gw.ConnToUsername[connID]
		}

		s.gw.Log.Log(game.CatTransfer, "cross-node transfer: netID=%d type=%d dest=%s sector=(%d,%d) player=%s",
			payload.NetworkID, payload.EntityType, t.destNodeID, t.newSector.SX, t.newSector.SY, username)

		// Convert to ghost (keep visible on source for visual continuity)
		s.gw.GhostMap.Add(t.entity, &component.Ghost{
			TTL:        10,
			DestNodeID: t.destNodeID,
		})

		// Remove from active player tracking on this node (ghost is not playable)
		if isPlayer {
			delete(s.gw.PlayerEntities, connID)
			delete(s.gw.ConnToUsername, connID)

			// Revert position so the ghost stays at the boundary
			// (don't normalize — ghost keeps its current position)
			_ = sec // sector stays unchanged for ghost

			// Notify coordinator of player routing change
			if s.gw.OnPlayerTransfer != nil {
				s.gw.OnPlayerTransfer(connID, t.destNodeID)
			}
		}

		// Send transfer payload to destination node
		if s.gw.SendTransfer != nil {
			s.gw.SendTransfer(t.destNodeID, payload)
		}
	}
}
