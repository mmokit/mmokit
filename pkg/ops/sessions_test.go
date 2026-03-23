package ops

import (
	"sync"
	"testing"
)

func TestSetGet(t *testing.T) {
	ps := NewPlayerSessions()
	ps.Set(1, "alice")

	if got := ps.Get(1); got != "alice" {
		t.Fatalf("Get(1) = %q, want %q", got, "alice")
	}
}

func TestGetUnknown(t *testing.T) {
	ps := NewPlayerSessions()

	if got := ps.Get(999); got != "" {
		t.Fatalf("Get(999) = %q, want empty string", got)
	}
}

func TestRemoveThenGet(t *testing.T) {
	ps := NewPlayerSessions()
	ps.Set(1, "alice")
	ps.Remove(1)

	if got := ps.Get(1); got != "" {
		t.Fatalf("Get(1) after Remove = %q, want empty string", got)
	}
}

func TestConcurrentAccess(t *testing.T) {
	ps := NewPlayerSessions()
	var wg sync.WaitGroup
	const n = 1000

	// Concurrent Set
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(id uint32) {
			defer wg.Done()
			ps.Set(id, "user")
		}(uint32(i))
	}
	wg.Wait()

	// Concurrent Get
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(id uint32) {
			defer wg.Done()
			ps.Get(id)
		}(uint32(i))
	}
	wg.Wait()

	// Concurrent Remove
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(id uint32) {
			defer wg.Done()
			ps.Remove(id)
		}(uint32(i))
	}
	wg.Wait()

	// Verify all removed
	for i := 0; i < n; i++ {
		if got := ps.Get(uint32(i)); got != "" {
			t.Fatalf("Get(%d) after concurrent Remove = %q, want empty", i, got)
		}
	}
}
