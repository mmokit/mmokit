package auth_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zenion/mmokit/pkg/services/auth"
)

type counterRepo struct {
	auth.Repository
	calls int64
}

func (c *counterRepo) HasCapability(ctx context.Context, u uuid.UUID, cap string) (bool, error) {
	atomic.AddInt64(&c.calls, 1)
	return cap == "chat.admin", nil
}

func TestCapabilityCache_HitsAreCached(t *testing.T) {
	r := &counterRepo{}
	cache := auth.NewCapabilityCache(r, 30*time.Second)
	uid := uuid.New()
	for i := 0; i < 5; i++ {
		has, _ := cache.HasCapability(context.Background(), uid, "chat.admin")
		if !has {
			t.Fatal("expected cached hit to remain true")
		}
	}
	if got := atomic.LoadInt64(&r.calls); got != 1 {
		t.Fatalf("expected 1 underlying call, got %d", got)
	}
}

func TestCapabilityCache_InvalidateClearsEntry(t *testing.T) {
	r := &counterRepo{}
	cache := auth.NewCapabilityCache(r, 30*time.Second)
	uid := uuid.New()
	_, _ = cache.HasCapability(context.Background(), uid, "chat.admin")
	cache.Invalidate(uid, "chat.admin")
	_, _ = cache.HasCapability(context.Background(), uid, "chat.admin")
	if got := atomic.LoadInt64(&r.calls); got != 2 {
		t.Fatalf("expected 2 calls (invalidate forces re-fetch), got %d", got)
	}
}
