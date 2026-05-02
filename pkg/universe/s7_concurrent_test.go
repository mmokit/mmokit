package universe

import (
	"sync"
	"testing"
	"time"
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
//     at a cell ID missing from the ownership map.
func TestS7ConcurrentHandoffDuringSplit(t *testing.T) {
	forEachTopology(t, FixtureConfig{
		CellsX: 2, CellsY: 2, CellSize: 1024,
		HostIDs: []string{"host-a", "host-b"},
	}, func(t *testing.T, fx clusterFixture) {
		parent := CellID{X: 0, Y: 0}
		parentKey := string(parent.MeshID())

		// Capture pre-split owner so we can observe async teardown in
		// distributed mode.
		parentHost := fx.CellOwner(parentKey)
		if parentHost == "" {
			t.Fatalf("pre-split: parent %s has no owner", parentKey)
		}

		// Pre-seed a handful of session routes pointing at the parent cell.
		// The split commit's fallback-remap path will rewrite these to the
		// first child, which is what we want to stress-test against the
		// concurrent writes coming from the handoff goroutine.
		for connID := uint32(100); connID < 108; connID++ {
			fx.Coord().sessionRoutes.Set(&SessionRoute{
				Key:      SessionKey{GatewayID: InprocGatewayID, ConnID: connID},
				Username: "u",
				HostID:   parentHost,
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
			splitDone <- fx.Coord().SplitCell(parent, true)
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
					fx.Coord().setPlayerNode(connID, parentKey)
				}
				// Also exercise the read path: a gateway would normally
				// call Get to resolve the destination host.
				for connID := uint32(100); connID < 108; connID++ {
					_, _ = fx.Coord().sessionRoutes.Get(SessionKey{
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

		// Invariant 1: the parent cell is gone.
		// req.Done blocks until teardown completes, so a single-shot check suffices.
		if fx.HostOwnsCell(parentHost, parentKey) {
			t.Errorf("post-split: source host %s still owns cell %s", parentHost, parentKey)
		}
		if owner := fx.CellOwner(parentKey); owner != "" {
			t.Errorf("post-split: parent %s still owned by %q, want no owner", parentKey, owner)
		}

		// Invariant 2: all 4 children exist in the ownership view.
		children := parent.Children()
		for _, ch := range children {
			if owner := fx.CellOwner(string(ch.MeshID())); owner == "" {
				t.Errorf("post-split: child %v missing from ownership map", ch)
			}
		}

		// Invariant 3: session routes that used to point at the parent key
		// now point at something still in the ownership map. The split
		// commit's fallback remap moves them to children[0]; the concurrent
		// handoff writer may have re-pointed some of them mid-flight.
		// Either way, no route should be left pointing at a cell ID that
		// no longer exists anywhere in the cluster.
		for connID := uint32(100); connID < 108; connID++ {
			key := SessionKey{GatewayID: InprocGatewayID, ConnID: connID}
			route, ok := fx.Coord().sessionRoutes.Get(key)
			if !ok {
				continue
			}
			if owner := fx.CellOwner(route.CellID); owner == "" {
				t.Errorf("session conn=%d: post-split CellID=%s no longer has an owner",
					connID, route.CellID)
			}
		}
	})
}
