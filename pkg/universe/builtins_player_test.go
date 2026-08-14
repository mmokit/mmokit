package universe

// Tests for builtins_player.go — player.tp, player.info, player.tpto,
// player.list, player.list_offline, player.kick.
//
// Handler coverage:
//   - Registration: all 5 user-facing + 1 hidden command registered with correct
//     Verb, Route, Capability, Hidden, Args, Result types.
//   - player.tp offline: mutates mockPDA state; asserts new position + DirtyMark.
//   - player.tp not-found: returns error.
//   - player.info offline: returns correct world coords.
//   - player.info not-found: returns error.
//   - player.list: returns online players; no panic without PlayerDataLocator.
//   - player.list_offline: returns empty result when no locator.
//   - player.list_offline with locator: returns all offline rows.
//   - player.kick not-online: returns error.
//   - player.tpto and player.kick online: registration tests only (requires live
//     session / dispatcher fan-out that is heavy to set up in unit tests).

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/zenion/mmokit/pkg/cmdsys"
	"github.com/zenion/mmokit/pkg/coords"
)

// ── mock PlayerDataLocator ────────────────────────────────────────────────────

type mockPDA struct {
	username     string
	cellX, cellY int32
	x, y         float32
}

func (m *mockPDA) GetUsername() string { return m.username }
func (m *mockPDA) GetCellX() int32     { return m.cellX }
func (m *mockPDA) GetCellY() int32     { return m.cellY }
func (m *mockPDA) GetX() float32       { return m.x }
func (m *mockPDA) GetY() float32       { return m.y }
func (m *mockPDA) SetCell(cx, cy int32) {
	m.cellX, m.cellY = cx, cy
}
func (m *mockPDA) SetPosition(x, y float32) {
	m.x, m.y = x, y
}

type mockLocator struct {
	pdas  map[string]*mockPDA
	dirty map[string]bool
}

func newMockLocator() *mockLocator {
	return &mockLocator{
		pdas:  make(map[string]*mockPDA),
		dirty: make(map[string]bool),
	}
}

func (m *mockLocator) Get(username string) (PlayerDataAccessor, func(), bool) {
	pd, ok := m.pdas[username]
	if !ok {
		return nil, nil, false
	}
	return pd, func() { m.dirty[username] = true }, true
}

func (m *mockLocator) ListOffline() []PlayerDataAccessor {
	out := make([]PlayerDataAccessor, 0, len(m.pdas))
	for _, pd := range m.pdas {
		out = append(out, pd)
	}
	return out
}

// newTestCoordWithPlayer sets up a Process with a mock PlayerDataLocator
// pre-seeded with the given player.
func newTestCoordWithPlayer(t *testing.T, username string, cellX, cellY int32, x, y float32) (*Process, *mockLocator) {
	t.Helper()
	coords.SetCellSize(1024)
	coord := &Process{
		Cells: map[MeshCellID]*Cell{},
		Hosts: map[string]*Host{},
	}
	withFreshRegistry(coord)
	loc := newMockLocator()
	loc.pdas[username] = &mockPDA{
		username: username,
		cellX:    cellX,
		cellY:    cellY,
		x:        x,
		y:        y,
	}
	coord.playerDataLocator = loc
	return coord, loc
}

// envWithProcess builds a *cmdsys.Env whose Local.Process points to coord.
func envWithProcess(coord *Process) *cmdsys.Env {
	return &cmdsys.Env{
		Local: &cmdsys.LocalContext{Process: coord},
	}
}

// ── Registration tests ────────────────────────────────────────────────────────

func TestPlayerCommandsRegistration(t *testing.T) {
	coord := newTestCoordWithStage(t, "0_0", "host-a")
	withFreshRegistry(coord)

	if err := registerPlayerCommands(coord); err != nil {
		t.Fatalf("registerPlayerCommands: %v", err)
	}

	tests := []struct {
		verb       string
		route      cmdsys.RouteKind
		capability cmdsys.Capability
		hidden     bool
	}{
		{"player.tp", cmdsys.RoutePlayerHomeOrOwner, "player.tp", false},
		{"player.info", cmdsys.RoutePlayerHomeOrOwner, "player.info", false},
		{"player.tpto", cmdsys.RoutePlayerHomeOrOwner, "player.tpto", false},
		{"player.list", cmdsys.RouteCoordinator, "player.list", false},
		{"player.kick", cmdsys.RoutePlayerOwner, "player.kick", true},
		{"player.list_offline", cmdsys.RouteSpecificHost, "player.list_offline", true},
	}

	for _, tt := range tests {
		t.Run(tt.verb, func(t *testing.T) {
			cmd, ok := coord.registry.Lookup(tt.verb)
			if !ok {
				t.Fatalf("%s not registered", tt.verb)
			}
			if cmd.Verb != tt.verb {
				t.Errorf("Verb = %q, want %q", cmd.Verb, tt.verb)
			}
			if cmd.Route != tt.route {
				t.Errorf("Route = %v, want %v", cmd.Route, tt.route)
			}
			if cmd.Capability != tt.capability {
				t.Errorf("Capability = %q, want %q", cmd.Capability, tt.capability)
			}
			if cmd.Hidden != tt.hidden {
				t.Errorf("Hidden = %v, want %v", cmd.Hidden, tt.hidden)
			}
		})
	}
}

func TestPlayerCommandsArgResultTypes(t *testing.T) {
	coord := newTestCoordWithStage(t, "0_0", "host-a")
	withFreshRegistry(coord)
	if err := registerPlayerCommands(coord); err != nil {
		t.Fatalf("registerPlayerCommands: %v", err)
	}

	for _, tt := range []struct {
		verb       string
		wantArgs   any
		wantResult any
	}{
		{"player.tp", playerTpArgs{}, playerTpResult{}},
		{"player.info", playerInfoArgs{}, playerInfoResult{}},
		{"player.tpto", playerTptoArgs{}, playerTptoResult{}},
		{"player.list", playerListArgs{}, playerListResult{}},
		{"player.kick", playerKickArgs{}, playerKickResult{}},
		{"player.list_offline", playerListOfflineArgs{}, playerListResult{}},
	} {
		t.Run(tt.verb, func(t *testing.T) {
			cmd, _ := coord.registry.Lookup(tt.verb)
			if reflect.TypeOf(cmd.Args) != reflect.TypeOf(tt.wantArgs) {
				t.Errorf("Args type = %T, want %T", cmd.Args, tt.wantArgs)
			}
			if reflect.TypeOf(cmd.Result) != reflect.TypeOf(tt.wantResult) {
				t.Errorf("Result type = %T, want %T", cmd.Result, tt.wantResult)
			}
		})
	}
}

// ── player.tp offline ─────────────────────────────────────────────────────────

func TestPlayerTpHandler_OfflineUpdatesPosition(t *testing.T) {
	// cell 0_0, local pos (100, 200) → world (100, 200) with CellSize=1024
	coord, loc := newTestCoordWithPlayer(t, "alice", 0, 0, 100, 200)
	withFreshRegistry(coord)
	if err := registerPlayerCommands(coord); err != nil {
		t.Fatalf("registerPlayerCommands: %v", err)
	}

	cmd, _ := coord.registry.Lookup("player.tp")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Teleport to world (2200, 300) → cell (2,0), local (152, 300).
	res, err := cmd.Handler(ctx, envWithProcess(coord), playerTpArgs{
		Username: "alice",
		X:        2200,
		Y:        300,
	})
	if err != nil {
		t.Fatalf("player.tp offline: %v", err)
	}
	out, ok := res.(playerTpResult)
	if !ok {
		t.Fatalf("result type = %T, want playerTpResult", res)
	}
	if out.Status != "offline" {
		t.Errorf("Status = %q, want offline", out.Status)
	}
	if out.PrevWorldX != 100 || out.PrevWorldY != 200 {
		t.Errorf("prev = (%g,%g), want (100,200)", out.PrevWorldX, out.PrevWorldY)
	}
	if out.NewWorldX != 2200 || out.NewWorldY != 300 {
		t.Errorf("new = (%g,%g), want (2200,300)", out.NewWorldX, out.NewWorldY)
	}

	// Verify PlayerData was mutated.
	pd := loc.pdas["alice"]
	if pd.cellX != 2 || pd.cellY != 0 {
		t.Errorf("cell = (%d,%d), want (2,0)", pd.cellX, pd.cellY)
	}
	expectedLocalX := float32(2200) - float32(2)*coords.CellSize
	if pd.x != expectedLocalX {
		t.Errorf("local X = %g, want %g", pd.x, expectedLocalX)
	}
	if pd.y != 300 {
		t.Errorf("local Y = %g, want 300", pd.y)
	}

	// Verify DirtyMark was called.
	if !loc.dirty["alice"] {
		t.Error("DirtyMark not called for alice")
	}
}

func TestPlayerTpHandler_NotFound(t *testing.T) {
	coord, _ := newTestCoordWithPlayer(t, "bob", 0, 0, 0, 0)
	withFreshRegistry(coord)
	if err := registerPlayerCommands(coord); err != nil {
		t.Fatalf("registerPlayerCommands: %v", err)
	}

	cmd, _ := coord.registry.Lookup("player.tp")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := cmd.Handler(ctx, envWithProcess(coord), playerTpArgs{
		Username: "unknown_xyz",
		X:        0,
		Y:        0,
	})
	if err == nil {
		t.Fatal("expected error for unknown player, got nil")
	}
}

// ── player.info offline ───────────────────────────────────────────────────────

func TestPlayerInfoHandler_OfflineReturnsWorldCoords(t *testing.T) {
	// Player in cell (1,0) at local (512,256) → world (1024+512, 256) = (1536, 256)
	coord, _ := newTestCoordWithPlayer(t, "carol", 1, 0, 512, 256)
	withFreshRegistry(coord)
	if err := registerPlayerCommands(coord); err != nil {
		t.Fatalf("registerPlayerCommands: %v", err)
	}

	cmd, _ := coord.registry.Lookup("player.info")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := cmd.Handler(ctx, envWithProcess(coord), playerInfoArgs{Username: "carol"})
	if err != nil {
		t.Fatalf("player.info offline: %v", err)
	}
	out, ok := res.(playerInfoResult)
	if !ok {
		t.Fatalf("result type = %T, want playerInfoResult", res)
	}
	if out.Status != "offline" {
		t.Errorf("Status = %q, want offline", out.Status)
	}
	wantX := float32(1)*coords.CellSize + 512
	wantY := float32(0)*coords.CellSize + 256
	if out.WorldX != wantX || out.WorldY != wantY {
		t.Errorf("world = (%g,%g), want (%g,%g)", out.WorldX, out.WorldY, wantX, wantY)
	}
}

func TestPlayerInfoHandler_NotFound(t *testing.T) {
	coord, _ := newTestCoordWithPlayer(t, "dave", 0, 0, 0, 0)
	withFreshRegistry(coord)
	if err := registerPlayerCommands(coord); err != nil {
		t.Fatalf("registerPlayerCommands: %v", err)
	}

	cmd, _ := coord.registry.Lookup("player.info")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := cmd.Handler(ctx, envWithProcess(coord), playerInfoArgs{Username: "no_such_player"})
	if err == nil {
		t.Fatal("expected error for unknown player, got nil")
	}
}

// ── player.list ───────────────────────────────────────────────────────────────

func TestPlayerListHandler_OnlineOnly_NoPanic(t *testing.T) {
	coord := newTestCoordWithStage(t, "0_0", "host-a")
	withFreshRegistry(coord)
	if err := registerPlayerCommands(coord); err != nil {
		t.Fatalf("registerPlayerCommands: %v", err)
	}

	cmd, _ := coord.registry.Lookup("player.list")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// No active users, no locator — should return empty list without panic.
	res, err := cmd.Handler(ctx, envWithProcess(coord), playerListArgs{All: false})
	if err != nil {
		t.Fatalf("player.list: %v", err)
	}
	out, ok := res.(playerListResult)
	if !ok {
		t.Fatalf("result type = %T, want playerListResult", res)
	}
	if len(out.Players) != 0 {
		t.Errorf("expected 0 players, got %d", len(out.Players))
	}
}

func TestPlayerListHandler_AllWithNoDBHost_ReturnsError(t *testing.T) {
	// Use a full coord with a real hostRegistry (no hosts registered with DB)
	// so PickDBHost() returns "" → handler must return an error.
	coord := newTestCoordWithStage(t, "0_0", "host-a")
	coord.hostRegistry = NewHostRegistry(coord.Log)
	withFreshRegistry(coord)
	if err := registerPlayerCommands(coord); err != nil {
		t.Fatalf("registerPlayerCommands: %v", err)
	}

	cmd, _ := coord.registry.Lookup("player.list")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// No hosts have HasPlayerDB=true, so PickDBHost returns "".
	_, err := cmd.Handler(ctx, envWithProcess(coord), playerListArgs{All: true})
	if err == nil {
		t.Fatal("expected error for --all without DB host, got nil")
	}
}

// ── player.list_offline ────────────────────────────────────────────────────────

func TestPlayerListOfflineHandler_NoLocator_ReturnsEmpty(t *testing.T) {
	coord := newTestCoordWithStage(t, "0_0", "host-a")
	withFreshRegistry(coord)
	if err := registerPlayerCommands(coord); err != nil {
		t.Fatalf("registerPlayerCommands: %v", err)
	}

	cmd, _ := coord.registry.Lookup("player.list_offline")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := cmd.Handler(ctx, envWithProcess(coord), playerListOfflineArgs{HostID: "host-a"})
	if err != nil {
		t.Fatalf("player.list_offline: %v", err)
	}
	out, ok := res.(playerListResult)
	if !ok {
		t.Fatalf("result type = %T, want playerListResult", res)
	}
	if len(out.Players) != 0 {
		t.Errorf("expected 0 players, got %d", len(out.Players))
	}
}

func TestPlayerListOfflineHandler_WithLocator_ReturnsRows(t *testing.T) {
	coord, _ := newTestCoordWithPlayer(t, "eve", 0, 0, 0, 0)
	// Add a second player.
	coord.playerDataLocator.(*mockLocator).pdas["frank"] = &mockPDA{username: "frank"}
	withFreshRegistry(coord)
	if err := registerPlayerCommands(coord); err != nil {
		t.Fatalf("registerPlayerCommands: %v", err)
	}

	cmd, _ := coord.registry.Lookup("player.list_offline")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := cmd.Handler(ctx, envWithProcess(coord), playerListOfflineArgs{HostID: "host-a"})
	if err != nil {
		t.Fatalf("player.list_offline: %v", err)
	}
	out, ok := res.(playerListResult)
	if !ok {
		t.Fatalf("result type = %T, want playerListResult", res)
	}
	if len(out.Players) != 2 {
		t.Errorf("expected 2 players, got %d", len(out.Players))
	}
	for _, row := range out.Players {
		if row.Status != "offline" {
			t.Errorf("row %q: Status = %q, want offline", row.Username, row.Status)
		}
	}
}

// ── player.kick not-online ────────────────────────────────────────────────────

func TestPlayerKickHandler_NotOnline_ReturnsError(t *testing.T) {
	coord, _ := newTestCoordWithPlayer(t, "grace", 0, 0, 0, 0)
	withFreshRegistry(coord)
	if err := registerPlayerCommands(coord); err != nil {
		t.Fatalf("registerPlayerCommands: %v", err)
	}

	cmd, _ := coord.registry.Lookup("player.kick")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// grace is offline (only in the locator, no online session).
	_, err := cmd.Handler(ctx, envWithProcess(coord), playerKickArgs{Username: "grace"})
	if err == nil {
		t.Fatal("expected error kicking offline player, got nil")
	}
}
