package universe

import (
	"testing"

	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

func newTestCoordinator() *Coordinator {
	log := logger.New()
	connMgr := net.NewConnManager()
	playerDB := game.NewPlayerRepo(nil)
	cfg := game.DefaultGameConfig()
	platformCfg := engine.Config{TickRate: 20}
	return NewCoordinator(platformCfg, cfg, connMgr, playerDB, log)
}

func TestNewCoordinator_Creates9Nodes(t *testing.T) {
	c := newTestCoordinator()
	if len(c.Nodes) != 9 {
		t.Fatalf("expected 9 nodes, got %d", len(c.Nodes))
	}
}

func TestNewCoordinator_NetIDBaseNonOverlapping(t *testing.T) {
	c := newTestCoordinator()

	// Collect netIDBase values from all nodes by calling NextNetID
	// and verifying they fall in non-overlapping ranges.
	seen := make(map[uint32]bool)
	for _, node := range c.Nodes {
		id := node.Engine.NextNetID()
		// Each node's IDs should be in range [base+1, base+netIDRangeSize)
		// The base is id-1 (since NextNetID increments atomically before returning).
		base := id - 1
		bucket := base / netIDRangeSize
		if seen[bucket] {
			t.Fatalf("overlapping netIDBase bucket %d for node %s", bucket, node.ID)
		}
		seen[bucket] = true
	}
	if len(seen) != 9 {
		t.Fatalf("expected 9 unique netID buckets, got %d", len(seen))
	}
}

func TestNewCoordinator_TopologyWired(t *testing.T) {
	c := newTestCoordinator()

	// Center node (0,0) should have 8 neighbors
	centerID := pkguniverse.SectorID(coords.SectorCoord{SX: 0, SY: 0})
	centerNode := c.Nodes[centerID]
	if len(centerNode.Neighbors) != 8 {
		t.Fatalf("expected center node to have 8 neighbors, got %d", len(centerNode.Neighbors))
	}

	// Corner node (-1,-1) should have 3 neighbors: (0,-1), (-1,0), (0,0)
	cornerID := pkguniverse.SectorID(coords.SectorCoord{SX: -1, SY: -1})
	cornerNode := c.Nodes[cornerID]
	if len(cornerNode.Neighbors) != 3 {
		t.Fatalf("expected corner node (-1,-1) to have 3 neighbors, got %d", len(cornerNode.Neighbors))
	}
}

func TestNewCoordinator_BridgeWired(t *testing.T) {
	c := newTestCoordinator()

	for _, node := range c.Nodes {
		bridge := node.World.Bridge
		if bridge == nil {
			t.Fatalf("node %s has nil Bridge", node.ID)
		}

		// Should NOT be the NoopNodeBridge
		if _, ok := bridge.(game.NoopNodeBridge); ok {
			t.Fatalf("node %s has NoopNodeBridge, expected *nodeBridge", node.ID)
		}

		// Should be a *nodeBridge
		nb, ok := bridge.(*nodeBridge)
		if !ok {
			t.Fatalf("node %s Bridge is %T, expected *nodeBridge", node.ID, bridge)
		}
		if nb.node != node {
			t.Fatalf("node %s bridge.node does not point back to node", node.ID)
		}
	}
}
