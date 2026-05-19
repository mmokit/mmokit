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
// mutate the player's components in place; discrete actions schedule
// game-world methods via stage.Commands().Defer so they run at the next
// per-system flush boundary with the ECS world unlocked.
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
		// Firing any ability cancels supercruise (auto-cancel site).
		// No lockout — auto-cancel is voluntary, lockout is combat-only.
		cancelSupercruise(player)
		input.AbilityCast |= 1 << msg.Slot
		input.LastCastAimX = msg.AimX
		input.LastCastAimY = msg.AimY
		if netID != nil {
			player.Stage().Engine().Log.Log(CatPlayerInput,
				"player=%d cast=0x%x slot=%d seq=%d aim=(%.1f,%.1f)",
				netID.ID, input.AbilityCast, msg.Slot, input.Sequence,
				msg.AimX, msg.AimY)
		}
	})

	// SelectTarget — left-click selects an entity; right-click sends
	// NetID=0 to clear. Selection drives ability dispatch (Tasks 3-7
	// wire abilities to read the player's Selection.EntityNetID).
	// Invalid / dead targets drop silently.
	mmokit.HandleClient(mmo, func(player mmokit.Entity, msg *SelectTarget) {
		if !player.Alive() {
			return
		}
		sel := mmokit.Get[gamecomp.Selection](player)
		if sel == nil {
			return
		}
		if msg.NetID == 0 {
			sel.EntityNetID = 0
			return
		}
		gw := gameWorldFromStage(player.Stage())
		if gw == nil {
			return
		}
		target := mmokit.EntityByNetID(gw.stage, msg.NetID)
		if !target.Alive() {
			return
		}
		sel.EntityNetID = msg.NetID
		gw.eng.Log.Log(CatCombatAbility, "select: player=%d netID=%d", player.NetID(), msg.NetID)
	})

	// ChannelAim — cursor-tracking update for an in-flight skillshot
	// channel (SustainedBeam). Writes AimX/AimY onto the player's
	// Channeling component so tickChannels hitscans along the latest
	// cursor direction. Silently dropped if the player isn't channeling
	// or the slot doesn't match the active channel.
	mmokit.HandleClient(mmo, func(player mmokit.Entity, msg *ChannelAim) {
		state := mmokit.PlayerStateOf(player)
		if state != mmokit.StateActive {
			return
		}
		input := mmokit.Get[gamecomp.PlayerInput](player)
		if input != nil {
			input.Sequence = msg.Sequence
		}
		ch := mmokit.Get[gamecomp.Channeling](player)
		if ch == nil {
			return // not channeling — drop silently
		}
		if ch.SlotID != msg.Slot {
			return // mismatched slot — drop
		}
		ch.AimX = msg.AimX
		ch.AimY = msg.AimY
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
		// Initiating docking cancels supercruise.
		cancelSupercruise(player)
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
		connID := conn.ConnID
		gw.stage.Commands().Defer(func() {
			gw.startUndockingFor(connID)
		})
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
		connID := conn.ConnID
		gw.eng.Log.Log(CatPlayerSpawn, "respawn requested: conn=%d", connID)
		gw.stage.Commands().Defer(func() {
			gw.executeRespawnFor(connID)
		})
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
		connID := conn.ConnID
		itemID := msg.ItemID
		amount := msg.Quantity
		deposit := msg.Deposit
		gw.stage.Commands().Defer(func() {
			gw.performTransferFor(connID, itemID, amount, deposit)
		})
	})

	// (BankRequest is a typed-op now — see op_bank.go: HandleBankRequest
	// runs on the player's cell engine via Process.DispatchCellRoutedOp.
	// The per-tick queue + drain that preceded the typed-op migration
	// are gone; typed-op delivery is synchronous within the loop.)

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
		connID := conn.ConnID
		itemID := msg.ItemID
		slot := item.EquipSlot(msg.Slot)
		targetBank := msg.TargetBank
		gw.stage.Commands().Defer(func() {
			gw.performEquipFor(connID, itemID, slot, targetBank)
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
		connID := conn.ConnID
		crateNetID := msg.CrateNetID
		itemID := msg.ItemID
		gw.stage.Commands().Defer(func() {
			gw.performLootItemFor(connID, crateNetID, itemID)
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
		connID := conn.ConnID
		crateNetID := msg.CrateNetID
		gw.stage.Commands().Defer(func() {
			gw.performLootAllFor(connID, crateNetID)
		})
	})

	// ToggleSuperCruise — Z key press. Idle → Channeling, or Channeling/
	// Active → Idle (manual cancel). Lockout (LockoutRemaining > 0) blocks
	// re-entry. State machine lives in SupercruiseSystem; this handler
	// only flips Phase / arms the timer.
	mmokit.HandleClient(mmo, func(player mmokit.Entity, msg *ToggleSuperCruise) {
		state := mmokit.PlayerStateOf(player)
		if state != mmokit.StateActive {
			return
		}
		input := mmokit.Get[gamecomp.PlayerInput](player)
		if input == nil {
			return
		}
		input.Sequence = msg.Sequence

		sc := mmokit.Get[gamecomp.Supercruise](player)
		if sc == nil {
			return
		}
		gw := gameWorldOfEntity(player)
		if gw == nil {
			return
		}
		switch sc.Phase {
		case gamecomp.SupercruiseIdle:
			if sc.LockoutRemaining > 0 {
				gw.eng.Log.Log(CatSupercruise, "z-press ignored (lockout=%.1f) netID=%d",
					sc.LockoutRemaining, player.NetID())
				return
			}
			sc.Phase = gamecomp.SupercruiseChanneling
			sc.ChannelRemaining = gw.Config.SupercruiseChannelTime
			if mt := mmokit.Get[mmokit.MoveTarget](player); mt != nil {
				mt.Active = false
			}
			gw.eng.Log.Log(CatSupercruise, "channel start: netID=%d duration=%.1f",
				player.NetID(), sc.ChannelRemaining)
		case gamecomp.SupercruiseChanneling, gamecomp.SupercruiseActive:
			phase := sc.Phase
			cancelSupercruise(player)
			gw.eng.Log.Log(CatSupercruise, "manual cancel: netID=%d phase=%d",
				player.NetID(), phase)
		}
	})
}

