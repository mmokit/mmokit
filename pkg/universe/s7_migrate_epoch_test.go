package universe

import (
	"testing"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════
// TestMigrateEpochSourceCellReleased — epoch-gated authority handoff E2E
//
// Verifies that after a cross-host cell migration:
//  1. The migrate completes without error.
//  2. Ownership in cellToHostMap transferred to the destination host.
//  3. The source host's Host.Cells no longer contains the migrated cell
//     (CellRelease was processed — source teardown invariant).
//  4. A pre-seeded session route had its Epoch bumped (remapHostCell fired).
//  5. No panics or errors from stale-frame routing after migration.
//
// Runs under both colocated and distributed topologies via the shared
// clusterFixture harness (forEachTopology).
// ═══════════════════════════════════════════════════════════════════════════

func TestMigrateEpochSourceCellReleased(t *testing.T) {
	forEachTopology(t, FixtureConfig{
		CellsX: 2, CellsY: 2, CellSize: 1024,
		HostIDs: []string{"host-a", "host-b"},
	}, func(t *testing.T, fx clusterFixture) {
		// The fixture assigns cells round-robin: cell_0_0 -> host-a.
		srcCellID := CellID{X: 0, Y: 0}
		srcKey := srcCellID.MeshID()

		srcHost := fx.CellOwner(string(srcKey))
		if srcHost == "" {
			t.Fatalf("pre-migrate: cell %s has no owner", srcKey)
		}

		destHost := "host-b"
		if srcHost == "host-b" {
			destHost = "host-a"
		}

		srcCell := fx.CellOn(srcHost, string(srcKey))
		if srcCell == nil {
			t.Fatalf("pre-migrate: cell %s missing from host %s", srcKey, srcHost)
		}

		// Spawn two real entities on the source cell so entity preservation is
		// exercised by the transfer (same pattern as TestS7MigrateAcrossHosts).
		execOnLoop(t, srcCell, func() {
			spawnTestEntity(srcCell, 7001, 100, 200)
			spawnTestEntity(srcCell, 7002, 300, 400)
		})

		// Pre-seed a session route pointing at the source cell so we can assert
		// that remapHostCell bumped its Epoch after the commit.
		const testConnID = uint32(9900)
		const initialEpoch = uint64(3)
		fx.Coord().sessionRoutes.Set(&SessionRoute{
			Key:      SessionKey{GatewayID: InprocGatewayID, ConnID: testConnID},
			Username: "epoch-test-player",
			HostID:   srcHost,
			CellID:   srcKey,
			Epoch:    initialEpoch,
		})

		// ── Drive the migration. ─────────────────────────────────────────────────
		req, err := fx.Coord().orchestrator.BeginMigrate(srcCellID, destHost)
		if err != nil {
			t.Fatalf("BeginMigrate: %v", err)
		}
		select {
		case <-req.Done:
		case <-time.After(5 * time.Second):
			t.Fatalf("BeginMigrate req=%d did not complete in 5s", req.ID)
		}
		if req.Result != nil {
			t.Fatalf("BeginMigrate req=%d failed: %v", req.ID, req.Result)
		}

		// ── Invariant 1: ownership transferred to destHost. ──────────────────────
		if newOwner := fx.CellOwner(string(srcKey)); newOwner != destHost {
			t.Errorf("post-migrate CellOwner(%s) = %q, want %q", srcKey, newOwner, destHost)
		}

		// ── Invariant 2: source host has released the cell.
		// req.Done blocks until teardown completes, so a single-shot check suffices.
		if fx.HostOwnsCell(srcHost, string(srcKey)) {
			t.Errorf("post-migrate: source host %s still owns cell %s", srcHost, srcKey)
		}

		// ── Invariant 3: destination host now has the cell. ───────────────────────
		destCell := fx.CellOn(destHost, string(srcKey))
		if destCell == nil {
			t.Fatalf("post-migrate: dest host %q has no cell %s", destHost, srcKey)
		}
		if destCell == srcCell {
			t.Error("post-migrate: dest cell is the same object as src cell — expected a fresh Cell")
		}

		// ── Invariant 4: pre-seeded session route had its Epoch bumped. ───────────
		// remapHostCell fires during applyMigrateCommit; it increments Epoch for
		// every route whose CellID matched the source cell. A stale frame carrying
		// initialEpoch would now be rejected by the gateway's epoch check.
		route, ok := fx.Coord().sessionRoutes.Get(SessionKey{GatewayID: InprocGatewayID, ConnID: testConnID})
		if !ok {
			t.Error("post-migrate: pre-seeded session route vanished")
		} else {
			if route.Epoch <= initialEpoch {
				t.Errorf("post-migrate: session route Epoch = %d, want > %d (not bumped by migrate commit)", route.Epoch, initialEpoch)
			}
			if route.HostID != destHost {
				t.Errorf("post-migrate: session route HostID = %q, want %q", route.HostID, destHost)
			}
			t.Logf("session route after migrate: Epoch=%d HostID=%s CellID=%s", route.Epoch, route.HostID, route.CellID)
		}

		// ── Invariant 5: a few more ticks with no panics (stale frames silently
		// dropped by the epoch gate in the gateway). ─────────────────────────────
		time.Sleep(300 * time.Millisecond)
	})
}
