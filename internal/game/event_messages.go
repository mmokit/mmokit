package game

// Typed server→client event messages — registered via mmokit.RegisterEvent[T]
// and emitted with mmokit.SendEvent. Replaces the legacy gamepb.* /
// enginepb.* protobuf-envelope path on the 0x00 channel.
//
// Wire format: pkguniverse.EncodeTypedEventFrame — fields encoded in source
// declaration order, little-endian, no padding (mirrors the codec defined in
// pkg/universe/reflect_marshal.go). String fields are length-prefixed (uint16
// LE length + UTF-8 bytes). The reflect codec rejects unsupported field types
// at registration time.
//
// Naming: proto field foo_bar_baz → Go field FooBarBaz. The TS codegen
// emits camelCase property names (fooBarBaz) automatically — see
// cmd/sdkgen/broadcasts.go.

// PlayerDied — sent to a player when their entity dies.
// Replaces gamepb.PlayerDiedMsg.
type PlayerDied struct {
	KillerID uint32 // network ID of who killed you, 0 if unknown
}

// DockingState — progress update while docking (in-progress or cancelled).
// Replaces gamepb.DockingStateMsg.
type DockingState struct {
	Docking   bool    // true = docking in progress, false = cancelled
	Progress  float32 // 0.0 to 1.0
	TotalTime float32 // total docking duration (for client progress bar)
	StationID uint32  // net ID of station being docked at (for tractor beam VFX)
}

// Docked — fired once the docking sequence completes.
// Replaces gamepb.DockedMsg.
type Docked struct{}

// CurrencyUpdate — notifies the client of a change to a currency balance.
// Replaces gamepb.CurrencyUpdateMsg.
type CurrencyUpdate struct {
	CurrencyID uint32
	Balance    int64
	Earned     int64
}

// EquipResult — server response to an equip request.
// Replaces gamepb.EquipResultMsg. Slot is the underlying uint32 of the
// EquipSlot enum (matches the proto wire format — proto enums are int32
// on the wire, but EquipSlot values are all small non-negative).
type EquipResult struct {
	Success        bool
	Reason         string
	Slot           uint32 // EquipSlot enum value (0=NONE, 1=WEAPON1, 2=WEAPON2, 3=SHIELD, 4=THRUSTER)
	EquippedItemID uint32 // 0 if slot is now empty
	PreviousItemID uint32 // what was in the slot before
}

// TransferResult — bank/cargo transfer outcome (deposit or withdraw).
// Replaces gamepb.TransferResultMsg.
type TransferResult struct {
	Success  bool
	Reason   string
	ItemID   uint32
	Quantity int32 // actual amount transferred
	Deposit  bool  // direction of transfer
}

// MapStationInfo — single station marker on the world map.
// Replaces gamepb.MapStationInfo.
type MapStationInfo struct {
	CellX  int32
	CellY  int32
	LocalX float32
	LocalY float32
	Name   string
}

// MapData — world-map info bundle (currently just stations).
// Replaces gamepb.MapDataMsg.
type MapData struct {
	Stations []MapStationInfo
}

// InventoryItem — typed mirror of gamepb.InventoryItem for typed-event use.
type InventoryItem struct {
	ItemID   uint32
	Quantity int32
}

// CurrencyBalance — typed mirror of gamepb.CurrencyBalance for typed-event use.
type CurrencyBalance struct {
	CurrencyID uint32
	Balance    int64
}

// BankContents — full snapshot of a player's bank, cargo, and currency
// balances. Sent on dock + on every bank/cargo mutation.
// Replaces gamepb.BankContentsMsg.
type BankContents struct {
	Items        []InventoryItem
	TotalMass    float32
	MaxMass      float32
	CargoItems   []InventoryItem
	CargoMass    float32
	MaxCargoMass float32
	Currencies   []CurrencyBalance
}

// EquipmentState — typed mirror of gamepb.EquipmentState for typed-event use.
type EquipmentState struct {
	Weapon1  uint32
	Weapon2  uint32
	Shield   uint32
	Thruster uint32
}

// AbilityCooldownState — typed mirror of gamepb.AbilityCooldownState.
type AbilityCooldownState struct {
	Slot      uint32  // 0=Q, 1=W, 2=E, 3=R, 4=D, 5=F
	Remaining float32 // seconds remaining
	Total     float32 // total cooldown duration
}

// PlayerOwnState — per-tick state sent only to the owning player.
// Replaces gamepb.PlayerOwnStateMsg.
type PlayerOwnState struct {
	LockProgress          float32
	LockTargetID          uint32
	AbilityCooldowns      []AbilityCooldownState
	Equipment             EquipmentState
	CargoItems            []InventoryItem
	CargoMass             float32
	MaxCargoMass          float32
	BeingLockedByID       uint32
	BeingLockedByProgress float32
}
