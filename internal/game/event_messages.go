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
