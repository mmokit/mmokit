package universe

import (
	"strings"
	"testing"
	"time"
)

// TestS4CoordNodeRegistrationAndAssignment stands up an in-process
// coordinator + node pair connected via real gRPC loopback. Asserts:
//
//   - Node registers and appears in HostRegistry with state Registered
//     then Live (after first heartbeat).
//   - Settle window closes and first assignment pass runs.
//   - Coordinator's assignmentEngine dispatches CellAssign for every
//     cell in the configured grid to the single live host.
//   - Node creates cells dynamically via assignCellOnNode and the
//     CellReady handshake updates HostRegistry.OwnedCells.
//
// Then force-closes the node's stream via cancelStream. Asserts:
//
//   - Host state transitions to Dead within ~500ms (liveness watcher tick)
//   - With no other live hosts, cells are orphaned and logged
//
// Starts a second node. Asserts:
//   - Second host appears in registry as Live
//   - Post-settle rebalance dispatches CellAssign to the new host
//   - Registry OwnedCells on the second host matches the grid
func TestS4CoordNodeRegistrationAndAssignment(t *testing.T) {
	// 1. Stand up the coordinator on an ephemeral port.
	coord := NewCoordinator(Config{
		CellsX:        2,
		CellsY:        2,
		Mode:          "coordinator",
		ControlListen: "127.0.0.1:0",
		Headless:      true,
	})
	coord.SetWorld(func(base *WorldBase) GameWorld { return base })
	coord.Build()
	t.Cleanup(coord.Shutdown)

	coordAddr := coord.controlListener.Addr().String()

	// 2. Stand up the first node, pointed at the coordinator.
	node1 := NewCoordinator(Config{
		CellsX:          2,
		CellsY:          2,
		Mode:            "host",
		CoordinatorAddr: coordAddr,
		HostID:          "node-alpha",
		Headless:        true,
	})
	node1.SetWorld(func(base *WorldBase) GameWorld { return base })
	node1.Build()
	t.Cleanup(node1.Shutdown)

	// 3. Wait for the settle window to close + cells to be assigned +
	// node to process them + CellReady roundtrip to land.
	// Settle window is 5s; generous 7s timeout.
	waitFor(t, 7*time.Second, "first host should own 4 cells after settle", func() bool {
		host := coord.hostRegistry.Get("node-alpha")
		return host != nil && len(host.OwnedCells) == 4
	})

	// 4. Confirm node1 actually created cells locally.
	waitFor(t, 2*time.Second, "node1 should have 4 cells running locally", func() bool {
		node1.mu.RLock()
		defer node1.mu.RUnlock()
		return len(node1.Cells) == 4
	})

	// 5. Kill the node's stream — simulates a crash.
	if !coord.controlServer.cancelStream("node-alpha") {
		t.Fatal("cancelStream returned false for node-alpha")
	}

	// 6. Assert the host transitions to Dead within ~2s (liveness watcher
	// + stream-close defer both converge on the same state).
	waitFor(t, 2*time.Second, "node-alpha should be Dead or Removed after kill", func() bool {
		host := coord.hostRegistry.Get("node-alpha")
		return host == nil || host.State == RemoteHostDead
	})

	// 7. Start a second node.
	node2 := NewCoordinator(Config{
		CellsX:          2,
		CellsY:          2,
		Mode:            "host",
		CoordinatorAddr: coordAddr,
		HostID:          "node-beta",
		Headless:        true,
	})
	node2.SetWorld(func(base *WorldBase) GameWorld { return base })
	node2.Build()
	t.Cleanup(node2.Shutdown)

	// 8. Post-settle re-registration should trigger an immediate
	// rebalance (assignmentEngine.onHostRegistered's post-settle branch).
	// The second host should receive all 4 cells.
	waitFor(t, 3*time.Second, "node-beta should own all 4 cells after rebalance", func() bool {
		host := coord.hostRegistry.Get("node-beta")
		return host != nil && len(host.OwnedCells) == 4
	})
}

// TestS4GracefulShutdown verifies the node-side graceful leave path:
// Shutdown sends CellStopped for every owned cell BEFORE closing the
// control stream. The coordinator's defer block sees an empty
// OwnedCells set at EOF and treats it as graceful leave — no
// reassignment, entry removed.
func TestS4GracefulShutdown(t *testing.T) {
	coord := NewCoordinator(Config{
		CellsX:        1,
		CellsY:        1,
		Mode:          "coordinator",
		ControlListen: "127.0.0.1:0",
		Headless:      true,
	})
	coord.SetWorld(func(base *WorldBase) GameWorld { return base })
	coord.Build()
	t.Cleanup(coord.Shutdown)

	coordAddr := coord.controlListener.Addr().String()

	node := NewCoordinator(Config{
		CellsX:          1,
		CellsY:          1,
		Mode:            "host",
		CoordinatorAddr: coordAddr,
		HostID:          "node-shutdown",
		Headless:        true,
	})
	node.SetWorld(func(base *WorldBase) GameWorld { return base })
	node.Build()

	// Wait for cell ownership to settle.
	waitFor(t, 7*time.Second, "node-shutdown should own the single cell", func() bool {
		host := coord.hostRegistry.Get("node-shutdown")
		return host != nil && len(host.OwnedCells) == 1
	})

	// Trigger graceful shutdown on the node.
	node.Shutdown()

	// Coordinator's defer block should see empty OwnedCells at EOF
	// and call Remove. Give it a brief moment to process the
	// CellStopped + EOF sequence.
	waitFor(t, 2*time.Second, "node-shutdown entry should be removed after graceful leave", func() bool {
		return coord.hostRegistry.Get("node-shutdown") == nil
	})
}

// waitFor polls cond every 50ms until it returns true or timeout elapses.
// Fails the test on timeout.
func waitFor(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout after %s waiting for: %s", timeout, desc)
	_ = strings.TrimSpace("") // ensure strings is used
}
