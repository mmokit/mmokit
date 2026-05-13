package persist

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PlayerStateRepository persists per-player space-game state.
// Implementation: internal/persist/postgres.
//
// Keyed by user_id (UUID), FK to engine.players(user_id) ON DELETE CASCADE.
type PlayerStateRepository interface {
	// Load returns the player's game state by user_id.
	// Returns (nil, ErrNotFound) when the user has no game state row;
	// callers should treat this as "fresh player, all-zero state".
	Load(ctx context.Context, userID uuid.UUID) (*PlayerStateSnapshot, error)

	// LoadAll streams every player state row. Used at game startup
	// to warm the in-memory game-state cache. Iteration order is
	// unspecified. Stops + returns the error if fn returns non-nil.
	LoadAll(ctx context.Context, fn func(*PlayerStateSnapshot) error) error

	// SaveBatch upserts multiple snapshots in a single pgx.Batch
	// round-trip. Caller MUST sort by UserID before calling
	// (deadlock-prevention contract — matches PlayerRepository.SaveBatch).
	// Empty slice is a no-op.
	SaveBatch(ctx context.Context, snapshots []*PlayerStateSnapshot) error

	// SaveBatchTx upserts inside the caller-supplied transaction.
	// Same sort-by-UserID deadlock-prevention contract as SaveBatch.
	// Used by the game-side PlayerFlusher to write engine identity and
	// game state atomically under one pgx.Tx.
	SaveBatchTx(ctx context.Context, tx pgx.Tx, snapshots []*PlayerStateSnapshot) error
}

// PlayerStateSnapshot is the persistence DTO for a player's game state.
// Identity columns (cell, position, login times, debug flags) live in
// engine.players via pkg/persist.PlayerSnapshot — this struct holds
// ONLY the game-side JSONB fields keyed off the same user_id.
type PlayerStateSnapshot struct {
	UserID     uuid.UUID
	Currencies map[uint32]int64 // currency_id -> balance
	Cargo      map[uint32]int32 // item_id -> quantity
	Bank       map[uint32]int32 // item_id -> quantity
	Equipment  EquipmentSnapshot
}

// EquipmentSnapshot is the equipped-gear subset of player state.
// Each field holds an item ID (0 = empty slot). Stored as a JSONB
// blob so adding new slots is a code-only change.
type EquipmentSnapshot struct {
	Weapon1  uint32 `json:"weapon_1,omitempty"`
	Weapon2  uint32 `json:"weapon_2,omitempty"`
	Shield   uint32 `json:"shield,omitempty"`
	Thruster uint32 `json:"thruster,omitempty"`
}

// MarketRepository persists order book state. All methods are
// synchronous — the marketplace settlement code calls them from
// operation-router worker goroutines, so the orderbook is read after
// the DB confirms the write.
//
// The orderbook owns ID allocation, not the database. Callers seed
// the in-memory NextID counter from LoadMaxOrderID at startup so
// allocations resume past the highest persisted ID.
type MarketRepository interface {
	// PlaceOrder inserts a new resting order. The caller MUST set
	// OrderRecord.ID before calling — the repository does NOT assign
	// IDs.
	PlaceOrder(ctx context.Context, o *OrderRecord) error

	// UpdateQuantity decrements the remaining quantity on a partial
	// fill.
	UpdateQuantity(ctx context.Context, id uint64, newQty int32) error

	// DeleteOrder removes a fully-filled, cancelled, or expired order.
	// No error if the ID is already absent.
	DeleteOrder(ctx context.Context, id uint64) error

	// RecordTrade appends a completed trade to the audit log. The
	// input TradeRecord has no ID field by design — the audit log is
	// append-only and never read back by ID.
	RecordTrade(ctx context.Context, t *TradeRecord) error

	// LoadActiveOrders streams every non-expired order at startup so
	// the in-memory orderbook can re-hydrate. Orders are delivered in
	// id-ascending order so the book sees them in placement order.
	LoadActiveOrders(ctx context.Context, fn func(*OrderRecord) error) error

	// LoadMaxOrderID returns the highest persisted order id, or 0 if
	// the table is empty. Used at startup to seed the orderbook's
	// NextID counter.
	LoadMaxOrderID(ctx context.Context) (uint64, error)
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

// ConfigSnapshot is the persistence DTO for the singleton game config.
// data is opaque to this layer — the marshalling format is owned by
// internal/game.GameConfig.
type ConfigSnapshot struct {
	Data    []byte
	Version int64
}
