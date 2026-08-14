package universe

import (
	"testing"

	"github.com/google/uuid"
	meshpb "github.com/mmokit/mmokit/gen/go/meshpb"
	"github.com/mmokit/mmokit/pkg/logger"
)

// TestApplyResolveSpawnReconnect_DisconnectedRoutesToOldCell pins the
// distributed-mode reconnect contract: after a player disconnects (Active
// flips false on the coord) and reconnects via a fresh WS, the
// SpawnResolved RPC must hand the gateway back the user's old cell + host
// so the cell reattaches to the lingering entity instead of spawning a
// fresh one. The same recurring bug surfaced previously in embedded mode
// (commit 18e7297) — this test guards the standalone gateway path too.
func TestApplyResolveSpawnReconnect_DisconnectedRoutesToOldCell(t *testing.T) {
	c := minimalCoordWithCell(t, "h-1", "cell_2_3")
	uid := uuid.New()

	c.registerAuthenticatedSession(uid, "alice", "gw-1", 7, "h-1", "cell_2_3")
	c.notifySessionDisconnected("alice", "h-1", "cell_2_3")

	resp := &meshpb.SpawnResolved{}
	c.applyResolveSpawnReconnect(uid, resp)

	if !resp.IsReconnect {
		t.Fatalf("IsReconnect=false; expected true after disconnect")
	}
	if resp.TargetCellId != "cell_2_3" {
		t.Errorf("TargetCellId=%q; want %q", resp.TargetCellId, "cell_2_3")
	}
	if resp.TargetHostId != "h-1" {
		t.Errorf("TargetHostId=%q; want %q", resp.TargetHostId, "h-1")
	}
}

// TestApplyResolveSpawnReconnect_ActiveRejects covers the duplicate-login
// branch: when the same user_id is currently online, the new login is
// rejected (resp.Rejected=true) so the original session keeps playing. The
// existing activeUsers entry MUST be preserved — kicking the original
// session here is what caused the entity-duplication exploit when two
// browser windows raced auto-reconnect against each other.
func TestApplyResolveSpawnReconnect_ActiveRejects(t *testing.T) {
	c := minimalCoordWithCell(t, "h-1", "cell_0_0")
	uid := uuid.New()

	c.registerAuthenticatedSession(uid, "alice", "gw-1", 7, "h-1", "cell_0_0")
	c.notifySessionActive("alice", "h-1", "cell_0_0")

	resp := &meshpb.SpawnResolved{}
	c.applyResolveSpawnReconnect(uid, resp)

	if !resp.Rejected {
		t.Errorf("Rejected=false; expected true when an Active session already exists")
	}
	if resp.IsReconnect {
		t.Errorf("IsReconnect=true; expected false on rejection")
	}
	c.mu.RLock()
	_, stillActive := c.activeUsers[uid]
	c.mu.RUnlock()
	if !stillActive {
		t.Errorf("activeUsers[uid] removed; expected to be preserved (original session still wins)")
	}
}

// TestApplyResolveSpawnReconnect_StaleCellFallsBackToFresh covers the
// safety net: if the cell that owned the disconnected session has been
// merged or migrated away (CellOwner has no entry), we don't dispatch a
// PlayerAssignment to a non-existent cell — resp stays empty so the
// gateway picks a fresh spawn cell from topology.
func TestApplyResolveSpawnReconnect_StaleCellFallsBackToFresh(t *testing.T) {
	c := minimalCoordWithCell(t, "h-1", "cell_0_0")
	uid := uuid.New()

	c.registerAuthenticatedSession(uid, "alice", "gw-1", 7, "h-1", "ghost_cell")
	c.notifySessionDisconnected("alice", "h-1", "ghost_cell")

	resp := &meshpb.SpawnResolved{}
	c.applyResolveSpawnReconnect(uid, resp)

	if resp.IsReconnect {
		t.Errorf("IsReconnect=true; expected false when cell no longer owned")
	}
}

// TestApplyResolveSpawnReconnect_UnknownUserIsNoop guards the common
// fresh-login case — no activeUsers entry → resp untouched.
func TestApplyResolveSpawnReconnect_UnknownUserIsNoop(t *testing.T) {
	c := minimalCoordWithCell(t, "h-1", "cell_0_0")

	resp := &meshpb.SpawnResolved{}
	c.applyResolveSpawnReconnect(uuid.New(), resp)

	if resp.IsReconnect || resp.TargetHostId != "" || resp.TargetCellId != "" {
		t.Errorf("expected resp untouched for unknown user; got %+v", resp)
	}
}

// minimalCoordWithCell builds a Process whose Control.cellToHostMap has
// one entry — enough for HostForCellID + the activeUser maps to function.
// Avoids spinning up the full coordinator (no networking, no game loop).
func minimalCoordWithCell(t *testing.T, hostID string, cellID MeshCellID) *Process {
	t.Helper()
	log := logger.New()
	c := &Process{Log: log}
	c.Control = newControlPlane(log)
	c.Control.process = c
	c.Control.cellToHostMap = map[MeshCellID]string{cellID: hostID}
	c.players = map[string]*PlayerLocation{}
	c.activeUsers = map[uuid.UUID]*activeUser{}
	c.userIDByConn = map[uint32]uuid.UUID{}
	c.sessionRoutes = newSessionRoutes()
	return c
}
