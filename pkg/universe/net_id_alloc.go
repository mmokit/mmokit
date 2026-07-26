package universe

import "sync"

// NetIDAllocator hands out net ID range bases for dynamically created nodes.
// Ranges are recycled when nodes are destroyed during cell merges.
type NetIDAllocator struct {
	mu       sync.Mutex
	next     uint32
	size     uint32
	freeList []uint32
}

// NewNetIDAllocator creates an allocator that starts handing out ranges from
// startBase with the given range size per node.
func NewNetIDAllocator(startBase, rangeSize uint32) *NetIDAllocator {
	return &NetIDAllocator{
		next: startBase,
		size: rangeSize,
	}
}

// Allocate returns the base of a fresh net ID range. Reuses previously released
// ranges before allocating new ones.
func (a *NetIDAllocator) Allocate() uint32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.freeList) > 0 {
		base := a.freeList[len(a.freeList)-1]
		a.freeList = a.freeList[:len(a.freeList)-1]
		return base
	}
	base := a.next
	if a.size > 0 && base > ^uint32(0)-a.size {
		panic("universe: NetIDAllocator exhausted uint32 range")
	}
	a.next += a.size
	return base
}

// Release returns a net ID range base to the free list. Only call after the
// node using this range has been fully drained of entities.
func (a *NetIDAllocator) Release(base uint32) {
	a.mu.Lock()
	a.freeList = append(a.freeList, base)
	a.mu.Unlock()
}

// SetBase advances the allocator's floor to base. Used by node mode when a
// NetIDRangeGrant arrives from the coordinator. A delayed/stale grant must
// never move next backward and reissue a range that Allocate already handed
// out. All allocator operations are safe for concurrent use.
func (a *NetIDAllocator) SetBase(base uint32) {
	a.mu.Lock()
	if base > a.next {
		a.next = base
	}
	a.mu.Unlock()
}
