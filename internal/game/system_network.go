package game

import (
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// NetworkSystem wraps the generic ReplicationSystem with game-specific
// lifecycle handling (PlayerOwnState, auto-broadcast typed events).
type NetworkSystem struct {
	mmokit.SystemBase
	gw       *GameWorld
	replSys *mmokit.ReplicationSystem

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

	// Send own-entity state.
	if sess := gw.Players.ByConnID(viewer.ConnID); sess != nil && sess.State == mmokit.StateActive && gw.stage.ECSWorld().Alive(sess.Entity) {
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
func (s *NetworkSystem) sendOwnState(connID uint32, entity mmokit.EntityHandle) {
	gw := s.gw
	e := mmokit.EntityFromECS(gw.stage, entity)

	msg := &PlayerOwnState{}

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
