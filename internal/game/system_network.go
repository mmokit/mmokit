package game

import (
	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// NetworkSystem wraps the generic ReplicationSystem with game-specific
// lifecycle handling (reverse lock map, PlayerOwnState, auto-broadcast
// typed events).
type NetworkSystem struct {
	mmokit.SystemBase[*GameWorld]
	replSys *mmokit.ReplicationSystem
	ctx     *gameNetContext

	locks mmokit.Query[struct {
		Lock  *gamecomp.TargetLock
		NetID *mmokit.NetworkID
	}]

	lockVictims mmokit.Query[struct {
		LB *gamecomp.LockedBy
	}]

	// Per-tick shared data hoisted outside the per-viewer loop
	pendingBroadcasts []mmokit.BroadcastEvent
}

func (s *NetworkSystem) Init() {
	gw := s.World()

	s.ctx = &gameNetContext{
		lockedBy: make(map[ecs.Entity]lockerInfo),
	}

	s.locks.With(mmokit.IncludeAll())
	s.lockVictims.With(mmokit.IncludeAll())

	// Build replicators from EntityKindDefs (auto-discovery).
	defs := gw.EntityKindDefs()
	defSlice := make([]mmokit.EntityKindDef, 0, len(defs))
	for _, d := range defs {
		defSlice = append(defSlice, *d)
	}
	replicators := mmokit.BuildReplicators(gw.ECSWorld(), gw.Process(), defSlice...)

	// Process is nil in some unit tests (newTestCell wires Stage with
	// a nil coordinator); guard the ClusterClock lookup accordingly. In
	// that fallback path the ReplicationSystem stamps with the local wall
	// clock — acceptable for single-process tests, never correct across
	// hosts.
	var clock mmokit.ClusterClock
	if p := gw.Process(); p != nil {
		clock = p.ClusterClock
	}
	cfg := mmokit.DefaultReplicationConfig(gw.eng, gw.Spatial, clock)
	// StateDocked players keep their entity (Dormant + at station center), so
	// they're still a valid viewer with a position. Including them in the
	// viewer list keeps WorldUpdateMsg flowing — tick counter, AoI of
	// other ships flying past the station — so the HUD stays alive while
	// the player is hangared.
	cfg.Viewers = mmokit.NewPlayerViewerSource(gw.eng.ECS, gw.Players, mmokit.StateActive, StateDocking, StateDocked)
	cfg.Replicators = replicators
	cfg.AoIRadius = gw.Config.AoIRadius
	cfg.GetAoIRadius = func() float32 { return gw.Config.AoIRadius }
	cfg.FullRefreshInterval = gw.FullRefreshInterval
	cfg.RemovedIDs = func() []uint32 { return gw.eng.RemovedNetIDs }
	cfg.OnBeforeTick = s.beforeTick
	cfg.OnBeforeSend = s.beforeSend
	cfg.OnAfterSend = s.afterSend
	cfg.OnAfterTick = s.afterTick
	s.replSys = mmokit.NewReplicationSystem(cfg)
}

func (s *NetworkSystem) Update(dt float32) {
	s.replSys.Update(dt)
}

// beforeTick builds the reverse lock map, syncs the LockedBy component on all
// lockable entities, and hoists per-tick lookups.
func (s *NetworkSystem) beforeTick(tick uint32) {
	gw := s.World()

	// Build reverse lock map: for each entity being locked, track the most-
	// progressed locker. Resolve via TargetNetID (not the locker's TargetEntity)
	// — the cross-cell codec skips ecs.Entity fields, so on a border replica of
	// the locker that handle is zero. NetID lookup gives the local entity in
	// either case, which is what lets LockedBy populate over a cell line.
	clear(s.ctx.lockedBy)
	for _, b := range s.locks.Iter {
		if b.Lock.TargetNetID == 0 || b.Lock.Progress <= 0 {
			continue
		}
		targetE := mmokit.EntityByNetID(gw.Stage, b.Lock.TargetNetID)
		if !targetE.Alive() {
			continue
		}
		target := targetE.Handle()
		if existing, ok := s.ctx.lockedBy[target]; !ok || b.Lock.Progress > existing.progress {
			s.ctx.lockedBy[target] = lockerInfo{netID: b.NetID.ID, progress: b.Lock.Progress}
		}
	}

	// Sync LockedBy component on every lockable entity. Zero first, then
	// populate from the reverse map. Entities with LockedBy that aren't in
	// the map get zeroed out — the client reads LockerNetID==0 as "not locked".
	// Log only on locker transitions to avoid per-tick spam.
	for e, b := range s.lockVictims.Iter {
		if info, ok := s.ctx.lockedBy[e]; ok {
			if b.LB.LockerNetID != info.netID {
				gw.eng.Log.Log(CatCombatLock, "lockedBy acquired: locker=%d progress=%.2f",
					info.netID, info.progress)
			}
			b.LB.LockerNetID = info.netID
			b.LB.LockerProgress = info.progress
		} else {
			if b.LB.LockerNetID != 0 {
				gw.eng.Log.Log(CatCombatLock, "lockedBy released: prev_locker=%d", b.LB.LockerNetID)
			}
			b.LB.LockerNetID = 0
			b.LB.LockerProgress = 0
		}
	}

	// Drain the per-stage auto-broadcast queue: each event carries an opaque
	// reflect-codec body + a list of anchor NetIDs whose positions drive the
	// per-viewer AoI filter applied in afterSend.
	s.pendingBroadcasts = gw.Stage.BroadcastQueue().Drain()
}

// beforeSend sends PlayerOwnState per viewer.
func (s *NetworkSystem) beforeSend(viewer *mmokit.ViewerInfo, visible map[uint32]bool) {
	gw := s.World()

	// Send own-entity state.
	if sess := gw.Players.ByConnID(viewer.ConnID); sess != nil && sess.State == mmokit.StateActive && gw.Stage.ECSWorld().Alive(sess.Entity) {
		s.sendOwnState(viewer.ConnID, sess.Entity)
	}
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

	gw := s.World()
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
func (s *NetworkSystem) sendOwnState(connID uint32, entity ecs.Entity) {
	gw := s.World()
	e := mmokit.EntityFromECS(gw.Stage, entity)

	msg := &PlayerOwnState{}

	// Lock-on state
	if lock := mmokit.Get[gamecomp.TargetLock](e); lock != nil {
		msg.LockProgress = lock.Progress
		msg.LockTargetID = lock.TargetNetID
	}

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

	// Being-locked state from reverse map
	if lb, ok := s.ctx.lockedBy[entity]; ok {
		msg.BeingLockedByID = lb.netID
		msg.BeingLockedByProgress = lb.progress
	}

	mmokit.SendEvent(gw.Stage, connID, msg)
}
