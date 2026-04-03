package system

import (
	"math"

	"github.com/mlange-42/ark/ecs"
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// DockingSystem handles the docking sequence: tractor beam physics, progress
// tracking, and docking state transitions.
type DockingSystem struct {
	mmokit.SystemBase
	gw            *game.GameWorld
	stationFilter *ecs.Filter3[gamecomp.Station, mmokit.Position, mmokit.NetworkID]
}

type stationInfo struct {
	x, y  float32
	netID uint32
}

func (s *DockingSystem) Init() {
	s.gw = unwrapGW(s.GameWorld())
	s.stationFilter = ecs.NewFilter3[gamecomp.Station, mmokit.Position, mmokit.NetworkID](s.ECSWorld()).Without(ecs.C[mmokit.Ghost](), ecs.C[mmokit.Replica]())
}

func (s *DockingSystem) Update(dt float32) {
	gw := s.gw

	// Collect station positions
	var stations []stationInfo
	stationQuery := s.stationFilter.Query()
	for stationQuery.Next() {
		_, pos, netID := stationQuery.Get()
		stations = append(stations, stationInfo{x: pos.X, y: pos.Y, netID: netID.ID})
	}

	dockRange2 := float64(gw.Config.DockRange) * float64(gw.Config.DockRange)

	// Process new dock requests
	for _, req := range mmokit.Drain[game.PendingDockRequest](gw.Queue) {
		sess := gw.Players.ByConnID(req.ConnID)
		if sess == nil {
			continue
		}
		// Already docking or docked?
		if sess.State == game.StateDocking || sess.State == game.StateDocked {
			continue
		}
		if sess.State != mmokit.StateActive {
			continue
		}

		entity := sess.Entity
		if !gw.ECS.Alive(entity) {
			continue
		}

		pos := gw.C.Position.Get(entity)

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

		// Start docking — transition to StateDocking and store DockingState in session.Data
		sess.Data = &game.DockingState{
			Remaining:    gw.Config.DockTime,
			StationX:     nearest.x,
			StationY:     nearest.y,
			StationNetID: nearest.netID,
		}
		gw.Players.Transition(sess, game.StateDocking)

		// Deactivate move target and direction input immediately
		if gw.C.MoveTarget.HasAll(entity) {
			gw.C.MoveTarget.Get(entity).Active = false
		}
		if gw.C.PlayerInput.HasAll(entity) {
			gw.C.PlayerInput.Get(entity).DirActive = false
		}

		s.sendDockingState(req.ConnID, true, 0, gw.Config.DockTime, nearest.netID)
		gw.Log.Log(game.CatPlayerDock, "docking started: conn=%d station_net_id=%d", req.ConnID, nearest.netID)
	}

	// Tick docking timers — tractor beam physics
	dragFactor := float32(math.Exp(float64(-gw.Config.DockDragCoeff * dt)))

	gw.Players.ForEach(game.StateDocking, func(sess *mmokit.PlayerSession) {
		ds, ok := sess.Data.(*game.DockingState)
		if !ok || ds == nil {
			return
		}

		entity := sess.Entity
		if !gw.ECS.Alive(entity) {
			return
		}

		// Deactivate move target each tick (prevent input override)
		if gw.C.MoveTarget.HasAll(entity) {
			gw.C.MoveTarget.Get(entity).Active = false
		}

		pos := gw.C.Position.Get(entity)
		vel := gw.C.Velocity.Get(entity)

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
		s.sendDockingState(sess.ConnID, true, progress, gw.Config.DockTime, ds.StationNetID)
	})
}

func (s *DockingSystem) sendDockingState(connID uint32, docking bool, progress float32, totalTime float32, stationID uint32) {
	data := mmokit.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_DOCKING_STATE), &gamepb.DockingStateMsg{
		Docking:   docking,
		Progress:  progress,
		TotalTime: totalTime,
		StationId: stationID,
	})
	if data != nil {
		s.gw.ConnMgr.SendReliable(connID, data)
	}
}
