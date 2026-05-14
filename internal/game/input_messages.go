package game

// Typed client-input messages — each registers via mmokit.HandleClient;
// the client SDK exposes them via client.send(new MessageClass{...}).
//
// Inputs are decomposed into discrete typed messages aligned with their
// semantic shape (continuous state vs discrete event) rather than bundled
// per-tick snapshots.

// SetMoveTarget — continuous-state click-to-move target. Active=false
// clears (player stops at current position).
type SetMoveTarget struct {
	Sequence uint32
	Active   bool
	X, Y     float32
}

// LockTarget — begin locking onto an entity. Server rejects if slots
// full, target invalid, or out of range — emits LockRejectedMsg.
type LockTarget struct {
	Sequence uint32
	NetID    uint32
}

// UnlockTarget — drop a specific slot. NetID=0 is a no-op.
type UnlockTarget struct {
	Sequence uint32
	NetID    uint32
}

// SetActiveTarget — switch the active firing target. NetID must be
// among locked slots and Locked=true; otherwise silently ignored.
// NetID=0 explicitly clears active.
type SetActiveTarget struct {
	Sequence uint32
	NetID    uint32
}

// SelectTarget — left-click sets the player's selection to NetID; NetID=0
// clears (right-click). Server validates the target is alive + in AoI;
// invalid requests are dropped silently (no error event).
type SelectTarget struct {
	Sequence uint32
	NetID    uint32
}

// CastAbility — discrete ability press. Slot identifies which equipped
// ability fired. AimX/AimY carry the cursor world position; meaning
// depends on the ability's TargetingMode:
//   Self            — ignored
//   LockOn          — ignored (server reads TargetLock.ActiveNetID)
//   SkillshotLine   — direction = (AimX - shipX, AimY - shipY), normalized
//   SkillshotGround — drop the AoE at (AimX, AimY), clamped to ability range
//   SkillshotChannel — initial aim point; updates via ChannelAim while held
type CastAbility struct {
	Sequence uint32
	Slot     uint8
	AimX     float32
	AimY     float32
}

// ChannelAim — cursor update for an in-flight SkillshotChannel ability.
// Client sends one per tick while the channel is held; server applies
// to the player's Channeling component AimX/AimY fields.
type ChannelAim struct {
	Sequence uint32
	Slot     uint8
	AimX     float32
	AimY     float32
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

// (BankRequest moved to a typed-op in op_bank.go — Plan 2 Phase 3.
// Clients now call client.bankRequest(req) on channel 0x01 with
// promise-based response correlation instead of the fire-and-forget
// HandleClient[BankRequest] input.)

// Equip — equip or unequip an item. ItemID=0 unequips the slot.
// TargetBank=true (when docked) sources from / returns to the bank
// instead of cargo; ignored for non-docked players.
type Equip struct {
	Sequence   uint32
	ItemID     uint32
	Slot       uint8 // gamecomp.EquipSlot enum value
	TargetBank bool
}

// LootItem — take a specific item from a loot crate. No quantity field;
// the loot handler transfers the full stack of that item from the crate.
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
