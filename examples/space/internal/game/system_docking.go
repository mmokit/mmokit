package game

import (
	"math"

	gamecomp "github.com/zenion/mmokit/examples/space/internal/component"
	"github.com/zenion/mmokit/pkg/mmokit"
)

// stationInfo is a tiny snapshot of a station entity used by startDockingFor.
type stationInfo struct {
	x, y   float32
	radius float32
	netID  uint32
}

// startDockingFor processes a single client Dock request. Dispatched via
// Commands.Defer from the Dock input handler, so it runs at the next
// per-system flush boundary with the ECS world unlocked. Finds the nearest
// station whose surface is within DockRange of the ship, transitions the
// session to StateDocking, and records a DockingProgress entry the
// DockingSystem then ticks each frame.
func (gw *GameWorld) startDockingFor(connID uint32) {
	sess := gw.Players.ByConnID(connID)
	if sess == nil {
		return
	}
	// Already docking or docked?
	if sess.State == StateDocking || sess.State == StateDocked {
		return
	}
	if sess.State != mmokit.StateActive {
		return
	}

	entity := mmokit.EntityFromECS(gw.stage, sess.Entity)
	if !entity.Alive() {
		return
	}

	pos := mmokit.Get[mmokit.Position](entity)
	if pos == nil {
		return
	}

	// Collect station positions + radii (rare event — recomputed per call).
	var stations []stationInfo
	mmokit.ForEach2(gw.stage, func(e mmokit.Entity, _ *gamecomp.Station, sp *mmokit.Position) {
		netID := uint32(0)
		if n := mmokit.Get[mmokit.NetworkID](e); n != nil {
			netID = n.ID
		}
		radius := float32(0)
		if c := mmokit.Get[mmokit.Collider](e); c != nil {
			radius = c.Radius
		}
		stations = append(stations, stationInfo{x: sp.X, y: sp.Y, radius: radius, netID: netID})
	})

	// Find nearest station whose surface is within DockRange of the ship.
	// Threshold scales with station radius so large stations remain dockable
	// without the ship having to clip through their collider.
	var nearest *stationInfo
	bestDist2 := math.MaxFloat64
	for i := range stations {
		dx := float64(pos.X - stations[i].x)
		dy := float64(pos.Y - stations[i].y)
		d2 := dx*dx + dy*dy
		threshold := float64(stations[i].radius) + float64(gw.Config.DockRange)
		if d2 <= threshold*threshold && d2 < bestDist2 {
			bestDist2 = d2
			nearest = &stations[i]
		}
	}
	if nearest == nil {
		return
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

	// Deactivate move target immediately.
	if mt := mmokit.Get[mmokit.MoveTarget](entity); mt != nil {
		mt.Active = false
	}

	mmokit.SendEvent(gw.stage, connID, &DockingState{
		Docking:   true,
		Progress:  0,
		TotalTime: gw.Config.DockTime,
		StationID: nearest.netID,
	})
	gw.eng.Log.Log(CatPlayerDock, "docking started: conn=%d station_net_id=%d", connID, nearest.netID)
}

// DockingSystem handles the docking sequence: tractor beam physics, progress
// tracking, and docking state transitions. Initial dock-request handling has
// moved to GameWorld.startDockingFor, dispatched via Commands.Defer from the
// Dock input handler.
type DockingSystem struct {
	mmokit.SystemBase
	gw *GameWorld
}

func (s *DockingSystem) Init() {
	s.gw = mmokit.State[GameWorld](s.Stage())
}

func (s *DockingSystem) Update(dt float32) {
	gw := s.gw

	// Tick docking timers — tractor beam physics
	dragFactor := float32(math.Exp(float64(-gw.Config.DockDragCoeff * dt)))

	gw.Players.ForEach(StateDocking, func(sess *mmokit.PlayerSession) {
		ds := gw.dockingStates[sess.Username]
		if ds == nil {
			return
		}

		entity := mmokit.EntityFromECS(gw.stage, sess.Entity)
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
	gw := s.gw
	mmokit.SendEvent(gw.stage, connID, &DockingState{
		Docking:   docking,
		Progress:  progress,
		TotalTime: totalTime,
		StationID: stationID,
	})
}
