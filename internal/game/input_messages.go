package game

// Typed client-input messages — replace the pre-Plan-G gamepb.* proto
// types. Each registers via mmokit.HandleClient (Phase 6); the client
// SDK exposes them via client.send(new MessageClass{...}).
//
// PlayerInputMsg's bundled snapshot pattern is decomposed into four
// discrete typed messages aligned with their semantic shape (continuous
// state vs discrete event). See spec §5.

// SetMoveTarget — continuous-state click-to-move target. Active=false
// clears (player stops at current position).
type SetMoveTarget struct {
	Sequence uint32
	Active   bool
	X, Y     float32
}

// SetLockTarget — change of target lock. TargetNetID=0 clears the lock.
type SetLockTarget struct {
	Sequence    uint32
	TargetNetID uint32
}

// CastAbility — discrete ability press. Slot identifies which equipped
// ability fired (0 = primary weapon, etc.). Replaces the pre-split
// `ability_cast` bitmask in PlayerInputMsg — the client now sends one
// CastAbility per press instead of OR-ing bits into a per-tick snapshot.
type CastAbility struct {
	Sequence uint32
	Slot     uint8
}

// JettisonItem — discrete cargo jettison.
type JettisonItem struct {
	Sequence uint32
	ItemID   uint32
}

// Dock — request dock at the nearest station within range.
type Dock struct {
	Sequence uint32
}

// Undock — request undock from the current station.
type Undock struct {
	Sequence uint32
}

// Respawn — request respawn after death.
type Respawn struct {
	Sequence uint32
}

// InventoryTransfer — move items between cargo and bank (when docked).
type InventoryTransfer struct {
	Sequence uint32
	ItemID   uint32
	Quantity int32 // 0 = all
	Deposit  bool  // true: cargo→bank; false: bank→cargo
}

// BankRequest — open the bank UI (returns BankContents via server event).
type BankRequest struct {
	Sequence uint32
}

// Equip — equip or unequip an item. ItemID=0 unequips the slot.
// TargetBank=true (when docked) sources from / returns to the bank
// instead of cargo; ignored for non-docked players.
type Equip struct {
	Sequence   uint32
	ItemID     uint32
	Slot       uint8 // gamecomp.EquipSlot enum value
	TargetBank bool
}

// LootItem — take a specific item from a loot crate.
//
// Mirrors gamepb.LootItemMsg exactly: only crate_net_id + item_id. The
// proto has no quantity field — the loot handler transfers the full
// stack of that item from the crate.
type LootItem struct {
	Sequence   uint32
	CrateNetID uint32
	ItemID     uint32
}

// LootAll — take everything from a loot crate.
type LootAll struct {
	Sequence   uint32
	CrateNetID uint32
}
