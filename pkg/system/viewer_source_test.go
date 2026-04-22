package system

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/engine"
)

// TestPlayerViewerSource_SkipsShadowEntity verifies the destination
// cell of an in-flight handoff does not emit replication frames for
// the migrating player during warmup. Without this skip, both source
// and destination emit alternating frames and the client oscillates.
func TestPlayerViewerSource_SkipsShadowEntity(t *testing.T) {
	world := ecs.NewWorld()
	players := engine.NewPlayerManager()

	// Spawn an entity with Position and Shadow components.
	posMap := ecs.NewMap1[component.Position](world)
	shadowMap := ecs.NewMap1[component.Shadow](world)
	ent := world.NewEntity()
	posMap.Add(ent, &component.Position{X: 100, Y: 200})
	shadowMap.Add(ent, &component.Shadow{NetID: 42})

	// Register a transfer session and manually promote it to StateActive
	// (simulating processPendingSessions having fired).
	connID := uint32(1)
	players.RegisterTransferSession(connID, "alice")
	sess := players.ByConnID(connID)
	if sess == nil {
		t.Fatal("expected session after RegisterTransferSession")
	}
	sess.Entity = ent
	sess.State = engine.StateActive

	src := NewPlayerViewerSource(world, players, engine.StateActive)

	// ActiveViewers must skip the Shadow entity — the source cell is
	// authoritative during warmup; including the shadow here would cause
	// both cells to emit frames to the client.
	viewers := src.ActiveViewers()
	if len(viewers) != 0 {
		t.Fatalf("Shadow viewer leaked into ActiveViewers: got %d viewers, want 0", len(viewers))
	}

	// Sanity: removing the Shadow component (PromoteShadow at Commit)
	// makes the viewer appear again.
	shadowMap.Remove(ent)
	viewers = src.ActiveViewers()
	if len(viewers) != 1 {
		t.Fatalf("post-promote: got %d viewers, want 1", len(viewers))
	}
	if viewers[0].ConnID != connID {
		t.Fatalf("viewer ConnID = %d, want %d", viewers[0].ConnID, connID)
	}
}
