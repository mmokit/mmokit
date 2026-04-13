package persist

import (
	"context"
	"time"
)

// PlayerRepository persists player state. Implementation: pkg/persist/postgres.
//
// The repository is a thin storage abstraction — translation between
// the in-memory game type (internal/game.PlayerData) and the
// persistence DTO (PlayerSnapshot) happens in the game-domain layer.
// This separation lets storage representation evolve independently
// from the runtime type.
type PlayerRepository interface {
	// Load fetches a player snapshot by username. Returns
	// (nil, ErrNotFound) when the username is unknown; (nil, err) for
	// any other error.
	Load(ctx context.Context, username string) (*PlayerSnapshot, error)

	// LoadAll streams every player record, calling fn for each.
	// Used at game startup to warm the in-memory PlayerRepo cache.
	// Iteration order is unspecified. If fn returns a non-nil error,
	// iteration stops and that error is returned.
	LoadAll(ctx context.Context, fn func(*PlayerSnapshot) error) error

	// SaveBatch upserts multiple player snapshots in a single
	// round-trip via pgx.Batch (or equivalent). The caller MUST sort
	// snapshots by Username before calling — the repository does NOT
	// sort internally because deadlock prevention is a contract
	// between concurrent flushers, and only the caller knows whether
	// multiple flushes might race. An empty slice is a no-op.
	SaveBatch(ctx context.Context, snapshots []*PlayerSnapshot) error
}

// PlayerSnapshot is the persistence-layer representation of a player.
// Distinct from internal/game.PlayerData: this struct contains only
// the subset that needs to survive process restart, with no runtime
// fields (ECS entity references, mutex, etc.).
//
// Map fields use Go-native types; the postgres backend marshals them
// to JSONB columns. Empty maps are stored as '{}'::jsonb, not NULL.
type PlayerSnapshot struct {
	Username   string
	CellID     string           // e.g. "cell_2_1"
	PosX       float32
	PosY       float32
	Currencies map[uint32]int64 // currency_id -> balance
	Cargo      map[uint32]int32 // item_id -> quantity
	Bank       map[uint32]int32 // item_id -> quantity
	Equipment  EquipmentSnapshot
	CreatedAt  time.Time
	LastLogin  time.Time
}

// EquipmentSnapshot is the equipped-gear subset of player state.
// Each field holds an item ID (0 = empty slot). Stored as a JSONB
// blob so adding new slots is a code-only change.
type EquipmentSnapshot struct {
	Weapon1  uint32 `json:"weapon1,omitempty"`
	Weapon2  uint32 `json:"weapon2,omitempty"`
	Shield   uint32 `json:"shield,omitempty"`
	Thruster uint32 `json:"thruster,omitempty"`
}

// MarketRepository persists order book state. All methods are
// synchronous: orders need to survive even brief crashes because the
// in-memory book gets rebuilt from this storage at startup.
//
// Order IDs are assigned by a database sequence (BIGSERIAL); the
// caller passes ID=0 to PlaceOrder and reads the assigned ID from
// the return value.
type MarketRepository interface {
	// PlaceOrder inserts a new resting order. The OrderRecord's ID
	// field is ignored on input. Returns the database-assigned ID.
	PlaceOrder(ctx context.Context, o *OrderRecord) (uint64, error)

	// UpdateQuantity decrements the remaining quantity on a partial
	// fill. Updating to 0 is allowed but the caller is expected to
	// call DeleteOrder for fully-filled orders.
	UpdateQuantity(ctx context.Context, id uint64, newQty int32) error

	// DeleteOrder removes a fully-filled, cancelled, or expired
	// order. No error if the ID doesn't exist.
	DeleteOrder(ctx context.Context, id uint64) error

	// RecordTrade appends a completed trade to the audit log. Trade
	// IDs are assigned by the database sequence; the input
	// TradeRecord's ID is ignored if present.
	RecordTrade(ctx context.Context, t *TradeRecord) error

	// LoadActiveOrders streams every non-expired order at startup,
	// calling fn for each. Used to rebuild the in-memory order book.
	// Iteration order is by id ascending so the in-memory book sees
	// orders in placement order.
	LoadActiveOrders(ctx context.Context, fn func(*OrderRecord) error) error
}

// OrderRecord is the persistence-layer representation of a market
// order. The Side field follows the convention 0 = buy, 1 = sell.
// ExpiresAt is the zero value for orders that never expire.
type OrderRecord struct {
	ID         uint64
	Side       uint8
	Owner      string
	LocationID uint32
	ItemID     uint32
	Price      int64
	Quantity   int32 // remaining
	OrigQty    int32
	CreatedAt  time.Time
	ExpiresAt  time.Time // zero value = never expires
}

// TradeRecord is one row of the market trade audit log. Append-only;
// never updated or deleted.
type TradeRecord struct {
	ItemID     uint32
	LocationID uint32
	Price      int64
	Quantity   int32
	Buyer      string
	Seller     string
	OccurredAt time.Time
}

// ConfigRepository persists the singleton GameConfig blob. The
// implementation uses a single-row table with a CHECK constraint
// enforcing id = 1, so the storage layer doesn't need any
// concurrency control of its own.
type ConfigRepository interface {
	// Load returns the saved config blob and its version. Returns
	// (nil, ErrNotFound) if no config has been saved yet (first run).
	Load(ctx context.Context) (*ConfigSnapshot, error)

	// Save upserts the config blob.
	Save(ctx context.Context, snapshot *ConfigSnapshot) error
}

// ConfigSnapshot is the persistence-layer representation of the
// singleton game config. Data is opaque to the persist layer — the
// game-domain code is responsible for marshaling/unmarshaling the
// payload (currently JSON via internal/game.GameConfig).
type ConfigSnapshot struct {
	Data    []byte
	Version int64
}
