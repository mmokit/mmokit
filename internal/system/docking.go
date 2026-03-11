package system

import (
	"math"

	"github.com/mlange-42/ark/ecs"
	"google.golang.org/protobuf/proto"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/logger"
)

// DockingSystem handles the docking sequence: tractor beam physics, progress
// tracking, and docking state transitions.
type DockingSystem struct {
	gw            *game.GameWorld
	stationFilter *ecs.Filter3[component.Station, component.Position, component.NetworkID]
}

func NewDockingSystem(gw *game.GameWorld) *DockingSystem {
	return &DockingSystem{gw: gw}
}

type stationInfo struct {
	x, y  float32
	netID uint32
}

func (s *DockingSystem) Update(dt float32) {
	gw := s.gw
	if s.stationFilter == nil {
		s.stationFilter = ecs.NewFilter3[component.Station, component.Position, component.NetworkID](gw.ECS)
	}

	// Collect station positions
	var stations []stationInfo
	stationQuery := s.stationFilter.Query()
	for stationQuery.Next() {
		_, pos, netID := stationQuery.Get()
		stations = append(stations, stationInfo{x: pos.X, y: pos.Y, netID: netID.ID})
	}

	dockRange2 := float64(gw.Config.DockRange) * float64(gw.Config.DockRange)

	// Process new dock requests
	for _, req := range gw.PendingDockRequests {
		// Already docking or docked?
		if gw.DockingPlayers[req.ConnID] != nil || gw.DockedPlayers[req.ConnID] {
			continue
		}

		entity, ok := gw.PlayerEntities[req.ConnID]
		if !ok || !gw.ECS.Alive(entity) {
			continue
		}

		pos := gw.PositionMap.Get(entity)

		// Find nearest station within range
		var nearest *stationInfo
		bestDist2 := math.MaxFloat64
		for i := range stations {
			dx := float64(pos.X - stations[i].x)
			dy := float64(pos.Y - stations[i].y)
			d2 := dx*dx + dy*dy
			if d2 <= dockRange2 && d2 < bestDist2 {
				bestDist2 = d2
				nearest = &stations[i]
			}
		}
		if nearest == nil {
			continue
		}

		// Start docking
		gw.DockingPlayers[req.ConnID] = &game.DockingState{
			Remaining:    gw.Config.DockTime,
			StationX:     nearest.x,
			StationY:     nearest.y,
			StationNetID: nearest.netID,
		}

		// Deactivate move target immediately
		if gw.MoveTargetMap.HasAll(entity) {
			gw.MoveTargetMap.Get(entity).Active = false
		}

		s.sendDockingState(req.ConnID, true, 0, gw.Config.DockTime, nearest.netID)
		gw.Log.Log(logger.CatDock, "docking started: conn=%d station_net_id=%d", req.ConnID, nearest.netID)
	}

	// Tick docking timers — tractor beam physics
	dragFactor := float32(math.Exp(float64(-gw.Config.DockDragCoeff * dt)))

	for connID, ds := range gw.DockingPlayers {
		entity, ok := gw.PlayerEntities[connID]
		if !ok || !gw.ECS.Alive(entity) {
			// Player was killed or disconnected during docking
			delete(gw.DockingPlayers, connID)
			continue
		}

		// Deactivate move target each tick (prevent input override)
		if gw.MoveTargetMap.HasAll(entity) {
			gw.MoveTargetMap.Get(entity).Active = false
		}

		pos := gw.PositionMap.Get(entity)
		vel := gw.VelocityMap.Get(entity)

		// Exponential velocity decay (heavy drag)
		vel.X *= dragFactor
		vel.Y *= dragFactor

		// Pull toward station center
		dx := ds.StationX - pos.X
		dy := ds.StationY - pos.Y
		dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))
		if dist > 1 {
			nx := dx / dist
			ny := dy / dist
			pull := gw.Config.DockPullStrength * dt
			vel.X += nx * pull
			vel.Y += ny * pull
		}

		// Decrement timer
		ds.Remaining -= dt
		if ds.Remaining <= 0 {
			ds.Remaining = 0
			// processDockCompletions in lifecycle will handle entity removal
		}

		// Send progress update
		progress := 1.0 - ds.Remaining/gw.Config.DockTime
		if progress > 1 {
			progress = 1
		}
		s.sendDockingState(connID, true, progress, gw.Config.DockTime, ds.StationNetID)
	}
}

func (s *DockingSystem) sendDockingState(connID uint32, docking bool, progress float32, totalTime float32, stationID uint32) {
	msg := &gamepb.ServerMessage{
		Msg: &gamepb.ServerMessage_DockingState{
			DockingState: &gamepb.DockingStateMsg{
				Docking:   docking,
				Progress:  progress,
				TotalTime: totalTime,
				StationId: stationID,
			},
		},
	}
	if data, err := proto.Marshal(msg); err == nil {
		s.gw.ConnMgr.SendReliable(connID, data)
	}
}
