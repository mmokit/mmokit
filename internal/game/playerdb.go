package game

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/zenion/mmoserver/pkg/persist"
)

const playersCollection = "players"

// PlayerRepo is an in-memory player database with async persistence.
// All runtime reads hit the in-memory map. The Store is read at startup
// (LoadAll) and written to asynchronously via the AsyncWriter.
// Thread-safe: the mutex protects concurrent access from the marketplace
// service running on operation router worker goroutines.
type PlayerRepo struct {
	mu      sync.RWMutex
	players map[string]*PlayerData
	dirty   map[string]bool
	writer  *persist.AsyncWriter
}

// NewPlayerRepo creates a PlayerRepo backed by the given async writer.
func NewPlayerRepo(writer *persist.AsyncWriter) *PlayerRepo {
	return &PlayerRepo{
		players: make(map[string]*PlayerData),
		dirty:   make(map[string]bool),
		writer:  writer,
	}
}

// LoadAll reads all player data from the store synchronously.
// Call during startup before the game loop starts.
func (r *PlayerRepo) LoadAll(store persist.Store) error {
	count := 0
	err := store.ForEach(playersCollection, func(key string, value []byte) error {
		var pd PlayerData
		if err := json.Unmarshal(value, &pd); err != nil {
			return fmt.Errorf("unmarshal player %s: %w", key, err)
		}
		pd.MigrateCargoIDs()
		pd.MigrateBankIDs()

		// After normal unmarshal, migrate any Flux stored in Bank[1] to the Flux field
		// and handle legacy float maps
		type legacyData struct {
			Cargo map[uint32]float64 `json:"cargo"`
			Bank  map[uint32]float64 `json:"bank"`
			Flux  float64            `json:"flux"`
		}
		var legacy legacyData
		if err := json.Unmarshal(value, &legacy); err == nil {
			// If Cargo map is nil after normal unmarshal but legacy has data, convert
			if pd.Cargo == nil && len(legacy.Cargo) > 0 {
				pd.Cargo = make(map[uint32]int32, len(legacy.Cargo))
				for id, qty := range legacy.Cargo {
					if v := int32(qty); v > 0 {
						pd.Cargo[id] = v
					}
				}
			}
			if pd.Bank == nil && len(legacy.Bank) > 0 {
				pd.Bank = make(map[uint32]int32, len(legacy.Bank))
				for id, qty := range legacy.Bank {
					if v := int32(qty); v > 0 {
						pd.Bank[id] = v
					}
				}
			}
			// Migrate Flux from Bank[1] or legacy flux field
			if fluxInBank, ok := pd.Bank[1]; ok {
				pd.Flux += int64(fluxInBank)
				delete(pd.Bank, 1)
			}
			if legacy.Flux > 0 {
				pd.Flux += int64(legacy.Flux)
			}
		}

		r.players[key] = &pd
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

// FlushDirty serializes all dirty players and enqueues them for async write.
func (r *PlayerRepo) FlushDirty() {
	r.mu.Lock()
	if len(r.dirty) == 0 {
		r.mu.Unlock()
		return
	}
	for username := range r.dirty {
		p := r.players[username]
		if p == nil {
			continue
		}
		data, err := json.Marshal(p)
		if err != nil {
			log.Printf("persist: marshal player %s: %v", username, err)
			continue
		}
		r.writer.Enqueue(persist.Op{
			Collection: playersCollection,
			Key:        username,
			Value:      data,
		})
	}
	count := len(r.dirty)
	r.dirty = make(map[string]bool)
	r.mu.Unlock()
	log.Printf("persist: flushed %d dirty players", count)
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

// GetFlux returns the player's flux balance. Thread-safe.
func (r *PlayerRepo) GetFlux(player string) int64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p := r.players[player]
	if p == nil {
		return 0
	}
	return p.Flux
}

// ModifyFlux atomically modifies a player's flux balance. Thread-safe.
func (r *PlayerRepo) ModifyFlux(player string, delta int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.players[player]
	if p == nil {
		return
	}
	p.Flux += delta
}
