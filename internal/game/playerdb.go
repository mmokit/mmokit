package game

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/persist"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// PlayerRepo is an in-memory player database with async persistence
// via PlayerFlusher. All runtime reads hit the in-memory map. The
// backing persist.PlayerRepository is read at startup (LoadAll) and
// written to via batched upserts on FlushDirty.
//
// Thread-safe: the mutex protects concurrent access from the
// marketplace service running on operation router worker goroutines.
type PlayerRepo struct {
	mu      sync.RWMutex
	players map[string]*PlayerData
	dirty   map[string]bool
	repo    persist.PlayerRepository
	flusher *PlayerFlusher
}

// NewPlayerRepo creates a PlayerRepo backed by the given repository.
// log may be nil if the caller doesn't want flush log output.
func NewPlayerRepo(repo persist.PlayerRepository, log *logger.Logger) *PlayerRepo {
	return &PlayerRepo{
		players: make(map[string]*PlayerData),
		dirty:   make(map[string]bool),
		repo:    repo,
		flusher: NewPlayerFlusher(repo, log),
	}
}

// LoadAll streams every player from the repository into the in-memory
// cache. Call during startup before the game loop starts.
func (r *PlayerRepo) LoadAll(ctx context.Context) error {
	count := 0
	err := r.repo.LoadAll(ctx, func(snap *persist.PlayerSnapshot) error {
		pd := snapshotToPlayerData(snap)
		r.players[pd.Username] = pd
		count++
		return nil
	})
	if err != nil {
		return fmt.Errorf("load players: %w", err)
	}
	if count > 0 {
		log.Printf("persist: loaded %d players", count)
	}
	return nil
}

// Get returns the player data for a username, or nil if not found.
func (r *PlayerRepo) Get(username string) *PlayerData {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.players[username]
}

// GetOrCreate returns existing player data or creates a new entry.
// New players are automatically marked dirty.
func (r *PlayerRepo) GetOrCreate(username string) *PlayerData {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.players[username]; ok {
		return p
	}
	p := &PlayerData{
		Username:  username,
		CreatedAt: time.Now(),
	}
	r.players[username] = p
	r.dirty[username] = true
	return p
}

// MarkDirty flags a player for persistence on the next flush.
func (r *PlayerRepo) MarkDirty(username string) {
	r.mu.Lock()
	r.dirty[username] = true
	r.mu.Unlock()
}

// FlushDirty snapshots all dirty players and submits them as one
// batched upsert via PlayerFlusher. Returns the number of records
// flushed and any error from the underlying repository call.
//
// The dirty map is reset whether or not the flush succeeds — the
// flusher restores entries on failure so the next call retries.
func (r *PlayerRepo) FlushDirty(ctx context.Context) (int, error) {
	r.mu.Lock()
	if len(r.dirty) == 0 {
		r.mu.Unlock()
		return 0, nil
	}
	for username := range r.dirty {
		p := r.players[username]
		if p == nil {
			continue
		}
		r.flusher.Mark(playerSnapshot(p))
	}
	r.dirty = make(map[string]bool)
	r.mu.Unlock()
	return r.flusher.Flush(ctx)
}

// All returns the full player map (for shutdown save-all).
func (r *PlayerRepo) All() map[string]*PlayerData {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.players
}

// GetBankBalance returns the quantity of an item in a player's bank. Thread-safe.
func (r *PlayerRepo) GetBankBalance(player string, itemID uint32) int32 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p := r.players[player]
	if p == nil || p.Bank == nil {
		return 0
	}
	return p.Bank[itemID]
}

// ModifyBank atomically modifies a player's bank map. Thread-safe.
func (r *PlayerRepo) ModifyBank(player string, fn func(bank map[uint32]int32)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.players[player]
	if p == nil {
		return
	}
	if p.Bank == nil {
		p.Bank = make(map[uint32]int32)
	}
	fn(p.Bank)
}

// GetCurrency returns the player's balance of the given currency. Thread-safe.
func (r *PlayerRepo) GetCurrency(player string, currencyID uint32) int64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p := r.players[player]
	if p == nil {
		return 0
	}
	return p.GetCurrency(currencyID)
}

// ModifyCurrency atomically modifies a player's currency balance. Thread-safe.
func (r *PlayerRepo) ModifyCurrency(player string, currencyID uint32, delta int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.players[player]
	if p == nil {
		return
	}
	if p.Currencies == nil {
		p.Currencies = make(map[uint32]int64)
	}
	p.Currencies[currencyID] += delta
}

// playerSnapshot converts the in-memory PlayerData to the persist
// snapshot DTO. The two types are deliberately distinct so storage
// representation can evolve independently from the runtime type.
func playerSnapshot(pd *PlayerData) *persist.PlayerSnapshot {
	return &persist.PlayerSnapshot{
		Username:   pd.Username,
		CellID:     fmt.Sprintf("cell_%d_%d", pd.CellX, pd.CellY),
		PosX:       pd.X,
		PosY:       pd.Y,
		Currencies: cloneU32Int64Map(pd.Currencies),
		Cargo:      cloneU32Int32Map(pd.Cargo),
		Bank:       cloneU32Int32Map(pd.Bank),
		Equipment: persist.EquipmentSnapshot{
			Weapon1:  pd.Equipment.Weapon1,
			Weapon2:  pd.Equipment.Weapon2,
			Shield:   pd.Equipment.Shield,
			Thruster: pd.Equipment.Thruster,
		},
		CreatedAt: pd.CreatedAt,
		LastLogin: pd.LastLogin,
	}
}

// snapshotToPlayerData converts a persist snapshot back to the
// in-memory game type. CellID parse failures default to (0, 0) —
// players at origin if the persisted cell_id is malformed.
func snapshotToPlayerData(snap *persist.PlayerSnapshot) *PlayerData {
	var cellX, cellY int32
	fmt.Sscanf(snap.CellID, "cell_%d_%d", &cellX, &cellY)
	pd := &PlayerData{
		Username:   snap.Username,
		X:          snap.PosX,
		Y:          snap.PosY,
		CellX:      cellX,
		CellY:      cellY,
		Currencies: snap.Currencies,
		Cargo:      snap.Cargo,
		Bank:       snap.Bank,
		Equipment: EquipmentSave{
			Weapon1:  snap.Equipment.Weapon1,
			Weapon2:  snap.Equipment.Weapon2,
			Shield:   snap.Equipment.Shield,
			Thruster: snap.Equipment.Thruster,
		},
		HasSave:   true,
		CreatedAt: snap.CreatedAt,
		LastLogin: snap.LastLogin,
	}
	return pd
}

// cloneU32Int64Map returns a deep copy. Used by playerSnapshot so the
// captured snapshot doesn't share map storage with the live PlayerData
// (otherwise concurrent MarkDirty/Flush would race on map mutations).
func cloneU32Int64Map(src map[uint32]int64) map[uint32]int64 {
	if src == nil {
		return nil
	}
	out := make(map[uint32]int64, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func cloneU32Int32Map(src map[uint32]int32) map[uint32]int32 {
	if src == nil {
		return nil
	}
	out := make(map[uint32]int32, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// Locator returns a universe.PlayerDataLocator backed by this repo. The
// returned value implements universe.PlayerDataLocator so the engine can
// look up offline players via the universe-side ResolvePlayerTarget
// helper (no engine→game type leakage). Called once at startup from
// cmd/server/main.go.
func (r *PlayerRepo) Locator() pkguniverse.PlayerDataLocator {
	return repoLocator{repo: r}
}

// repoLocator adapts *PlayerRepo to the universe.PlayerDataLocator
// interface. Get returns the persisted *PlayerData (which itself
// satisfies PlayerDataAccessor) plus a DirtyMark closure that calls
// MarkDirty so the repo flushes the change to Postgres on the next
// FlushDirty cycle.
type repoLocator struct{ repo *PlayerRepo }

func (l repoLocator) Get(username string) (pkguniverse.PlayerDataAccessor, func(), bool) {
	pd := l.repo.Get(username)
	if pd == nil {
		return nil, nil, false
	}
	return pd, func() { l.repo.MarkDirty(username) }, true
}
