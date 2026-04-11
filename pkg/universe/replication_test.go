package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
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
		0x01,                   // layer = 1
		0x02,                   // shape = 2
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
	if c.Shape != 2 {
		t.Fatalf("expected shape 2, got %d", c.Shape)
	}
}
