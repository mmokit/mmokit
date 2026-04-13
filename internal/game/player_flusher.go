package game

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/persist"
)

// PlayerFlusher owns dirty-tracking + batched upserts for player state.
// Replaces the legacy generic AsyncWriter with a typed flush coordinator
// that converts internal PlayerData to persist.PlayerSnapshot and
// submits via persist.PlayerRepository.SaveBatch (which uses pgx.Batch
// under the hood).
//
// Thread safety: Mark and Flush both take mu. Flush snapshots and
// clears the dirty map under the lock, then runs the batch outside
// the lock so concurrent Mark calls don't block on DB latency.
type PlayerFlusher struct {
	repo persist.PlayerRepository
	log  *logger.Logger

	mu    sync.Mutex
	dirty map[string]*persist.PlayerSnapshot
}

// NewPlayerFlusher constructs a flusher. The log argument may be nil
// for tests that don't need flush logging.
func NewPlayerFlusher(repo persist.PlayerRepository, log *logger.Logger) *PlayerFlusher {
	return &PlayerFlusher{
		repo:  repo,
		log:   log,
		dirty: make(map[string]*persist.PlayerSnapshot),
	}
}

// Mark records that the given snapshot needs to be flushed. The
// snapshot pointer is captured immediately — callers should pass a
// fresh copy if they continue mutating the source.
func (f *PlayerFlusher) Mark(snapshot *persist.PlayerSnapshot) {
	if snapshot == nil {
		return
	}
	f.mu.Lock()
	f.dirty[snapshot.Username] = snapshot
	f.mu.Unlock()
}

// Flush sends the current dirty set as one pgx.Batch upsert. Sorts by
// username to satisfy the PlayerRepository deadlock-prevention
// contract. Resets the dirty map on success; preserves it on error so
// the next flush retries.
//
// Returns the number of snapshots flushed (0 if the dirty set was
// empty) and any error from the underlying SaveBatch call.
func (f *PlayerFlusher) Flush(ctx context.Context) (int, error) {
	f.mu.Lock()
	if len(f.dirty) == 0 {
		f.mu.Unlock()
		return 0, nil
	}
	snapshots := make([]*persist.PlayerSnapshot, 0, len(f.dirty))
	for _, s := range f.dirty {
		snapshots = append(snapshots, s)
	}
	f.dirty = make(map[string]*persist.PlayerSnapshot)
	f.mu.Unlock()

	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].Username < snapshots[j].Username
	})

	if err := f.repo.SaveBatch(ctx, snapshots); err != nil {
		// Restore the dirty entries so the next flush retries them.
		f.mu.Lock()
		for _, s := range snapshots {
			if _, exists := f.dirty[s.Username]; !exists {
				f.dirty[s.Username] = s
			}
		}
		f.mu.Unlock()
		return 0, fmt.Errorf("player flush: %w", err)
	}
	return len(snapshots), nil
}

// FlushCell is a synchronous flush of every dirty player currently
// assigned to the given cell. S6/S7 will call this from the cell
// migration path so the destination node sees a consistent view of
// the players being handed off. Stub for S5 — no caller yet.
func (f *PlayerFlusher) FlushCell(ctx context.Context, cellID string) (int, error) {
	f.mu.Lock()
	var toFlush []*persist.PlayerSnapshot
	for username, snapshot := range f.dirty {
		if snapshot.CellID == cellID {
			toFlush = append(toFlush, snapshot)
			delete(f.dirty, username)
		}
	}
	f.mu.Unlock()
	if len(toFlush) == 0 {
		return 0, nil
	}
	sort.Slice(toFlush, func(i, j int) bool {
		return toFlush[i].Username < toFlush[j].Username
	})
	if err := f.repo.SaveBatch(ctx, toFlush); err != nil {
		// Restore so the next periodic flush retries.
		f.mu.Lock()
		for _, s := range toFlush {
			if _, exists := f.dirty[s.Username]; !exists {
				f.dirty[s.Username] = s
			}
		}
		f.mu.Unlock()
		return 0, fmt.Errorf("player flush cell %q: %w", cellID, err)
	}
	return len(toFlush), nil
}
