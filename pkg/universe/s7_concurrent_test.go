package universe

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
)

// TestS7ConcurrentHandoffDuringSplit exercises the atomicity of the S7-T9
// commit path under concurrent pressure: a goroutine drives a real
// SplitCell on a live 2x2 coordinator (cell game loops running) while a
// second goroutine repeatedly writes session routes against the parent
// cell key via the coordinator's public setPlayerNode path (the same
// entry point cellBridge uses for a normal cross-cell handoff).
//
// The test focuses narrowly on T9's atomic-commit surface: the
// sessionRoutes.remapCell call, the applyRegistryDelta helper, the
// broadcastPeerListIfReady call, targeted UpstreamSwitch dispatch, and
// the PendingAdminCmds-routed neighbor rewire (which moved off c.mu in
// T9 specifically so it wouldn't race with PostSystems).
//
// A successful run means:
//   - SplitCell returns without error
//   - No race detector violations on sessionRoutes / cellToHostMap /
//     HostRegistry / node.Neighbors
//   - Post-split topology is consistent (4 children exist, parent is gone)
//   - Session routes that used to point at the parent key no longer point
//     at a cell ID missing from cellToHostMap
func TestS7ConcurrentHandoffDuringSplit(t *testing.T) {
	coords.SetCellSize(8192)
	c := NewCoordinator(Config{
		CellsX:              2,
		CellsY:              2,
		CellSize:            8192,
		ConnManager:         net.NewConnManager(),
		Logger:              logger.New(),
		Headless:            true,
		DynamicPartitioning: DefaultPartitionConfig(),
		LoginHandler: func(connID uint32, msgs [][]byte) (string, any, error) {
			return "", nil, ErrLoginPending
		},
	})
	c.OnInit(func(w *WorldBase) {})
	c.Build()

	// Start every cell's game loop so the executor's serialize /
	// populate closures drain through PendingAdminCmds, and so the
	// neighbor-rewire directives applied after commit actually run
	// on the cell goroutine (where PostSystems reads them).
	ctx, cancel := context.WithCancel(context.Background())
	for _, cell := range c.Cells {
		go cell.Run(ctx)
	}
	time.Sleep(30 * time.Millisecond)
	t.Cleanup(func() {
		// Drop the cell game-loop context, then fully Shutdown the
		// coordinator and wait long enough for every ticker to drain.
		// Without the sleep, leftover cell goroutines can still hold
		// references to coords package globals when the next test in
		// the suite runs — a pre-existing test-isolation hazard that
		// T9's changes don't add but DO surface via -race.
		cancel()
		c.Shutdown()
		time.Sleep(100 * time.Millisecond)
	})

	parent := CellID{X: 0, Y: 0}
	parentKey := MeshCellID(parent)

	// Pre-seed a handful of session routes pointing at the parent cell.
	// The split commit's fallback-remap path will rewrite these to the
	// first child, which is what we want to stress-test against the
	// concurrent writes coming from the handoff goroutine.
	for connID := uint32(100); connID < 108; connID++ {
		c.sessionRoutes.Set(&SessionRoute{
			Key:      SessionKey{GatewayID: InprocGatewayID, ConnID: connID},
			Username: "u",
			HostID:   "local",
			CellID:   parentKey,
			Epoch:    1,
		})
	}

	var wg sync.WaitGroup
	splitDone := make(chan error, 1)

	// Split goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		splitDone <- c.SplitCell(parent, true)
	}()

	// Concurrent handoff goroutine: repeatedly rewrite session routes
	// against the parent key while the split is in-flight. Exits when
	// the split completes.
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			for connID := uint32(200); connID < 210; connID++ {
				c.setPlayerNode(connID, parentKey)
			}
			// Also exercise the read path: a gateway would normally
			// call Get to resolve the destination host.
			for connID := uint32(100); connID < 108; connID++ {
				_, _ = c.sessionRoutes.Get(SessionKey{
					GatewayID: InprocGatewayID, ConnID: connID,
				})
			}
			time.Sleep(100 * time.Microsecond)
		}
	}()

	var splitErr error
	select {
	case splitErr = <-splitDone:
	case <-time.After(10 * time.Second):
		close(stop)
		wg.Wait()
		t.Fatal("SplitCell did not complete within 10s")
	}
	close(stop)
	wg.Wait()

	if splitErr != nil {
		t.Fatalf("SplitCell failed: %v", splitErr)
	}

	// Invariant 1: the parent cell is gone from CellOwner.
	c.mu.RLock()
	_, parentStillInOwner := c.CellOwner[parent]
	children := parent.Children()
	childOwners := make(map[CellID]bool, 4)
	for _, ch := range children {
		_, childOwners[ch] = c.CellOwner[ch]
	}
	// Snapshot known cell IDs for invariant 3.
	known := make(map[string]struct{}, len(c.cellToHostMap))
	for k := range c.cellToHostMap {
		known[k] = struct{}{}
	}
	c.mu.RUnlock()

	if parentStillInOwner {
		t.Errorf("post-split: parent %v still in CellOwner", parent)
	}

	// Invariant 2: all 4 children exist in CellOwner.
	for _, ch := range children {
		if !childOwners[ch] {
			t.Errorf("post-split: child %v missing from CellOwner", ch)
		}
	}

	// Invariant 3: session routes that used to point at the parent key
	// now point at something in cellToHostMap. The split commit's
	// fallback remap moves them to children[0]; the concurrent handoff
	// writer may have re-pointed some of them mid-flight. Either way,
	// no route should be left pointing at a cell ID that no longer
	// exists in the ownership map.
	for connID := uint32(100); connID < 108; connID++ {
		key := SessionKey{GatewayID: InprocGatewayID, ConnID: connID}
		route, ok := c.sessionRoutes.Get(key)
		if !ok {
			continue
		}
		if _, exists := known[route.CellID]; !exists {
			t.Errorf("session conn=%d: post-split CellID=%s not in cellToHostMap",
				connID, route.CellID)
		}
	}
}
