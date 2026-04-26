package game

import (
	"github.com/mlange-42/ark/ecs"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// NetworkSystem wraps the generic ReplicationSystem with game-specific
// lifecycle handling (reverse lock map, PlayerOwnState, chat, ability events).
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
	pendingChat          []*enginepb.ChatMsg
	pendingAbilityEvents []*gamepb.AbilityCastResultMsg
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

	// Process is nil in some unit tests (newTestCell wires WorldBase with
	// a nil coordinator); guard the ClusterClock lookup accordingly. In
	// that fallback path the ReplicationSystem stamps with the local wall
	// clock — acceptable for single-process tests, never correct across
	// hosts.
	var clock mmokit.ClusterClock
	if p := gw.Process(); p != nil {
		clock = p.ClusterClock
	}
	cfg := mmokit.DefaultReplicationConfig(gw.eng, gw.Spatial, clock)
	cfg.Viewers = mmokit.NewPlayerViewerSource(gw.eng.ECS, gw.Players, mmokit.StateActive, StateDocking)
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

	// Build reverse lock map: for each entity being locked, track the most-progressed locker.
	clear(s.ctx.lockedBy)
	for _, b := range s.locks.Iter {
		if b.Lock.TargetNetID == 0 || b.Lock.Progress <= 0 {
			continue
		}
		if !gw.eng.ECS.Alive(b.Lock.TargetEntity) {
			continue
		}
		if existing, ok := s.ctx.lockedBy[b.Lock.TargetEntity]; !ok || b.Lock.Progress > existing.progress {
			s.ctx.lockedBy[b.Lock.TargetEntity] = lockerInfo{netID: b.NetID.ID, progress: b.Lock.Progress}
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

	// Hoist per-tick lookups outside the viewer loop.
	s.pendingChat = mmokit.Peek[*enginepb.ChatMsg](gw.Queue)
	s.pendingAbilityEvents = mmokit.Peek[*gamepb.AbilityCastResultMsg](gw.Queue)
}

// beforeSend sends chat messages reliably and PlayerOwnState per viewer.
func (s *NetworkSystem) beforeSend(viewer *mmokit.ViewerInfo, visible map[uint32]bool) {
	gw := s.World()

	// Send chat messages reliably.
	if len(s.pendingChat) > 0 {
		gw.ServerEvents().Send(gw.eng.ConnMgr, viewer.ConnID, uint32(enginepb.ServerEventCode_SE_WORLD_UPDATE), &gamepb.WorldUpdateMsg{
			Tick:         gw.eng.Tick,
			ChatMessages: s.pendingChat,
		})
	}

	// Send own-entity state.
	if sess := gw.Players.ByConnID(viewer.ConnID); sess != nil && sess.State == mmokit.StateActive && gw.eng.ECS.Alive(sess.Entity) {
		s.sendOwnState(viewer.ConnID, sess.Entity)
	}
}

// afterSend filters and sends ability events by AoI.
func (s *NetworkSystem) afterSend(viewer *mmokit.ViewerInfo, visible map[uint32]bool) {
	if len(s.pendingAbilityEvents) == 0 {
		return
	}

	gw := s.World()
	var abilityEvents []*gamepb.AbilityCastResultMsg
	for _, evt := range s.pendingAbilityEvents {
		if visible[evt.CasterId] || visible[evt.TargetId] {
			abilityEvents = append(abilityEvents, evt)
		}
	}

	if len(abilityEvents) > 0 {
		frame := gw.ServerEvents().Build(uint32(enginepb.ServerEventCode_SE_WORLD_UPDATE), &gamepb.WorldUpdateMsg{
			Tick:          gw.eng.Tick,
			AbilityEvents: abilityEvents,
		})
		gw.eng.ConnMgr.Send(viewer.ConnID, frame)
	}
}

// afterTick drains queues and sends chat to docked players.
func (s *NetworkSystem) afterTick(tick uint32) {
	gw := s.World()

	// Send chat messages to docked players (they have no entity in the AoI loop).
	if len(s.pendingChat) > 0 {
		chatFrame := gw.ServerEvents().Build(uint32(enginepb.ServerEventCode_SE_WORLD_UPDATE), &gamepb.WorldUpdateMsg{
			Tick:         gw.eng.Tick,
			ChatMessages: s.pendingChat,
		})
		gw.Players.ForEach(StateDocked, func(sess *mmokit.PlayerSession) {
			gw.eng.ConnMgr.SendReliable(sess.ConnID, chatFrame)
		})
	}

	// Drain chat and ability events after broadcasting to all players.
	mmokit.Drain[*enginepb.ChatMsg](gw.Queue)
	mmokit.Drain[*gamepb.AbilityCastResultMsg](gw.Queue)
}

// sendOwnState builds and sends PlayerOwnStateMsg to the owning player each tick.
func (s *NetworkSystem) sendOwnState(connID uint32, entity ecs.Entity) {
	gw := s.World()

	msg := &gamepb.PlayerOwnStateMsg{}

	// Lock-on state
	if gw.C.TargetLock.HasAll(entity) {
		lock := gw.C.TargetLock.Get(entity)
		msg.LockProgress = lock.Progress
		msg.LockTargetId = lock.TargetNetID
	}

	// Ability cooldowns
	if gw.C.AbilitySet.HasAll(entity) {
		abilities := gw.C.AbilitySet.Get(entity)
		for slot := uint32(0); slot < uint32(6); slot++ {
			cd := abilities.Cooldowns[slot]
			if cd > 0 {
				msg.AbilityCooldowns = append(msg.AbilityCooldowns, &gamepb.AbilityCooldownState{
					Slot:      slot,
					Remaining: cd,
					Total:     gw.AbilityCooldownForSlot(entity, uint8(slot)),
				})
			}
		}
	}

	// Equipment state
	if gw.C.Equipment.HasAll(entity) {
		eq := gw.C.Equipment.Get(entity)
		msg.Equipment = &gamepb.EquipmentState{
			Weapon1:  eq.Weapon1,
			Weapon2:  eq.Weapon2,
			Shield:   eq.Shield,
			Thruster: eq.Thruster,
		}
	}

	// Cargo inventory
	if gw.C.Inventory.HasAll(entity) {
		inv := gw.C.Inventory.Get(entity)
		for itemID, qty := range inv.Items {
			if qty > 0 {
				msg.CargoItems = append(msg.CargoItems, &gamepb.InventoryItem{
					ItemId:   itemID,
					Quantity: qty,
				})
			}
		}
		msg.CargoMass = inv.TotalMass()
		msg.MaxCargoMass = inv.MaxMass
	}

	// Being-locked state from reverse map
	if lb, ok := s.ctx.lockedBy[entity]; ok {
		msg.BeingLockedById = lb.netID
		msg.BeingLockedByProgress = lb.progress
	}

	gw.eng.ConnMgr.Send(connID, gw.ServerEvents().Build(uint32(enginepb.ServerEventCode_SE_PLAYER_OWN_STATE), msg))
}
