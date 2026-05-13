package persist

import (
	"context"
	"time"
)

// PlayerStateRepository persists per-player space-game state.
// Implementation: internal/persist/postgres.
//
// Keyed by username, FK to engine.players(username) ON DELETE CASCADE.
type PlayerStateRepository interface {
	// Load returns the player's game state by username.
	// Returns (nil, ErrNotFound) when the username has no game state row;
	// callers should treat this as "fresh player, all-zero state".
	Load(ctx context.Context, username string) (*PlayerStateSnapshot, error)

	// LoadAll streams every player state row. Used at game startup
	// to warm the in-memory game-state cache. Iteration order is
	// unspecified. Stops + returns the error if fn returns non-nil.
	LoadAll(ctx context.Context, fn func(*PlayerStateSnapshot) error) error

	// SaveBatch upserts multiple snapshots in a single pgx.Batch
	// round-trip. Caller MUST sort by Username before calling
	// (deadlock-prevention contract — matches PlayerRepository.SaveBatch).
	// Empty slice is a no-op.
	SaveBatch(ctx context.Context, snapshots []*PlayerStateSnapshot) error
}

// PlayerStateSnapshot is the persistence DTO for a player's game state.
// Identity columns (cell, position, login times, debug flags) live in
// engine.players via pkg/persist.PlayerSnapshot — this struct holds
// ONLY the game-side JSONB fields keyed off the same username.
type PlayerStateSnapshot struct {
	Username   string
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

// MarketRepository persists order book state for the in-game marketplace.
type MarketRepository interface {
	SaveOrder(ctx context.Context, order *OrderRecord) error
	LoadAllOrders(ctx context.Context) ([]*OrderRecord, error)
	LoadMaxOrderID(ctx context.Context) (uint64, error)
	UpdateOrderQuantity(ctx context.Context, id uint64, quantity int32) error
	DeleteOrder(ctx context.Context, id uint64) error
	RecordTrade(ctx context.Context, trade *TradeRecord) error
}

// OrderRecord is the persistence DTO for one marketplace order.
type OrderRecord struct {
	ID         uint64
	Side       int16 // 0 = buy, 1 = sell
	Owner      string
	LocationID uint32
	ItemID     uint32
	Price      int64
	Quantity   int32
	OrigQty    int32
	CreatedAt  time.Time
	ExpiresAt  *time.Time // nil = no expiry
}

// TradeRecord is one row of the marketplace trade audit log.
type TradeRecord struct {
	ID         int64 // BIGSERIAL — populated by RecordTrade for new rows
	ItemID     uint32
	LocationID uint32
	Price      int64
	Quantity   int32
	Buyer      string
	Seller     string
	OccurredAt time.Time
}

// ConfigRepository persists the singleton GameConfig blob (id=1 row).
type ConfigRepository interface {
	Load(ctx context.Context) (*ConfigSnapshot, error)
	Save(ctx context.Context, snap *ConfigSnapshot) error
}

// ConfigSnapshot is the persistence DTO for the singleton game config.
// data is opaque to this layer — the marshalling format is owned by
// internal/game.GameConfig.
type ConfigSnapshot struct {
	Data    []byte
	Version int64
}
