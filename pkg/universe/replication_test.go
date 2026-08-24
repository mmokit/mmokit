package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/component"
)

func TestReplicationRegistry_RegisterAndGet(t *testing.T) {
	reg := NewReplicationRegistry()

	reg.Register(ComponentReplicator{
		Scan: func(_ ecs.Entity) []byte { return nil },
	})
	reg.Register(ComponentReplicator{
		Scan: func(_ ecs.Entity) []byte { return nil },
	})

	if reg.Len() != 2 {
		t.Fatalf("expected 2 replicators, got %d", reg.Len())
	}
	if reg.Get(1) == nil {
		t.Fatal("expected to find replicator with auto-assigned ID 1")
	}
	if reg.Get(2) == nil {
		t.Fatal("expected to find replicator with auto-assigned ID 2")
	}
	if reg.Get(3) != nil {
		t.Fatal("expected nil for unregistered ID 3")
	}
	if len(reg.All()) != 2 {
		t.Fatalf("expected All() to return 2, got %d", len(reg.All()))
	}
}

func TestUnmarshalCollider(t *testing.T) {
	c := UnmarshalCollider([]byte{
		0x00, 0x00, 0x80, 0x3F, // radius = 1.0
		0x00, 0x00, 0x00, 0x40, // width = 2.0
		0x00, 0x00, 0x40, 0x40, // height = 3.0
		0x01, // layer = 1
		0x01, // shape = box
	})

	if c.Radius != 1.0 {
		t.Fatalf("expected radius 1.0, got %f", c.Radius)
	}
	if c.Width != 2.0 {
		t.Fatalf("expected width 2.0, got %f", c.Width)
	}
	if c.Height != 3.0 {
		t.Fatalf("expected height 3.0, got %f", c.Height)
	}
	if c.Layer != 1 {
		t.Fatalf("expected layer 1, got %d", c.Layer)
	}
	if c.Shape != component.ShapeBox {
		t.Fatalf("expected ShapeBox, got %v", c.Shape)
	}
}

// A shape byte this build does not implement is clamped to the sphere arm
// rather than carried through.
//
// This test previously asserted the opposite — that shape 2 round-trips — using
// 2 as an arbitrary value rather than a meaningful one. Carrying it through is
// what the validation exists to stop: pkg/spatial's narrow phase dispatches the
// circle and rect pairs and then FALLS THROUGH to the OBB routine, so an
// unimplemented discriminant collides as a degenerate box, while the raycast
// skips it entirely and the entity becomes invisible to line-of-sight. This
// byte comes off the mesh, so the value is peer-supplied.
func TestUnmarshalColliderClampsAnUnknownShape(t *testing.T) {
	c := UnmarshalCollider([]byte{
		0x00, 0x00, 0x80, 0x3F,
		0x00, 0x00, 0x00, 0x40,
		0x00, 0x00, 0x40, 0x40,
		0x01,
		// Past ShapeCount. Deliberately not "the next value after the last
		// shape" written as a literal: this test used 0x02, and phase 4b
		// added ShapeCapsule = 2, so it started asserting that a VALID shape
		// was clamped. 0xFF stays unknown for as long as the enum is a uint8
		// with fewer than 255 members.
		0xFF,
	})
	if c.Shape != component.ShapeSphere {
		t.Fatalf("unknown shape byte became %v, want ShapeSphere", c.Shape)
	}
	if !c.Shape.Valid() {
		t.Fatal("clamped shape is not Valid()")
	}
}
