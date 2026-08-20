package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/component"
)

// TestTransferCore_CarriesZ closes the gap phase 1 left open: the transfer
// frame reserved PosZ and VelZ and its codec round-tripped them, but
// SerializeEntityCore never wrote either and SpawnFromTransferCore never read
// them, so Z was dropped at both ends of every split, merge and migrate.
//
// The failure this guards is silent by construction — a 3D entity crosses a
// cell line and lands at Z=0 with no error anywhere — which is the §7.3 class
// of defect the whole one-component-set decision exists to make impossible.
func TestTransferCore_CarriesZ(t *testing.T) {
	src := newTestStage(t)

	const (
		wantPosZ float32 = 42.5
		wantVelZ float32 = -3.25
	)
	entity := src.Spawn(
		component.Position{X: 10, Y: 20, Z: wantPosZ},
		component.Velocity{X: 1, Y: 2, Z: wantVelZ},
	)

	frame := src.SerializeEntityCore(entity.Handle())
	if frame.PosZ != wantPosZ {
		t.Errorf("SerializeEntityCore left PosZ = %v, want %v", frame.PosZ, wantPosZ)
	}
	if frame.VelZ != wantVelZ {
		t.Errorf("SerializeEntityCore left VelZ = %v, want %v", frame.VelZ, wantVelZ)
	}

	data, err := MarshalTransferFrame(frame)
	if err != nil {
		t.Fatalf("MarshalTransferFrame: %v", err)
	}

	dst := newTestStage(t)
	spawned, got, err := dst.SpawnFromTransferCore(data, PresenceLive)
	if err != nil {
		t.Fatalf("SpawnFromTransferCore: %v", err)
	}
	if got.PosZ != wantPosZ || got.VelZ != wantVelZ {
		t.Errorf("decoded frame PosZ/VelZ = %v/%v, want %v/%v", got.PosZ, got.VelZ, wantPosZ, wantVelZ)
	}

	posMap := ecs.NewMap1[component.Position](dst.ECSWorld())
	velMap := ecs.NewMap1[component.Velocity](dst.ECSWorld())
	pos := posMap.Get(spawned)
	vel := velMap.Get(spawned)
	if pos.Z != wantPosZ {
		t.Errorf("destination entity Position.Z = %v, want %v — Z was dropped crossing the cell", pos.Z, wantPosZ)
	}
	if vel.Z != wantVelZ {
		t.Errorf("destination entity Velocity.Z = %v, want %v", vel.Z, wantVelZ)
	}
}
