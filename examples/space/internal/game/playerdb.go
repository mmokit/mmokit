package game

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	gamepersist "github.com/mmokit/mmokit/examples/space/internal/persist"
	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/logger"
	"github.com/mmokit/mmokit/pkg/persist"
	pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

// PlayerRepo is an in-memory player database with async persistence
// via PlayerFlusher. All runtime reads hit the in-memory map. The
// backing repositories are read at startup (LoadAll) and written via
// batched upserts on FlushDirty — engine identity goes to
// engine.players, game state goes to space.player_state, both inside
// one pgx.Tx per flush.
//
// Keyed by the immutable UserID; byUsername is a secondary index for
// the username-based admin/console lookups.
//
// Thread-safe: the mutex protects concurrent access from the
// marketplace service running on operation router worker goroutines.
type PlayerRepo struct {
	mu         sync.RWMutex
	players    map[uuid.UUID]*PlayerData
	byUsername map[string]uuid.UUID
	dirty      map[uuid.UUID]bool
	engineRepo persist.PlayerRepository
	gameRepo   gamepersist.PlayerStateRepository
	flusher    *PlayerFlusher
}

// NewPlayerRepo creates a PlayerRepo backed by the engine identity repo
// (pkg/persist.PlayerRepository) and the game-state repo
// (internal/persist.PlayerStateRepository). pool is used by the flusher
// to open one transaction per flush so both halves commit atomically.
// log may be nil if the caller doesn't want flush log output.
func NewPlayerRepo(
	pool *pgxpool.Pool,
	engineRepo persist.PlayerRepository,
	gameRepo gamepersist.PlayerStateRepository,
	log *logger.Logger,
) *PlayerRepo {
	return &PlayerRepo{
		players:    make(map[uuid.UUID]*PlayerData),
		byUsername: make(map[string]uuid.UUID),
		dirty:      make(map[uuid.UUID]bool),
		engineRepo: engineRepo,
		gameRepo:   gameRepo,
		flusher:    NewPlayerFlusher(pool, engineRepo, gameRepo, log),
	}
}

// LoadAll streams every player from both repositories into the
// in-memory cache. First pass builds skeleton PlayerData entries from
// engine.players (identity); second pass merges currencies / cargo /
// bank / equipment from space.player_state. Players with no game-state
// row keep zero/empty defaults so a fresh player is indistinguishable
// from an existing one with no balances. Call during startup before
// the game loop starts.
func (r *PlayerRepo) LoadAll(ctx context.Context) error {
	count := 0
	err := r.engineRepo.LoadAll(ctx, func(snap *persist.PlayerSnapshot) error {
		pd := snapshotToPlayerData(snap, nil)
		r.players[pd.UserID] = pd
		r.byUsername[pd.Username] = pd.UserID
		count++
		return nil
	})
	if err != nil {
		return fmt.Errorf("load players: %w", err)
	}

	if r.gameRepo != nil {
		err = r.gameRepo.LoadAll(ctx, func(snap *gamepersist.PlayerStateSnapshot) error {
			pd, ok := r.players[snap.UserID]
			if !ok {
				// Orphan game-state row (no matching engine identity).
				// FK enforces this shouldn't happen, but tolerate it by
				// skipping — the orphan will get cleaned up the next time
				// the engine row is deleted with ON DELETE CASCADE.
				return nil
			}
			mergeGameStateIntoPlayerData(pd, snap)
			return nil
		})
		if err != nil && !errors.Is(err, gamepersist.ErrNotFound) {
			return fmt.Errorf("load player state: %w", err)
		}
	}

	if count > 0 {
		log.Printf("persist: loaded %d players", count)
	}
	return nil
}

// Get returns the player data for a username, or nil if not found.
// Username-keyed lookups go through the byUsername secondary index;
// callers who already have a session should prefer Bind or GetByUserID.
func (r *PlayerRepo) Get(username string) *PlayerData {
	r.mu.RLock()
	defer r.mu.RUnlock()
	uid, ok := r.byUsername[username]
	if !ok {
		return nil
	}
	return r.players[uid]
}

// GetByUserID returns the player data for a UserID, or nil if not found.
func (r *PlayerRepo) GetByUserID(userID uuid.UUID) *PlayerData {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.players[userID]
}

// Bind is the canonical "session→PlayerData" entry point. Returns the
// existing PlayerData if one is already cached for s.UserID; otherwise
// creates a new in-memory entry, indexes it by both UserID and Username,
// and marks it dirty so the flusher persists it on the next cycle.
//
// Callers MUST have a session whose UserID is non-zero — Bind panics if
// it isn't, because we can't safely fabricate an identity from just a
// display name (the schema FK requires user_id to match a real auth
// principal). Tests must populate UserID on their PlayerSession before
// calling Bind.
func (r *PlayerRepo) Bind(s *engine.PlayerSession) *PlayerData {
	if s == nil {
		panic("PlayerRepo.Bind: nil session")
	}
	if s.UserID == uuid.Nil {
		panic(fmt.Sprintf("PlayerRepo.Bind: session for %q has zero UserID — auth must populate it before bind", s.Username))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.players[s.UserID]; ok {
		// Keep the secondary index pointing at the current username in
		// case the display name changed since the snapshot was loaded.
		if p.Username != s.Username && s.Username != "" {
			delete(r.byUsername, p.Username)
			p.Username = s.Username
			r.byUsername[s.Username] = s.UserID
			r.dirty[s.UserID] = true
		}
		return p
	}
	p := &PlayerData{
		UserID:    s.UserID,
		Username:  s.Username,
		CreatedAt: time.Now(),
	}
	r.players[s.UserID] = p
	if s.Username != "" {
		r.byUsername[s.Username] = s.UserID
	}
	r.dirty[s.UserID] = true
	return p
}

// MarkDirty flags a player for persistence on the next flush.
// Resolves the username through byUsername; no-op when the user is
// unknown to this repo.
func (r *PlayerRepo) MarkDirty(username string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if uid, ok := r.byUsername[username]; ok {
		r.dirty[uid] = true
	}
}

// MarkDirtyByUserID flags a player for persistence by their UserID.
// Preferred when the caller already has a session or PlayerData in
// hand (avoids the byUsername round-trip).
func (r *PlayerRepo) MarkDirtyByUserID(userID uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.players[userID]; ok {
		r.dirty[userID] = true
	}
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
	for uid := range r.dirty {
		p := r.players[uid]
		if p == nil {
			continue
		}
		r.flusher.Mark(enginePlayerSnapshot(p), gamePlayerStateSnapshot(p))
	}
	r.dirty = make(map[uuid.UUID]bool)
	r.mu.Unlock()
	return r.flusher.Flush(ctx)
}

// All returns the full player map keyed by UserID (for shutdown save-all).
func (r *PlayerRepo) All() map[uuid.UUID]*PlayerData {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.players
}

// GetBankBalance returns the quantity of an item in a player's bank. Thread-safe.
func (r *PlayerRepo) GetBankBalance(player string, itemID uint32) int32 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	uid, ok := r.byUsername[player]
	if !ok {
		return 0
	}
	p := r.players[uid]
	if p == nil || p.Bank == nil {
		return 0
	}
	return p.Bank[itemID]
}

// ModifyBank atomically modifies a player's bank map. Thread-safe.
func (r *PlayerRepo) ModifyBank(player string, fn func(bank map[uint32]int32)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	uid, ok := r.byUsername[player]
	if !ok {
		return
	}
	p := r.players[uid]
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
	uid, ok := r.byUsername[player]
	if !ok {
		return 0
	}
	p := r.players[uid]
	if p == nil {
		return 0
	}
	return p.GetCurrency(currencyID)
}

// ModifyCurrency atomically modifies a player's currency balance. Thread-safe.
func (r *PlayerRepo) ModifyCurrency(player string, currencyID uint32, delta int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	uid, ok := r.byUsername[player]
	if !ok {
		return
	}
	p := r.players[uid]
	if p == nil {
		return
	}
	if p.Currencies == nil {
		p.Currencies = make(map[uint32]int64)
	}
	p.Currencies[currencyID] += delta
}

// enginePlayerSnapshot converts the identity half of PlayerData to the
// engine-side persist snapshot DTO. The two types are deliberately
// distinct so storage representation can evolve independently from the
// runtime type.
func enginePlayerSnapshot(pd *PlayerData) *persist.PlayerSnapshot {
	return &persist.PlayerSnapshot{
		UserID:    pd.UserID,
		Username:  pd.Username,
		CellID:    string(pkguniverse.CellID{X: pd.CellX, Y: pd.CellY, Depth: pd.CellDepth}.MeshID()),
		PosX:      pd.X,
		PosY:      pd.Y,
		CreatedAt: pd.CreatedAt,
		LastLogin: pd.LastLogin,
	}
}

// gamePlayerStateSnapshot converts the game-state half of PlayerData
// (currencies, cargo, bank, equipment) into the game-side persist DTO.
// The maps are deep-copied so concurrent MarkDirty/Flush doesn't race
// on the live PlayerData maps while the snapshot is in flight.
func gamePlayerStateSnapshot(pd *PlayerData) *gamepersist.PlayerStateSnapshot {
	return &gamepersist.PlayerStateSnapshot{
		UserID:     pd.UserID,
		Currencies: cloneU32Int64Map(pd.Currencies),
		Cargo:      cloneU32Int32Map(pd.Cargo),
		Bank:       cloneU32Int32Map(pd.Bank),
		Equipment: gamepersist.EquipmentSnapshot{
			Weapon1:  pd.Equipment.Weapon1,
			Weapon2:  pd.Equipment.Weapon2,
			Shield:   pd.Equipment.Shield,
			Thruster: pd.Equipment.Thruster,
		},
	}
}

// snapshotToPlayerData builds the in-memory game type from the engine
// identity snapshot. gameSnap may be nil — when absent, currencies /
// cargo / bank default to empty maps and equipment to its zero value,
// so a fresh player (no space.player_state row yet) is indistinguishable
// from one that exists but has no balances.
//
// CellID parse failures default to (0, 0, 0) — players at origin if the
// persisted cell_id is malformed.
func snapshotToPlayerData(engineSnap *persist.PlayerSnapshot, gameSnap *gamepersist.PlayerStateSnapshot) *PlayerData {
	var cell pkguniverse.CellID
	if engineSnap.CellID != "" {
		cell, _ = pkguniverse.ParseCellID(engineSnap.CellID)
	}
	pd := &PlayerData{
		UserID:     engineSnap.UserID,
		Username:   engineSnap.Username,
		X:          engineSnap.PosX,
		Y:          engineSnap.PosY,
		CellX:      cell.X,
		CellY:      cell.Y,
		CellDepth:  cell.Depth,
		Currencies: map[uint32]int64{},
		Cargo:      map[uint32]int32{},
		Bank:       map[uint32]int32{},
		HasSave:    true,
		CreatedAt:  engineSnap.CreatedAt,
		LastLogin:  engineSnap.LastLogin,
	}
	if gameSnap != nil {
		mergeGameStateIntoPlayerData(pd, gameSnap)
	}
	return pd
}

// mergeGameStateIntoPlayerData copies the game-state half (currencies,
// cargo, bank, equipment) into the PlayerData. Used by both
// snapshotToPlayerData (single-player path) and PlayerRepo.LoadAll
// (startup two-pass merge).
func mergeGameStateIntoPlayerData(pd *PlayerData, snap *gamepersist.PlayerStateSnapshot) {
	if snap.Currencies != nil {
		pd.Currencies = snap.Currencies
	}
	if snap.Cargo != nil {
		pd.Cargo = snap.Cargo
	}
	if snap.Bank != nil {
		pd.Bank = snap.Bank
	}
	pd.Equipment = EquipmentSave{
		Weapon1:  snap.Equipment.Weapon1,
		Weapon2:  snap.Equipment.Weapon2,
		Shield:   snap.Equipment.Shield,
		Thruster: snap.Equipment.Thruster,
	}
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

// Compile-time assertion that *PlayerData satisfies the universe-side
// accessor contract used by ResolvePlayerTarget's offline branch.
var _ pkguniverse.PlayerDataAccessor = (*PlayerData)(nil)

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
//
// Get holds no lock after returning: the *PlayerData pointer aliases the
// repo's shared map entry. This matches every other PlayerRepo.Get caller
// (ModifyCurrency, ModifyBank, marketplace settlement, etc.). Mutations
// of int32/float32 scalar fields are practically safe on amd64; callers
// mutating composite fields (maps, slices) must coordinate externally.
type repoLocator struct{ repo *PlayerRepo }

func (l repoLocator) Get(username string) (pkguniverse.PlayerDataAccessor, func(), bool) {
	pd := l.repo.Get(username)
	if pd == nil {
		return nil, nil, false
	}
	return pd, func() { l.repo.MarkDirtyByUserID(pd.UserID) }, true
}

// ListOffline returns every persisted player as a PlayerDataAccessor
// snapshot. Used by player.list --all to enumerate offline players.
func (l repoLocator) ListOffline() []pkguniverse.PlayerDataAccessor {
	all := l.repo.All()
	out := make([]pkguniverse.PlayerDataAccessor, 0, len(all))
	for _, pd := range all {
		out = append(out, pd)
	}
	return out
}
