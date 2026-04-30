package universe

// Tests for builtins_entity.go — entity.spawn, entity.despawn, entity.list, entity.tp.
//
// Handler test coverage:
//   - Registration: all four commands are registered with correct Verb, Route, Capability.
//   - entity.despawn handler: pre-spawn an entity, call handler, verify MarkForRemoval path.
//   - entity.list handler:    pre-spawn entities, call handler, verify rows returned.
//   - entity.tp handler:      pre-spawn an entity, call handler, verify position updated.
//
// entity.spawn handler test is omitted at the unit level because it requires a
// registered entity kind (mmokit.RegisterKind[T]), which has heavy setup not
// available in pkg/universe tests. Spawn handler coverage happens during Task 24
// manual smoke (integration). The registration test + count-validation path
// are covered below.

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	netpkg "github.com/zenion/mmoserver/pkg/net"
)

// newTestCoordWithStage builds a *Process with one Cell that has both an
// Engine and a Stage. The Stage is wired with a moveTestBridge so
// CellOwnerAtPos works without a real coordinator.
func newTestCoordWithStage(t *testing.T, cellID, hostID string) *Process {
	t.Helper()
	// Use a small cell size consistent with other stage tests.
	coords.SetCellSize(1024)
	parsed, err := ParseCellID(cellID)
	if err != nil {
		t.Fatalf("ParseCellID(%q): %v", cellID, err)
	}
	log := logger.New()
	eng := engine.New(engine.DefaultConfig(), netpkg.NewConnManager(), log)
	stage := NewStage(eng, parsed, 300, nil)
	stage.SetBridge(moveTestBridge{})

	cell := &Cell{ID: cellID, Engine: eng, Stage: stage}
	host := &Host{
		ID:    hostID,
		Cells: map[CellID]*Cell{parsed: cell},
	}
	return &Process{
		Cells: map[string]*Cell{cellID: cell},
		Hosts: map[string]*Host{hostID: host},
	}
}

// newTestRegistryForCoord creates a fresh Registry — used when we want to
// register entity commands without touching coord.registry.
// For testing registerEntityCommands we set coord.registry directly.
func withFreshRegistry(coord *Process) *cmdsys.Registry {
	reg := cmdsys.NewRegistry()
	coord.registry = reg
	return reg
}

// ── Registration tests ────────────────────────────────────────────────────────

func TestEntityCommandsRegistration(t *testing.T) {
	coord := newTestCoordWithStage(t, "0_0", "host-a")
	withFreshRegistry(coord)

	if err := registerEntityCommands(coord); err != nil {
		t.Fatalf("registerEntityCommands: %v", err)
	}

	tests := []struct {
		verb       string
		route      cmdsys.RouteKind
		capability cmdsys.Capability
	}{
		{"entity.spawn", cmdsys.RouteLocal, "entity.spawn"},
		{"entity.despawn", cmdsys.RouteEntityOwner, "entity.despawn"},
		{"entity.list", cmdsys.RouteAllHosts, "entity.list"},
		{"entity.tp", cmdsys.RouteEntityOwner, "entity.tp"},
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
		})
	}
}

func TestEntityCommandsArgResultTypes(t *testing.T) {
	coord := newTestCoordWithStage(t, "0_0", "host-a")
	withFreshRegistry(coord)
	if err := registerEntityCommands(coord); err != nil {
		t.Fatalf("registerEntityCommands: %v", err)
	}

	for _, tt := range []struct {
		verb       string
		wantArgs   any
		wantResult any
	}{
		{"entity.spawn", entitySpawnArgs{}, entitySpawnResult{}},
		{"entity.despawn", entityDespawnArgs{}, entityDespawnResult{}},
		{"entity.list", entityListArgs{}, entityListResult{}},
		{"entity.tp", entityTpArgs{}, entityTpResult{}},
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

// ── entity.spawn handler — count validation ───────────────────────────────────

func TestEntitySpawnHandler_RejectsZeroCount(t *testing.T) {
	coord := newTestCoordWithStage(t, "0_0", "host-a")
	withFreshRegistry(coord)
	if err := registerEntityCommands(coord); err != nil {
		t.Fatalf("registerEntityCommands: %v", err)
	}
	cmd, _ := coord.registry.Lookup("entity.spawn")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := cmd.Handler(ctx, &cmdsys.Env{}, entitySpawnArgs{Kind: "Foo", Count: 0, X: 10, Y: 10})
	if err == nil {
		t.Fatal("expected error for count=0, got nil")
	}
}

// ── entity.despawn handler ────────────────────────────────────────────────────

func TestEntityDespawnHandler_MarksEntityForRemoval(t *testing.T) {
	coord := newTestCoordWithStage(t, "0_0", "host-a")
	withFreshRegistry(coord)
	if err := registerEntityCommands(coord); err != nil {
		t.Fatalf("registerEntityCommands: %v", err)
	}

	// Spawn an entity directly on the stage (not via game kind registration).
	stage := coord.Cells["0_0"].Stage
	stage.SpawnEntity(component.Position{X: 100, Y: 100})

	// Find the netID that was assigned (it's the only entity).
	netID := findFirstLiveNetID(t, stage)

	cmd, _ := coord.registry.Lookup("entity.despawn")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := cmd.Handler(ctx, &cmdsys.Env{}, entityDespawnArgs{NetID: netID})
	if err != nil {
		t.Fatalf("despawn handler: %v", err)
	}
	out, ok := res.(entityDespawnResult)
	if !ok {
		t.Fatalf("result type = %T, want entityDespawnResult", res)
	}
	if out.NetID != netID {
		t.Errorf("NetID = %d, want %d", out.NetID, netID)
	}
	if out.CellID != "0_0" {
		t.Errorf("CellID = %q, want %q", out.CellID, "0_0")
	}
	if out.HostID != "host-a" {
		t.Errorf("HostID = %q, want %q", out.HostID, "host-a")
	}

	// Flush removals to verify the entity is actually gone.
	eng := coord.Cells["0_0"].Engine
	eng.FlushRemovals()
	entity, _, exists := stage.LookupNetID(netID)
	// After flush the ECS row is removed; entity should no longer be alive.
	if exists && eng.ECS.Alive(entity) {
		t.Error("entity should be dead after despawn + FlushRemovals")
	}
}

func TestEntityDespawnHandler_UnknownNetID(t *testing.T) {
	coord := newTestCoordWithStage(t, "0_0", "host-a")
	withFreshRegistry(coord)
	if err := registerEntityCommands(coord); err != nil {
		t.Fatalf("registerEntityCommands: %v", err)
	}
	cmd, _ := coord.registry.Lookup("entity.despawn")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := cmd.Handler(ctx, &cmdsys.Env{}, entityDespawnArgs{NetID: 99999})
	if err == nil {
		t.Fatal("expected error for unknown netID, got nil")
	}
}

// ── entity.list handler ───────────────────────────────────────────────────────

func TestEntityListHandler_ReturnsLiveEntities(t *testing.T) {
	coord := newTestCoordWithStage(t, "0_0", "host-a")
	withFreshRegistry(coord)
	if err := registerEntityCommands(coord); err != nil {
		t.Fatalf("registerEntityCommands: %v", err)
	}

	stage := coord.Cells["0_0"].Stage
	stage.SpawnEntity(component.Position{X: 50, Y: 75})
	stage.SpawnEntity(component.Position{X: 200, Y: 300})

	cmd, _ := coord.registry.Lookup("entity.list")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := cmd.Handler(ctx, &cmdsys.Env{}, entityListArgs{})
	if err != nil {
		t.Fatalf("list handler: %v", err)
	}
	out, ok := res.(entityListResult)
	if !ok {
		t.Fatalf("result type = %T, want entityListResult", res)
	}
	if len(out.Entities) != 2 {
		t.Fatalf("got %d entities, want 2", len(out.Entities))
	}
	for _, row := range out.Entities {
		if row.CellID != "0_0" {
			t.Errorf("row.CellID = %q, want 0_0", row.CellID)
		}
		if row.HostID != "host-a" {
			t.Errorf("row.HostID = %q, want host-a", row.HostID)
		}
	}
}

func TestEntityListHandler_EmptyWhenNoEntities(t *testing.T) {
	coord := newTestCoordWithStage(t, "0_0", "host-a")
	withFreshRegistry(coord)
	if err := registerEntityCommands(coord); err != nil {
		t.Fatalf("registerEntityCommands: %v", err)
	}
	cmd, _ := coord.registry.Lookup("entity.list")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := cmd.Handler(ctx, &cmdsys.Env{}, entityListArgs{})
	if err != nil {
		t.Fatalf("list handler: %v", err)
	}
	out := res.(entityListResult)
	if len(out.Entities) != 0 {
		t.Errorf("expected 0 entities, got %d", len(out.Entities))
	}
}

// ── entity.tp handler ─────────────────────────────────────────────────────────

func TestEntityTpHandler_UpdatesPosition(t *testing.T) {
	coord := newTestCoordWithStage(t, "0_0", "host-a")
	withFreshRegistry(coord)
	if err := registerEntityCommands(coord); err != nil {
		t.Fatalf("registerEntityCommands: %v", err)
	}

	stage := coord.Cells["0_0"].Stage
	// Spawn at (100, 200) inside cell 0_0 (cell size = 1024).
	stage.SpawnEntity(component.Position{X: 100, Y: 200})
	netID := findFirstLiveNetID(t, stage)

	cmd, _ := coord.registry.Lookup("entity.tp")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Teleport to (400, 500) — still within cell 0_0.
	res, err := cmd.Handler(ctx, &cmdsys.Env{}, entityTpArgs{NetID: netID, X: 400, Y: 500})
	if err != nil {
		t.Fatalf("tp handler: %v", err)
	}
	out, ok := res.(entityTpResult)
	if !ok {
		t.Fatalf("result type = %T, want entityTpResult", res)
	}
	if out.NetID != netID {
		t.Errorf("NetID = %d, want %d", out.NetID, netID)
	}
	if out.NewWorldX != 400 || out.NewWorldY != 500 {
		t.Errorf("new position = (%g,%g), want (400,500)", out.NewWorldX, out.NewWorldY)
	}
	// Previous world coords: cell 0_0 origin is (0,0), so local (100,200) = world (100,200).
	if out.PrevWorldX != 100 || out.PrevWorldY != 200 {
		t.Errorf("prev position = (%g,%g), want (100,200)", out.PrevWorldX, out.PrevWorldY)
	}
	if out.HostID != "host-a" {
		t.Errorf("HostID = %q, want host-a", out.HostID)
	}

	// Verify the position was actually updated in ECS.
	entity, _, _ := stage.LookupNetID(netID)
	pos := stage.PositionMap().Get(entity)
	// After MoveEntityTo with same-cell: local position = 400, 500.
	if pos.X != 400 || pos.Y != 500 {
		t.Errorf("ECS position after tp = (%g,%g), want (400,500)", pos.X, pos.Y)
	}
}

func TestEntityTpHandler_UnknownNetID(t *testing.T) {
	coord := newTestCoordWithStage(t, "0_0", "host-a")
	withFreshRegistry(coord)
	if err := registerEntityCommands(coord); err != nil {
		t.Fatalf("registerEntityCommands: %v", err)
	}
	cmd, _ := coord.registry.Lookup("entity.tp")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := cmd.Handler(ctx, &cmdsys.Env{}, entityTpArgs{NetID: 99999, X: 0, Y: 0})
	if err == nil {
		t.Fatal("expected error for unknown netID, got nil")
	}
}

// ── test helpers ──────────────────────────────────────────────────────────────

// findFirstLiveNetID returns the netID of the first live entity found in the
// stage's netIDIndex. Fails the test if none exist.
func findFirstLiveNetID(t *testing.T, stage *Stage) uint32 {
	t.Helper()
	stage.netIDIdx.mu.RLock()
	defer stage.netIDIdx.mu.RUnlock()
	for netID, slot := range stage.netIDIdx.slots {
		if slot.Presence == PresenceLive {
			return netID
		}
	}
	t.Fatal("no live entity found in netIDIndex")
	return 0
}

