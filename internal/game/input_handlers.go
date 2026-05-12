package game

import (
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// RegisterInputs installs every space-game input handler on the process via
// the typed-message HandleClient[T] surface. Called from GameSetup(coord).
//
// Pattern: each handler runs first-line state-filter via PlayerStateOf, then
// resolves the GameWorld via gameWorldOfEntity. Continuous-state inputs
// mutate the player's components in place; discrete actions queue Pending*
// jobs onto gw.Queue (the existing task queue is preserved as-is — this
// migration is about the input registration surface, not the task queue).
func RegisterInputs(mmo *mmokit.Process) {
	// ─── Continuous-state inputs ──────────────────────────────────────────
	//
	// SetMoveTarget — click-to-move target update. Replaces the
	// MoveTarget portion of the legacy bundled PlayerInputMsg snapshot.
	// Active=false clears the target (player stops at current position).
	mmokit.HandleClient(mmo, func(player mmokit.Entity, msg *SetMoveTarget) {
		state := mmokit.PlayerStateOf(player)
		// StateDocking still acks the sequence but ignores the target —
		// players can't issue movement while the dock animation runs.
		if state != mmokit.StateActive && state != StateDocking {
			return
		}
		input := mmokit.Get[gamecomp.PlayerInput](player)
		if input == nil {
			return
		}
		input.Sequence = msg.Sequence
		if state == StateDocking {
			return
		}
		mt := mmokit.Get[mmokit.MoveTarget](player)
		if mt == nil {
			return
		}
		if msg.Active {
			mt.SetTarget(msg.X, msg.Y)
		} else {
			mt.Active = false
		}
	})

	// LockTarget — request to add a new lock slot.
	mmokit.HandleClient(mmo, func(player mmokit.Entity, msg *LockTarget) {
		gw := gameWorldFromStage(player.Stage())
		lock := mmokit.Get[gamecomp.TargetLock](player)
		if lock == nil || msg.NetID == 0 {
			return
		}
		// Already-locked → no-op (idempotent).
		for _, slot := range lock.Slots {
			if slot.TargetNetID == msg.NetID {
				return
			}
		}
		if uint8(len(lock.Slots)) >= lock.MaxSlots {
			sendLockRejected(gw, player, msg.NetID, LockRejectedSlotsFull)
			return
		}
		targetE := mmokit.EntityByNetID(gw.stage, msg.NetID)
		if !targetE.Alive() {
			sendLockRejected(gw, player, msg.NetID, LockRejectedInvalidTarget)
			return
		}
		if mmokit.Has[mmokit.Dormant](targetE) || mmokit.Has[gamecomp.Leashing](targetE) {
			sendLockRejected(gw, player, msg.NetID, LockRejectedInvalidTarget)
			return
		}
		kindComp := mmokit.Get[mmokit.EntityKind](targetE)
		if kindComp == nil {
			sendLockRejected(gw, player, msg.NetID, LockRejectedInvalidTarget)
			return
		}
		kind := kindComp.Type
		if kind != gamecomp.KindShip && kind != gamecomp.KindNPC && kind != gamecomp.KindAsteroid {
			sendLockRejected(gw, player, msg.NetID, LockRejectedInvalidTarget)
			return
		}
		// Range check
		pos := mmokit.Get[mmokit.Position](player)
		tpos := mmokit.Get[mmokit.Position](targetE)
		if pos == nil || tpos == nil {
			return
		}
		dx, dy := tpos.X-pos.X, tpos.Y-pos.Y
		if dx*dx+dy*dy > lock.Range*lock.Range {
			sendLockRejected(gw, player, msg.NetID, LockRejectedOutOfRange)
			return
		}
		lockTime := gw.Config.LockOnTime
		if kind == gamecomp.KindAsteroid {
			lockTime = gw.Config.MiningLockTime
		}
		lock.Slots = append(lock.Slots, gamecomp.LockSlot{
			TargetNetID:  msg.NetID,
			TargetEntity: targetE.Handle(),
			Progress:     0,
			LockTime:     lockTime,
			Locked:       false,
		})
		gw.eng.Log.Log(CatCombatLock, "lock: started slot=%d player=%d target=%d",
			len(lock.Slots)-1, player.NetID(), msg.NetID)
		sendLockSlots(gw, player)
	})

	// UnlockTarget — remove a slot.
	mmokit.HandleClient(mmo, func(player mmokit.Entity, msg *UnlockTarget) {
		gw := gameWorldFromStage(player.Stage())
		lock := mmokit.Get[gamecomp.TargetLock](player)
		if lock == nil || msg.NetID == 0 {
			return
		}
		removed := false
		for i, slot := range lock.Slots {
			if slot.TargetNetID == msg.NetID {
				lock.Slots = append(lock.Slots[:i], lock.Slots[i+1:]...)
				removed = true
				break
			}
		}
		if !removed {
			return
		}
		if lock.ActiveNetID == msg.NetID {
			lock.ActiveNetID = pickActiveFallback(lock)
		}
		gw.eng.Log.Log(CatCombatLock, "lock: unlocked player=%d target=%d active=%d",
			player.NetID(), msg.NetID, lock.ActiveNetID)
		sendLockSlots(gw, player)
	})

	// SetActiveTarget — switch active.
	mmokit.HandleClient(mmo, func(player mmokit.Entity, msg *SetActiveTarget) {
		gw := gameWorldFromStage(player.Stage())
		lock := mmokit.Get[gamecomp.TargetLock](player)
		if lock == nil {
			return
		}
		if msg.NetID == 0 {
			lock.ActiveNetID = 0
			sendLockSlots(gw, player)
			return
		}
		for _, slot := range lock.Slots {
			if slot.TargetNetID == msg.NetID && slot.Locked {
				lock.ActiveNetID = msg.NetID
				gw.eng.Log.Log(CatCombatLock, "lock: active player=%d -> target=%d",
					player.NetID(), msg.NetID)
				sendLockSlots(gw, player)
				return
			}
		}
		// Slot not present / not locked — silent ignore.
	})

	// CastAbility — discrete ability press. Replaces the ability_cast
	// bitmask in the legacy PlayerInputMsg: slot is OR-ed into the
	// per-tick bitmask consumed by AbilitySystem (which clears it after
	// processing).
	mmokit.HandleClient(mmo, func(player mmokit.Entity, msg *CastAbility) {
		state := mmokit.PlayerStateOf(player)
		if state != mmokit.StateActive && state != StateDocking {
			return
		}
		input := mmokit.Get[gamecomp.PlayerInput](player)
		netID := mmokit.Get[mmokit.NetworkID](player)
		if input == nil {
			return
		}
		input.Sequence = msg.Sequence
		if state == StateDocking {
			return
		}
		if msg.Slot >= gamecomp.AbilityCount {
			return
		}
		input.AbilityCast |= 1 << msg.Slot
		if netID != nil {
			player.Stage().Engine().Log.Log(CatPlayerInput,
				"player=%d cast=0x%x slot=%d seq=%d",
				netID.ID, input.AbilityCast, msg.Slot, input.Sequence)
		}
	})

	// JettisonItem — discrete cargo jettison.
	mmokit.HandleClient(mmo, func(player mmokit.Entity, msg *JettisonItem) {
		state := mmokit.PlayerStateOf(player)
		if state != mmokit.StateActive && state != StateDocking {
			return
		}
		input := mmokit.Get[gamecomp.PlayerInput](player)
		if input == nil {
			return
		}
		input.Sequence = msg.Sequence
		if state == StateDocking {
			return
		}
		input.JettisonItemID = msg.ItemID
	})

	// ─── Discrete request handlers ────────────────────────────────────────
	mmokit.HandleClient(mmo, func(player mmokit.Entity, msg *Dock) {
		if mmokit.PlayerStateOf(player) != mmokit.StateActive {
			return
		}
		gw := gameWorldOfEntity(player)
		if gw == nil {
			return
		}
		conn := mmokit.Get[mmokit.PlayerConn](player)
		if conn == nil {
			return
		}
		connID := conn.ConnID
		gw.stage.Commands().Defer(func() {
			gw.startDockingFor(connID)
		})
	})

	mmokit.HandleClient(mmo, func(player mmokit.Entity, msg *Undock) {
		if mmokit.PlayerStateOf(player) != StateDocked {
			return
		}
		gw := gameWorldOfEntity(player)
		if gw == nil {
			return
		}
		conn := mmokit.Get[mmokit.PlayerConn](player)
		if conn == nil {
			return
		}
		mmokit.Enqueue(gw.Queue, PendingUndockRequest{ConnID: conn.ConnID})
	})

	mmokit.HandleClient(mmo, func(player mmokit.Entity, msg *Respawn) {
		if mmokit.PlayerStateOf(player) != StateDead {
			return
		}
		gw := gameWorldOfEntity(player)
		if gw == nil {
			return
		}
		conn := mmokit.Get[mmokit.PlayerConn](player)
		if conn == nil {
			return
		}
		gw.eng.Log.Log(CatPlayerSpawn, "respawn requested: conn=%d", conn.ConnID)
		mmokit.Enqueue(gw.Queue, PendingRespawn{ConnID: conn.ConnID})
	})

	mmokit.HandleClient(mmo, func(player mmokit.Entity, msg *InventoryTransfer) {
		state := mmokit.PlayerStateOf(player)
		if state != mmokit.StateActive && state != StateDocked {
			return
		}
		gw := gameWorldOfEntity(player)
		if gw == nil {
			return
		}
		conn := mmokit.Get[mmokit.PlayerConn](player)
		if conn == nil {
			return
		}
		mmokit.Enqueue(gw.Queue, PendingTransfer{
			ConnID:  conn.ConnID,
			ItemID:  msg.ItemID,
			Amount:  msg.Quantity,
			Deposit: msg.Deposit,
		})
	})

	// (BankRequest is a typed-op now — see op_bank.go: HandleBankRequest
	// runs on the player's cell engine via Process.DispatchCellRoutedOp.
	// The per-tick PendingBankRequest queue + EconomySystem.processBankRequests
	// drain are deleted — typed-op delivery is synchronous within the loop.)

	mmokit.HandleClient(mmo, func(player mmokit.Entity, msg *Equip) {
		state := mmokit.PlayerStateOf(player)
		if state != mmokit.StateActive && state != StateDocked {
			return
		}
		gw := gameWorldOfEntity(player)
		if gw == nil {
			return
		}
		conn := mmokit.Get[mmokit.PlayerConn](player)
		if conn == nil {
			return
		}
		mmokit.Enqueue(gw.Queue, PendingEquipRequest{
			ConnID:     conn.ConnID,
			ItemID:     msg.ItemID,
			Slot:       item.EquipSlot(msg.Slot),
			TargetBank: msg.TargetBank,
		})
	})

	mmokit.HandleClient(mmo, func(player mmokit.Entity, msg *LootItem) {
		if mmokit.PlayerStateOf(player) != mmokit.StateActive {
			return
		}
		gw := gameWorldOfEntity(player)
		if gw == nil {
			return
		}
		conn := mmokit.Get[mmokit.PlayerConn](player)
		if conn == nil {
			return
		}
		mmokit.Enqueue(gw.Queue, PendingLootItem{
			ConnID:     conn.ConnID,
			CrateNetID: msg.CrateNetID,
			ItemID:     msg.ItemID,
		})
	})

	mmokit.HandleClient(mmo, func(player mmokit.Entity, msg *LootAll) {
		if mmokit.PlayerStateOf(player) != mmokit.StateActive {
			return
		}
		gw := gameWorldOfEntity(player)
		if gw == nil {
			return
		}
		conn := mmokit.Get[mmokit.PlayerConn](player)
		if conn == nil {
			return
		}
		mmokit.Enqueue(gw.Queue, PendingLootAll{
			ConnID:     conn.ConnID,
			CrateNetID: msg.CrateNetID,
		})
	})
}

// pickActiveFallback chooses the most-recently-locked surviving locked
// slot as the new active. Returns 0 if no locked slot exists.
func pickActiveFallback(lock *gamecomp.TargetLock) uint32 {
	for i := len(lock.Slots) - 1; i >= 0; i-- {
		if lock.Slots[i].Locked {
			return lock.Slots[i].TargetNetID
		}
	}
	return 0
}

// sendLockSlots emits a LockSlotsMsg for this player's own slots.
func sendLockSlots(gw *GameWorld, player mmokit.Entity) {
	lock := mmokit.Get[gamecomp.TargetLock](player)
	if lock == nil {
		return
	}
	wire := make([]LockSlotWire, len(lock.Slots))
	for i, s := range lock.Slots {
		wire[i] = LockSlotWire{
			TargetNetID: s.TargetNetID,
			Progress:    s.Progress,
			Locked:      s.Locked,
		}
	}
	connID := connIDForPlayerEntity(player)
	if connID == 0 {
		return
	}
	mmokit.SendEvent(gw.stage, connID, &LockSlotsMsg{
		Slots:       wire,
		ActiveNetID: lock.ActiveNetID,
	})
}

// sendLockRejected emits a LockRejectedMsg for the owning player.
func sendLockRejected(gw *GameWorld, player mmokit.Entity, netID uint32, reason LockRejectReason) {
	connID := connIDForPlayerEntity(player)
	if connID == 0 {
		return
	}
	mmokit.SendEvent(gw.stage, connID, &LockRejectedMsg{
		TargetNetID: netID,
		Reason:      uint8(reason),
	})
}

// connIDForPlayerEntity reverse-maps a player ECS entity → its connID via
// the PlayerConn component. Returns 0 when the entity has no PlayerConn
// (e.g. NPCs or already-removed players).
func connIDForPlayerEntity(e mmokit.Entity) uint32 {
	conn := mmokit.Get[mmokit.PlayerConn](e)
	if conn == nil {
		return 0
	}
	return conn.ConnID
}
