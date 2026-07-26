package universe

import (
	"sync"
	"testing"
)

func TestNetIDAllocator_Sequential(t *testing.T) {
	a := NewNetIDAllocator(0, 1000)

	if got := a.Allocate(); got != 0 {
		t.Errorf("first Allocate() = %d, want 0", got)
	}
	if got := a.Allocate(); got != 1000 {
		t.Errorf("second Allocate() = %d, want 1000", got)
	}
	if got := a.Allocate(); got != 2000 {
		t.Errorf("third Allocate() = %d, want 2000", got)
	}
}

func TestNetIDAllocator_Recycle(t *testing.T) {
	a := NewNetIDAllocator(0, 1000)

	b0 := a.Allocate() // 0
	b1 := a.Allocate() // 1000
	_ = a.Allocate()   // 2000

	// Release two ranges
	a.Release(b0)
	a.Release(b1)

	// Should reuse released ranges (LIFO)
	if got := a.Allocate(); got != b1 {
		t.Errorf("recycled Allocate() = %d, want %d", got, b1)
	}
	if got := a.Allocate(); got != b0 {
		t.Errorf("recycled Allocate() = %d, want %d", got, b0)
	}

	// Next should be fresh
	if got := a.Allocate(); got != 3000 {
		t.Errorf("fresh Allocate() = %d, want 3000", got)
	}
}

func TestNetIDAllocator_StartOffset(t *testing.T) {
	a := NewNetIDAllocator(40_000_000, 10_000_000)

	if got := a.Allocate(); got != 40_000_000 {
		t.Errorf("Allocate() = %d, want 40000000", got)
	}
	if got := a.Allocate(); got != 50_000_000 {
		t.Errorf("Allocate() = %d, want 50000000", got)
	}
}

func TestNetIDAllocator_SetBaseNeverMovesBackward(t *testing.T) {
	a := NewNetIDAllocator(100, 10)
	if got := a.Allocate(); got != 100 {
		t.Fatalf("first Allocate() = %d, want 100", got)
	}
	a.SetBase(100) // delayed grant for the range already consumed
	if got := a.Allocate(); got != 110 {
		t.Fatalf("Allocate() after stale SetBase = %d, want 110", got)
	}
	a.SetBase(1_000)
	if got := a.Allocate(); got != 1_000 {
		t.Fatalf("Allocate() after forward SetBase = %d, want 1000", got)
	}
}

func TestNetIDAllocator_ConcurrentSetBaseAndAllocateNeverDuplicates(t *testing.T) {
	const allocations = 2_000
	a := NewNetIDAllocator(0, 1)
	results := make(chan uint32, allocations)
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range allocations / 4 {
				results <- a.Allocate()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for base := uint32(500); base < 1_500; base++ {
			a.SetBase(base)
		}
	}()
	wg.Wait()
	close(results)

	seen := make(map[uint32]struct{}, allocations)
	for base := range results {
		if _, duplicate := seen[base]; duplicate {
			t.Fatalf("allocator returned duplicate base %d", base)
		}
		seen[base] = struct{}{}
	}
}

func TestNetIDAllocator_PanicsBeforeUint32Wrap(t *testing.T) {
	a := NewNetIDAllocator(^uint32(0)-5, 4)
	if got := a.Allocate(); got != ^uint32(0)-5 {
		t.Fatalf("first near-wrap Allocate() = %d", got)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("Allocate did not panic before wrapping into a duplicate range")
		}
	}()
	_ = a.Allocate()
}
