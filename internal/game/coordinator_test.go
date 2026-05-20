package game

import (
	"testing"

	gamepersisttest "github.com/zenion/mmoserver/internal/persist/persisttest"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/ops"
	"github.com/zenion/mmoserver/pkg/persist/persisttest"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

func newTestCoordinator() *pkguniverse.Process {
	log := logger.New()
	connMgr := net.NewConnManager()
	playerDB := NewPlayerRepo(nil, persisttest.NewPlayerRepoMock(), gamepersisttest.NewPlayerStateRepoMock(), nil)
	playerSessions := ops.NewPlayerSessions()
	cfg := DefaultGameConfig()

	coord := pkguniverse.New(pkguniverse.Config{
		CellsX:      3,
		CellsY:      3,
		TickRate:    20,
		ConnManager: connMgr,
		Logger:      log,
	})
	mmokit.AddState(coord, NewGameWorldStateFactory(&cfg, playerDB, playerSessions, nil, nil))
	GameSetup(coord)
	coord.Build()
	return coord
}

func TestNewCoordinator_Creates9Cells(t *testing.T) {
	c := newTestCoordinator()
	if len(c.Cells) != 9 {
		t.Fatalf("expected 9 nodes, got %d", len(c.Cells))
	}
}

func TestNewCoordinator_NetIDBaseNonOverlapping(t *testing.T) {
	c := newTestCoordinator()

	seen := make(map[uint32]bool)
	for _, node := range c.Cells {
		id := node.Engine.NextNetID()
		base := id - 1
		bucket := base / 10_000_000
		if seen[bucket] {
			t.Fatalf("overlapping netIDBase bucket %d for node %s", bucket, node.MeshID)
		}
		seen[bucket] = true
	}
	if len(seen) != 9 {
		t.Fatalf("expected 9 unique netID buckets, got %d", len(seen))
	}
}

func TestNewCoordinator_TopologyWired(t *testing.T) {
	c := newTestCoordinator()

	centerID := pkguniverse.CellID{X: 1, Y: 1}.MeshID()
	centerNode := c.Cells[centerID]
	if len(centerNode.Neighbors) != 8 {
		t.Fatalf("expected center node to have 8 neighbors, got %d", len(centerNode.Neighbors))
	}

	cornerID := pkguniverse.CellID{X: 0, Y: 0}.MeshID()
	cornerNode := c.Cells[cornerID]
	if len(cornerNode.Neighbors) != 3 {
		t.Fatalf("expected corner node (0,0) to have 3 neighbors, got %d", len(cornerNode.Neighbors))
	}
}

func TestNewCoordinator_BridgeWired(t *testing.T) {
	c := newTestCoordinator()

	for _, node := range c.Cells {
		bridge := node.Bridge
		if bridge == nil {
			t.Fatalf("node %s has nil Bridge", node.MeshID)
		}

		// Should NOT be the NoopBridge
		if _, ok := bridge.(pkguniverse.NoopBridge); ok {
			t.Fatalf("node %s has NoopBridge, expected real bridge", node.MeshID)
		}
	}
}
