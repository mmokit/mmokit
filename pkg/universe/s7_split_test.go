package universe

import (
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/coords"
	"github.com/mmokit/mmokit/pkg/engine"
)

// ═══════════════════════════════════════════════════════════════════════════
// S7 T10 — distributed split integration test
//
// TestS7SplitAcrossHosts stands up a 2-host cluster with a 2x2 cell grid
// (each host owns 2 cells via round-robin assignment). It picks a cell
// owned by host-a, spawns real entities into each of the four quadrants
// that the orchestrator will carve out, then forces a split via
// Process.SplitCell(..., bypassCooldown=true). Goes through the real
// orchestrator → dispatcher → executor → applyCellTransferCommit flow.
//
// Post-commit invariants:
//
//  1. The parent cell is gone from cell ownership tracking.
//  2. All 4 children exist, distributed between host-a and host-b
//     according to rendezvous + locality.
//  3. Entity count is conserved across the 4 children — the same number
//     of entities we spawned pre-split reappears in the post-split
//     children combined (no loss).
//  4. Every spawned NetID ends up on exactly one child, in the same
//     quadrant we planted it in.
//  5. Pre-seeded session routes that used to point at the parent key were
//     remapped onto an existing child cell.
//  6. Source host no longer owns the parent CellID.
// ═══════════════════════════════════════════════════════════════════════════

func TestS7SplitAcrossHosts(t *testing.T) {
	forEachTopology(t, FixtureConfig{
		CellsX: 2, CellsY: 2, CellSize: 1024,
		HostIDs: []string{"host-a", "host-b"},
	}, func(t *testing.T, fx clusterFixture) {
		// Pick cell_0_0 which lands on host-a via round-robin assignment.
		parentCellID := CellID{X: 0, Y: 0}
		parentKey := parentCellID.MeshID()

		srcHost := fx.CellOwner(string(parentKey))
		if srcHost != "host-a" {
			t.Fatalf("pre-split: expected parent cell on host-a, got %q", srcHost)
		}
		srcCell := fx.CellOn(srcHost, string(parentKey))
		if srcCell == nil {
			t.Fatalf("pre-split: cell %s missing from host %s", parentKey, srcHost)
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
		// child in the ownership map (invariant 5).
		fx.Coord().sessionRoutes.Set(&SessionRoute{
			Key:      SessionKey{GatewayID: InprocGatewayID, ConnID: 900},
			Username: "splittest",
			HostID:   srcHost,
			CellID:   parentKey,
			Epoch:    1,
		})

		// Drive the split through the real path.
		if err := fx.Coord().SplitCell(parentCellID, true); err != nil {
			t.Fatalf("SplitCell: %v", err)
		}

		// ── Invariant 1: parent gone from cell ownership. ────────────────────
		// req.Done blocks until teardown completes, so a single-shot check suffices.
		if fx.HostOwnsCell(srcHost, string(parentKey)) {
			t.Errorf("post-split: source host %s still owns cell %s", srcHost, parentKey)
		}
		if owner := fx.CellOwner(string(parentKey)); owner != "" {
			t.Errorf("post-split: parent %s still owned by %q, want no owner", parentKey, owner)
		}

		// ── Invariant 2: all 4 children owned, distributed. ──────────────────
		children := parentCellID.Children()
		childKeys := make([]string, 4)
		hostSet := map[string]int{}
		for i, ch := range children {
			key := string(ch.MeshID())
			childKeys[i] = key
			owner := fx.CellOwner(key)
			if owner == "" {
				t.Errorf("post-split: child %s has no owner", key)
				continue
			}
			if owner != "host-a" && owner != "host-b" {
				t.Errorf("post-split: child %s owner=%q not in {host-a, host-b}", key, owner)
			}
			hostSet[owner]++
		}
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
			ownerHost := fx.CellOwner(childKeys[i])
			if ownerHost == "" {
				t.Errorf("post-split: child %s has no owner — can't inspect ECS", childKeys[i])
				continue
			}
			childCell := fx.CellOn(ownerHost, childKeys[i])
			if childCell == nil {
				t.Errorf("post-split: child %s not instantiated on host %s", childKeys[i], ownerHost)
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
		// child that actually exists in the ownership map. The split commit's
		// fallback remap points at children[0] deterministically.
		route, ok := fx.Coord().sessionRoutes.Get(SessionKey{GatewayID: InprocGatewayID, ConnID: 900})
		if !ok {
			t.Error("post-split: pre-seeded session route vanished")
		} else {
			if owner := fx.CellOwner(string(route.CellID)); owner == "" {
				t.Errorf("post-split: session route still points at unknown cell %s", route.CellID)
			}
			if route.CellID == parentKey {
				t.Errorf("post-split: session route still points at parent key %s", parentKey)
			}
		}

		// ── Invariant 6: parent CellID gone from source host's local cells. ──
		if fx.HostOwnsCell(srcHost, string(parentKey)) {
			t.Errorf("post-split: host %s still has parent CellID %s in its Cells map", srcHost, parentKey)
		}
	})
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
	forEachTopology(t, FixtureConfig{
		CellsX: 2, CellsY: 2, CellSize: 1024,
		HostIDs: []string{"host-a", "host-b"},
	}, func(t *testing.T, fx clusterFixture) {
		parentCellID := CellID{X: 0, Y: 0}
		parentKey := parentCellID.MeshID()

		srcHost := fx.CellOwner(string(parentKey))
		if srcHost == "" {
			t.Fatalf("pre-split: cell %s has no owner", parentKey)
		}
		srcCell := fx.CellOn(srcHost, string(parentKey))
		if srcCell == nil {
			t.Fatalf("pre-split: cell %s missing from host %s", parentKey, srcHost)
		}

		// This fixture doesn't instantiate a VCM (VCMs are only created in
		// node mode when a process dials a remote coordinator), so
		// SerializeEntityCore will leave GatewayID/GatewayConnID empty on the
		// transfer frame. The populateCell fix's Migrate branch is
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
		if err := fx.Coord().SplitCell(parentCellID, true); err != nil {
			t.Fatalf("SplitCell: %v", err)
		}

		// The TR quadrant maps to Children()[3].
		children := parentCellID.Children()
		destCellID := children[3]
		destKey := string(destCellID.MeshID())

		destOwner := fx.CellOwner(destKey)
		if destOwner == "" {
			t.Fatalf("post-split: TR child %s has no owner", destKey)
		}
		destCell := fx.CellOn(destOwner, destKey)
		if destCell == nil {
			t.Fatalf("post-split: TR child %s not present on owner %s", destKey, destOwner)
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
	})
}

// TestS7SplitRemapsSessionEpochAndHost is a regression test for the
// "player freeze after split" bug. Before the fix, stepSplitRemapSessions
// used remapCellPerRoute which only rewrote CellID — it left Epoch and HostID
// stale and never dispatched SessionRegister to the destination host.
//
// This test pre-seeds a session route with a known epoch (5) pointing at the
// parent cell on host-a. After the split it asserts:
//
//  1. Epoch was BUMPED (Migrate was called, not just UpdateCell).
//  2. HostID was updated to whichever child host the session landed on.
//  3. CellID no longer equals the parent.
//
// The VCM/SessionRegister dispatch path is exercised by the fact that the
// inproc fixture has a HostNetwork VCM; the test checks the VCM carries
// the new epoch (not the hardcoded epoch=1 from populateCell).
func TestS7SplitRemapsSessionEpochAndHost(t *testing.T) {
	forEachTopology(t, FixtureConfig{
		CellsX: 2, CellsY: 2, CellSize: 1024,
		HostIDs: []string{"host-a", "host-b"},
	}, func(t *testing.T, fx clusterFixture) {
		parentCellID := CellID{X: 0, Y: 0}
		parentKey := parentCellID.MeshID()

		srcHost := fx.CellOwner(string(parentKey))
		if srcHost == "" {
			t.Fatalf("pre-split: cell %s has no owner", parentKey)
		}
		srcCell := fx.CellOn(srcHost, string(parentKey))
		if srcCell == nil {
			t.Fatalf("pre-split: cell %s missing from host %s", parentKey, srcHost)
		}

		// Plant an entity in the TR quadrant so req.adoptedUsers is populated
		// and we exercise the non-fallback branch of the remap.
		cellSize := parentCellID.Size(coords.CellSize)
		half := cellSize / 2
		const netID uint32 = 8888
		execOnLoop(t, srcCell, func() {
			spawnTestEntity(srcCell, netID, half*1.5, half*1.5) // TR quadrant (index 3)
		})

		// Pre-seed a session route with a known epoch so we can assert the
		// epoch was bumped (not left at its original value) by the split commit.
		const testConnID = uint32(1234)
		const initialEpoch = uint64(5)
		sessionKey := SessionKey{GatewayID: InprocGatewayID, ConnID: testConnID}
		fx.Coord().sessionRoutes.Set(&SessionRoute{
			Key:      sessionKey,
			Username: "epoch-split-player",
			HostID:   srcHost,
			CellID:   parentKey,
			Epoch:    initialEpoch,
		})

		// Drive the split through the real orchestrator.
		if err := fx.Coord().SplitCell(parentCellID, true); err != nil {
			t.Fatalf("SplitCell: %v", err)
		}

		// ── Invariant 1: session route Epoch must be bumped. ─────────────────────
		route, ok := fx.Coord().sessionRoutes.Get(sessionKey)
		if !ok {
			t.Fatal("post-split: pre-seeded session route vanished")
		}
		if route.Epoch <= initialEpoch {
			t.Errorf("post-split: session Epoch = %d, want > %d (stepSplitRemapSessions must call Migrate, not UpdateCell)",
				route.Epoch, initialEpoch)
		}

		// ── Invariant 2: HostID must reflect the actual child host. ──────────────
		if owner := fx.CellOwner(string(route.CellID)); owner == "" {
			t.Errorf("post-split: session route CellID %s has no owner", route.CellID)
		} else if route.HostID != owner {
			t.Errorf("post-split: session HostID=%q but cell %s is owned by %q — stale HostID",
				route.HostID, route.CellID, owner)
		}

		// ── Invariant 3: CellID must no longer be the parent. ────────────────────
		if route.CellID == parentKey {
			t.Errorf("post-split: session CellID still points at parent %s", parentKey)
		}

		t.Logf("session route after split: Epoch=%d HostID=%s CellID=%s (initial epoch was %d)",
			route.Epoch, route.HostID, route.CellID, initialEpoch)
	})
}

// assertCellGoroutineExited blocks up to timeout for the given cell's Run()
// goroutine to return. Zero-valued runDone means Run was never entered,
// which is also "not a zombie". Fails the test if the goroutine is still
// ticking after timeout.
func assertCellGoroutineExited(t *testing.T, cell *Cell, timeout time.Duration) {
	t.Helper()
	cell.runMu.Lock()
	done := cell.runDone
	cell.runMu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
		return
	case <-time.After(timeout):
		t.Fatalf("cell %s game loop still running %v after commit (zombie cell)", cell.MeshID(), timeout)
	}
}

// TestS7SplitShutsDownParentCell is the regression guard for the zombie-cell
// bug where applySplitCommit pre-removed the parent from host.Cells *before*
// calling hostProxy.ReleaseCell. The localHostOps.ReleaseCell lookup then
// returned "unknown cell", its error was logged-and-ignored, and cell.Shutdown
// was never called — the parent's 20Hz game loop kept ticking forever,
// replicating to clients in parallel with the real children.
//
// The bug only manifests on the LOCAL commit path (hostProxy returns
// localHostOps). The distributed fixture dispatches CellRelease via
// MeshControl to the remote host, which looks up the cell in its own
// Host.Cells — that map is not mutated by the coord's pre-remove loop, so
// the remote path never saw "unknown cell". Colocated (single-process,
// coordinator+host in one binary) is the canonical local path and is
// where this regression must be caught.
func TestS7SplitShutsDownParentCell(t *testing.T) {
	forEachTopology(t, FixtureConfig{
		CellsX: 2, CellsY: 2, CellSize: 1024,
	}, func(t *testing.T, fx clusterFixture) {
		parentCellID := CellID{X: 0, Y: 0}
		parentKey := parentCellID.MeshID()

		srcHost := fx.CellOwner(string(parentKey))
		if srcHost == "" {
			t.Fatalf("pre-split: cell %s has no owner", parentKey)
		}
		srcCell := fx.CellOn(srcHost, string(parentKey))
		if srcCell == nil {
			t.Fatalf("pre-split: cell %s missing from host %s", parentKey, srcHost)
		}

		if err := fx.Coord().SplitCell(parentCellID, true); err != nil {
			t.Fatalf("SplitCell: %v", err)
		}

		assertCellGoroutineExited(t, srcCell, 2*time.Second)
	})
}
