package system

import (
	"github.com/mlange-42/ark/ecs"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// NetworkSystem wraps the generic ReplicationSystem with game-specific
// lifecycle handling (reverse lock map, PlayerOwnState, chat, ability events).
type NetworkSystem struct {
	mmokit.SystemBase
	gw      *game.GameWorld
	replSys *mmokit.ReplicationSystem
	ctx     *gameNetContext

	locks mmokit.Query[struct {
		Lock  *mmokit.TargetLock
		NetID *mmokit.NetworkID
	}]

	// Per-tick shared data hoisted outside the per-viewer loop
	pendingChat          []*enginepb.ChatMsg
	pendingAbilityEvents []*gamepb.AbilityCastResultMsg
}

func (s *NetworkSystem) Init() {
	s.gw = unwrapGW(s.GameWorld())
	gw := s.gw

	s.ctx = &gameNetContext{
		lockedBy: make(map[ecs.Entity]lockerInfo),
	}

	s.locks.Init(s, mmokit.IncludeAll())

	// Register entity replicators.
	replicators := mmokit.NewReplicatorRegistry()
	replicators.Register(&ShipNetHandler{gw: gw, ctx: s.ctx})
	replicators.Register(&NpcNetHandler{gw: gw, ctx: s.ctx})
	replicators.Register(&AsteroidNetHandler{gw: gw, ctx: s.ctx})
	replicators.Register(&LootCrateNetHandler{gw: gw, ctx: s.ctx})
	replicators.Register(&StationNetHandler{gw: gw, ctx: s.ctx})

	cfg := mmokit.DefaultReplicationConfig(gw.Engine, gw.Spatial)
	cfg.Replicators = replicators
	cfg.AoIRadius = gw.Config.AoIRadius
	cfg.GetAoIRadius = func() float32 { return gw.Config.AoIRadius }
	cfg.FullRefreshInterval = gw.FullRefreshInterval
	cfg.KilledIDs = func() []uint32 { return gw.RemovedNetIDs }
	cfg.OnBeforeTick = s.beforeTick
	cfg.OnBeforeSend = s.beforeSend
	cfg.OnAfterSend = s.afterSend
	cfg.OnAfterTick = s.afterTick
	s.replSys = mmokit.NewReplicationSystem(cfg)
}

func (s *NetworkSystem) Update(dt float32) {
	s.replSys.Update(dt)
}

// beforeTick builds the reverse lock map and hoists per-tick lookups.
func (s *NetworkSystem) beforeTick(tick uint32) {
	gw := s.gw

	// Build reverse lock map: for each entity being locked, track the most-progressed locker.
	clear(s.ctx.lockedBy)
	for _, b := range s.locks.All() {
		if b.Lock.TargetNetID == 0 || b.Lock.Progress <= 0 {
			continue
		}
		if !gw.ECS.Alive(b.Lock.TargetEntity) {
			continue
		}
		if existing, ok := s.ctx.lockedBy[b.Lock.TargetEntity]; !ok || b.Lock.Progress > existing.progress {
			s.ctx.lockedBy[b.Lock.TargetEntity] = lockerInfo{netID: b.NetID.ID, progress: b.Lock.Progress}
		}
	}

	// Hoist per-tick lookups outside the viewer loop.
	s.pendingChat = mmokit.Peek[*enginepb.ChatMsg](gw.Queue)
	s.pendingAbilityEvents = mmokit.Peek[*gamepb.AbilityCastResultMsg](gw.Queue)
}

// beforeSend sends chat messages reliably and PlayerOwnState per viewer.
func (s *NetworkSystem) beforeSend(viewer *mmokit.ViewerInfo, visible map[uint32]bool) {
	gw := s.gw

	// Send chat messages reliably.
	if len(s.pendingChat) > 0 {
		chatData := mmokit.MakeEvent(uint32(enginepb.ServerEventCode_SE_WORLD_UPDATE), &gamepb.WorldUpdateMsg{
			Tick:         gw.Tick,
			ChatMessages: s.pendingChat,
		})
		if chatData != nil {
			gw.ConnMgr.SendReliable(viewer.ConnID, chatData)
		}
	}

	// Send own-entity state.
	if sess := gw.Players.ByConnID(viewer.ConnID); sess != nil && sess.State == mmokit.StateActive && gw.ECS.Alive(sess.Entity) {
		s.sendOwnState(viewer.ConnID, sess.Entity)
	}
}

// afterSend filters and sends ability events by AoI.
func (s *NetworkSystem) afterSend(viewer *mmokit.ViewerInfo, visible map[uint32]bool) {
	if len(s.pendingAbilityEvents) == 0 {
		return
	}

	gw := s.gw
	var abilityEvents []*gamepb.AbilityCastResultMsg
	for _, evt := range s.pendingAbilityEvents {
		if visible[evt.CasterId] || visible[evt.TargetId] {
			abilityEvents = append(abilityEvents, evt)
		}
	}

	if len(abilityEvents) > 0 {
		data := mmokit.MakeEvent(uint32(enginepb.ServerEventCode_SE_WORLD_UPDATE), &gamepb.WorldUpdateMsg{
			Tick:          gw.Tick,
			AbilityEvents: abilityEvents,
		})
		if data != nil {
			gw.ConnMgr.Send(viewer.ConnID, data)
		}
	}
}

// afterTick drains queues and sends chat to docked players.
func (s *NetworkSystem) afterTick(tick uint32) {
	gw := s.gw

	// Send chat messages to docked players (they have no entity in the AoI loop).
	if len(s.pendingChat) > 0 {
		gw.Players.ForEach(game.StateDocked, func(sess *mmokit.PlayerSession) {
			chatData := mmokit.MakeEvent(uint32(enginepb.ServerEventCode_SE_WORLD_UPDATE), &gamepb.WorldUpdateMsg{
				Tick:         gw.Tick,
				ChatMessages: s.pendingChat,
			})
			if chatData != nil {
				gw.ConnMgr.SendReliable(sess.ConnID, chatData)
			}
		})
	}

	// Drain chat and ability events after broadcasting to all players.
	mmokit.Drain[*enginepb.ChatMsg](gw.Queue)
	mmokit.Drain[*gamepb.AbilityCastResultMsg](gw.Queue)
}

// sendOwnState builds and sends PlayerOwnStateMsg to the owning player each tick.
func (s *NetworkSystem) sendOwnState(connID uint32, entity ecs.Entity) {
	gw := s.gw

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

	data := mmokit.MakeEvent(uint32(enginepb.ServerEventCode_SE_PLAYER_OWN_STATE), msg)
	if data != nil {
		gw.ConnMgr.Send(connID, data)
	}
}
