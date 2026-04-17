// Package universe — builtins_inspection_test.go
//
// Covers the cluster / gateway / session inspection verbs plus the new
// --live fan-out + --json adapter rendering. Uses newCmdsysTestCoord's
// in-process fixture (TestHosts) so these tests don't need the control
// plane; hostRegistry + c.Hosts get populated via RegisterLocal during
// Build().

package universe

import (
	"strings"
	"testing"
)

// seedGateway registers an embedded gateway in the coord's gateway registry,
// allocating it first if the coord was built without the control plane.
func seedGateway(coord *Coordinator, id, grpcAddr string) {
	if coord.gatewayRegistry == nil {
		coord.gatewayRegistry = NewGatewayRegistry(coord.Log)
	}
	coord.gatewayRegistry.RegisterLocal(id, grpcAddr)
}

// wireInspectionBuiltins registers the cluster/gateway/session/cell builtins
// on a headless test coord. In production these are wired by startConsole,
// but Headless=true skips that. Called by each inspection test after
// newCmdsysTestCoord.
func wireInspectionBuiltins(t *testing.T, coord *Coordinator) {
	t.Helper()
	if err := registerCellBuiltins(coord.registry, coord); err != nil {
		t.Fatalf("registerCellBuiltins: %v", err)
	}
	if err := registerHostBuiltins(coord.registry, coord); err != nil {
		t.Fatalf("registerHostBuiltins: %v", err)
	}
	if err := registerGatewayBuiltins(coord.registry, coord); err != nil {
		t.Fatalf("registerGatewayBuiltins: %v", err)
	}
	if err := registerSessionBuiltins(coord.registry, coord); err != nil {
		t.Fatalf("registerSessionBuiltins: %v", err)
	}
	if err := registerClusterBuiltins(coord.registry, coord); err != nil {
		t.Fatalf("registerClusterBuiltins: %v", err)
	}
}

// seedSession writes a route into sessionRoutes and marks the player active
// on the coord so ActiveUserHost + c.players reflect the state.
func seedSession(coord *Coordinator, gwID string, connID uint32, username, hostID, cellID string) {
	coord.sessionRoutes.Set(&SessionRoute{
		Key:      SessionKey{GatewayID: gwID, ConnID: connID},
		Username: username,
		HostID:   hostID,
		CellID:   cellID,
		Epoch:    1,
	})
	coord.notifySessionActive(username, hostID)
}

// ── gateway.list ──────────────────────────────────────────────────────────────

func TestGatewayList_Empty(t *testing.T) {
	coord := newCmdsysTestCoord(t, []string{"host-a"})
	wireInspectionBuiltins(t, coord)
	res, err := coord.CmdDispatcher().Invoke(cmdCtx(t), opCaller(), "gateway.list", gatewayListArgs{})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if len(res.PerTarget) != 1 || !res.PerTarget[0].OK {
		t.Fatalf("bad result: %+v", res)
	}
	rows := res.PerTarget[0].Result.(gatewayListResult).Gateways
	if len(rows) != 0 {
		t.Errorf("expected 0 gateways, got %d", len(rows))
	}
}

func TestGatewayList_WithSeededGateways(t *testing.T) {
	coord := newCmdsysTestCoord(t, []string{"host-a"})
	wireInspectionBuiltins(t, coord)
	seedGateway(coord, "gw-a", "127.0.0.1:9101")
	seedGateway(coord, "gw-b", "127.0.0.1:9102")

	res, err := coord.CmdDispatcher().Invoke(cmdCtx(t), opCaller(), "gateway.list", gatewayListArgs{})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	rows := res.PerTarget[0].Result.(gatewayListResult).Gateways
	if len(rows) != 2 {
		t.Fatalf("expected 2 gateways, got %d", len(rows))
	}
	byID := map[string]GatewayRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	if byID["gw-a"].GRPCAddr != "127.0.0.1:9101" {
		t.Errorf("gw-a GRPCAddr = %q, want 127.0.0.1:9101", byID["gw-a"].GRPCAddr)
	}
	if !strings.Contains(byID["gw-a"].State, "Live") {
		t.Errorf("gw-a state = %q, want contains Live", byID["gw-a"].State)
	}
}

// ── gateway.info ──────────────────────────────────────────────────────────────

func TestGatewayInfo_UnknownReturnsError(t *testing.T) {
	coord := newCmdsysTestCoord(t, []string{"host-a"})
	wireInspectionBuiltins(t, coord)
	// Must allocate the registry even though we're not seeding — gateway.info
	// short-circuits on nil registry.
	if coord.gatewayRegistry == nil {
		coord.gatewayRegistry = NewGatewayRegistry(coord.Log)
	}
	res, _ := coord.CmdDispatcher().Invoke(cmdCtx(t), opCaller(), "gateway.info", gatewayInfoArgs{ID: "missing"})
	if res.PerTarget[0].OK {
		t.Fatal("expected error, got OK")
	}
}

func TestGatewayInfo_WithSessions(t *testing.T) {
	coord := newCmdsysTestCoord(t, []string{"host-a"})
	wireInspectionBuiltins(t, coord)
	seedGateway(coord, "gw-a", "127.0.0.1:9101")
	key := SessionKey{GatewayID: "gw-a", ConnID: 42}
	coord.gatewayRegistry.AddSession("gw-a", key)
	seedSession(coord, "gw-a", 42, "alice", "host-a", "cell_0_0")

	res, err := coord.CmdDispatcher().Invoke(cmdCtx(t), opCaller(), "gateway.info", gatewayInfoArgs{ID: "gw-a"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	info := res.PerTarget[0].Result.(gatewayInfoResult)
	if info.ID != "gw-a" {
		t.Errorf("ID = %q", info.ID)
	}
	if info.SessionCount != 1 {
		t.Errorf("SessionCount = %d, want 1", info.SessionCount)
	}
}

// ── session.list / session.info ──────────────────────────────────────────────

func TestSessionList_Populated(t *testing.T) {
	coord := newCmdsysTestCoord(t, []string{"host-a"})
	wireInspectionBuiltins(t, coord)
	seedSession(coord, "gw-a", 1, "alice", "host-a", "cell_0_0")
	seedSession(coord, "gw-a", 2, "bob", "host-a", "cell_1_0")

	res, err := coord.CmdDispatcher().Invoke(cmdCtx(t), opCaller(), "session.list", sessionListArgs{})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	rows := res.PerTarget[0].Result.(sessionListResult).Sessions
	if len(rows) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(rows))
	}
	// Sorted by (gateway, connID).
	if rows[0].ConnID != 1 || rows[1].ConnID != 2 {
		t.Errorf("ordering: got %d,%d", rows[0].ConnID, rows[1].ConnID)
	}
	if rows[0].Username != "alice" || rows[1].Username != "bob" {
		t.Errorf("usernames: got %q,%q", rows[0].Username, rows[1].Username)
	}
}

func TestSessionInfo_HappyAndMissing(t *testing.T) {
	coord := newCmdsysTestCoord(t, []string{"host-a"})
	wireInspectionBuiltins(t, coord)
	seedSession(coord, "gw-a", 42, "xennion", "host-a", "cell_0_0")

	// Happy path.
	res, err := coord.CmdDispatcher().Invoke(cmdCtx(t), opCaller(), "session.info", sessionInfoArgs{Key: "gw-a:42"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	info := res.PerTarget[0].Result.(sessionInfoResult)
	if info.Username != "xennion" || info.Host != "host-a" || info.Cell != "cell_0_0" {
		t.Errorf("unexpected session: %+v", info)
	}
	if !info.Online {
		t.Errorf("Online = false, want true (seedSession calls notifySessionActive)")
	}

	// Missing key.
	res2, _ := coord.CmdDispatcher().Invoke(cmdCtx(t), opCaller(), "session.info", sessionInfoArgs{Key: "gw-a:99"})
	if res2.PerTarget[0].OK {
		t.Fatal("expected error for unknown session")
	}

	// Malformed key.
	res3, _ := coord.CmdDispatcher().Invoke(cmdCtx(t), opCaller(), "session.info", sessionInfoArgs{Key: "malformed"})
	if res3.PerTarget[0].OK {
		t.Fatal("expected parse error for malformed key")
	}
}

// ── cluster.overview ─────────────────────────────────────────────────────────

func TestClusterOverview_Rendering(t *testing.T) {
	coord := newCmdsysTestCoord(t, []string{"host-a", "host-b"})
	wireInspectionBuiltins(t, coord)
	seedGateway(coord, "gw-a", "127.0.0.1:9101")
	seedSession(coord, "gw-a", 1, "alice", "host-a", "cell_0_0")

	res, err := coord.CmdDispatcher().Invoke(cmdCtx(t), opCaller(), "cluster.overview", clusterOverviewArgs{})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	out := res.PerTarget[0].Result.(clusterOverviewResult).Output
	mustContain := []string{"host-a", "host-b", "gw-a", "hosts:", "gateways:", "cell distribution:"}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Errorf("overview missing %q; output was:\n%s", s, out)
		}
	}
}

// ── cell.snapshot (internal fan-out verb) ────────────────────────────────────

func TestCellSnapshot_MultiHost(t *testing.T) {
	coord := newCmdsysTestCoord(t, []string{"host-a", "host-b"})
	wireInspectionBuiltins(t, coord)

	res, err := coord.CmdDispatcher().Invoke(cmdCtx(t), opCaller(), "cell.snapshot", cellSnapshotArgs{})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	// Two hosts → two PerTarget entries. Multi-target result is expected.
	if len(res.PerTarget) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(res.PerTarget))
	}
	for _, tr := range res.PerTarget {
		if !tr.OK {
			t.Errorf("target %s failed: %s", tr.TargetID, tr.Error)
		}
	}
}

// ── cell.list --live (fan-out consumer) ──────────────────────────────────────

func TestCellList_LiveFanOutMerges(t *testing.T) {
	coord := newCmdsysTestCoord(t, []string{"host-a", "host-b"})
	wireInspectionBuiltins(t, coord)

	// Without --live: baseline rows from coord-local state.
	base, err := coord.CmdDispatcher().Invoke(cmdCtx(t), opCaller(), "cell.list", cellListArgs{})
	if err != nil {
		t.Fatalf("invoke baseline: %v", err)
	}
	baseRows := base.PerTarget[0].Result.(cellListResult).Cells
	if len(baseRows) == 0 {
		t.Fatal("expected at least one cell row in baseline")
	}

	// With --live: same cells, possibly fresher metric columns.
	live, err := coord.CmdDispatcher().Invoke(cmdCtx(t), opCaller(), "cell.list", cellListArgs{Live: true})
	if err != nil {
		t.Fatalf("invoke --live: %v", err)
	}
	liveRows := live.PerTarget[0].Result.(cellListResult).Cells
	if len(liveRows) != len(baseRows) {
		t.Errorf("baseline rows=%d, --live rows=%d (should match)", len(baseRows), len(liveRows))
	}
}

// ── gateway.list --json path (adapter level) ─────────────────────────────────

// Adapter-level --json handling is covered at the engine level; we validate
// here that gateway.list itself produces a sensible JSON-marshalable result
// (no unexported fields that would lose data in marshaling).

func TestGatewayList_JSONShape(t *testing.T) {
	coord := newCmdsysTestCoord(t, []string{"host-a"})
	wireInspectionBuiltins(t, coord)
	seedGateway(coord, "gw-a", "127.0.0.1:9101")

	res, err := coord.CmdDispatcher().Invoke(cmdCtx(t), opCaller(), "gateway.list", gatewayListArgs{})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	// Every public field is exported capital-first, so json.Marshal of Result
	// round-trips every piece of data. Smoke-check by asserting the rendered
	// row has non-empty derived columns.
	rows := res.PerTarget[0].Result.(gatewayListResult).Gateways
	if len(rows) != 1 {
		t.Fatalf("rows: %+v", rows)
	}
	if rows[0].HeartbeatAge == "" {
		t.Error("HeartbeatAge empty — expected '---' for Local or a duration")
	}
}
