package universe

import (
	"sync"
	"testing"
)

// TestCellBridgeDispatcherInvalidationConcurrentWithNeighborReconcile is a
// race-detector regression for mesh-control topology updates running beside a
// cell's PostSystems dispatcher initialization. Cell.Neighbors is protected by
// Process.mu; the cached dispatcher has its own lock, with Process acquired
// first whenever both are needed.
func TestCellBridgeDispatcherInvalidationConcurrentWithNeighborReconcile(t *testing.T) {
	coord := New(Config{CellsX: 2, CellsY: 1, CellSize: 1024, Headless: true})
	coord.Build()
	t.Cleanup(coord.Shutdown)

	left := coord.Cells[MeshCellID("cell_0_0")]
	right := coord.Cells[MeshCellID("cell_1_0")]
	if left == nil || right == nil {
		t.Fatalf("fixture cells missing: left=%p right=%p", left, right)
	}
	bridge := unwrapCellBridge(left.Bridge)
	if bridge == nil {
		t.Fatal("left cell has no cellBridge")
	}

	const iterations = 1000
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range iterations {
			_ = bridge.ensureBorderDispatcher()
		}
	}()
	go func() {
		defer wg.Done()
		for i := range iterations {
			coord.mu.Lock()
			if i%2 == 0 {
				left.Neighbors[right.MeshID] = right
			} else {
				delete(left.Neighbors, right.MeshID)
			}
			bridge.invalidateBorderDispatcher()
			coord.mu.Unlock()
		}
	}()
	wg.Wait()
}
