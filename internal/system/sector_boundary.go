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
		s.filter = ecs.NewFilter2[component.Position, component.SectorCoord](s.gw.ECS)
	}

	query := s.filter.Query()
	for query.Next() {
		pos, sec := query.Get()

		oldSX, oldSY := sec.SX, sec.SY

		// Normalize position into [0, SectorSize)
		for pos.X >= coords.SectorSize {
			pos.X -= coords.SectorSize
			sec.SX++
		}
		for pos.X < 0 {
			pos.X += coords.SectorSize
			sec.SX--
		}
		for pos.Y >= coords.SectorSize {
			pos.Y -= coords.SectorSize
			sec.SY++
		}
		for pos.Y < 0 {
			pos.Y += coords.SectorSize
			sec.SY--
		}

		// If sector changed and this is a player entity, notify the client
		if sec.SX != oldSX || sec.SY != oldSY {
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
	}
}
