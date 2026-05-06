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

	// SetLockTarget — change of target lock. TargetNetID=0 clears.
	mmokit.HandleClient(mmo, func(player mmokit.Entity, msg *SetLockTarget) {
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
		prev := input.LockTargetNetID
		input.LockTargetNetID = msg.TargetNetID
		if prev != input.LockTargetNetID && netID != nil {
			player.Stage().Engine().Log.Log(CatPlayerInput,
				"player=%d lock=%d seq=%d", netID.ID, input.LockTargetNetID, input.Sequence)
		}
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
		mmokit.Enqueue(gw.Queue, PendingDockRequest{ConnID: conn.ConnID})
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

	mmokit.HandleClient(mmo, func(player mmokit.Entity, msg *BankRequest) {
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
		mmokit.Enqueue(gw.Queue, PendingBankRequest{ConnID: conn.ConnID})
	})

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
