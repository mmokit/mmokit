package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/spatial"
)

func TestRemoveReplicaByNetID_UsesLocalCleanupWithoutTombstone(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	grid := spatial.NewHashGrid(100)
	base.SetSpatialGrid(grid)
	base.Engine().BeginRemovalTick()

	const netID uint32 = 42
	base.upsertBorderReplica(netID, 1, 3, 10, 20, 4, 1, 2, 0, "cell_1_0", 1000, nil)
	entity, ok := base.replicaNetIDs[netID]
	if !ok {
		t.Fatal("replica was not created")
	}
	grid.Register(spatial.Entry{Entity: entity, X: 10, Y: 20, Radius: 4})
	base.borderLastSeen["cell_1_0"] = map[uint32]struct{}{netID: {}}

	cleanupCalls := 0
	base.Engine().OnEntityRemoved = func(ecs.Entity) { cleanupCalls++ }
	base.RemoveReplicaByNetID(netID)
	base.RemoveReplicaByNetID(netID)

	if base.ECSWorld().Alive(entity) {
		t.Fatal("replica remains alive")
	}
	if cleanupCalls != 1 {
		t.Fatalf("cleanup calls = %d, want 1", cleanupCalls)
	}
	if grid.IsRegistered(entity) {
		t.Fatal("replica remains registered in spatial grid")
	}
	if _, ok := base.replicaNetIDs[netID]; ok {
		t.Fatal("replica remains in replicaNetIDs")
	}
	if _, _, ok := base.LookupNetID(netID); ok {
		t.Fatal("replica remains in netID index")
	}
	if _, ok := base.borderLastSeen["cell_1_0"][netID]; ok {
		t.Fatal("replica remains in border interest snapshot")
	}
	if got := base.Engine().SampleRemovedNetIDs(); len(got) != 0 {
		t.Fatalf("local replica teardown published tombstones: %v", got)
	}
}

func TestExpireReplicas_DeferredCleanupIsLocalOnly(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	grid := spatial.NewHashGrid(100)
	base.SetSpatialGrid(grid)
	base.Engine().BeginRemovalTick()

	const netID uint32 = 51
	base.upsertBorderReplica(netID, 1, 3, 10, 20, 4, 0, 0, 0, "cell_1_0", 1000, nil)
	entity := base.replicaNetIDs[netID]
	base.replicaMap.Get(entity).TTL = 1
	grid.Register(spatial.Entry{Entity: entity, X: 10, Y: 20, Radius: 4})
	base.borderLastSeen["cell_1_0"] = map[uint32]struct{}{netID: {}}

	base.ExpireReplicas()
	if !base.ECSWorld().Alive(entity) {
		t.Fatal("replica expired before the end-of-tick removal flush")
	}
	base.Engine().FlushRemovals()

	if base.ECSWorld().Alive(entity) {
		t.Fatal("expired replica remains alive after removal flush")
	}
	if grid.IsRegistered(entity) {
		t.Fatal("expired replica remains registered in spatial grid")
	}
	if _, _, ok := base.LookupNetID(netID); ok {
		t.Fatal("expired replica remains in netID index")
	}
	if _, ok := base.borderLastSeen["cell_1_0"][netID]; ok {
		t.Fatal("expired replica remains in border interest snapshot")
	}
	if got := base.Engine().SampleRemovedNetIDs(); len(got) != 0 {
		t.Fatalf("replica expiry published tombstones: %v", got)
	}
}

func TestTickGhosts_DeferredCleanupIsLocalOnly(t *testing.T) {
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	grid := spatial.NewHashGrid(100)
	base.SetSpatialGrid(grid)
	base.Engine().BeginRemovalTick()

	entity := base.Spawn(
		component.Position{X: 10, Y: 20},
		component.Collider{Radius: 4},
	).Handle()
	netID := base.netIDMap.Get(entity).ID
	base.ghostMap.Add(entity, &component.Ghost{})

	base.TickGhosts()
	if !base.ECSWorld().Alive(entity) {
		t.Fatal("ghost expired before the end-of-tick removal flush")
	}
	base.Engine().FlushRemovals()

	if base.ECSWorld().Alive(entity) {
		t.Fatal("expired ghost remains alive after removal flush")
	}
	if grid.IsRegistered(entity) {
		t.Fatal("expired ghost remains registered in spatial grid")
	}
	if _, _, ok := base.LookupNetID(netID); ok {
		t.Fatal("expired ghost remains in netID index")
	}
	if got := base.Engine().SampleRemovedNetIDs(); len(got) != 0 {
		t.Fatalf("ghost expiry published tombstones: %v", got)
	}
}
