package universe

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/mlange-42/ark/ecs"
)

func TestRegisterComponent_ReflectRoundTrip(t *testing.T) {
	type Shield struct {
		Current float32
		Max     float32
		Regen   float32
	}
	w := ecs.NewWorld(64)
	m := ecs.NewMap1[Shield](w)
	reg := NewReplicationRegistry()
	RegisterComponent(reg, m)

	rep := reg.Get(1)
	if rep == nil {
		t.Fatal("auto-assigned ID 1 not found")
	}

	entity := w.NewEntity()
	m.Add(entity, &Shield{Current: 50, Max: 100, Regen: 2.5})

	data := rep.Scan(entity)
	if data == nil {
		t.Fatal("Scan returned nil")
	}

	entity2 := w.NewEntity()
	rep.Add(entity2, data)
	s := m.Get(entity2)
	if s.Current != 50 || s.Max != 100 || s.Regen != 2.5 {
		t.Fatalf("got %+v", s)
	}
}

func TestRegisterComponent_WithMarshal(t *testing.T) {
	type Pos struct {
		X float32
		Y float32
	}
	w := ecs.NewWorld(64)
	m := ecs.NewMap1[Pos](w)
	reg := NewReplicationRegistry()

	RegisterComponent(reg, m, WithMarshal(
		func(p *Pos) []byte {
			buf := make([]byte, 8)
			binary.LittleEndian.PutUint32(buf[0:], math.Float32bits(p.X))
			binary.LittleEndian.PutUint32(buf[4:], math.Float32bits(p.Y))
			return buf
		},
		func(data []byte, p *Pos) {
			p.X = math.Float32frombits(binary.LittleEndian.Uint32(data[0:]))
			p.Y = math.Float32frombits(binary.LittleEndian.Uint32(data[4:]))
		},
	))

	entity := w.NewEntity()
	m.Add(entity, &Pos{X: 10, Y: 20})

	rep := reg.Get(1)
	data := rep.Scan(entity)
	if data == nil {
		t.Fatal("Scan returned nil")
	}

	entity2 := w.NewEntity()
	rep.Add(entity2, data)
	p := m.Get(entity2)
	if p.X != 10 || p.Y != 20 {
		t.Fatalf("got %+v", p)
	}
}

func TestRegisterComponent_WithPreMarshal(t *testing.T) {
	type Targeting struct {
		TargetID uint32
		Range    float32
	}
	w := ecs.NewWorld(64)
	m := ecs.NewMap1[Targeting](w)
	reg := NewReplicationRegistry()

	RegisterComponent(reg, m, WithPreMarshal(func(tgt *Targeting) {
		tgt.TargetID = 0 // clear before sending over wire
	}))

	entity := w.NewEntity()
	m.Add(entity, &Targeting{TargetID: 999, Range: 500})

	rep := reg.Get(1)
	data := rep.Scan(entity)
	if data == nil {
		t.Fatal("Scan returned nil")
	}

	// Original should be untouched.
	orig := m.Get(entity)
	if orig.TargetID != 999 {
		t.Fatalf("PreMarshal mutated original: TargetID=%d", orig.TargetID)
	}

	// Deserialized should have cleared TargetID.
	entity2 := w.NewEntity()
	rep.Add(entity2, data)
	out := m.Get(entity2)
	if out.TargetID != 0 {
		t.Fatalf("expected TargetID=0 after PreMarshal, got %d", out.TargetID)
	}
	if out.Range != 500 {
		t.Fatalf("expected Range=500, got %f", out.Range)
	}
}

func TestRegisterComponent_ScanMissingComponent(t *testing.T) {
	type Health struct {
		Current float32
		Max     float32
	}
	w := ecs.NewWorld(64)
	m := ecs.NewMap1[Health](w)
	reg := NewReplicationRegistry()
	RegisterComponent(reg, m)

	// Entity without the Health component.
	entity := w.NewEntity()

	rep := reg.Get(1)
	data := rep.Scan(entity)
	if data != nil {
		t.Fatalf("expected nil for entity without component, got %d bytes", len(data))
	}
}

func TestRegisterComponent_ApplyUpdatesExisting(t *testing.T) {
	type Vel struct {
		VX float32
		VY float32
	}
	w := ecs.NewWorld(64)
	m := ecs.NewMap1[Vel](w)
	reg := NewReplicationRegistry()
	RegisterComponent(reg, m)

	entity := w.NewEntity()
	m.Add(entity, &Vel{VX: 1, VY: 2})

	// Marshal from entity
	rep := reg.Get(1)
	data := rep.Scan(entity)

	// Create another entity with different values, then Apply
	entity2 := w.NewEntity()
	m.Add(entity2, &Vel{VX: 99, VY: 99})

	rep.Apply(entity2, data)
	v := m.Get(entity2)
	if v.VX != 1 || v.VY != 2 {
		t.Fatalf("Apply did not update: got %+v", v)
	}
}

// TestApply_RefusedBodyLeavesComponentUntorn pins the atomicity of a replicated
// component update. A body whose leading field decodes and whose trailing field
// is truncated must leave the live component exactly as it was, not half
// overwritten — a torn component is indistinguishable downstream from real data,
// and this path is reachable from anything that can put bytes on the border
// replication or handoff path.
//
// Before applyInPlace this decoded straight into world storage, so Current held
// the peer's 999 while Max and Regen kept the previous tick's values.
func TestApply_RefusedBodyLeavesComponentUntorn(t *testing.T) {
	type Shield struct {
		Current float32
		Max     float32
		Regen   float32
	}
	w := ecs.NewWorld(64)
	m := ecs.NewMap1[Shield](w)
	reg := NewReplicationRegistry()
	RegisterComponent(reg, m)
	rep := reg.Get(1)

	entity := w.NewEntity()
	m.Add(entity, &Shield{Current: 50, Max: 100, Regen: 2.5})

	// A full body is 12 bytes (three float32). Supply the first field plus a
	// truncated second so the decoder accepts Current and then refuses.
	torn := make([]byte, 6)
	binary.LittleEndian.PutUint32(torn[0:], math.Float32bits(999))

	if err := rep.Apply(entity, torn); err == nil {
		t.Fatal("Apply accepted a truncated body")
	}
	if got := m.Get(entity); got.Current != 50 || got.Max != 100 || got.Regen != 2.5 {
		t.Fatalf("refused body tore the live component: got %+v, want the pre-Apply value", got)
	}

	// The same bytes through Add's pre-existing-component branch, which shares
	// the path and had the identical defect.
	if err := rep.Add(entity, torn); err == nil {
		t.Fatal("Add accepted a truncated body")
	}
	if got := m.Get(entity); got.Current != 50 || got.Max != 100 || got.Regen != 2.5 {
		t.Fatalf("refused body tore the live component via Add: got %+v", got)
	}

	// A well-formed body must still apply, so the scratch is actually committed.
	ok := make([]byte, 12)
	binary.LittleEndian.PutUint32(ok[0:], math.Float32bits(7))
	binary.LittleEndian.PutUint32(ok[4:], math.Float32bits(8))
	binary.LittleEndian.PutUint32(ok[8:], math.Float32bits(9))
	if err := rep.Apply(entity, ok); err != nil {
		t.Fatalf("Apply refused a well-formed body: %v", err)
	}
	if got := m.Get(entity); got.Current != 7 || got.Max != 8 || got.Regen != 9 {
		t.Fatalf("accepted body was not committed: got %+v", got)
	}
}
