package universe

import (
	"context"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
)

// ═══════════════════════════════════════════════════════════════════════════
// S7 T10 — distributed split integration test
//
// TestS7SplitAcrossHosts stands up a 2-host in-process Coordinator with a
// 2x2 cell grid (each host owns 2 cells via round-robin assignment). It
// picks a cell owned by host-a, spawns real entities into each of the four
// quadrants that the orchestrator will carve out, then forces a split via
// Coordinator.SplitCell(..., bypassCooldown=true). Goes through the real
// orchestrator → dispatcher → executor → applyCellTransferCommit flow.
//
// Post-commit invariants:
//
//  1. The parent cell is gone from coord.Cells and coord.CellOwner.
//  2. All 4 children exist in cellToHostMap, distributed between host-a
//     and host-b according to rendezvous + locality.
//  3. Entity count is conserved across the 4 children — the same number
//     of entities we spawned pre-split reappears in the post-split
//     children combined (no loss).
//  4. Every spawned NetID ends up on exactly one child, in the same
//     quadrant we planted it in.
//  5. Pre-seeded session routes that used to point at the parent key were
//     remapped onto an existing child cell in cellToHostMap.
//  6. Source host's Host.Cells map no longer contains the parent CellID.
// ═══════════════════════════════════════════════════════════════════════════

func TestS7SplitAcrossHosts(t *testing.T) {
	coords.SetCellSize(1024)

	cfg := Config{
		CellsX:              2,
		CellsY:              2,
		CellSize:            1024,
		TestHosts:           []string{"host-a", "host-b"},
		Headless:            true,
		ConnManager:         net.NewConnManager(),
		Logger:              logger.New(),
		DynamicPartitioning: DefaultPartitionConfig(),
		LoginHandler:        func(connID uint32, msgs [][]byte) (string, any, error) { return "", nil, ErrLoginPending },
	}
	coord := NewCoordinator(cfg)
	coord.SetWorld(func(base *WorldBase) GameWorld { return base })
	coord.Build()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		coord.Shutdown()
	})
	for _, cell := range coord.Cells {
		go cell.Run(ctx)
	}
	time.Sleep(30 * time.Millisecond)

	// Pick cell_0_0 which lands on host-a via round-robin assignment.
	parentCellID := CellID{X: 0, Y: 0}
	parentKey := MeshCellID(parentCellID)
	coord.mu.RLock()
	srcHost := coord.cellToHostMap[parentKey]
	srcCell := coord.Cells[parentKey]
	coord.mu.RUnlock()
	if srcHost != "host-a" {
		t.Fatalf("pre-split: expected parent cell on host-a, got %q", srcHost)
	}
	if srcCell == nil {
		t.Fatalf("pre-split: coord.Cells[%s] is nil", parentKey)
	}

	// Spawn real entities in each of the 4 quadrants so we can verify
	// conservation after the split. Positions are in world-space units
	// because spawnTestEntity just stores them raw; the executor quadrant
	// filter uses local-cell coords, which at depth-0 cell {0,0} are the
	// same as world coords.
	cellSize := parentCellID.Size(coords.CellSize)
	half := cellSize / 2
	type plant struct {
		netID uint32
		x, y  float32
		// which quadrant we expect the executor to put this in
		wantQuadrant int
	}
	plants := []plant{
		{netID: 9001, x: half * 0.25, y: half * 0.25, wantQuadrant: 0}, // BL
		{netID: 9002, x: half * 1.75, y: half * 0.25, wantQuadrant: 1}, // BR
		{netID: 9003, x: half * 0.25, y: half * 1.75, wantQuadrant: 2}, // TL
		{netID: 9004, x: half * 1.75, y: half * 1.75, wantQuadrant: 3}, // TR
		// One extra in BR to double up and verify conservation counts 5.
		{netID: 9005, x: half * 1.5, y: half * 0.2, wantQuadrant: 1},
	}
	execOnLoop(t, srcCell, func() {
		for _, p := range plants {
			spawnTestEntity(srcCell, p.netID, p.x, p.y)
		}
	})

	// Pre-seed a session route pointing at the parent key so we can assert
	// that the split commit's remap fallback rewrote it to an existing
	// child in cellToHostMap (invariant 5).
	coord.sessionRoutes.Set(&SessionRoute{
		Key:      SessionKey{GatewayID: InprocGatewayID, ConnID: 900},
		Username: "splittest",
		HostID:   "host-a",
		CellID:   parentKey,
		Epoch:    1,
	})

	// Drive the split through the real path.
	if err := coord.SplitCell(parentCellID, true); err != nil {
		t.Fatalf("SplitCell: %v", err)
	}

	// ── Invariant 1: parent gone from coord.Cells / CellOwner. ───────────
	coord.mu.RLock()
	_, parentStillInCells := coord.Cells[parentKey]
	_, parentStillInOwner := coord.CellOwner[parentCellID]
	coord.mu.RUnlock()
	if parentStillInCells {
		t.Errorf("post-split: coord.Cells still has parent %s", parentKey)
	}
	if parentStillInOwner {
		t.Errorf("post-split: coord.CellOwner still has parent %v", parentCellID)
	}

	// ── Invariant 2: all 4 children in cellToHostMap, distributed. ───────
	children := parentCellID.Children()
	childKeys := make([]string, 4)
	coord.mu.RLock()
	hostSet := map[string]int{}
	for i, ch := range children {
		key := MeshCellID(ch)
		childKeys[i] = key
		owner, ok := coord.cellToHostMap[key]
		if !ok {
			t.Errorf("post-split: child %s missing from cellToHostMap", key)
			continue
		}
		if owner != "host-a" && owner != "host-b" {
			t.Errorf("post-split: child %s owner=%q not in {host-a, host-b}", key, owner)
		}
		hostSet[owner]++
	}
	coord.mu.RUnlock()
	if hostSet["host-a"]+hostSet["host-b"] != 4 {
		t.Errorf("post-split: distribution %v doesn't sum to 4", hostSet)
	}
	// Rendezvous across 2 hosts for 4 keys is not guaranteed to split
	// evenly, so we tolerate 4-0 / 3-1 / 2-2. What matters is that no
	// child is orphaned.
	t.Logf("post-split: children distributed %v", hostSet)

	// ── Invariant 3 + 4: entity count conserved across children, each
	// NetID ends up in the correct quadrant. Walk every child's ECS
	// on its own game loop and aggregate the set of observed NetIDs.
	type found struct {
		cellIdx int
		pos     component.Position
	}
	foundMap := make(map[uint32]found)
	for i := 0; i < 4; i++ {
		coord.mu.RLock()
		childCell := coord.Cells[childKeys[i]]
		coord.mu.RUnlock()
		if childCell == nil {
			t.Errorf("post-split: coord.Cells[%s] nil — child not instantiated?", childKeys[i])
			continue
		}
		idx := i
		execOnLoop(t, childCell, func() {
			posMap := ecs.NewMap1[component.Position](childCell.Engine.ECS)
			netMap := ecs.NewMap1[component.NetworkID](childCell.Engine.ECS)
			filter := ecs.NewFilter2[component.Position, component.NetworkID](childCell.Engine.ECS).
				Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
			q := filter.Query()
			for q.Next() {
				e := q.Entity()
				if !posMap.HasAll(e) || !netMap.HasAll(e) {
					continue
				}
				id := netMap.Get(e).ID
				foundMap[id] = found{cellIdx: idx, pos: *posMap.Get(e)}
			}
		})
	}
	if len(foundMap) != len(plants) {
		t.Errorf("post-split: found %d entities across all children, want %d (plants=%v, foundMap=%v)",
			len(foundMap), len(plants), plants, foundMap)
	}
	for _, p := range plants {
		f, ok := foundMap[p.netID]
		if !ok {
			t.Errorf("post-split: netID %d missing from all children", p.netID)
			continue
		}
		if f.cellIdx != p.wantQuadrant {
			t.Errorf("post-split: netID %d landed in child %d want quadrant %d",
				p.netID, f.cellIdx, p.wantQuadrant)
		}
		if f.pos.X != p.x || f.pos.Y != p.y {
			t.Errorf("post-split: netID %d pos=(%.1f,%.1f) want (%.1f,%.1f)",
				p.netID, f.pos.X, f.pos.Y, p.x, p.y)
		}
	}

	// ── Invariant 5: the pre-seeded session route got remapped to a
	// child that actually exists in cellToHostMap. The split commit's
	// fallback remap points at children[0] deterministically.
	route, ok := coord.sessionRoutes.Get(SessionKey{GatewayID: InprocGatewayID, ConnID: 900})
	if !ok {
		t.Error("post-split: pre-seeded session route vanished")
	} else {
		coord.mu.RLock()
		_, routeDestKnown := coord.cellToHostMap[route.CellID]
		coord.mu.RUnlock()
		if !routeDestKnown {
			t.Errorf("post-split: session route still points at unknown cell %s", route.CellID)
		}
		if route.CellID == parentKey {
			t.Errorf("post-split: session route still points at parent key %s", parentKey)
		}
	}

	// ── Invariant 6: parent CellID gone from host-a's Host.Cells. ────────
	srcHostA := coord.Hosts["host-a"]
	if srcHostA == nil {
		t.Fatal("post-split: host-a missing from coord.Hosts")
	}
	if stale := srcHostA.CellByCellID(parentCellID); stale != nil {
		t.Errorf("post-split: host-a still has parent CellID %v in its Cells map", parentCellID)
	}
}

// TestS7SplitPreservesPlayerSessionsOnDest verifies the fix for the S7
// demo's "player freeze after split" bug: when a split migrates a player
// entity from the parent cell to a child cell, the destination cell's
// Engine.Players MUST contain a session for the player's connID, and that
// session's Entity field MUST point at the migrated entity. Without this,
// the destination's InputRouter drops every subsequent ClientInput for the
// player silently.
//
// The test is minimal: stand up a 2-host fixture, register a player
// session on the source cell, plant a PlayerConn-bearing entity at a known
// quadrant, force the split, and assert the session reappears on the
// quadrant's child with Entity wired up.
func TestS7SplitPreservesPlayerSessionsOnDest(t *testing.T) {
	coords.SetCellSize(1024)

	cfg := Config{
		CellsX:              2,
		CellsY:              2,
		CellSize:            1024,
		TestHosts:           []string{"host-a", "host-b"},
		Headless:            true,
		ConnManager:         net.NewConnManager(),
		Logger:              logger.New(),
		DynamicPartitioning: DefaultPartitionConfig(),
		LoginHandler:        func(connID uint32, msgs [][]byte) (string, any, error) { return "", nil, ErrLoginPending },
	}
	coord := NewCoordinator(cfg)
	coord.SetWorld(func(base *WorldBase) GameWorld { return base })
	coord.Build()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		coord.Shutdown()
	})
	for _, cell := range coord.Cells {
		go cell.Run(ctx)
	}
	time.Sleep(30 * time.Millisecond)

	parentCellID := CellID{X: 0, Y: 0}
	parentKey := MeshCellID(parentCellID)
	coord.mu.RLock()
	srcCell := coord.Cells[parentKey]
	coord.mu.RUnlock()
	if srcCell == nil {
		t.Fatalf("pre-split: coord.Cells[%s] is nil", parentKey)
	}

	// This TestHosts fixture doesn't instantiate a VCM (VCMs are only
	// created in node mode when a process dials a remote coordinator),
	// so SerializeEntityCore will leave GatewayID/GatewayConnID empty
	// on the transfer frame. The populateCell fix's Migrate branch is
	// consequently a no-op here — but the critical fix (registering an
	// Active engine session + wiring sess.Entity on the destination
	// cell) still runs and IS what this test verifies.
	const localConnID uint32 = 555

	// On the source cell's game loop: register an Active engine session
	// for localConnID, plant a PlayerConn-bearing entity in the TR
	// quadrant (so we know which child it should land on), and wire the
	// session's Entity to it.
	cellSize := parentCellID.Size(coords.CellSize)
	half := cellSize / 2
	const netID uint32 = 7777
	execOnLoop(t, srcCell, func() {
		srcCell.Engine.Players.RegisterSessionTransfer(localConnID, "pmig", "active", nil)
		e := spawnTestEntity(srcCell, netID, half*1.5, half*1.5) // TR quadrant (index 3)
		ecs.NewMap1[component.PlayerConn](srcCell.Engine.ECS).Add(e, &component.PlayerConn{ConnID: localConnID})
		if sess := srcCell.Engine.Players.ByConnID(localConnID); sess != nil {
			sess.Entity = e
		}
	})

	// Force the split through the real orchestrator → executor path.
	if err := coord.SplitCell(parentCellID, true); err != nil {
		t.Fatalf("SplitCell: %v", err)
	}

	// The TR quadrant maps to Children()[3].
	children := parentCellID.Children()
	destCellID := children[3]
	destKey := MeshCellID(destCellID)
	coord.mu.RLock()
	destCell := coord.Cells[destKey]
	coord.mu.RUnlock()
	if destCell == nil {
		t.Fatalf("post-split: TR child %s not in coord.Cells", destKey)
	}

	// Verify the dest cell has a live PlayerSession for this connID,
	// state=Active, and sess.Entity is wired to a live entity with the
	// expected NetworkID. Run on the dest cell's game loop for race-free
	// ECS reads.
	execOnLoop(t, destCell, func() {
		sess := destCell.Engine.Players.ByConnID(localConnID)
		if sess == nil {
			t.Fatalf("post-split: dest cell %s has no session for connID %d", destKey, localConnID)
		}
		if sess.State != engine.StateActive {
			t.Errorf("post-split: dest session state=%v, want Active", sess.State)
		}
		if sess.Entity == (ecs.Entity{}) {
			t.Fatal("post-split: dest session has zero Entity — input router would drop all input")
		}
		if !destCell.Engine.ECS.Alive(sess.Entity) {
			t.Fatalf("post-split: dest session Entity %v is not alive", sess.Entity)
		}
		netMap := ecs.NewMap1[component.NetworkID](destCell.Engine.ECS)
		if !netMap.HasAll(sess.Entity) {
			t.Fatal("post-split: dest session Entity missing NetworkID")
		}
		if got := netMap.Get(sess.Entity).ID; got != netID {
			t.Errorf("post-split: dest session Entity NetworkID=%d, want %d", got, netID)
		}
	})

}
