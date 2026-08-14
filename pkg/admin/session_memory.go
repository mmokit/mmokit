package admin

import (
	"encoding/hex"
	"sync"
	"time"

	"github.com/zenion/mmokit/pkg/services/auth"
)

// MemorySessionStore is the default SessionStore. Keyed by hex-encoded
// HashToken bytes.
type MemorySessionStore struct {
	mu      sync.RWMutex
	records map[string]SessionRecord
}

func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{records: make(map[string]SessionRecord)}
}

func (m *MemorySessionStore) Create(rec SessionRecord) (string, []byte, error) {
	token, hash, err := auth.NewToken()
	if err != nil {
		return "", nil, err
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now()
	}
	if rec.LastSeenAt.IsZero() {
		rec.LastSeenAt = rec.CreatedAt
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[hex.EncodeToString(hash)] = rec
	return token, hash, nil
}

func (m *MemorySessionStore) Lookup(hash []byte) (SessionRecord, bool) {
	m.mu.RLock()
	rec, ok := m.records[hex.EncodeToString(hash)]
	m.mu.RUnlock()
	if !ok {
		return SessionRecord{}, false
	}
	if time.Now().After(rec.ExpiresAt) {
		return SessionRecord{}, false
	}
	return rec, true
}

func (m *MemorySessionStore) Touch(hash []byte, slidingTTL time.Duration) error {
	key := hex.EncodeToString(hash)
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.records[key]
	if !ok {
		return nil
	}
	rec.LastSeenAt = time.Now()
	if slidingTTL > 0 {
		newExp := time.Now().Add(slidingTTL)
		if newExp.After(rec.ExpiresAt) {
			rec.ExpiresAt = newExp
		}
	}
	m.records[key] = rec
	return nil
}

func (m *MemorySessionStore) Delete(hash []byte) error {
	m.mu.Lock()
	delete(m.records, hex.EncodeToString(hash))
	m.mu.Unlock()
	return nil
}

func (m *MemorySessionStore) DeleteExpired() error {
	now := time.Now()
	m.mu.Lock()
	for k, rec := range m.records {
		if now.After(rec.ExpiresAt) {
			delete(m.records, k)
		}
	}
	m.mu.Unlock()
	return nil
}
