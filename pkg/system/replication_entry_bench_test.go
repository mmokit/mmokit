package system

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/quantize"
	"github.com/mmokit/mmokit/pkg/spatial"
)

// BenchmarkReplicationSystem_Update measures the real per-tick replication
// work, over the same harness the allocation tests use.
//
// It was written to decide with numbers, rather than intuition, whether
// spatial.Entry can afford the quaternion and Depth phase 4b needs. The chain
// passes an Entry BY VALUE at roughly forty points per (viewer, visible
// entity, tick): QueryRadius appends one into the result slice,
// ReplicationSystem ranges those results by value, six EntityReplicator
// methods take one, and autoReplicator re-copies it into every binding.
//
// THE ANSWER IS THAT IT COSTS NOTHING MEASURABLE. Four variants, 256 entities,
// median of three:
//
//	by-value, 40 B   36.8 us   (today)
//	pointers, 40 B   36.8 us
//	pointers, 56 B   36.1 us
//	by-value, 56 B   37.0 us
//
// So widening Entry by 16 bytes is free, and migrating the chain to *Entry to
// afford it — which the phase-4b plan priced at 2.5 days — buys nothing. The
// copies are dwarfed by hashing, delta encoding and frame building. See the
// fan-out benchmark below, which checks the same thing where the copies are
// densest and agrees.
//
// Deliberately the whole Update rather than a model of the copy chain: a model
// measures the number of hops I assumed, which is the thing most likely to be
// wrong. The absolute nanoseconds are machine-specific; the ratio is not.
func BenchmarkReplicationSystem_Update(b *testing.B) {
	for _, entities := range []int{1, 64, 256} {
		b.Run(benchName(entities), func(b *testing.B) {
			h := newReplicationAllocationHarness(entities, 0)
			// One step outside the timer so the first-frame keyframe work and
			// the baseline allocations do not land in the measurement.
			h.step()

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				h.step()
			}
		})
	}
}

func benchName(n int) string {
	switch n {
	case 1:
		return "1_entity"
	case 64:
		return "64_entities"
	default:
		return "256_entities"
	}
}

// BenchmarkAutoReplicator_HashSnapshot isolates the binding FAN-OUT, which is
// where the by-value Entry copies actually concentrate.
//
// BenchmarkReplicationSystem_Update above uses a two-field test replicator, so
// on its own it under-measures the fan-out. A real kind is closer to this: six
// top-level bindings, one expanding to seven fields, with autoReplicator
// re-copying the entry into every one on both the hash and the snapshot pass.
//
// Measured at a 56-byte Entry, median of three: 419 ns by value, 426 ns by
// pointer. The pointer version is marginally SLOWER, within noise — so even
// where the copies are densest there is nothing to reclaim.
func BenchmarkAutoReplicator_HashSnapshot(b *testing.B) {
	rep, entry, viewer := benchReplicator()
	var h Hasher
	// SnapshotWriter indexes a pre-sized buffer rather than appending, so the
	// buffer must be the payload's LENGTH — which is what the layout sums to.
	size := 0
	for _, f := range rep.SnapshotLayout() {
		size += f
	}
	buf := make([]byte, size)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		h.Reset()
		rep.Hash(&h, viewer, entry)
		w := quantize.NewSnapshotWriter(buf)
		rep.Snapshot(w, viewer, entry)
	}
}

// benchReplicator mirrors goldenReplicator without needing a *testing.T.
func benchReplicator() (EntityReplicator, spatial.Entry, *ViewerInfo) {
	world := ecs.NewWorld()
	scalars := ecs.NewMap1[goldenScalars](world)
	initial := ecs.NewMap1[goldenInitial](world)
	vel := ecs.NewMap1[component.Velocity](world)
	col := ecs.NewMap1[component.Collider](world)
	rot := ecs.NewMap1[component.Rotation](world)

	e := scalars.NewEntity(&goldenScalars{
		F32: 1.5, U8: 7, U16: 600, U32: 70000, I16: -300, Flag: true, QNorm: 0.5,
	})
	initial.Add(e, &goldenInitial{Label: "gold", Tier: 3})
	vel.Add(e, &component.Velocity{X: 1.25, Y: -2.5})
	col.Add(e, &component.Collider{Radius: 8})
	rotv := component.RotationFromYaw(0.5)
	rot.Add(e, &rotv)

	rep := AutoReplicator(42,
		EntryPosition(), QVelocity(vel, 1000), QSize(col, 500), QAngle(rot),
		Component(scalars), Component(initial),
	)
	return rep, spatial.Entry{Entity: e, X: 10.5, Y: -20.25}, &ViewerInfo{ConnID: 1}
}
