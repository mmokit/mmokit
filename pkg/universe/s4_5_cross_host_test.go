package universe

import (
	"testing"
	"time"
)

// TestS45CrossHostBorderFrameAndHandoff is the S4.5 validation gate:
// it stands up a coordinator + 2 nodes in-process, waits for the
// coordinator's PeerList broadcast to propagate, asserts both nodes
// see the full cellToHostMap + have peer HostNetwork connections to
// each other, then sends a synthetic HandoffPrepare from a cell on
// node A to a cell on node B via the grpcBridge. Verifies the frame
// arrives on the destination cell's Inbox over the MeshData gRPC
// stream — the first end-to-end proof that cross-node routing works.
func TestS45CrossHostBorderFrameAndHandoff(t *testing.T) {
	// 1. Stand up the coordinator on an ephemeral port.
	coord := New(Config{
		CellsX:              2,
		CellsY:              2,
		Mode:                "coordinator",
		ControlListen:       "127.0.0.1:0",
		Headless:            true,
		SettleWindow:        50 * time.Millisecond,
		ShutdownGracePeriod: 50 * time.Millisecond,
		World:               func(base *Stage) GameWorld { return base },
	})
	coord.Build()
	t.Cleanup(coord.Shutdown)

	coordAddr := coord.controlListener.Addr().String()

	// 2. Stand up two nodes pointed at the coordinator.
	// Host IDs "test-node-0" and "test-node-3" are chosen because the
	// rendezvous hash (fnv64a) deterministically produces a 2-2 cell
	// split for the 2x2 grid with these IDs. Generic names like "node-a"
	// / "node-b" produce a 4-0 split (all cells to one host).
	const hostIDA = "test-node-0"
	const hostIDB = "test-node-3"

	nodeA := startS45Host(t, coordAddr, hostIDA)
	t.Cleanup(nodeA.Shutdown)

	nodeB := startS45Host(t, coordAddr, hostIDB)
	t.Cleanup(nodeB.Shutdown)

	// 3. Wait for settle window to close + both nodes to own cells.
	// Test config pins SettleWindow=50ms; 3s tolerates ticker + roundtrip.
	waitFor(t, 3*time.Second, "both nodes should own all 4 cells between them", func() bool {
		a := coord.hostRegistry.Get(hostIDA)
		b := coord.hostRegistry.Get(hostIDB)
		if a == nil || b == nil {
			return false
		}
		return len(a.OwnedCells)+len(b.OwnedCells) == 4
	})

	// 4. Wait for the PeerList broadcast to populate each node's
	// cellToHostMap. Every cell (4 total) should appear in both.
	waitFor(t, 3*time.Second, "nodeA should see all 4 cells in cellToHostMap", func() bool {
		nodeA.Control.mu.RLock()
		defer nodeA.Control.mu.RUnlock()
		return len(nodeA.Control.cellToHostMap) == 4
	})
	waitFor(t, 3*time.Second, "nodeB should see all 4 cells in cellToHostMap", func() bool {
		nodeB.Control.mu.RLock()
		defer nodeB.Control.mu.RUnlock()
		return len(nodeB.Control.cellToHostMap) == 4
	})

	// 5. Each node should have connected to the other as a peer via
	// HostNetwork after receiving the PeerList.
	waitFor(t, 3*time.Second, "nodeA should have nodeB as a HostNetwork peer", func() bool {
		h := nodeA.localHost()
		if h == nil || h.Network == nil {
			return false
		}
		h.Network.mu.RLock()
		_, ok := h.Network.peers[hostIDB]
		h.Network.mu.RUnlock()
		return ok
	})
	waitFor(t, 3*time.Second, "nodeB should have nodeA as a HostNetwork peer", func() bool {
		h := nodeB.localHost()
		if h == nil || h.Network == nil {
			return false
		}
		h.Network.mu.RLock()
		_, ok := h.Network.peers[hostIDA]
		h.Network.mu.RUnlock()
		return ok
	})

	// 6. Pick a cell owned by nodeA and a cell owned by nodeB. The
	// host IDs above are chosen to guarantee a 2-2 rendezvous split,
	// so this should always find cells on both nodes. The Skipf is a
	// defensive fallback in case the assignment engine behaves
	// unexpectedly (e.g. both registered so close together that the
	// coordinator only saw one host at settle time).
	var cellOnA, cellOnB string
	hostA := coord.hostRegistry.Get(hostIDA)
	hostB := coord.hostRegistry.Get(hostIDB)
	for cid := range hostA.OwnedCells {
		cellOnA = string(cid)
		break
	}
	for cid := range hostB.OwnedCells {
		cellOnB = string(cid)
		break
	}
	if cellOnA == "" || cellOnB == "" {
		t.Skipf("rendezvous gave one node all cells (a=%d b=%d); can't exercise cross-node path",
			len(hostA.OwnedCells), len(hostB.OwnedCells))
	}

	// 7. Grab the source cell from nodeA.
	nodeA.mu.RLock()
	srcCell := nodeA.Cells[MeshCellID(cellOnA)]
	nodeA.mu.RUnlock()
	if srcCell == nil {
		t.Fatalf("nodeA has no local cell %s (ownership table vs node.Cells mismatch)", cellOnA)
	}

	// 8. Send a synthetic Handoff via the source cell's bridge.
	// In node mode this is a grpcBridge (Task 4), so shouldUseLocal
	// returns false for cross-host destinations and the payload routes
	// through HostNetwork.SendReliable to nodeB.
	payload := &HandoffPayload{
		NetID:        12345,
		Epoch:        1,
		CommitTick:   1,
		TransferBlob: []byte("s4.5 cross-node test"),
	}
	srcCell.Bridge.SendHandoff(MeshCellID(cellOnB), payload)

	// 9. Verify arrival on the destination cell's inbox (on nodeB).
	nodeB.mu.RLock()
	dstCell := nodeB.Cells[MeshCellID(cellOnB)]
	nodeB.mu.RUnlock()
	if dstCell == nil {
		t.Fatalf("nodeB has no local cell %s", cellOnB)
	}

	select {
	case msg := <-dstCell.Inbox:
		if msg.Type != MsgHandoff {
			t.Fatalf("expected MsgHandoff, got %d", msg.Type)
		}
		if msg.Handoff == nil {
			t.Fatal("Handoff payload is nil")
		}
		if msg.Handoff.NetID != 12345 {
			t.Errorf("NetID = %d, want 12345", msg.Handoff.NetID)
		}
		if string(msg.Handoff.TransferBlob) != "s4.5 cross-node test" {
			t.Errorf("TransferBlob = %q, want %q", msg.Handoff.TransferBlob, "s4.5 cross-node test")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for Handoff on nodeB — cross-node routing failed")
	}
}

// startS45Host is a test helper that builds a node-mode Process
// pointed at the given coordinator address.
func startS45Host(t *testing.T, coordAddr, hostID string) *Process {
	t.Helper()
	node := New(Config{
		CellsX:              2,
		CellsY:              2,
		Mode:                "host",
		CoordinatorAddr:     coordAddr,
		HostID:              hostID,
		Headless:            true,
		ShutdownGracePeriod: 50 * time.Millisecond,
		World:               func(base *Stage) GameWorld { return base },
	})
	node.Build()
	return node
}
