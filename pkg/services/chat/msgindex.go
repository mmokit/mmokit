package chat

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

type msgIDTTLMap struct {
	ttl   time.Duration
	mu    sync.Mutex
	items map[string]msgIDEntry
}

type msgIDEntry struct {
	ChannelID uuid.UUID
	ExpiresAt time.Time
}

func newMsgIDTTLMap(ttl time.Duration) *msgIDTTLMap {
	return &msgIDTTLMap{ttl: ttl, items: map[string]msgIDEntry{}}
}

func (m *msgIDTTLMap) Put(msgID string, channelID uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[msgID] = msgIDEntry{ChannelID: channelID, ExpiresAt: time.Now().Add(m.ttl)}
}

func (m *msgIDTTLMap) Get(msgID string) (uuid.UUID, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.items[msgID]
	if !ok || time.Now().After(e.ExpiresAt) {
		return uuid.Nil, false
	}
	return e.ChannelID, true
}

func (m *msgIDTTLMap) Sweep(now time.Time) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k, e := range m.items {
		if now.After(e.ExpiresAt) {
			delete(m.items, k)
			n++
		}
	}
	return n
}
