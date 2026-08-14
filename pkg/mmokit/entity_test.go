package mmokit_test

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmokit/pkg/component"
	"github.com/zenion/mmokit/pkg/mmokit"
	pkguniverse "github.com/zenion/mmokit/pkg/universe"
)

// mustMarshal is the mmokit_test form of ReflectMarshal for values expected to
// fit the wire; the encoder length guards are asserted in pkg/universe.
func mustMarshal(tb testing.TB, ptr any) []byte {
	tb.Helper()
	data, err := pkguniverse.ReflectMarshal(ptr)
	if err != nil {
		tb.Fatalf("ReflectMarshal(%T): %v", ptr, err)
	}
	return data
}

func TestEntity_ZeroValueIsNotAlive(t *testing.T) {
	var e mmokit.Entity
	if e.Alive() {
		t.Fatal("zero-value Entity should not be Alive")
	}
	if e.NetID() != 0 {
		t.Fatalf("zero-value NetID = %d, want 0", e.NetID())
	}
}

func TestEntityByNetID_ResolvesAlive(t *testing.T) {
	stage, _ := newTestStage(t)
	e := spawnTestEntity(t, stage, 42)
	h := mmokit.EntityByNetID(stage, 42)
	if !h.Alive() {
		t.Fatal("EntityByNetID(42).Alive() = false, want true")
	}
	if h.NetID() != 42 {
		t.Fatalf("NetID = %d, want 42", h.NetID())
	}
	_ = e
}

func TestEntityByNetID_UnknownReturnsDead(t *testing.T) {
	stage, _ := newTestStage(t)
	h := mmokit.EntityByNetID(stage, 999)
	if h.Alive() {
		t.Fatal("unknown netID should not be Alive")
	}
}

func TestEntity_PosReturnsPosition(t *testing.T) {
	stage, _ := newTestStage(t)
	h := spawnTestEntity(t, stage, 42)
	posMap := ecs.NewMap1[component.Position](stage.ECSWorld())
	posMap.Get(h).X = 50
	posMap.Get(h).Y = -25
	e := mmokit.EntityByNetID(stage, 42)
	x, y := e.Pos()
	if x != 50 || y != -25 {
		t.Fatalf("Pos = (%v, %v), want (50, -25)", x, y)
	}
}

func TestEntity_PosOnDeadIsZero(t *testing.T) {
	stage, _ := newTestStage(t)
	e := mmokit.EntityByNetID(stage, 999)
	x, y := e.Pos()
	if x != 0 || y != 0 {
		t.Fatalf("dead Pos = (%v, %v), want (0, 0)", x, y)
	}
}

func TestEntity_LocalReportsAuthority(t *testing.T) {
	stage, _ := newTestStage(t)
	spawnTestEntity(t, stage, 42)
	e := mmokit.EntityByNetID(stage, 42)
	if !e.Local() {
		t.Fatal("local Live entity should report Local()==true")
	}
}

// TestEntity_ReflectRoundtrip exercises the codec registered in entity.go's
// init(): a struct with an mmokit.Entity field round-trips through the
// universe ReflectMarshal / ReflectUnmarshalOnStage path, with the NetID
// resolved against the destination stage's index on decode.
func TestEntity_ReflectRoundtrip(t *testing.T) {
	type DamageMsg struct {
		Amount float32
		Source mmokit.Entity
		Slot   uint8
	}

	stage, _ := newTestStage(t)
	spawnTestEntity(t, stage, 42)
	src := mmokit.EntityByNetID(stage, 42)

	in := DamageMsg{Amount: 25.0, Source: src, Slot: 3}
	body := mustMarshal(t, &in)

	// Wire format: float32 (4) + Entity codec (4) + uint8 (1) = 9 bytes.
	if len(body) != 9 {
		t.Fatalf("body length: got %d, want 9", len(body))
	}

	var out DamageMsg
	if err := pkguniverse.ReflectUnmarshalOnStage(stage, body, &out); err != nil {
		t.Fatalf("ReflectUnmarshalOnStage: %v", err)
	}

	if out.Amount != 25.0 || out.Slot != 3 {
		t.Fatalf("primitives: got amount=%v slot=%v", out.Amount, out.Slot)
	}
	if out.Source.NetID() != 42 {
		t.Fatalf("Source.NetID: got %d, want 42", out.Source.NetID())
	}
	if !out.Source.Alive() {
		t.Fatal("Source should resolve to alive entity on stage")
	}
}
