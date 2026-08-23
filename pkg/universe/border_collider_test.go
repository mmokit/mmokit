package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/spatial"
)

// TestBorderReplicaCarriesTheWholeCollider pins the defect this unit exists to
// fix, and it is a 2D defect that shipped.
//
// The border header carries the collider's RADIUS and nothing else — no Width,
// Height, Depth, Layer or Shape — and upsertBorderReplica built the replica as
// Collider{Radius: radius}. A zero Layer is invisible to every layer-masked
// query (pkg/spatial/layers.go), and a zero Shape is ShapeSphere. So a
// neighbour-owned wall arrived as a zero-extent, layer-0 sphere: it stopped
// colliding and stopped blocking line of sight at the cell seam, which is
// exactly where a corridor runs.
//
// The fix is that Collider now rides the border component TAIL. This asserts
// the tail actually lands.
func TestBorderReplicaCarriesTheWholeCollider(t *testing.T) {
	stage := newTestStage(t)
	RegisterComponent(stage.ReplicationRegistry(), ecs.NewMap1[component.Collider](stage.ECSWorld()))

	want := component.Collider{
		Radius: 9,
		Width:  40,
		Height: 12,
		Depth:  7,
		Layer:  spatial.LayerStatic,
		Shape:  component.ShapeBox,
	}
	tail := colliderTail(t, stage, want)

	const netID uint32 = 5150
	rot := component.RotationIdentity()
	stage.upsertBorderReplica(netID, 1, 3, 10, 20, 30, want.Radius, 0, 0, 0, rot, "cell_1_0", 100, tail)

	ent, ok := stage.replicaNetIDs[netID]
	if !ok {
		t.Fatal("replica was not created")
	}
	got := *stage.colliderMap.Get(ent)

	if got.Layer != want.Layer {
		t.Errorf("replica Layer = %d, want %d — a layer-0 replica is invisible to every masked query, "+
			"so a neighbour-owned wall blocks nothing", got.Layer, want.Layer)
	}
	if got.Shape != want.Shape {
		t.Errorf("replica Shape = %v, want %v — a zero shape is a sphere, so a wall collides as a ball",
			got.Shape, want.Shape)
	}
	if got.Width != want.Width || got.Height != want.Height || got.Depth != want.Depth {
		t.Errorf("replica extents = (%v, %v, %v), want (%v, %v, %v) — a zero-extent replica has no surface",
			got.Width, got.Height, got.Depth, want.Width, want.Height, want.Depth)
	}
}

// TestBorderReplicaHeaderRadiusWinsOverTheTail pins the ordering.
//
// Radius is per-tick animatable — the WASM pulse demo breathed it — and the
// header carries it every tick while the tail carries whatever the last
// component scan produced. If the tail were applied last, a stale collider
// blob would freeze the size, which is exactly the size-jump-at-the-boundary
// bug the per-tick refresh was added to fix. So the header lands after the
// tail, and this asserts it.
func TestBorderReplicaHeaderRadiusWinsOverTheTail(t *testing.T) {
	stage := newTestStage(t)
	RegisterComponent(stage.ReplicationRegistry(), ecs.NewMap1[component.Collider](stage.ECSWorld()))

	// A tail whose radius disagrees with the header — a stale baseline.
	stale := component.Collider{Radius: 1, Width: 40, Height: 12, Layer: spatial.LayerStatic, Shape: component.ShapeBox}
	tail := colliderTail(t, stage, stale)

	const netID uint32 = 5151
	const headerRadius float32 = 33
	rot := component.RotationIdentity()
	stage.upsertBorderReplica(netID, 1, 3, 10, 20, 30, headerRadius, 0, 0, 0, rot, "cell_1_0", 100, tail)

	ent := stage.replicaNetIDs[netID]
	got := *stage.colliderMap.Get(ent)
	if got.Radius != headerRadius {
		t.Errorf("replica Radius = %v, want the header's %v — a stale tail froze the per-tick size",
			got.Radius, headerRadius)
	}
	// ...while everything the header cannot carry still comes from the tail.
	if got.Width != stale.Width || got.Layer != stale.Layer {
		t.Errorf("tail fields were lost: Width=%v Layer=%v", got.Width, got.Layer)
	}

	// And the same on the UPDATE branch, which is a separate code path.
	stage.upsertBorderReplica(netID, 1, 3, 11, 21, 31, 44, 0, 0, 0, rot, "cell_1_0", 200, tail)
	if got := stage.colliderMap.Get(ent).Radius; got != 44 {
		t.Errorf("updated replica Radius = %v, want the header's 44", got)
	}
}

// colliderTail encodes a Collider through the REAL border sender.
//
// Deliberately not a hand-rolled framing: using scanEntityComponents means
// this round-trips the actual encoder against the actual decoder, so a
// mismatch in either end fails here rather than in production. It also fails
// loudly if Collider is not border-eligible, which is the bug under test.
func colliderTail(t *testing.T, stage *Stage, c component.Collider) []byte {
	t.Helper()
	src := stage.ECSWorld().NewEntity()
	ecs.NewMap1[component.Collider](stage.ECSWorld()).Add(src, &c)

	tail := stage.scanEntityComponents(src, nil)
	if len(tail) < 2 || tail[0] == 0 && tail[1] == 0 {
		t.Fatal("the border component tail is empty — Collider is being skipped on the border path, " +
			"which is the defect this test exists to catch")
	}
	return tail
}
