package mmokit

import (
	"reflect"
	"testing"
)

// ---------------------------------------------------------------------------
// Test component + bundle types
// ---------------------------------------------------------------------------

type kindRegTestNameComp struct {
	Name string
}

type kindRegTestHealthComp struct {
	HP float32
}

type kindRegTestBundle struct {
	Name   *kindRegTestNameComp
	Health *kindRegTestHealthComp
}

// ---------------------------------------------------------------------------
// buildKindSpec — low-level validation tests
// ---------------------------------------------------------------------------

func TestRegisterKind_BuildsKindSpec(t *testing.T) {
	// Spy on the realize fn — record each component type registered.
	var registered []reflect.Type

	fields := []KindFieldSpec{
		Field[kindRegTestNameComp](),
		Field[kindRegTestHealthComp](),
	}
	spec := buildKindSpec[kindRegTestBundle](42, "TestKind", nil, fields, func(ty reflect.Type) {
		registered = append(registered, ty)
	})

	if spec == nil {
		t.Fatal("expected non-nil kind spec")
	}
	if len(registered) != 2 {
		t.Fatalf("expected 2 components registered, got %d (%v)", len(registered), registered)
	}
	want := map[reflect.Type]bool{
		reflect.TypeFor[kindRegTestNameComp]():   true,
		reflect.TypeFor[kindRegTestHealthComp](): true,
	}
	for _, ty := range registered {
		if !want[ty] {
			t.Errorf("unexpected component type %v registered", ty)
		}
	}
}

func TestRegisterKind_RejectsNonStruct(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on non-struct T")
		}
	}()
	buildKindSpec[int](0, "Bad", nil, nil, func(reflect.Type) {})
}

func TestRegisterKind_RejectsNonPointerField(t *testing.T) {
	type badBundle struct {
		Name kindRegTestNameComp // value, not pointer
	}
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on non-pointer field")
		}
	}()
	buildKindSpec[badBundle](0, "Bad", nil, nil, func(reflect.Type) {})
}

func TestRegisterKind_RejectsEmptyBundle(t *testing.T) {
	type emptyBundle struct{}
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on bundle with zero exported pointer-to-struct fields")
		}
	}()
	buildKindSpec[emptyBundle](0, "Empty", nil, nil, func(reflect.Type) {})
}

func TestRegisterKind_RejectsAllUnexportedFields(t *testing.T) {
	type unexportedBundle struct {
		name *kindRegTestNameComp // lowercase = unexported
	}
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on bundle with no exported fields")
		}
	}()
	buildKindSpec[unexportedBundle](0, "Unexported", nil, nil, func(reflect.Type) {})
}

// TestRegisterKind_RejectsMissingFields panics when fewer Field() specs are
// provided than the bundle has exported fields.
func TestRegisterKind_RejectsMissingFields(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic when Field() count < bundle field count")
		}
	}()
	// Bundle has 2 fields; only 1 Field spec provided.
	buildKindSpec[kindRegTestBundle](0, "Bad", nil, []KindFieldSpec{
		Field[kindRegTestNameComp](),
	}, nil)
}

// TestRegisterKind_RejectsWrongFieldType panics when a Field() spec type
// doesn't match the corresponding bundle field type.
func TestRegisterKind_RejectsWrongFieldType(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on Field() type mismatch")
		}
	}()
	// Bundle field 0 is *kindRegTestNameComp but we supply Field[kindRegTestHealthComp]().
	buildKindSpec[kindRegTestBundle](0, "Bad", nil, []KindFieldSpec{
		Field[kindRegTestHealthComp](), // wrong order
		Field[kindRegTestNameComp](),
	}, nil)
}

// ---------------------------------------------------------------------------
// RegisterKind — integration tests using a live Process
// ---------------------------------------------------------------------------

func TestRegisterKind_RealizesPerCell(t *testing.T) {
	mmo := New(Config{
		CellsX:    1,
		CellsY:    1,
		CellSize:  1000,
		TickRate:  20,
		AoIRadius: 100,
		Headless:  true,
	})
	RegisterKind[kindRegTestBundle](mmo, 100, "TestKind", EngineBindingsConfig{},
		Field[kindRegTestNameComp](),
		Field[kindRegTestHealthComp](),
	)
	mmo.Build()
	t.Cleanup(func() { mmo.Shutdown() })

	cells := mmo.Cells
	var cell *Cell
	for _, c := range cells {
		cell = c
		break
	}
	if cell == nil {
		t.Fatal("expected at least one cell")
	}
	defs := cell.Stage.EntityKindDefs()
	def, ok := defs[100]
	if !ok {
		t.Fatalf("kind 100 not registered on cell %s", cell.ID)
	}
	if def.Name != "TestKind" {
		t.Errorf("expected kind name TestKind, got %q", def.Name)
	}
}

// TestRegisterKind_WiresTransferRegistry verifies that components registered
// via RegisterKind are present in the cell's ReplicationRegistry (transfer path).
func TestRegisterKind_WiresTransferRegistry(t *testing.T) {
	mmo := New(Config{
		CellsX:    1,
		CellsY:    1,
		CellSize:  1000,
		TickRate:  20,
		AoIRadius: 100,
		Headless:  true,
	})
	RegisterKind[kindRegTestBundle](mmo, 101, "TransferKind", EngineBindingsConfig{},
		Field[kindRegTestNameComp](),
		Field[kindRegTestHealthComp](),
	)
	mmo.Build()
	t.Cleanup(func() { mmo.Shutdown() })

	var cell *Cell
	for _, c := range mmo.Cells {
		cell = c
		break
	}
	if cell == nil {
		t.Fatal("expected at least one cell")
	}

	reg := cell.Stage.ReplicationRegistry()
	if reg == nil {
		t.Fatal("expected non-nil ReplicationRegistry")
	}
	// Both components should be registered in the transfer registry.
	// kindRegTestNameComp and kindRegTestHealthComp — so Len >= 2.
	if reg.Len() < 2 {
		t.Errorf("expected >= 2 components in ReplicationRegistry, got %d; cross-cell transfer will not serialize them", reg.Len())
	}
}

// TestRegisterKind_WiresNetworkBindings verifies that components registered
// via RegisterKind populate def.NetworkBindings (client replication path).
func TestRegisterKind_WiresNetworkBindings(t *testing.T) {
	mmo := New(Config{
		CellsX:    1,
		CellsY:    1,
		CellSize:  1000,
		TickRate:  20,
		AoIRadius: 100,
		Headless:  true,
	})
	RegisterKind[kindRegTestBundle](mmo, 102, "NetKind", EngineBindingsConfig{},
		Field[kindRegTestNameComp](),
		Field[kindRegTestHealthComp](),
	)
	mmo.Build()
	t.Cleanup(func() { mmo.Shutdown() })

	var cell *Cell
	for _, c := range mmo.Cells {
		cell = c
		break
	}
	if cell == nil {
		t.Fatal("expected at least one cell")
	}

	defs := cell.Stage.EntityKindDefs()
	def, ok := defs[102]
	if !ok {
		t.Fatalf("kind 102 not registered on cell %s", cell.ID)
	}
	// Both components should have NetworkBindings for AutoReplicator.
	if len(def.NetworkBindings) != 2 {
		t.Errorf("expected 2 NetworkBindings, got %d; components won't reach clients", len(def.NetworkBindings))
	}
}

// TestRegisterKind_FieldLocalOnly verifies that FieldLocalOnly components do
// not appear in NetworkBindings or the transfer ReplicationRegistry.
func TestRegisterKind_FieldLocalOnly(t *testing.T) {
	type localBundle struct {
		Name  *kindRegTestNameComp
		Local *kindRegTestHealthComp
	}
	mmo := New(Config{
		CellsX:    1,
		CellsY:    1,
		CellSize:  1000,
		TickRate:  20,
		AoIRadius: 100,
		Headless:  true,
	})
	RegisterKind[localBundle](mmo, 103, "LocalKind", EngineBindingsConfig{},
		Field[kindRegTestNameComp](),
		FieldLocalOnly[kindRegTestHealthComp](),
	)
	mmo.Build()
	t.Cleanup(func() { mmo.Shutdown() })

	var cell *Cell
	for _, c := range mmo.Cells {
		cell = c
		break
	}
	if cell == nil {
		t.Fatal("expected at least one cell")
	}

	defs := cell.Stage.EntityKindDefs()
	def, ok := defs[103]
	if !ok {
		t.Fatalf("kind 103 not registered on cell %s", cell.ID)
	}
	// Only the Name field (via Field) contributes a NetworkBinding.
	if len(def.NetworkBindings) != 1 {
		t.Errorf("expected 1 NetworkBinding (Name only), got %d", len(def.NetworkBindings))
	}
	// Only the Name component should be in the transfer registry.
	reg := cell.Stage.ReplicationRegistry()
	if reg.Len() != 1 {
		t.Errorf("expected 1 component in ReplicationRegistry (Name only), got %d", reg.Len())
	}
}
