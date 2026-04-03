package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
)

func TestReplicationRegistry_RegisterAndGet(t *testing.T) {
	reg := NewReplicationRegistry()

	reg.Register(ComponentReplicator{
		ID:   1,
		Scan: func(_ ecs.Entity) []byte { return nil },
	})
	reg.Register(ComponentReplicator{
		ID:   2,
		Scan: func(_ ecs.Entity) []byte { return nil },
	})

	if reg.Len() != 2 {
		t.Fatalf("expected 2 replicators, got %d", reg.Len())
	}
	if reg.Get(1) == nil {
		t.Fatal("expected to find replicator with ID 1")
	}
	if reg.Get(3) != nil {
		t.Fatal("expected nil for unregistered ID 3")
	}
	if len(reg.All()) != 2 {
		t.Fatalf("expected All() to return 2, got %d", len(reg.All()))
	}
}

func TestReplicationRegistry_DuplicatePanics(t *testing.T) {
	reg := NewReplicationRegistry()
	reg.Register(ComponentReplicator{ID: 1})

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate ID")
		}
	}()
	reg.Register(ComponentReplicator{ID: 1})
}

func TestReplicaFrame_MarshalRoundTrip(t *testing.T) {
	original := &ReplicaFrame{
		NetworkID:  42,
		EntityType: 3,
		PosX:       100.5,
		PosY:       200.75,
		CellX:    -1,
		CellY:    2,
		Components: []ComponentSlice{
			{ID: 0, Data: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14}},
			{ID: 1, Data: []byte{0x00, 0x00, 0x80, 0x3F, 0x00, 0x00, 0x00, 0x40}}, // vel (1.0, 2.0)
			{ID: 3, Data: []byte{0x00, 0x00, 0xC8, 0x42, 0x00, 0x00, 0xC8, 0x42}}, // health (100, 100)
		},
	}

	data := MarshalReplicaFrame(original)
	decoded, err := UnmarshalReplicaFrame(data)
	if err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if decoded.NetworkID != 42 {
		t.Fatalf("expected NetworkID 42, got %d", decoded.NetworkID)
	}
	if decoded.EntityType != 3 {
		t.Fatalf("expected EntityType 3, got %d", decoded.EntityType)
	}
	if decoded.PosX != 100.5 || decoded.PosY != 200.75 {
		t.Fatalf("expected pos (100.5, 200.75), got (%.2f, %.2f)", decoded.PosX, decoded.PosY)
	}
	if decoded.CellX != -1 || decoded.CellY != 2 {
		t.Fatalf("expected cell (-1, 2), got (%d, %d)", decoded.CellX, decoded.CellY)
	}
	if len(decoded.Components) != 3 {
		t.Fatalf("expected 3 components, got %d", len(decoded.Components))
	}
	if decoded.Components[0].ID != 0 || len(decoded.Components[0].Data) != 14 {
		t.Fatalf("unexpected collider component: ID=%d len=%d", decoded.Components[0].ID, len(decoded.Components[0].Data))
	}
	if decoded.Components[1].ID != 1 || len(decoded.Components[1].Data) != 8 {
		t.Fatalf("unexpected velocity component: ID=%d len=%d", decoded.Components[1].ID, len(decoded.Components[1].Data))
	}
}

func TestReplicaFrame_EmptyComponents(t *testing.T) {
	original := &ReplicaFrame{
		NetworkID:  1,
		EntityType: 0,
		PosX:       0,
		PosY:       0,
		CellX:    0,
		CellY:    0,
	}

	data := MarshalReplicaFrame(original)
	decoded, err := UnmarshalReplicaFrame(data)
	if err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(decoded.Components) != 0 {
		t.Fatalf("expected 0 components, got %d", len(decoded.Components))
	}
}

func TestReplicaFrame_UnmarshalTruncated(t *testing.T) {
	_, err := UnmarshalReplicaFrame([]byte{1, 2, 3})
	if err == nil {
		t.Fatal("expected error for truncated data")
	}
}

func TestProxySummary_MarshalRoundTrip(t *testing.T) {
	original := &ProxySummary{
		NetworkID:  42,
		EntityType: 3,
		PosX:       100.5,
		PosY:       200.75,
		CellX:      -1,
		CellY:      2,
		Radius:     15.0,
		QVelX:      -1234,
		QVelY:      5678,
	}

	data := MarshalProxySummary(original)
	if len(data) != ProxySummarySize {
		t.Fatalf("expected %d bytes, got %d", ProxySummarySize, len(data))
	}

	decoded, err := UnmarshalProxySummary(data)
	if err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if decoded.NetworkID != 42 {
		t.Fatalf("expected NetworkID 42, got %d", decoded.NetworkID)
	}
	if decoded.EntityType != 3 {
		t.Fatalf("expected EntityType 3, got %d", decoded.EntityType)
	}
	if decoded.PosX != 100.5 || decoded.PosY != 200.75 {
		t.Fatalf("expected pos (100.5, 200.75), got (%.2f, %.2f)", decoded.PosX, decoded.PosY)
	}
	if decoded.CellX != -1 || decoded.CellY != 2 {
		t.Fatalf("expected cell (-1, 2), got (%d, %d)", decoded.CellX, decoded.CellY)
	}
	if decoded.Radius != 15.0 {
		t.Fatalf("expected radius 15.0, got %f", decoded.Radius)
	}
	if decoded.QVelX != -1234 {
		t.Fatalf("expected QVelX -1234, got %d", decoded.QVelX)
	}
	if decoded.QVelY != 5678 {
		t.Fatalf("expected QVelY 5678, got %d", decoded.QVelY)
	}
}

func TestProxySummary_UnmarshalTruncated(t *testing.T) {
	_, err := UnmarshalProxySummary([]byte{1, 2, 3})
	if err == nil {
		t.Fatal("expected error for truncated data")
	}
}

func TestProxySummary_ZeroValues(t *testing.T) {
	original := &ProxySummary{}
	data := MarshalProxySummary(original)
	decoded, err := UnmarshalProxySummary(data)
	if err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if decoded.NetworkID != 0 || decoded.EntityType != 0 || decoded.PosX != 0 ||
		decoded.PosY != 0 || decoded.CellX != 0 || decoded.CellY != 0 ||
		decoded.Radius != 0 || decoded.QVelX != 0 || decoded.QVelY != 0 {
		t.Fatal("zero-value round trip failed")
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
