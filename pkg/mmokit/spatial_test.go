package mmokit_test

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/spatial"
)

func TestNearby_ReturnsEntitiesInRadius(t *testing.T) {
	stage, _ := newTestStage(t)

	aH := spawnTestEntity(t, stage, 1)
	bH := spawnTestEntity(t, stage, 2)
	cH := spawnTestEntity(t, stage, 3)

	// Set positions: a@(0,0), b@(5,0), c@(100,100).
	posMap := ecs.NewMap1[component.Position](stage.ECSWorld())
	posMap.Get(aH).X, posMap.Get(aH).Y = 0, 0
	posMap.Get(bH).X, posMap.Get(bH).Y = 5, 0
	posMap.Get(cH).X, posMap.Get(cH).Y = 100, 100

	// Add Collider components and register in the spatial grid.
	colMap := ecs.NewMap1[component.Collider](stage.ECSWorld())
	grid := stage.SpatialGrid()
	for _, h := range []ecs.Entity{aH, bH, cH} {
		colMap.Add(h, &component.Collider{Radius: 1})
		pos := posMap.Get(h)
		grid.Register(spatial.Entry{Entity: h, X: pos.X, Y: pos.Y, Radius: 1})
	}

	aNetID := mmokit.EntityFromECS(stage, aH).NetID()
	bNetID := mmokit.EntityFromECS(stage, bH).NetID()
	cNetID := mmokit.EntityFromECS(stage, cH).NetID()

	found := map[uint32]bool{}
	for e := range mmokit.Nearby(stage, 0, 0, 10) {
		found[e.NetID()] = true
	}
	if !found[aNetID] || !found[bNetID] {
		t.Fatal("Nearby should include both within-radius entities")
	}
	if found[cNetID] {
		t.Fatal("Nearby should exclude out-of-radius entity")
	}
}

type testNearbyTag struct{ V int32 }

func TestNearbyWith_FiltersByComponent(t *testing.T) {
	stage, _ := newTestStage(t)

	aH := spawnTestEntity(t, stage, 1)
	bH := spawnTestEntity(t, stage, 2)

	posMap := ecs.NewMap1[component.Position](stage.ECSWorld())
	posMap.Get(aH).X, posMap.Get(aH).Y = 0, 0
	posMap.Get(bH).X, posMap.Get(bH).Y = 5, 0

	colMap := ecs.NewMap1[component.Collider](stage.ECSWorld())
	grid := stage.SpatialGrid()
	for _, h := range []ecs.Entity{aH, bH} {
		colMap.Add(h, &component.Collider{Radius: 1})
		pos := posMap.Get(h)
		grid.Register(spatial.Entry{Entity: h, X: pos.X, Y: pos.Y, Radius: 1})
	}

	// Only b has the tag component.
	tagMap := ecs.NewMap1[testNearbyTag](stage.ECSWorld())
	tagMap.Add(bH, &testNearbyTag{V: 7})

	aNetID := mmokit.EntityFromECS(stage, aH).NetID()
	bNetID := mmokit.EntityFromECS(stage, bH).NetID()

	found := map[uint32]bool{}
	for e := range mmokit.NearbyWith[testNearbyTag](stage, 0, 0, 10) {
		found[e.NetID()] = true
	}
	if found[aNetID] {
		t.Fatal("NearbyWith should exclude entities without component")
	}
	if !found[bNetID] {
		t.Fatal("NearbyWith should include entities with component")
	}
}
