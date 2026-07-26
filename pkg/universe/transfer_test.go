package universe

import (
	"encoding/binary"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/zenion/mmoserver/pkg/component"
)

// TestTransferFrame_EpochRoundtrip locks down the wire-format guarantee that
// an entity's NetworkID.Epoch survives a Marshal → Unmarshal cycle.
//
// The motivating bug: Epoch was missing from TransferFrame entirely, so
// every cell_transfer (split / merge / migrate) populate-side spawn reset
// the entity's epoch to 0. Neighbors that had previously observed the
// netID at a higher epoch (e.g. via boundary handoffs between sibling
// sub-cells during a split) would then reject every subsequent border
// frame from the merged cell as stale — silently dropping ~half the
// bot replicas after a split+merge cycle.
func TestTransferFrame_EpochRoundtrip(t *testing.T) {
	in := &TransferFrame{
		NetworkID:  42,
		Epoch:      7,
		EntityType: 1,
		PosX:       100, PosY: 200,
		VelX: 1, VelY: 2,
		Rotation: 0.5,
		Collider: component.Collider{Radius: 10, Width: 20, Height: 30, Layer: 1, Shape: 2},
		CellX:    3, CellY: 4,
	}
	data, err := MarshalTransferFrame(in)
	if err != nil {
		t.Fatalf("MarshalTransferFrame: %v", err)
	}
	out, err := UnmarshalTransferFrame(data)
	if err != nil {
		t.Fatalf("UnmarshalTransferFrame: %v", err)
	}
	if out.Epoch != in.Epoch {
		t.Errorf("Epoch round-trip: got %d, want %d", out.Epoch, in.Epoch)
	}
	if out.NetworkID != in.NetworkID {
		t.Errorf("NetworkID round-trip: got %d, want %d", out.NetworkID, in.NetworkID)
	}
}

func TestTransferFrame_StreamGenerationRoundtripAndPeek(t *testing.T) {
	userID := uuid.MustParse("8ebc2a6d-d92e-42a8-a8b9-a047b2b94ba7")
	for _, generation := range []uint32{0, 7, ^uint32(0)} {
		t.Run(fmt.Sprintf("generation_%d", generation), func(t *testing.T) {
			in := &TransferFrame{
				NetworkID:        42,
				Epoch:            9,
				StreamGeneration: generation,
				EntityType:       1,
				ConnID:           77,
				GatewayID:        "gateway-a",
				GatewayConnID:    88,
				Username:         "alice",
				UserID:           userID,
			}
			data, err := MarshalTransferFrame(in)
			if err != nil {
				t.Fatalf("MarshalTransferFrame: %v", err)
			}
			out, err := UnmarshalTransferFrame(data)
			if err != nil {
				t.Fatalf("UnmarshalTransferFrame: %v", err)
			}
			if out.StreamGeneration != generation {
				t.Fatalf("StreamGeneration round-trip = %d, want %d", out.StreamGeneration, generation)
			}
			connID, peekGeneration, gatewayID, gatewayConnID, username, peekUserID := PeekTransferPlayer(data)
			if connID != in.ConnID || peekGeneration != generation || gatewayID != in.GatewayID ||
				gatewayConnID != in.GatewayConnID || username != in.Username || peekUserID != userID {
				t.Fatalf("PeekTransferPlayer = (%d,%d,%q,%d,%q,%s), want (%d,%d,%q,%d,%q,%s)",
					connID, peekGeneration, gatewayID, gatewayConnID, username, peekUserID,
					in.ConnID, generation, in.GatewayID, in.GatewayConnID, in.Username, userID)
			}
		})
	}
}

func TestUnmarshalTransferFrame_RejectsShiftedVariableTail(t *testing.T) {
	data, err := MarshalTransferFrame(&TransferFrame{})
	if err != nil {
		t.Fatalf("MarshalTransferFrame: %v", err)
	}
	const gatewayLengthOffset = 4 + 4 + 4 + 1 + 4
	data[gatewayLengthOffset] = 60
	if _, err := UnmarshalTransferFrame(data); err == nil {
		t.Fatal("UnmarshalTransferFrame accepted a gateway length that shifts the fixed tail past the buffer")
	}
}

func TestUnmarshalTransferFrameRejectsImpossibleComponentCountBeforeAllocation(t *testing.T) {
	data, err := MarshalTransferFrame(&TransferFrame{})
	if err != nil {
		t.Fatalf("MarshalTransferFrame: %v", err)
	}
	binary.LittleEndian.PutUint16(data[len(data)-2:], ^uint16(0))
	if _, err := UnmarshalTransferFrame(data); err == nil || !strings.Contains(err.Error(), "component count") {
		t.Fatalf("UnmarshalTransferFrame error = %v, want impossible component count", err)
	}
}

func TestMarshalTransferFrameRejectsUnrepresentableLengths(t *testing.T) {
	tests := []struct {
		name  string
		frame *TransferFrame
		want  string
	}{
		{
			name:  "gateway id",
			frame: &TransferFrame{GatewayID: string(make([]byte, 256))},
			want:  "gateway id",
		},
		{
			name:  "username",
			frame: &TransferFrame{Username: string(make([]byte, 256))},
			want:  "username",
		},
		{
			name:  "component count",
			frame: &TransferFrame{Components: make([]ComponentSlice, 65536)},
			want:  "component count",
		},
		{
			name:  "component data",
			frame: &TransferFrame{Components: []ComponentSlice{{Data: make([]byte, 65536)}}},
			want:  "component 0 data",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := MarshalTransferFrame(tt.frame); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("MarshalTransferFrame error = %v, want error containing %q", err, tt.want)
			}
		})
	}
}

// TestTransferFrame_DebugFlagsRoundtrip locks down the wire-format
// guarantee that a player session's DebugFlags bitmask survives a
// Marshal → Unmarshal cycle. Without it, per-player debug grants
// would be silently dropped on every cross-cell or cross-host handoff
// — the topology overlay (and any future debug-gated stream) would
// vanish the instant the player crossed a cell boundary, until a DB
// re-fetch on the destination side. Carrying the bitmask in the
// frame avoids that round-trip on the hot path.
func TestTransferFrame_DebugFlagsRoundtrip(t *testing.T) {
	src := &TransferFrame{
		NetworkID:     12345,
		Epoch:         7,
		EntityType:    1,
		ConnID:        99,
		GatewayID:     "gw1",
		GatewayConnID: 100,
		Username:      "alice",
		PosX:          1.5,
		PosY:          2.5,
		VelX:          0.1,
		VelY:          0.2,
		Rotation:      1.57,
		Collider:      component.Collider{Radius: 5},
		CellX:         1,
		CellY:         2,
		DebugFlags:    0b101, // bits 0 and 2 set
	}

	bytes, err := MarshalTransferFrame(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	dst, err := UnmarshalTransferFrame(bytes)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if dst.DebugFlags != src.DebugFlags {
		t.Errorf("DebugFlags roundtrip: got 0b%b, want 0b%b", dst.DebugFlags, src.DebugFlags)
	}
}

// TestTransferFrame_EpochSurvivesSpawn verifies that SpawnFromTransferCore
// applies frame.Epoch to the spawned entity's NetworkID — so border frames
// emitted from the destination cell carry the entity's pre-transfer epoch
// and aren't rejected by neighbors with a higher highestSeenEpoch.
func TestTransferFrame_EpochSurvivesSpawn(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})

	source := base.Spawn(
		component.Position{X: 100, Y: 100},
		component.EntityKind{Type: 1},
		component.Collider{Radius: 5},
	).Handle()
	// Simulate prior boundary crossings by bumping epoch on the live
	// source entity before serialization.
	base.NetworkIDMap().Get(source).Epoch = 9

	blob, err := base.SerializeEntity(source)
	if err != nil {
		t.Fatalf("SerializeEntity: %v", err)
	}

	// Remove source to simulate the source cell handing the entity off
	// (donor-side teardown), then re-spawn from the blob on a fresh
	// destination — same flow as the merge populate path.
	base.eng.ECS.RemoveEntity(source)

	ent, frame, err := base.SpawnFromTransferCore(blob, PresenceLive)
	if err != nil {
		t.Fatalf("SpawnFromTransferCore: %v", err)
	}
	if frame.Epoch != 9 {
		t.Errorf("frame.Epoch = %d, want 9", frame.Epoch)
	}
	got := base.NetworkIDMap().Get(ent).Epoch
	if got != 9 {
		t.Errorf("spawned entity NetworkID.Epoch = %d, want 9 (epoch lost during transfer)", got)
	}
}
