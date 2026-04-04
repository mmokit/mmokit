package universe

// NetIDAllocator hands out net ID range bases for dynamically created nodes.
// Ranges are recycled when nodes are destroyed during cell merges.
type NetIDAllocator struct {
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
	if len(a.freeList) > 0 {
		base := a.freeList[len(a.freeList)-1]
		a.freeList = a.freeList[:len(a.freeList)-1]
		return base
	}
	base := a.next
	a.next += a.size
	return base
}

// Release returns a net ID range base to the free list. Only call after the
// node using this range has been fully drained of entities.
func (a *NetIDAllocator) Release(base uint32) {
	a.freeList = append(a.freeList, base)
}
