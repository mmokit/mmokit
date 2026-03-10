package game

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/zenion/gameserver/pkg/persist"
)

const playersCollection = "players"

// PlayerRepo is an in-memory player database with async persistence.
// All runtime reads hit the in-memory map. The Store is read at startup
// (LoadAll) and written to asynchronously via the AsyncWriter.
type PlayerRepo struct {
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
	return r.players[username]
}

// GetOrCreate returns existing player data or creates a new entry.
// New players are automatically marked dirty.
func (r *PlayerRepo) GetOrCreate(username string) *PlayerData {
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
	r.dirty[username] = true
}

// FlushDirty serializes all dirty players and enqueues them for async write.
func (r *PlayerRepo) FlushDirty() {
	if len(r.dirty) == 0 {
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
	log.Printf("persist: flushed %d dirty players", count)
}

// All returns the full player map (for shutdown save-all).
func (r *PlayerRepo) All() map[string]*PlayerData {
	return r.players
}
