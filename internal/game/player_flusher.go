package game

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	gamepersist "github.com/zenion/mmokit/internal/persist"
	"github.com/zenion/mmokit/pkg/logger"
	"github.com/zenion/mmokit/pkg/persist"
)

// PlayerFlusher owns dirty-tracking + batched upserts spanning engine
// identity (engine.players) and game state (space.player_state). Each
// Flush wraps both SaveBatchTx calls in a single pgx transaction so the
// two halves commit atomically.
//
// Thread safety: Mark and Flush both take mu. Flush snapshots and
// clears the dirty maps under the lock, then runs the batch outside
// the lock so concurrent Marks don't block on DB latency.
type PlayerFlusher struct {
	pool       *pgxpool.Pool
	engineRepo persist.PlayerRepository
	gameRepo   gamepersist.PlayerStateRepository
	log        *logger.Logger

	mu             sync.Mutex
	dirtyEngine    map[uuid.UUID]*persist.PlayerSnapshot
	dirtyGameState map[uuid.UUID]*gamepersist.PlayerStateSnapshot
}

// NewPlayerFlusher constructs a flusher. The log argument may be nil
// for tests that don't need flush logging. pool may be nil in tests
// that never call Flush.
func NewPlayerFlusher(
	pool *pgxpool.Pool,
	engineRepo persist.PlayerRepository,
	gameRepo gamepersist.PlayerStateRepository,
	log *logger.Logger,
) *PlayerFlusher {
	return &PlayerFlusher{
		pool:           pool,
		engineRepo:     engineRepo,
		gameRepo:       gameRepo,
		log:            log,
		dirtyEngine:    make(map[uuid.UUID]*persist.PlayerSnapshot),
		dirtyGameState: make(map[uuid.UUID]*gamepersist.PlayerStateSnapshot),
	}
}

// Mark records both halves of the player. The caller MUST pass the
// engine snapshot; gameSnap may be nil for snapshot-less callers (e.g.
// debug-flag-only mutations), in which case only the engine half is
// flushed for that user.
func (f *PlayerFlusher) Mark(engineSnap *persist.PlayerSnapshot, gameSnap *gamepersist.PlayerStateSnapshot) {
	if engineSnap == nil {
		return
	}
	f.mu.Lock()
	f.dirtyEngine[engineSnap.UserID] = engineSnap
	if gameSnap != nil {
		f.dirtyGameState[engineSnap.UserID] = gameSnap
	}
	f.mu.Unlock()
}

// Flush snapshots the dirty set, opens one tx, and runs both
// SaveBatchTx calls under it. On error the dirty maps are restored
// so the next flush retries.
//
// Returns the number of engine snapshots flushed (game-state count
// follows the same set; nil game snapshots are tolerated).
func (f *PlayerFlusher) Flush(ctx context.Context) (int, error) {
	f.mu.Lock()
	if len(f.dirtyEngine) == 0 {
		f.mu.Unlock()
		return 0, nil
	}
	engineSnaps := make([]*persist.PlayerSnapshot, 0, len(f.dirtyEngine))
	for _, s := range f.dirtyEngine {
		engineSnaps = append(engineSnaps, s)
	}
	gameSnaps := make([]*gamepersist.PlayerStateSnapshot, 0, len(f.dirtyGameState))
	for _, s := range f.dirtyGameState {
		gameSnaps = append(gameSnaps, s)
	}
	f.dirtyEngine = make(map[uuid.UUID]*persist.PlayerSnapshot)
	f.dirtyGameState = make(map[uuid.UUID]*gamepersist.PlayerStateSnapshot)
	f.mu.Unlock()

	sort.Slice(engineSnaps, func(i, j int) bool { return engineSnaps[i].UserID.String() < engineSnaps[j].UserID.String() })
	sort.Slice(gameSnaps, func(i, j int) bool { return gameSnaps[i].UserID.String() < gameSnaps[j].UserID.String() })

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		f.restoreDirty(engineSnaps, gameSnaps)
		return 0, fmt.Errorf("player flush: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	if err := f.engineRepo.SaveBatchTx(ctx, tx, engineSnaps); err != nil {
		f.restoreDirty(engineSnaps, gameSnaps)
		return 0, fmt.Errorf("player flush: engine save: %w", err)
	}
	if err := f.gameRepo.SaveBatchTx(ctx, tx, gameSnaps); err != nil {
		f.restoreDirty(engineSnaps, gameSnaps)
		return 0, fmt.Errorf("player flush: game save: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		f.restoreDirty(engineSnaps, gameSnaps)
		return 0, fmt.Errorf("player flush: commit: %w", err)
	}
	committed = true
	return len(engineSnaps), nil
}

// restoreDirty re-inserts snapshots into the dirty maps after a failed
// flush so the next call retries. Existing entries (from concurrent
// Marks during the flush) take precedence — they're newer than the
// in-flight snapshots.
func (f *PlayerFlusher) restoreDirty(engineSnaps []*persist.PlayerSnapshot, gameSnaps []*gamepersist.PlayerStateSnapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range engineSnaps {
		if _, exists := f.dirtyEngine[s.UserID]; !exists {
			f.dirtyEngine[s.UserID] = s
		}
	}
	for _, s := range gameSnaps {
		if _, exists := f.dirtyGameState[s.UserID]; !exists {
			f.dirtyGameState[s.UserID] = s
		}
	}
}

// FlushCell is a stub kept for S6/S7 cell-migration safety — no caller yet.
func (f *PlayerFlusher) FlushCell(ctx context.Context, cellID string) (int, error) {
	return 0, nil
}
