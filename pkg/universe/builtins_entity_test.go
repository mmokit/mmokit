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

	"github.com/mlange-42/ark/ecs"

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

	cell := &Cell{MeshID: MeshCellID(cellID), Engine: eng, Stage: stage}
	host := &Host{
		ID:    hostID,
		Cells: map[CellID]*Cell{parsed: cell},
	}
	return &Process{
		Cells: map[MeshCellID]*Cell{MeshCellID(cellID): cell},
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
		{"entity.inspect", cmdsys.RouteEntityOwner, "entity.inspect"},
		{"entity.modify", cmdsys.RouteEntityOwner, "entity.modify"},
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
		{"entity.inspect", entityInspectArgs{}, entityInspectResult{}},
		{"entity.modify", entityModifyArgs{}, entityModifyResult{}},
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
	stage.Spawn(component.Position{X: 100, Y: 100}, component.EntityKind{Type: 1})

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
	stage.Spawn(component.Position{X: 50, Y: 75}, component.EntityKind{Type: 1})
	stage.Spawn(component.Position{X: 200, Y: 300}, component.EntityKind{Type: 1})

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
	stage.Spawn(component.Position{X: 100, Y: 200}, component.EntityKind{Type: 1})
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


// ── ComponentAccessors ────────────────────────────────────────────────────────

func TestComponentAccessors_ReturnsLivePointer(t *testing.T) {
	coords.SetCellSize(1024)
	log := logger.New()
	eng := engine.New(engine.DefaultConfig(), netpkg.NewConnManager(), log)
	parsed, _ := ParseCellID("0_0")
	stage := NewStage(eng, parsed, 300, nil)
	w := stage.ECSWorld()

	// Build a kind def with Position as a Required component via the same
	// low-level primitive mmokit.RegisterKind uses.
	def := EntityKindDef{Kind: 7, Name: "Probe"}
	posType := reflect.TypeFor[component.Position]()
	KindComponentByID(&def, w, ecs.TypeID(w, posType), posType, KindComponentRequired)
	stage.RegisterEntityKind(def)

	e := stage.Spawn(component.Position{X: 12, Y: 34}, component.EntityKind{Type: 7})

	accs := stage.EntityKindDefs()[7].ComponentAccessors()
	if len(accs) != 1 {
		t.Fatalf("ComponentAccessors len = %d, want 1", len(accs))
	}
	if accs[0].Name != "Position" {
		t.Errorf("accessor Name = %q, want Position", accs[0].Name)
	}
	got, ok := accs[0].Get(e.Handle())
	if !ok {
		t.Fatal("Get returned ok=false for present component")
	}
	pos, ok := got.(*component.Position)
	if !ok {
		t.Fatalf("Get returned %T, want *component.Position", got)
	}
	if pos.X != 12 || pos.Y != 34 {
		t.Errorf("pos = %+v, want {12 34}", *pos)
	}
}

// ── entity.inspect handler ────────────────────────────────────────────────────

// registerProbeKind registers a kind (id 7, "Probe") whose only component is
// Position, using the low-level accessor primitive. Returns the stage.
func registerProbeKind(t *testing.T, coord *Process) *Stage {
	t.Helper()
	stage := coord.Cells["0_0"].Stage
	w := stage.ECSWorld()
	def := EntityKindDef{Kind: 7, Name: "Probe"}
	posType := reflect.TypeFor[component.Position]()
	KindComponentByID(&def, w, ecs.TypeID(w, posType), posType, KindComponentRequired)
	stage.RegisterEntityKind(def)
	return stage
}

func TestEntityInspectHandler_ReturnsComponentFields(t *testing.T) {
	coord := newTestCoordWithStage(t, "0_0", "host-a")
	withFreshRegistry(coord)
	if err := registerEntityCommands(coord); err != nil {
		t.Fatalf("registerEntityCommands: %v", err)
	}
	stage := registerProbeKind(t, coord)
	stage.Spawn(component.Position{X: 11, Y: 22}, component.EntityKind{Type: 7})
	netID := findFirstLiveNetID(t, stage)

	cmd, _ := coord.registry.Lookup("entity.inspect")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := cmd.Handler(ctx, &cmdsys.Env{}, entityInspectArgs{NetID: netID})
	if err != nil {
		t.Fatalf("inspect handler: %v", err)
	}
	out := res.(entityInspectResult)
	if out.Kind != "Probe" {
		t.Errorf("Kind = %q, want Probe", out.Kind)
	}
	var sawX bool
	for _, r := range out.Components {
		if r.Component == "Position" && r.Field == "X" {
			sawX = true
			if r.Value != "11" {
				t.Errorf("Position.X value = %q, want 11", r.Value)
			}
			if !r.Editable {
				t.Error("Position.X should be editable")
			}
		}
	}
	if !sawX {
		t.Errorf("expected a Position.X row; got %+v", out.Components)
	}
}

func TestEntityInspectHandler_UnknownNetID(t *testing.T) {
	coord := newTestCoordWithStage(t, "0_0", "host-a")
	withFreshRegistry(coord)
	if err := registerEntityCommands(coord); err != nil {
		t.Fatalf("registerEntityCommands: %v", err)
	}
	cmd, _ := coord.registry.Lookup("entity.inspect")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := cmd.Handler(ctx, &cmdsys.Env{}, entityInspectArgs{NetID: 99999}); err == nil {
		t.Fatal("expected error for unknown netID")
	}
}

// ── entity.modify handler ─────────────────────────────────────────────────────

func TestEntityModifyHandler_SetsField(t *testing.T) {
	coord := newTestCoordWithStage(t, "0_0", "host-a")
	withFreshRegistry(coord)
	if err := registerEntityCommands(coord); err != nil {
		t.Fatalf("registerEntityCommands: %v", err)
	}
	stage := registerProbeKind(t, coord)
	e := stage.Spawn(component.Position{X: 11, Y: 22}, component.EntityKind{Type: 7})
	netID := findFirstLiveNetID(t, stage)

	cmd, _ := coord.registry.Lookup("entity.modify")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := cmd.Handler(ctx, &cmdsys.Env{}, entityModifyArgs{
		NetID: netID, Component: "Position", Field: "X", Value: "99",
	})
	if err != nil {
		t.Fatalf("modify handler: %v", err)
	}
	out := res.(entityModifyResult)
	if out.Old != "11" || out.New != "99" {
		t.Errorf("old/new = %q/%q, want 11/99", out.Old, out.New)
	}
	// Verify the live component changed.
	pos := ecs.NewMap1[component.Position](stage.ECSWorld()).Get(e.Handle())
	if pos.X != 99 {
		t.Errorf("live Position.X = %v, want 99", pos.X)
	}
}

func TestEntityModifyHandler_Errors(t *testing.T) {
	coord := newTestCoordWithStage(t, "0_0", "host-a")
	withFreshRegistry(coord)
	if err := registerEntityCommands(coord); err != nil {
		t.Fatalf("registerEntityCommands: %v", err)
	}
	stage := registerProbeKind(t, coord)
	stage.Spawn(component.Position{X: 1, Y: 2}, component.EntityKind{Type: 7})
	netID := findFirstLiveNetID(t, stage)
	cmd, _ := coord.registry.Lookup("entity.modify")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Unknown component.
	if _, err := cmd.Handler(ctx, &cmdsys.Env{}, entityModifyArgs{NetID: netID, Component: "Nope", Field: "X", Value: "1"}); err == nil {
		t.Error("expected error for unknown component")
	}
	// Unknown field.
	if _, err := cmd.Handler(ctx, &cmdsys.Env{}, entityModifyArgs{NetID: netID, Component: "Position", Field: "Z", Value: "1"}); err == nil {
		t.Error("expected error for unknown field")
	}
	// Bad value.
	if _, err := cmd.Handler(ctx, &cmdsys.Env{}, entityModifyArgs{NetID: netID, Component: "Position", Field: "X", Value: "abc"}); err == nil {
		t.Error("expected error for uncoercible value")
	}
	// Unknown netID.
	if _, err := cmd.Handler(ctx, &cmdsys.Env{}, entityModifyArgs{NetID: 99999, Component: "Position", Field: "X", Value: "1"}); err == nil {
		t.Error("expected error for unknown netID")
	}
}
