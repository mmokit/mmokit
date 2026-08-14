package system

import (
	"bytes"
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmokit/pkg/quantize"
	"github.com/zenion/mmokit/pkg/spatial"
)

// fakeItem is a test-only item in a var-tail component.
type fakeItem struct {
	Type uint8
	Val  uint8
}

// fakeVarTailComp is a test-only component with a fixed-capacity variable-length list.
type fakeVarTailComp struct {
	Items [4]fakeItem
	N     uint8
}

func TestVarTailComponent_SnapshotLayout(t *testing.T) {
	w := ecs.NewWorld(1024)
	m := ecs.NewMap1[fakeVarTailComp](w)

	binding := VarTailComponent(m, VarTailAccessor[fakeVarTailComp]{
		Name:     "items",
		ItemSize: 2,
		ItemFields: []BindingSchemaField{
			{Name: "type", Encoding: "u8", Size: 1},
			{Name: "val", Encoding: "u8", Size: 1},
		},
		Count:      func(c *fakeVarTailComp) int { return int(c.N) },
		WriteItems: func(c *fakeVarTailComp, sw *quantize.SnapshotWriter) {},
		HashItems:  func(c *fakeVarTailComp, h *Hasher) {},
	})

	layout := binding.snapshotFields()
	if len(layout) != 1 || layout[0] != -1 {
		t.Fatalf("expected layout [-1], got %v", layout)
	}
}

func TestVarTailComponent_SnapshotBytes(t *testing.T) {
	w := ecs.NewWorld(1024)
	m := ecs.NewMap1[fakeVarTailComp](w)
	e := m.NewEntity(&fakeVarTailComp{
		Items: [4]fakeItem{{Type: 1, Val: 10}, {Type: 2, Val: 20}, {}, {}},
		N:     2,
	})

	binding := VarTailComponent(m, VarTailAccessor[fakeVarTailComp]{
		Name:     "items",
		ItemSize: 2,
		ItemFields: []BindingSchemaField{
			{Name: "type", Encoding: "u8", Size: 1},
			{Name: "val", Encoding: "u8", Size: 1},
		},
		Count: func(c *fakeVarTailComp) int { return int(c.N) },
		WriteItems: func(c *fakeVarTailComp, sw *quantize.SnapshotWriter) {
			for i := uint8(0); i < c.N; i++ {
				sw.Uint8(c.Items[i].Type)
				sw.Uint8(c.Items[i].Val)
			}
		},
		HashItems: func(c *fakeVarTailComp, h *Hasher) {
			for i := uint8(0); i < c.N; i++ {
				h.Uint8(c.Items[i].Type)
				h.Uint8(c.Items[i].Val)
			}
		},
	})

	buf := make([]byte, 64)
	sw := quantize.NewSnapshotWriter(buf)
	binding.snapshot(e, sw, nil, spatial.Entry{})

	// Expected: uint16 BE byte length (4) + [1,10,2,20]
	want := []byte{0, 4, 1, 10, 2, 20}
	if got := sw.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("snapshot bytes: want %v got %v", want, got)
	}
}

func TestVarTailComponent_EmptyTail(t *testing.T) {
	w := ecs.NewWorld(1024)
	m := ecs.NewMap1[fakeVarTailComp](w)
	e := m.NewEntity(&fakeVarTailComp{N: 0})

	binding := VarTailComponent(m, VarTailAccessor[fakeVarTailComp]{
		Name:     "items",
		ItemSize: 2,
		ItemFields: []BindingSchemaField{
			{Name: "type", Encoding: "u8", Size: 1},
			{Name: "val", Encoding: "u8", Size: 1},
		},
		Count:      func(c *fakeVarTailComp) int { return int(c.N) },
		WriteItems: func(c *fakeVarTailComp, sw *quantize.SnapshotWriter) {},
		HashItems:  func(c *fakeVarTailComp, h *Hasher) {},
	})

	buf := make([]byte, 64)
	sw := quantize.NewSnapshotWriter(buf)
	binding.snapshot(e, sw, nil, spatial.Entry{})

	// Empty tail still writes the uint16 length prefix (0).
	want := []byte{0, 0}
	if got := sw.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("empty snapshot: want %v got %v", want, got)
	}
}

func TestVarTailComponent_Schema(t *testing.T) {
	w := ecs.NewWorld(1024)
	m := ecs.NewMap1[fakeVarTailComp](w)

	binding := VarTailComponent(m, VarTailAccessor[fakeVarTailComp]{
		Name:     "items",
		ItemSize: 2,
		ItemFields: []BindingSchemaField{
			{Name: "type", Encoding: "u8", Size: 1},
			{Name: "val", Encoding: "u8", Size: 1},
		},
		Count:      func(c *fakeVarTailComp) int { return int(c.N) },
		WriteItems: func(c *fakeVarTailComp, sw *quantize.SnapshotWriter) {},
		HashItems:  func(c *fakeVarTailComp, h *Hasher) {},
	})

	// The binding itself exposes zero per-tick fields in its BindingSchema
	// (scalar layout only). The var-tail is surfaced separately via VarTailProvider.
	bs := binding.schema()
	if len(bs.Fields) != 0 {
		t.Fatalf("var-tail binding should have 0 scalar fields, got %d", len(bs.Fields))
	}

	provider, ok := binding.(VarTailProvider)
	if !ok {
		t.Fatalf("VarTailComponent must implement VarTailProvider")
	}
	vts := provider.VarTailSchema()
	if vts == nil || vts.Name != "items" || vts.ItemSize != 2 || len(vts.ItemFields) != 2 {
		t.Fatalf("unexpected VarTailSchema: %+v", vts)
	}
}
