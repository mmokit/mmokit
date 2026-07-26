package game

import (
	"math"
	"time"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// NetworkSystem wraps the generic ReplicationSystem with game-specific
// lifecycle handling (PlayerOwnState, auto-broadcast typed events).
type NetworkSystem struct {
	mmokit.SystemBase
	gw      *GameWorld
	replSys *mmokit.ReplicationSystem
	clock   mmokit.ClusterClock

	lockVictims mmokit.Query[struct {
		LB *gamecomp.LockedBy
	}]

	// Per-tick shared data hoisted outside the per-viewer loop
	pendingBroadcasts []mmokit.BroadcastEvent
}

func (s *NetworkSystem) Init() {
	s.gw = mmokit.State[GameWorld](s.Stage())
	gw := s.gw

	s.lockVictims.With(mmokit.IncludeAll())

	// Build replicators from EntityKindDefs (auto-discovery).
	defs := gw.stage.EntityKindDefs()
	defSlice := make([]mmokit.EntityKindDef, 0, len(defs))
	for _, d := range defs {
		defSlice = append(defSlice, *d)
	}
	replicators := mmokit.BuildReplicators(gw.stage.ECSWorld(), gw.stage.Process(), defSlice...)

	// Process is nil in some unit tests (newTestCell wires Stage with
	// a nil coordinator); guard the ClusterClock lookup accordingly. In
	// that fallback path the ReplicationSystem stamps with the local wall
	// clock — acceptable for single-process tests, never correct across
	// hosts.
	var clock mmokit.ClusterClock
	if p := gw.stage.Process(); p != nil {
		clock = p.ClusterClock
	}
	s.clock = clock
	cfg := mmokit.DefaultReplicationConfig(gw.eng, gw.Spatial, clock)
	// Dead and docked players keep their entities, so they remain valid viewers
	// with positions. Keeping those connected lifecycle states in the viewer
	// list preserves one ordered replication stream while their own Dormant
	// entity stays hidden from other players.
	cfg.Viewers = mmokit.NewPlayerViewerSource(
		gw.eng.ECS,
		gw.Players,
		mmokit.StateActive,
		StateDead,
		StateDocking,
		StateDocked,
	)
	cfg.Replicators = replicators
	cfg.AoIRadius = gw.Config.AoIRadius
	cfg.GetAoIRadius = func() float32 { return gw.Config.AoIRadius }
	cfg.FullRefreshInterval = gw.FullRefreshInterval
	cfg.RemovedIDs = gw.eng.SampleRemovedNetIDs
	cfg.OnBeforeTick = s.beforeTick
	cfg.OnBeforeSend = s.beforeSend
	cfg.OnAfterSend = s.afterSend
	cfg.OnAfterTick = s.afterTick
	cfg.ProcessedInputSeq = func(viewer *mmokit.ViewerInfo) (uint32, bool) {
		if !gw.stage.ECSWorld().Alive(viewer.Entity) {
			return 0, false
		}
		entity := mmokit.EntityFromECS(gw.stage, viewer.Entity)
		mt := mmokit.Get[mmokit.MoveTarget](entity)
		if mt == nil || mt.Sequence == 0 {
			return 0, false
		}
		return mt.Sequence, true
	}
	s.replSys = mmokit.NewReplicationSystem(cfg)
}

func (s *NetworkSystem) Update(dt float32) {
	s.replSys.Update(dt)
}

// beforeTick zeros LockedBy on every lockable entity (the selection-model
// rewrite removed the producer side; the field stays on the wire so clients
// don't see stale state) and drains the per-stage auto-broadcast queue.
func (s *NetworkSystem) beforeTick(tick uint32) {
	gw := s.gw

	// LockedBy has no producer post-TargetLock removal — always-zero. Keep
	// the iteration so any stale value on a freshly-transferred entity is
	// cleared promptly.
	for _, b := range s.lockVictims.Iter {
		b.LB.LockerNetID = 0
		b.LB.LockerProgress = 0
	}

	// Drain the per-stage auto-broadcast queue: each event carries an opaque
	// reflect-codec body + a list of anchor NetIDs whose positions drive the
	// per-viewer AoI filter applied in afterSend.
	s.pendingBroadcasts = gw.stage.BroadcastQueue().Drain()
}

// beforeSend sends PlayerOwnState per viewer.
func (s *NetworkSystem) beforeSend(viewer *mmokit.ViewerInfo, visible map[uint32]bool) {
	gw := s.gw

	// Send an authoritative movement seed for every live viewer state. Docking
	// and docked sessions still consume movement sequence numbers, so omitting
	// their seed would leave the client unable to retire pending commands. The
	// PredictionTicks tells the client how many ShipDynamics steps are safe.
	if sess := gw.Players.ByConnID(viewer.ConnID); sess != nil && gw.stage.ECSWorld().Alive(sess.Entity) {
		entity := mmokit.EntityFromECS(gw.stage, sess.Entity)
		predictionTicks := movementPredictionTicks(sess.State, entity, gw.eng.TickIntervalMs())
		s.sendOwnState(viewer.ConnID, sess.Entity, viewer.StreamEpoch, predictionTicks)
	}
}

const maxMovementPredictionHorizonMs uint64 = 250

func movementPredictionTicks(state mmokit.PlayerState, entity mmokit.Entity, tickIntervalMs uint64) uint8 {
	if state != mmokit.StateActive {
		return 0
	}
	// Supercruise's channel phase roots the ship in a later system, after
	// ShipDynamics has run. The lightweight client predictor deliberately does
	// not reproduce that surrounding system, so it must wait for authority.
	if supercruise := mmokit.Get[gamecomp.Supercruise](entity); supercruise != nil {
		if supercruise.Phase == gamecomp.SupercruiseChanneling {
			return 0
		}
	}
	if tickIntervalMs == 0 {
		return 0
	}

	maxTicks := uint8(1 + (maxMovementPredictionHorizonMs-1)/tickIntervalMs)
	// The seed carries one aggregate speed multiplier. Bound replay to the
	// earliest movement modifier expiry so packet loss cannot extend it past
	// the ShipDynamics step on which authority last consumes it. Repeated
	// float32 subtraction intentionally mirrors StatusEffectSystem.TickDown;
	// ceil(duration/dt) differs at representational boundaries.
	ticks := maxTicks
	if effects := mmokit.Get[gamecomp.StatusEffects](entity); effects != nil {
		if speedMultiplier := EffectiveSpeedMul(effects); math.IsNaN(float64(speedMultiplier)) || math.IsInf(float64(speedMultiplier), 0) {
			return 0
		}
		dt := float32(tickIntervalMs) / 1000
		for _, statusType := range [...]gamecomp.StatusType{
			gamecomp.StatusAfterburner,
			gamecomp.StatusSlow,
			gamecomp.StatusSupercruise,
		} {
			effect := effects.Get(statusType)
			if effect == nil {
				continue
			}
			if math.IsNaN(float64(effect.Duration)) || math.IsNaN(float64(effect.Value)) {
				return 0
			}
			remaining := effect.Duration
			for uses := uint8(1); uses <= maxTicks; uses++ {
				remaining -= dt
				if remaining <= 0 {
					if uses < ticks {
						ticks = uses
					}
					break
				}
			}
		}
	}
	return ticks
}

// afterSend filters auto-broadcast typed events by AoI and writes a single
// batched 0x00 typed-event frame to the viewer. Each broadcast event passes
// if any of its anchor NetIDs is in the viewer's currently-visible set.
//
// Wire format (one frame per viewer per tick):
//
//	[0x00] [typeID:u32 LE] [body_len:u32 LE] [body] ...
//
// Empty-AoI viewers skip the write entirely (encoder returns nil for an
// empty list).
func (s *NetworkSystem) afterSend(viewer *mmokit.ViewerInfo, visible map[uint32]bool) {
	if len(s.pendingBroadcasts) == 0 {
		return
	}

	gw := s.gw
	var passed []mmokit.BroadcastEvent
	for _, evt := range s.pendingBroadcasts {
		for _, nid := range evt.Anchors {
			if visible[nid] {
				passed = append(passed, evt)
				break
			}
		}
	}

	frame := mmokit.EncodeBatchedTypedEventFrame(passed)
	if frame == nil {
		return
	}
	gw.eng.ConnMgr.Send(viewer.ConnID, frame)
}

// afterTick is reserved for end-of-tick post-replication work. The
// auto-broadcast queue was drained in beforeTick (see s.pendingBroadcasts).
func (s *NetworkSystem) afterTick(tick uint32) {
}

// sendOwnState builds and sends a typed PlayerOwnState event to the owning
// player each tick.
func (s *NetworkSystem) sendOwnState(connID uint32, entity mmokit.EntityHandle, streamEpoch uint32, predictionTicks uint8) {
	gw := s.gw
	e := mmokit.EntityFromECS(gw.stage, entity)

	msg := &PlayerOwnState{}
	msg.Movement = s.buildMovementState(e, streamEpoch, predictionTicks)

	// Ability cooldowns
	if abilities := mmokit.Get[gamecomp.AbilitySet](e); abilities != nil {
		for slot := uint32(0); slot < uint32(6); slot++ {
			cd := abilities.Cooldowns[slot]
			if cd > 0 {
				msg.AbilityCooldowns = append(msg.AbilityCooldowns, AbilityCooldownState{
					Slot:      slot,
					Remaining: cd,
					Total:     gw.AbilityCooldownForSlot(e, uint8(slot)),
				})
			}
		}
	}

	// Equipment state
	if eq := mmokit.Get[gamecomp.Equipment](e); eq != nil {
		msg.Equipment = EquipmentState{
			Weapon1:  eq.Weapon1,
			Weapon2:  eq.Weapon2,
			Shield:   eq.Shield,
			Thruster: eq.Thruster,
		}
	}

	// Cargo inventory
	if inv := mmokit.Get[gamecomp.Inventory](e); inv != nil {
		for itemID, qty := range inv.Items {
			if qty > 0 {
				msg.CargoItems = append(msg.CargoItems, InventoryItem{
					ItemID:   itemID,
					Quantity: qty,
				})
			}
		}
		msg.CargoMass = inv.TotalMass()
		msg.MaxCargoMass = inv.MaxMass
	}

	mmokit.SendEvent(gw.stage, connID, msg)
}

func (s *NetworkSystem) buildMovementState(entity mmokit.Entity, streamEpoch uint32, predictionTicks uint8) PlayerMovementState {
	gw := s.gw
	pos := mmokit.Get[mmokit.Position](entity)
	cell := mmokit.Get[mmokit.CellCoord](entity)
	vel := mmokit.Get[mmokit.Velocity](entity)
	rot := mmokit.Get[mmokit.Rotation](entity)
	ship := mmokit.Get[gamecomp.ShipControl](entity)
	target := mmokit.Get[mmokit.MoveTarget](entity)
	netID := mmokit.Get[mmokit.NetworkID](entity)
	if pos == nil || cell == nil || vel == nil || rot == nil || ship == nil || target == nil || netID == nil {
		return PlayerMovementState{}
	}

	producedAtMs := uint64(time.Now().UnixMilli())
	if s.clock != nil {
		producedAtMs = s.clock.TickTime(gw.eng.TickIntervalMs())
	}
	cellSize := coords.CellSize
	return PlayerMovementState{
		Valid:             true,
		PredictionTicks:   predictionTicks,
		Tick:              gw.eng.Tick,
		ProducedAtMs:      producedAtMs,
		EntityNetID:       netID.ID,
		StreamEpoch:       streamEpoch,
		ProcessedSequence: target.Sequence,
		TickIntervalMs:    uint32(gw.eng.TickIntervalMs()),
		WorldX:            pos.X + float32(cell.CellX)*cellSize,
		WorldY:            pos.Y + float32(cell.CellY)*cellSize,
		VelocityX:         vel.X,
		VelocityY:         vel.Y,
		Angle:             rot.Angle,
		AngularVelocity:   ship.AngularVel,
		TargetActive:      target.Active,
		TargetX:           target.LocalX + float32(target.CellX)*cellSize,
		TargetY:           target.LocalY + float32(target.CellY)*cellSize,
		Thrust:            ship.Thrust,
		TurnRate:          ship.TurnRate,
		TurnAccel:         ship.TurnAccel,
		MaxSpeed:          ship.MaxSpeed,
		SpeedMultiplier:   EffectiveSpeedMul(mmokit.Get[gamecomp.StatusEffects](entity)),
		DragCoefficient:   gw.Config.ShipDragCoeff,
		ArrivalDistance:   gw.Config.MoveArrivalDist,
		DecelerationDist:  gw.Config.MoveDecelDist,
	}
}
