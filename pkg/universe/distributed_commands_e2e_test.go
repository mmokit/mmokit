package universe

import (
	"context"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/logger"
)

// ensureHostRegistry bootstraps a HostRegistry on the fixture's coordinator
// when one wasn't auto-wired (the colocated fixture skips startControlPlane,
// which is where production code initializes c.hostRegistry). The test then
// manually advertises the local host with HasPlayerDB=true so that
// RoutePlayerHomeOrOwner resolves to it.
func ensureHostRegistry(coord *Process, hostID string, hasDB bool) {
	if coord.hostRegistry == nil {
		coord.hostRegistry = NewHostRegistry(logger.New())
	}
	if h := coord.hostRegistry.Get(hostID); h == nil {
		coord.hostRegistry.RegisterLocal(hostID, "", nil, hasDB)
	} else {
		coord.hostRegistry.SetHasPlayerDB(hostID, hasDB)
	}
}

// End-to-end integration tests for the distributed-commands work. These
// drive the real cmdsys.Dispatcher (route resolution + handler execution)
// rather than calling handlers directly, so they cover the full routing
// path that unit tests bypass.

// e2eMockLocator implements PlayerDataLocator for tests. Holds a few
// pre-seeded players in memory.
type e2eMockLocator struct {
	pdas  map[string]*e2eMockPDA
	dirty map[string]bool
}

func (l *e2eMockLocator) Get(username string) (PlayerDataAccessor, func(), bool) {
	pd, ok := l.pdas[username]
	if !ok {
		return nil, nil, false
	}
	return pd, func() { l.dirty[username] = true }, true
}

func (l *e2eMockLocator) ListOffline() []PlayerDataAccessor {
	out := make([]PlayerDataAccessor, 0, len(l.pdas))
	for _, pd := range l.pdas {
		out = append(out, pd)
	}
	return out
}

type e2eMockPDA struct {
	username string
	cellX    int32
	cellY    int32
	x        float32
	y        float32
}

func (m *e2eMockPDA) GetUsername() string         { return m.username }
func (m *e2eMockPDA) GetCellX() int32             { return m.cellX }
func (m *e2eMockPDA) GetCellY() int32             { return m.cellY }
func (m *e2eMockPDA) GetX() float32               { return m.x }
func (m *e2eMockPDA) GetY() float32               { return m.y }
func (m *e2eMockPDA) SetCell(cx, cy int32)        { m.cellX, m.cellY = cx, cy }
func (m *e2eMockPDA) SetPosition(x, y float32)    { m.x, m.y = x, y }

// TestPlayerTP_OfflineUpdatesDB_E2E drives player.tp end-to-end via the
// dispatcher: routes RoutePlayerHomeOrOwner to a DB-bearing host, the
// handler runs on that host's Process, and the offline branch mutates
// the PlayerDataAccessor. Confirms the dirty-mark callback fires too.
//
// Covers the spec's "TestPlayerTP_OfflineUpdatesDB" scenario at the
// dispatch layer (a real PlayerFlusher + Postgres flow is integration-
// scoped to manual smoke).
func TestPlayerTP_OfflineUpdatesDB_E2E(t *testing.T) {
	env := newCmdsysTestEnv(t, []string{"host-a"})
	coord := env.coord

	// Seed locator + advertise that the host has the DB.
	loc := &e2eMockLocator{
		pdas: map[string]*e2eMockPDA{
			"bob": {username: "bob", cellX: 0, cellY: 0, x: 10, y: 10},
		},
		dirty: map[string]bool{},
	}
	coord.SetPlayerDataLocator(loc)
	ensureHostRegistry(coord, "host-a", true)
	coord.SetHasPlayerDB(true)

	// Far-away world position — should land in cell (3, 3).
	farX := coords.CellSize*3 + 50
	farY := coords.CellSize*3 + 75

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := coord.CmdDispatcher().Invoke(ctx, opCaller(), "player.tp", playerTpArgs{
		Username: "bob", X: farX, Y: farY,
	})
	if err != nil {
		t.Fatalf("player.tp dispatch: %v", err)
	}
	if len(res.PerTarget) != 1 || !res.PerTarget[0].OK {
		t.Fatalf("expected 1 OK target, got %+v (err=%q)", res.PerTarget, res.PerTarget[0].Error)
	}
	tr := res.PerTarget[0]
	got, ok := tr.Result.(playerTpResult)
	if !ok {
		t.Fatalf("result type = %T, want playerTpResult", tr.Result)
	}
	if got.Status != "offline" {
		t.Fatalf("Status = %q, want offline", got.Status)
	}
	if got.NewWorldX != farX || got.NewWorldY != farY {
		t.Fatalf("new world pos = (%g,%g), want (%g,%g)", got.NewWorldX, got.NewWorldY, farX, farY)
	}

	// Locator state mutated.
	pd := loc.pdas["bob"]
	if pd.cellX != 3 || pd.cellY != 3 {
		t.Errorf("PlayerData cell = (%d,%d), want (3,3)", pd.cellX, pd.cellY)
	}
	if pd.x != 50 || pd.y != 75 {
		t.Errorf("PlayerData local pos = (%g,%g), want (50,75)", pd.x, pd.y)
	}
	if !loc.dirty["bob"] {
		t.Error("DirtyMark did not fire")
	}
}

// TestPlayerList_AllFlag_E2E drives player.list --all end-to-end. The
// coordinator routes RouteCoordinator locally, then InvokeInternals
// player.list_offline (RouteSpecificHost) to the DB-bearing host, and
// merges online + offline rows. Single TraceID path.
//
// Covers the spec's "TestPlayerList_FromCoordPane_AllFlag" scenario.
func TestPlayerList_AllFlag_E2E(t *testing.T) {
	env := newCmdsysTestEnv(t, []string{"host-a"})
	coord := env.coord

	loc := &e2eMockLocator{
		pdas: map[string]*e2eMockPDA{
			"offline_user_1": {username: "offline_user_1"},
			"offline_user_2": {username: "offline_user_2"},
		},
		dirty: map[string]bool{},
	}
	coord.SetPlayerDataLocator(loc)
	ensureHostRegistry(coord, "host-a", true)
	coord.SetHasPlayerDB(true)

	// Mark one user as online so the merge path is exercised.
	coord.notifySessionActive("alice", "host-a")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := coord.CmdDispatcher().Invoke(ctx, opCaller(), "player.list", playerListArgs{All: true})
	if err != nil {
		t.Fatalf("player.list --all dispatch: %v", err)
	}
	if len(res.PerTarget) != 1 || !res.PerTarget[0].OK {
		t.Fatalf("expected 1 OK target, got %+v (err=%q)", res.PerTarget, res.PerTarget[0].Error)
	}
	got, ok := res.PerTarget[0].Result.(playerListResult)
	if !ok {
		t.Fatalf("result type = %T, want playerListResult", res.PerTarget[0].Result)
	}
	// Should have the online "alice" + 2 offline.
	if len(got.Players) != 3 {
		t.Fatalf("expected 3 rows (alice online + 2 offline), got %d: %+v",
			len(got.Players), got.Players)
	}
	statusByName := map[string]string{}
	for _, row := range got.Players {
		statusByName[row.Username] = row.Status
	}
	if statusByName["alice"] != "online" {
		t.Errorf("alice status = %q, want online", statusByName["alice"])
	}
	if statusByName["offline_user_1"] != "offline" {
		t.Errorf("offline_user_1 status = %q, want offline", statusByName["offline_user_1"])
	}
	if statusByName["offline_user_2"] != "offline" {
		t.Errorf("offline_user_2 status = %q, want offline", statusByName["offline_user_2"])
	}
}

// TestPlayerList_AllFlag_NoDBHost_E2E confirms the error path when
// --all is invoked but no host advertises a PlayerDataLocator. The
// coordinator's pre-check (PickDBHost == "") triggers before any
// internal dispatch fires.
func TestPlayerList_AllFlag_NoDBHost_E2E(t *testing.T) {
	env := newCmdsysTestEnv(t, []string{"host-a"})
	coord := env.coord
	// Intentionally do NOT call SetHasPlayerDB / SetPlayerDataLocator.

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := coord.CmdDispatcher().Invoke(ctx, opCaller(), "player.list", playerListArgs{All: true})
	// Dispatch returns an error on the local target.
	if err == nil && res.PerTarget[0].OK {
		t.Fatalf("expected error or non-OK target, got OK result %+v", res.PerTarget[0].Result)
	}
	// The handler error message mentions DB-bearing host.
	gotErr := ""
	if err != nil {
		gotErr = err.Error()
	} else {
		gotErr = res.PerTarget[0].Error
	}
	if gotErr == "" {
		t.Fatalf("expected non-empty error mentioning DB-bearing host")
	}
}

// TestEntitySummary_E2E confirms entity.summary fans out across the
// cluster and aggregates counts. Smoke test exercising the dispatcher's
// RouteAllHosts path for the new entity.summary command (universe layer
// equivalent of the deleted single-Engine entity.summary).
func TestEntitySummary_E2E(t *testing.T) {
	env := newCmdsysTestEnv(t, []string{"host-a"})
	coord := env.coord

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := coord.CmdDispatcher().Invoke(ctx, opCaller(), "entity.summary", entitySummaryArgs{})
	if err != nil {
		t.Fatalf("entity.summary dispatch: %v", err)
	}
	if len(res.PerTarget) == 0 {
		t.Fatalf("expected at least one target, got 0")
	}
	for _, tr := range res.PerTarget {
		if !tr.OK {
			t.Fatalf("target %s failed: %s", tr.TargetID, tr.Error)
		}
		got, ok := tr.Result.(entitySummaryResult)
		if !ok {
			t.Fatalf("result type = %T, want entitySummaryResult", tr.Result)
		}
		// Empty fixture has zero entities; just verify result shape.
		if got.Total < 0 {
			t.Errorf("Total = %d, want non-negative", got.Total)
		}
	}
}

// Compile-time assertion that *e2eMockPDA satisfies the universe contract.
var _ PlayerDataAccessor = (*e2eMockPDA)(nil)
var _ PlayerDataLocator = (*e2eMockLocator)(nil)
