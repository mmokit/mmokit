// Package persist defines the engine-side persistence interfaces.
//
// PlayerRepository persists per-player identity (user_id, username, cell,
// position, debug flags, login timestamps). Game-specific player
// state — currencies, cargo, bank, equipment, marketplace, game
// config — lives in a separate game-owned package (e.g.
// internal/persist for the space game).
//
// Implementation: pkg/persist/postgres targeting the `engine` schema.
package persist

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PlayerRepository persists engine-side player identity. The
// repository is a thin storage abstraction — translation between the
// in-memory game type (internal/game.PlayerData) and PlayerSnapshot
// happens in the game-domain layer. This separation lets storage
// representation evolve independently from the runtime type.
//
// Keyed on the immutable UserID (UUID); Username is a denormalized
// display column that may change over time.
type PlayerRepository interface {
	// Load fetches a player snapshot by user_id. Returns
	// (nil, ErrNotFound) when the id is unknown; (nil, err) for
	// any other error.
	Load(ctx context.Context, userID uuid.UUID) (*PlayerSnapshot, error)

	// LoadByUsername fetches a player snapshot by display name.
	// Used by console/admin lookups where only the username is
	// known. Returns (nil, ErrNotFound) when the username is unknown.
	LoadByUsername(ctx context.Context, username string) (*PlayerSnapshot, error)

	// LoadAll streams every player record, calling fn for each.
	// Used at game startup to warm the in-memory PlayerRepo cache.
	// Iteration order is unspecified. If fn returns a non-nil error,
	// iteration stops and that error is returned.
	LoadAll(ctx context.Context, fn func(*PlayerSnapshot) error) error

	// SaveBatch upserts multiple player snapshots in a single
	// round-trip via pgx.Batch (or equivalent). The caller MUST sort
	// snapshots by UserID before calling — the repository does NOT
	// sort internally because deadlock prevention is a contract
	// between concurrent flushers, and only the caller knows whether
	// multiple flushes might race. An empty slice is a no-op.
	SaveBatch(ctx context.Context, snapshots []*PlayerSnapshot) error

	// SaveBatchTx upserts inside the caller-supplied transaction.
	// Same sort-by-UserID deadlock-prevention contract as SaveBatch.
	// Used by the game-side PlayerFlusher to write engine identity and
	// game state atomically under one pgx.Tx.
	SaveBatchTx(ctx context.Context, tx pgx.Tx, snapshots []*PlayerSnapshot) error

	// LoadDebugFlags returns the persisted debug-flag names for the
	// given user. Returns (nil, ErrNotFound) if the user doesn't exist;
	// (empty, nil) if the user exists but has no flags set.
	LoadDebugFlags(ctx context.Context, username string) ([]string, error)

	// SaveDebugFlags writes the given list of flag names to the
	// player's debug_flags JSONB column. Replaces any existing list.
	// Synchronous (not batched) so console grant/revoke commits to
	// disk before returning.
	SaveDebugFlags(ctx context.Context, username string, flags []string) error

	// LoadAllDebugFlags returns every player with at least one debug
	// flag set, keyed by username. Backs the `debug list` console
	// command. Returns an empty map (not nil) when no users have any
	// grants.
	LoadAllDebugFlags(ctx context.Context) (map[string][]string, error)
}

// PlayerSnapshot is the engine-side persistence DTO for player
// identity. Game-specific state (currencies, cargo, bank, equipment,
// etc.) lives in a separate game-owned table joined by user_id; see
// internal/persist for the space game's PlayerStateSnapshot.
type PlayerSnapshot struct {
	UserID    uuid.UUID
	Username  string
	CellID    string // e.g. "cell_2_1"
	PosX      float32
	PosY      float32
	CreatedAt time.Time
	LastLogin time.Time
	// DebugFlags is the persisted set of enabled debug capability
	// names (e.g. ["topology"]). Stored as a JSONB array; mapped to
	// engine.DebugFlag bits via engine.DebugFlagByName at session
	// load time. Empty when no debug grants apply.
	DebugFlags []string
}

// AdminOperator is the persistence-layer representation of one admin
// dashboard operator. PasswordHash is the encoded argon2id string
// produced by pkg/services/auth.HashPassword. Grants follow the
// cmdsys grant syntax (e.g. "*.*", "cell.*", "bot.spawn").
type AdminOperator struct {
	Username     string
	PasswordHash string
	Grants       []string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// AdminOperatorRepository persists admin dashboard operators. The
// admin server queries this at login time and seeds a default
// admin/admin operator on first run when Count returns 0.
//
// Usernames are stored lowercase by contract (the admin server
// lowercases at every call site). Implementations should not
// re-normalize.
type AdminOperatorRepository interface {
	// GetByUsername returns the operator with the given username.
	// Returns (nil, ErrNotFound) when no row matches.
	GetByUsername(ctx context.Context, username string) (*AdminOperator, error)

	// Create inserts a new operator. Returns an error if the
	// username already exists (caller should pre-check with Count
	// for seeding flows, or GetByUsername for explicit creates).
	Create(ctx context.Context, op *AdminOperator) error

	// List returns every operator. Used by the future admin UI
	// "operators" page; today's only caller is tests.
	List(ctx context.Context) ([]*AdminOperator, error)

	// Delete removes an operator by username. No error when the
	// username doesn't exist.
	Delete(ctx context.Context, username string) error

	// UpdatePasswordHash rotates the password hash. Returns
	// ErrNotFound when the username doesn't exist.
	UpdatePasswordHash(ctx context.Context, username, hash string) error

	// Count returns the total number of operator rows. Backs the
	// "if 0, seed admin/admin" check at NewServer startup.
	Count(ctx context.Context) (int, error)
}
