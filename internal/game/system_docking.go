package game

import (
	"math"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// DockingSystem handles the docking sequence: tractor beam physics, progress
// tracking, and docking state transitions.
type DockingSystem struct {
	mmokit.SystemBase[*GameWorld]
	stations mmokit.Query[struct {
		Station *gamecomp.Station
		Pos     *mmokit.Position
		NetID   *mmokit.NetworkID
	}]
}

type stationInfo struct {
	x, y  float32
	netID uint32
}

func (s *DockingSystem) Update(dt float32) {
	gw := s.World()

	// Collect station positions
	var stations []stationInfo
	for _, b := range s.stations.Iter {
		stations = append(stations, stationInfo{x: b.Pos.X, y: b.Pos.Y, netID: b.NetID.ID})
	}
	dockRange2 := float64(gw.Config.DockRange) * float64(gw.Config.DockRange)

	// Process new dock requests
	for _, req := range mmokit.Drain[PendingDockRequest](gw.Queue) {
		sess := gw.Players.ByConnID(req.ConnID)
		if sess == nil {
			continue
		}
		// Already docking or docked?
		if sess.State == StateDocking || sess.State == StateDocked {
			continue
		}
		if sess.State != mmokit.StateActive {
			continue
		}

		entity := mmokit.EntityFromECS(gw.Stage, sess.Entity)
		if !entity.Alive() {
			continue
		}

		pos := mmokit.Get[mmokit.Position](entity)
		if pos == nil {
			continue
		}

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

		// Start docking — transition to StateDocking and store DockingProgress
		// keyed by username so it survives a mid-dock disconnect/reconnect
		// (the new ConnID inherits the same in-flight DockingProgress).
		gw.dockingStates[sess.Username] = &DockingProgress{
			Remaining:    gw.Config.DockTime,
			StationX:     nearest.x,
			StationY:     nearest.y,
			StationNetID: nearest.netID,
		}
		gw.Players.Transition(sess, StateDocking)

		// Deactivate move target immediately
		if mt := mmokit.Get[mmokit.MoveTarget](entity); mt != nil {
			mt.Active = false
		}

		s.sendDockingState(req.ConnID, true, 0, gw.Config.DockTime, nearest.netID)
		gw.eng.Log.Log(CatPlayerDock, "docking started: conn=%d station_net_id=%d", req.ConnID, nearest.netID)
	}

	// Tick docking timers — tractor beam physics
	dragFactor := float32(math.Exp(float64(-gw.Config.DockDragCoeff * dt)))

	gw.Players.ForEach(StateDocking, func(sess *mmokit.PlayerSession) {
		ds := gw.dockingStates[sess.Username]
		if ds == nil {
			return
		}

		entity := mmokit.EntityFromECS(gw.Stage, sess.Entity)
		if !entity.Alive() {
			return
		}

		// Deactivate move target each tick (prevent input override)
		if mt := mmokit.Get[mmokit.MoveTarget](entity); mt != nil {
			mt.Active = false
		}

		pos := mmokit.Get[mmokit.Position](entity)
		vel := mmokit.Get[mmokit.Velocity](entity)
		if pos == nil || vel == nil {
			return
		}

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
	gw := s.World()
	mmokit.SendEvent(gw.Stage, connID, &DockingState{
		Docking:   docking,
		Progress:  progress,
		TotalTime: totalTime,
		StationID: stationID,
	})
}
