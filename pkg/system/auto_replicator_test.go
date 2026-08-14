package system

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmokit/pkg/component"
	"github.com/zenion/mmokit/pkg/quantize"
	"github.com/zenion/mmokit/pkg/spatial"
)

func TestParseNetTag(t *testing.T) {
	tests := []struct {
		tag      string
		enc      string
		opts     map[string]string
		initial  bool
		wantSize int
	}{
		{"qnorm", "qnorm", nil, false, 1},
		{"u16", "u16", nil, false, 2},
		{"bool", "bool", nil, false, 1},
		{"qvel,scale=2000", "qvel", map[string]string{"scale": "2000"}, false, 2},
		{"initial,string", "string", nil, true, -1},
		{"initial,u8", "u8", nil, true, 1},
		{"f32", "f32", nil, false, 4},
		{"u32", "u32", nil, false, 4},
		{"u8", "u8", nil, false, 1},
		{"i16", "i16", nil, false, 2},
		{"pos", "pos", nil, false, 8},
		{"qangle", "qangle", nil, false, 2},
		{"qsize,scale=500", "qsize", map[string]string{"scale": "500"}, false, 2},
	}

	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			fm, err := parseNetTag(tt.tag)
			if err != nil {
				t.Fatalf("parseNetTag(%q) error: %v", tt.tag, err)
			}
			if fm.encoding != tt.enc {
				t.Errorf("encoding = %q, want %q", fm.encoding, tt.enc)
			}
			if fm.initial != tt.initial {
				t.Errorf("initial = %v, want %v", fm.initial, tt.initial)
			}
			if fm.wireSize != tt.wantSize {
				t.Errorf("wireSize = %d, want %d", fm.wireSize, tt.wantSize)
			}
			for k, v := range tt.opts {
				if fm.options[k] != v {
					t.Errorf("option[%s] = %q, want %q", k, fm.options[k], v)
				}
			}
		})
	}
}

func TestParseNetTagInvalid(t *testing.T) {
	_, err := parseNetTag("unknownencoding")
	if err == nil {
		t.Fatal("expected error for unknown encoding")
	}
}

func TestFieldHashWriter(t *testing.T) {
	hw, err := hashWriterFor(fieldMeta{encoding: "f32", wireSize: 4})
	if err != nil {
		t.Fatal(err)
	}
	h := &Hasher{}
	h.Reset()
	hw(float32(1.5), h)
	got := h.Sum()

	h2 := &Hasher{}
	h2.Reset()
	h2.Float32(1.5)
	want := h2.Sum()

	if got != want {
		t.Errorf("hash mismatch: got %d, want %d", got, want)
	}
}

func TestFieldSnapshotWriter(t *testing.T) {
	sw, err := snapshotWriterFor(fieldMeta{encoding: "u16", wireSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	w := quantize.NewSnapshotWriter(buf)
	sw(uint16(42), w)

	buf2 := make([]byte, 64)
	w2 := quantize.NewSnapshotWriter(buf2)
	w2.Uint16(42)

	if w.Len() != w2.Len() {
		t.Errorf("length mismatch: got %d, want %d", w.Len(), w2.Len())
	}
	for i := 0; i < w.Len(); i++ {
		if w.Bytes()[i] != w2.Bytes()[i] {
			t.Errorf("byte %d mismatch: got %d, want %d", i, w.Bytes()[i], w2.Bytes()[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Test component types
// ---------------------------------------------------------------------------

type testHealth struct {
	Current float32 `net:"qnorm"`
	Max     float32 `net:"u16"`
}

type testName struct {
	Name string `net:"initial"`
}

// ---------------------------------------------------------------------------
// AutoReplicator tests
// ---------------------------------------------------------------------------

func TestAutoReplicatorEntityType(t *testing.T) {
	world := ecs.NewWorld()
	healthMap := ecs.NewMap1[testHealth](world)

	rep := AutoReplicator(7,
		Component(healthMap),
	)
	if rep.EntityType() != 7 {
		t.Errorf("EntityType() = %d, want 7", rep.EntityType())
	}
}

func TestAutoReplicatorLayout(t *testing.T) {
	world := ecs.NewWorld()
	healthMap := ecs.NewMap1[testHealth](world)
	velMap := ecs.NewMap1[component.Velocity](world)

	rep := AutoReplicator(1,
		EntryPosition(),         // 4, 4
		QVelocity(velMap, 1000), // 2, 2
		Component(healthMap),    // qnorm=1, u16=2
	)

	layout := rep.SnapshotLayout()
	want := []int{4, 4, 2, 2, 1, 2}
	if len(layout) != len(want) {
		t.Fatalf("layout len = %d, want %d", len(layout), len(want))
	}
	for i, v := range want {
		if layout[i] != v {
			t.Errorf("layout[%d] = %d, want %d", i, layout[i], v)
		}
	}
}

func TestAutoReplicatorSnapshot(t *testing.T) {
	world := ecs.NewWorld()
	healthMap := ecs.NewMap1[testHealth](world)

	// Create entity with testHealth via the map.
	entity := healthMap.NewEntity(&testHealth{Current: 0.75, Max: 100})

	rep := AutoReplicator(1,
		EntryPosition(),
		Component(healthMap),
	)

	entry := spatial.Entry{Entity: entity, X: 10.5, Y: 20.5}
	viewer := &ViewerInfo{ConnID: 1}

	// Snapshot via auto-replicator.
	buf := make([]byte, 256)
	w := quantize.NewSnapshotWriter(buf)
	rep.Snapshot(w, viewer, entry)
	got := make([]byte, w.Len())
	copy(got, w.Bytes())

	// Build expected manually.
	buf2 := make([]byte, 256)
	w2 := quantize.NewSnapshotWriter(buf2)
	w2.Float32(10.5) // entry X
	w2.Float32(20.5) // entry Y
	w2.QNorm(0.75)   // Current (qnorm)
	w2.Uint16(100)   // Max (u16)
	want := make([]byte, w2.Len())
	copy(want, w2.Bytes())

	if !bytes.Equal(got, want) {
		t.Errorf("snapshot mismatch:\n  got  %v\n  want %v", got, want)
	}
}

func TestAutoReplicatorInitialData(t *testing.T) {
	world := ecs.NewWorld()
	nameMap := ecs.NewMap1[testName](world)

	entity := nameMap.NewEntity(&testName{Name: "Alice"})

	rep := AutoReplicator(2,
		Component(nameMap),
	)

	entry := spatial.Entry{Entity: entity}
	viewer := &ViewerInfo{ConnID: 1}

	initData := rep.InitialData(viewer, entry)
	if initData == nil {
		t.Fatal("expected non-nil InitialData")
	}

	// Expected: length-prefixed string: [5, 'A', 'l', 'i', 'c', 'e']
	want := append([]byte{5}, []byte("Alice")...)
	if !bytes.Equal(initData, want) {
		t.Errorf("InitialData mismatch:\n  got  %v\n  want %v", initData, want)
	}
}

func TestAutoReplicatorInitialHash_ChangesWithField(t *testing.T) {
	world := ecs.NewWorld()
	nameMap := ecs.NewMap1[testName](world)
	entity := nameMap.NewEntity(&testName{Name: "Alice"})

	rep := AutoReplicator(2, Component(nameMap))
	entry := spatial.Entry{Entity: entity}
	viewer := &ViewerInfo{ConnID: 1}

	if !rep.HasInitial() {
		t.Fatal("replicator with a net:\"initial\" field must report HasInitial() == true")
	}

	var h1 Hasher
	h1.Reset()
	rep.InitialHash(&h1, viewer, entry)
	before := h1.Sum()

	nameMap.Get(entity).Name = "renamed"

	var h2 Hasher
	h2.Reset()
	rep.InitialHash(&h2, viewer, entry)
	after := h2.Sum()

	if before == after {
		t.Fatalf("InitialHash must change when an initial field changes: before=%d after=%d", before, after)
	}
}

func TestAutoReplicatorInitialDataNil(t *testing.T) {
	world := ecs.NewWorld()
	healthMap := ecs.NewMap1[testHealth](world)

	// No initial fields — should return nil.
	rep := AutoReplicator(3,
		Component(healthMap),
	)

	entity := healthMap.NewEntity(&testHealth{})
	entry := spatial.Entry{Entity: entity}
	viewer := &ViewerInfo{ConnID: 1}

	if got := rep.InitialData(viewer, entry); got != nil {
		t.Errorf("expected nil InitialData, got %v", got)
	}
}

func TestAutoReplicatorHash(t *testing.T) {
	world := ecs.NewWorld()
	healthMap := ecs.NewMap1[testHealth](world)

	entity := healthMap.NewEntity(&testHealth{Current: 0.5, Max: 200})

	rep := AutoReplicator(1,
		EntryPosition(),
		Component(healthMap),
	)

	entry := spatial.Entry{Entity: entity, X: 1.0, Y: 2.0}
	viewer := &ViewerInfo{ConnID: 1}

	// Hash twice — must be consistent.
	hasher := &Hasher{}
	hasher.Reset()
	rep.Hash(hasher, viewer, entry)
	hash1 := hasher.Sum()

	hasher.Reset()
	rep.Hash(hasher, viewer, entry)
	hash2 := hasher.Sum()

	if hash1 != hash2 {
		t.Errorf("hash not consistent: %d != %d", hash1, hash2)
	}

	// Change Max (u16 encoding) — hash must change.
	// We change Max rather than Current because the qnorm hash writer
	// truncates float32 to uint8 via toUint8, so small float changes
	// may not affect the hash.
	h := healthMap.Get(entity)
	h.Max = 500
	hasher.Reset()
	rep.Hash(hasher, viewer, entry)
	hash3 := hasher.Sum()

	if hash1 == hash3 {
		t.Error("hash did not change after modifying component")
	}
}

// TestComponentByID_ByteIdentity asserts that the type-erased ComponentByID
// path produces byte-identical snapshot bytes, hash bytes, and initial-data
// bytes compared to the typed Component[T] path. This is the wire-format
// invariant the bundle-reflection registration walker depends on.
func TestComponentByID_ByteIdentity(t *testing.T) {
	world := ecs.NewWorld()

	// Build typed maps for both component types.
	healthMap := ecs.NewMap1[testHealth](world)
	nameMap := ecs.NewMap1[testName](world)

	// Resolve component IDs for the type-erased path.
	healthID := ecs.TypeID(world, reflect.TypeFor[testHealth]())
	nameID := ecs.TypeID(world, reflect.TypeFor[testName]())

	// Spawn an entity carrying both components with non-zero values.
	entity := healthMap.NewEntity(&testHealth{Current: 0.42, Max: 250})
	nameMap.Add(entity, &testName{Name: "Bob"})

	// Build two replicators: one driven by Component[T], the other by ComponentByID.
	repTyped := AutoReplicator(9,
		EntryPosition(),
		Component(healthMap),
		Component(nameMap),
	)
	repByID := AutoReplicator(9,
		EntryPosition(),
		ComponentByID(world, healthID, reflect.TypeFor[testHealth]()),
		ComponentByID(world, nameID, reflect.TypeFor[testName]()),
	)

	entry := spatial.Entry{Entity: entity, X: 7.5, Y: -3.25}
	viewer := &ViewerInfo{ConnID: 1}

	// Snapshot via both paths and compare bytes.
	bufA := make([]byte, 256)
	wA := quantize.NewSnapshotWriter(bufA)
	repTyped.Snapshot(wA, viewer, entry)
	gotTyped := append([]byte(nil), wA.Bytes()...)

	bufB := make([]byte, 256)
	wB := quantize.NewSnapshotWriter(bufB)
	repByID.Snapshot(wB, viewer, entry)
	gotByID := append([]byte(nil), wB.Bytes()...)

	if !bytes.Equal(gotTyped, gotByID) {
		t.Errorf("snapshot bytes differ between Component[T] and ComponentByID:\n  typed = %v\n  byID  = %v", gotTyped, gotByID)
	}

	// Hash bytes must also be identical.
	hA := &Hasher{}
	hA.Reset()
	repTyped.Hash(hA, viewer, entry)
	hashTyped := hA.Sum()

	hB := &Hasher{}
	hB.Reset()
	repByID.Hash(hB, viewer, entry)
	hashByID := hB.Sum()

	if hashTyped != hashByID {
		t.Errorf("hash differs between Component[T] and ComponentByID: typed=%d byID=%d", hashTyped, hashByID)
	}

	// Initial data must be identical (testName carries an initial-tagged field).
	initTyped := repTyped.InitialData(viewer, entry)
	initByID := repByID.InitialData(viewer, entry)
	if !bytes.Equal(initTyped, initByID) {
		t.Errorf("initial data differs between Component[T] and ComponentByID:\n  typed = %v\n  byID  = %v", initTyped, initByID)
	}
}

// TestComponentByID_OptionalMissing exercises the type-erased optional path
// when the component is absent — must write zero bytes byte-identically to
// the typed OptionalComponent[T] equivalent.
func TestComponentByID_OptionalMissing(t *testing.T) {
	world := ecs.NewWorld()
	healthMap := ecs.NewMap1[testHealth](world)
	healthID := ecs.TypeID(world, reflect.TypeFor[testHealth]())

	// Entity without testHealth.
	entity := world.NewEntity()

	repTyped := AutoReplicator(1, OptionalComponent(healthMap))
	repByID := AutoReplicator(1, OptionalComponentByID(world, healthID, reflect.TypeFor[testHealth]()))

	entry := spatial.Entry{Entity: entity}
	viewer := &ViewerInfo{ConnID: 1}

	bufA := make([]byte, 64)
	wA := quantize.NewSnapshotWriter(bufA)
	repTyped.Snapshot(wA, viewer, entry)

	bufB := make([]byte, 64)
	wB := quantize.NewSnapshotWriter(bufB)
	repByID.Snapshot(wB, viewer, entry)

	if !bytes.Equal(wA.Bytes(), wB.Bytes()) {
		t.Errorf("optional-missing snapshot differs:\n  typed = %v\n  byID  = %v", wA.Bytes(), wB.Bytes())
	}
}

func TestAutoReplicatorOptionalMissing(t *testing.T) {
	world := ecs.NewWorld()
	healthMap := ecs.NewMap1[testHealth](world)

	// Entity without testHealth component.
	entity := world.NewEntity()

	rep := AutoReplicator(1,
		OptionalComponent(healthMap),
	)

	entry := spatial.Entry{Entity: entity}
	viewer := &ViewerInfo{ConnID: 1}

	// Should not panic — writes zeros.
	buf := make([]byte, 256)
	w := quantize.NewSnapshotWriter(buf)
	rep.Snapshot(w, viewer, entry)

	// Expected: qnorm zero (1 byte) + u16 zero (2 bytes) = 3 bytes of zeros.
	got := w.Bytes()
	if w.Len() != 3 {
		t.Fatalf("snapshot len = %d, want 3", w.Len())
	}
	for i, b := range got {
		if b != 0 {
			t.Errorf("byte %d = %d, want 0", i, b)
		}
	}

	// Hash should also not panic.
	hasher := &Hasher{}
	hasher.Reset()
	rep.Hash(hasher, viewer, entry)
}
