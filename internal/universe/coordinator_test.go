package universe

import (
	"testing"

	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/ops"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

func newTestCoordinator() *pkguniverse.Coordinator {
	log := logger.New()
	connMgr := net.NewConnManager()
	playerDB := game.NewPlayerRepo(nil)
	playerSessions := ops.NewPlayerSessions()
	cfg := game.DefaultGameConfig()
	platformCfg := engine.Config{TickRate: 20}

	grid := pkguniverse.GridConfig{MinSX: -1, MaxSX: 1, MinSY: -1, MaxSY: 1}
	factory := GameNodeFactory(cfg, connMgr, playerDB, playerSessions, log)
	return pkguniverse.NewCoordinator(grid, platformCfg, connMgr, log, factory)
}

func TestNewCoordinator_Creates9Nodes(t *testing.T) {
	c := newTestCoordinator()
	if len(c.Nodes) != 9 {
		t.Fatalf("expected 9 nodes, got %d", len(c.Nodes))
	}
}

func TestNewCoordinator_NetIDBaseNonOverlapping(t *testing.T) {
	c := newTestCoordinator()

	seen := make(map[uint32]bool)
	for _, node := range c.Nodes {
		id := node.Engine.NextNetID()
		base := id - 1
		bucket := base / 10_000_000
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

	centerID := pkguniverse.SectorID(coords.SectorCoord{SX: 0, SY: 0})
	centerNode := c.Nodes[centerID]
	if len(centerNode.Neighbors) != 8 {
		t.Fatalf("expected center node to have 8 neighbors, got %d", len(centerNode.Neighbors))
	}

	cornerID := pkguniverse.SectorID(coords.SectorCoord{SX: -1, SY: -1})
	cornerNode := c.Nodes[cornerID]
	if len(cornerNode.Neighbors) != 3 {
		t.Fatalf("expected corner node (-1,-1) to have 3 neighbors, got %d", len(cornerNode.Neighbors))
	}
}

func TestNewCoordinator_BridgeWired(t *testing.T) {
	c := newTestCoordinator()

	for _, node := range c.Nodes {
		bridge := node.Bridge
		if bridge == nil {
			t.Fatalf("node %s has nil Bridge", node.ID)
		}

		// Should NOT be the NoopNodeBridge
		if _, ok := bridge.(pkguniverse.NoopNodeBridge); ok {
			t.Fatalf("node %s has NoopNodeBridge, expected real bridge", node.ID)
		}
	}
}
