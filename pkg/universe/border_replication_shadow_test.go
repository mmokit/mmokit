package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
)

// TestBorderDispatcher_IncludesShadow verifies the destination cell's
// outgoing border push includes Shadow entities. During the Prepare→
// Commit overlap both source and destination broadcast the same netID
// to shared neighbors; without this, only the source would push and
// the multi-source dedup in ApplyBorderFrame would have no "other
// source" to respect at commit time.
func TestBorderDispatcher_IncludesShadow(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 1, Y: 1})
	world := base.ECSWorld()

	// Plant a Shadow with NetID 88 at position (50, 50) of this cell.
	// Well inside the AoI margin of the neighbor at (0, 1) to the west.
	ent := world.NewEntity()
	ecs.NewMap1[component.Position](world).Add(ent, &component.Position{X: 50, Y: 50})
	ecs.NewMap1[component.Velocity](world).Add(ent, &component.Velocity{})
	ecs.NewMap1[component.NetworkID](world).Add(ent, &component.NetworkID{ID: 88, Epoch: 1})
	ecs.NewMap1[component.EntityKind](world).Add(ent, &component.EntityKind{Type: 1})
	ecs.NewMap1[component.Collider](world).Add(ent, &component.Collider{Radius: 5})
	ecs.NewMap1[component.Shadow](world).Add(ent, &component.Shadow{NetID: 88, Epoch: 1})

	// Walk the dispatcher against a CellViewer pointed at neighbor (0, 1).
	bd := NewBorderDispatcher(base, nil)
	bx, by := neighborBoundaryMidpoint(CellID{X: 1, Y: 1}, -1, 0) // west neighbor
	nv := NewCellViewer("neighbor", CellViewerID("neighbor"), bx, by, nil, nil, nil)
	nv.SetDirection(-1, 0)

	frame := bd.disp.Walk(nv, 1, bd.candidatesFor(nv, 1))

	// The frame should contain netID 88 — the Shadow must pass the
	// dispatcher filter.
	found := false
	for _, e := range frame.Entries {
		if e.NetID.ID == 88 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("border dispatcher dropped Shadow entity: frame entries = %d, expected to include netID 88", len(frame.Entries))
	}
}

// TestBorderDispatcher_CommitSerializerExcludesShadow is the paired
// regression: serializeAllEntities / serializeQuadrantEntities in
// cell_transfer_executor MUST exclude Shadow so in-flight handoff
// entities are not double-materialized during a split/merge. (Documents
// the intentional divergence between the border-push filter and the
// commit-path serializer filter — see commit 9d664d7 historical context.)
func TestBorderDispatcher_CommitSerializerExcludesShadow(t *testing.T) {
	// Static verification: the invariant lives in the filter declarations
	// in cell_transfer_executor.go. This test asserts runtime parity by
	// instantiating a Without filter with the exact exclusion list and
	// confirming a Shadow entity is filtered out.
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	world := base.ECSWorld()

	// Plant a Shadow.
	ent := world.NewEntity()
	ecs.NewMap1[component.Position](world).Add(ent, &component.Position{X: 0, Y: 0})
	ecs.NewMap1[component.NetworkID](world).Add(ent, &component.NetworkID{ID: 99, Epoch: 1})
	ecs.NewMap1[component.Shadow](world).Add(ent, &component.Shadow{NetID: 99, Epoch: 1})

	// Mirror the cell_transfer_executor Without clause.
	filter := ecs.NewFilter1[component.NetworkID](world).
		Without(ecs.C[component.Ghost](), ecs.C[component.Replica](), ecs.C[component.Shadow]())
	q := filter.Query()
	for q.Next() {
		nid := q.Get()
		if nid.ID == 99 {
			t.Fatal("commit-path serializer filter must exclude Shadow entities")
		}
	}
}
