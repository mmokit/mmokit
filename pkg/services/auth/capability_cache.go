package auth

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// CapabilityCache wraps a Repository's HasCapability behind a TTL'd
// in-process cache. Reduces Postgres pressure when the same user/cap
// pair is checked often (e.g. on every chat op).
//
// Cache entries are stored only on positive results; negatives are
// cached too (for the same TTL) so repeated unauthorized callers don't
// hammer the DB. Invalidate clears a specific entry — call it from
// Grant/Revoke paths so revocation propagates within the cache TTL.
type CapabilityCache struct {
	repo Repository
	ttl  time.Duration

	mu    sync.RWMutex
	items map[capCacheKey]capCacheEntry
}

type capCacheKey struct {
	UserID     uuid.UUID
	Capability string
}

type capCacheEntry struct {
	Has       bool
	ExpiresAt time.Time
}

func NewCapabilityCache(repo Repository, ttl time.Duration) *CapabilityCache {
	return &CapabilityCache{
		repo:  repo,
		ttl:   ttl,
		items: map[capCacheKey]capCacheEntry{},
	}
}

func (c *CapabilityCache) HasCapability(ctx context.Context, userID uuid.UUID, capability string) (bool, error) {
	k := capCacheKey{userID, capability}
	now := time.Now()

	c.mu.RLock()
	if e, ok := c.items[k]; ok && now.Before(e.ExpiresAt) {
		c.mu.RUnlock()
		return e.Has, nil
	}
	c.mu.RUnlock()

	has, err := c.repo.HasCapability(ctx, userID, capability)
	if err != nil {
		return false, err
	}
	c.mu.Lock()
	c.items[k] = capCacheEntry{Has: has, ExpiresAt: now.Add(c.ttl)}
	c.mu.Unlock()
	return has, nil
}

func (c *CapabilityCache) Invalidate(userID uuid.UUID, capability string) {
	c.mu.Lock()
	delete(c.items, capCacheKey{userID, capability})
	c.mu.Unlock()
}
