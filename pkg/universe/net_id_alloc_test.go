package universe

import "testing"

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
