package game

import (
	"sync"

	"github.com/zenion/mmoserver/internal/marketplace"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// Typed server→client event messages — registered via mmokit.RegisterEvent[T]
// and emitted with mmokit.SendEvent. The full game wire format on channel
// 0x00 is the typed reflection-codec frame; no protobuf envelope is used.
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
type PlayerDied struct {
	KillerID uint32 // network ID of who killed you, 0 if unknown
}

// DockingState — progress update while docking (in-progress or cancelled).
type DockingState struct {
	Docking   bool    // true = docking in progress, false = cancelled
	Progress  float32 // 0.0 to 1.0
	TotalTime float32 // total docking duration (for client progress bar)
	StationID uint32  // net ID of station being docked at (for tractor beam VFX)
}

// Docked — fired once the docking sequence completes.
type Docked struct{}

// CurrencyUpdate — notifies the client of a change to a currency balance.
type CurrencyUpdate struct {
	CurrencyID uint32
	Balance    int64
	Earned     int64
}

// EquipResult — server response to an equip request. Slot is the
// underlying uint32 of the EquipSlot enum.
type EquipResult struct {
	Success        bool
	Reason         string
	Slot           uint32 // EquipSlot enum value (0=NONE, 1=WEAPON1, 2=WEAPON2, 3=SHIELD, 4=THRUSTER)
	EquippedItemID uint32 // 0 if slot is now empty
	PreviousItemID uint32 // what was in the slot before
}

// TransferResult — bank/cargo transfer outcome (deposit or withdraw).
type TransferResult struct {
	Success  bool
	Reason   string
	ItemID   uint32
	Quantity int32 // actual amount transferred
	Deposit  bool  // direction of transfer
}

// MapStationInfo — single station marker on the world map.
type MapStationInfo struct {
	CellX  int32
	CellY  int32
	LocalX float32
	LocalY float32
	Name   string
}

// MapData — world-map info bundle (currently just stations).
type MapData struct {
	Stations []MapStationInfo
}

// InventoryItem — one (item, qty) pair in a bank / cargo snapshot.
type InventoryItem struct {
	ItemID   uint32
	Quantity int32
}

// CurrencyBalance — one (currency, balance) pair in a bank snapshot.
type CurrencyBalance struct {
	CurrencyID uint32
	Balance    int64
}

// BankContents — full snapshot of a player's bank, cargo, and currency
// balances. Sent on dock + on every bank/cargo mutation.
type BankContents struct {
	Items        []InventoryItem
	TotalMass    float32
	MaxMass      float32
	CargoItems   []InventoryItem
	CargoMass    float32
	MaxCargoMass float32
	Currencies   []CurrencyBalance
}

// EquipmentState — equipped item IDs by slot (0 = empty slot).
type EquipmentState struct {
	Weapon1  uint32
	Weapon2  uint32
	Shield   uint32
	Thruster uint32
}

// AbilityCooldownState — cooldown timer for one ability slot.
type AbilityCooldownState struct {
	Slot      uint32  // 0=Q, 1=W, 2=E, 3=R, 4=D, 5=F
	Remaining float32 // seconds remaining
	Total     float32 // total cooldown duration
}

// PlayerOwnState — per-tick state sent only to the owning player.
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

// ItemDef — single item-registry entry (used in PlayerSpawned).
type ItemDef struct {
	ID          uint32
	Name        string
	MassPerUnit float32
	Category    uint32 // ItemCategory enum value
	EquipSlot   uint32 // EquipSlot enum value (0 = not equippable)
}

// PlayerSpawned — sent once when the player's entity is created in the
// destination cell after login or respawn. Carries the bootstrap data
// the client needs to render its avatar (own entity ID, item registry,
// current equipment, current cell coords).
type PlayerSpawned struct {
	YourEntityID uint32
	ItemDefs     []ItemDef
	Equipment    EquipmentState
	OriginCellX  int32
	OriginCellY  int32
}

// RegisterServerEvents registers every typed server event the space game
// emits. Called by GameSetup so both production (cmd/server/main.go) and
// tests (per-test newTestGameWorld → GameSetup) get the registrations.
//
// sync.Once-guarded because mmokit.RegisterEvent[T] panics on duplicate
// registration of the same Go type and tests build many GameWorlds in one
// process.
func RegisterServerEvents() {
	registerServerEventsOnce.Do(func() {
		mmokit.RegisterEvent[PlayerSpawned]()
		mmokit.RegisterEvent[PlayerDied]()
		mmokit.RegisterEvent[PlayerOwnState]()
		mmokit.RegisterEvent[BankContents]()
		mmokit.RegisterEvent[TransferResult]()
		mmokit.RegisterEvent[EquipResult]()
		mmokit.RegisterEvent[DockingState]()
		mmokit.RegisterEvent[Docked]()
		mmokit.RegisterEvent[MapData]()
		mmokit.RegisterEvent[CurrencyUpdate]()
		mmokit.RegisterEvent[marketplace.MarketTradeNotification]()
	})
}

var registerServerEventsOnce sync.Once
