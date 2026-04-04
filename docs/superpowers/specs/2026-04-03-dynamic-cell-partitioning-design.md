# Dynamic Cell Partitioning — Design Spec

## Context

The mmokit engine currently uses a fixed NxN grid with 1:1 cell-to-node mapping, determined at startup. If one cell experiences a population surge, that node bottlenecks while others idle. Dynamic cell partitioning solves this by allowing cells to split into 4 quadrants (and merge back) at runtime based on load, enabling elastic scaling without changing the spatial grid's base dimensions.

This is a generic engine feature in `pkg/` — it makes no assumptions about the game built on top of mmokit.

## Cell Identity

Replace `CellCoord` with `CellID` throughout the engine:

```go
type CellID struct {
    X, Y  int32  // grid coordinates at this cell's resolution
    Depth uint8  // cell size = BaseCellSize / 2^Depth
}
```

- **Depth 0** is the original grid. A 2x2 grid has cells `{0,0,0}`, `{1,0,0}`, `{0,1,0}`, `{1,1,0}`.
- **Depth 1** doubles the coordinate space. Splitting `{0,0,0}` produces `{0,0,1}`, `{1,0,1}`, `{0,1,1}`, `{1,1,1}`.
- **Depth 2** doubles again. Splitting `{1,0,1}` produces `{2,0,2}`, `{3,0,2}`, `{2,1,2}`, `{3,1,2}`.

**Derived properties:**

- Cell size: `BaseCellSize / 2^Depth`
- World-space origin: `(float32(X) * cellSize, float32(Y) * cellSize)`
- World-space extent: `origin + (cellSize, cellSize)`

**Parent/child math:**

- Split `{X, Y, D}` → `{2X, 2Y, D+1}`, `{2X+1, 2Y, D+1}`, `{2X, 2Y+1, D+1}`, `{2X+1, 2Y+1, D+1}`
- Merge 4 siblings at depth D+1 → `{X/2, Y/2, D}` (all siblings must share the same parent)

**Node ID format:** `node_0_0` (depth 0), `node_2_1_d2` (depth 2). Depth suffix only when > 0.

## Configuration & Opt-in API

Dynamic partitioning is disabled by default. Enabled by setting `DynamicPartitioning` to a non-nil config:

```go
coord := mmokit.NewCoordinator(mmokit.Config{
    CellsX:   2,
    CellsY:   2,
    CellSize: 8192,

    // Enable with all defaults
    DynamicPartitioning: mmokit.DefaultPartitionConfig(),
})
```

Override specific values:

```go
pc := mmokit.DefaultPartitionConfig()
pc.MinCellSize = 1024
pc.SplitThreshold = 0.80
coord := mmokit.NewCoordinator(mmokit.Config{
    DynamicPartitioning: pc,
})
```

### PartitionConfig

```go
type PartitionConfig struct {
    MinCellSize    float32          // smallest allowed cell size (default: BaseCellSize / 4)
    SplitThreshold float64         // load fraction to trigger split (default: 0.75)
    MergeThreshold float64         // load fraction to trigger merge (default: 0.20)
    SplitSustain   time.Duration   // how long split threshold must be exceeded (default: 30s)
    MergeSustain   time.Duration   // how long merge threshold must be sustained (default: 60s)
    Cooldown       time.Duration   // min time between auto split/merge on same cells (default: 60s)
    EvalInterval   time.Duration   // how often the monitor checks loads (default: 5s)
    MetricFunc     func(LoadSnapshot) float64  // custom load signal, nil = tick budget usage
}
```

### DefaultPartitionConfig()

Returns a `*PartitionConfig` with all defaults pre-filled. `MinCellSize` defaults to 0, which `Build()` resolves to `BaseCellSize / 4` (two levels of splitting) once the base cell size is known.

When `DynamicPartitioning` is nil: no monitoring goroutine, no quadtree tracking, `CellID.Depth` is always 0. Zero overhead.

### Custom Metric Function

The default metric is tick budget usage from `LoadSnapshot.CompositeLoad`. Games can override:

```go
// Player-count-based splitting: split when 75+ players in a cell
pc.MetricFunc = func(snap mmokit.LoadSnapshot) float64 {
    return float64(snap.Entities.Players) / 100.0
}
```

## Split Orchestration Flow

Triggered by the monitoring goroutine or manual console command.

### Split: Cell → 4 Sub-cells

**Step 1: Validate**
- Cell exists and is active
- Cell size > MinCellSize
- Not on cooldown (skipped for manual console commands)
- For auto-splits: sustained threshold confirmed

**Step 2: Create 3 new nodes**
- Determine heaviest quadrant by entity position scan (via `ExecOnGameLoop` on the source node)
- Original node keeps the heaviest quadrant — minimizes entity movement
- Call `WorldFactory` for the other 3 quadrants, same as `Build()` does at startup
- Allocate net ID ranges from `NetIDAllocator`
- Start game loops for new nodes

**Step 3: Rewire topology**
- Remove old cell from `NodeOwner`, add 4 sub-cells
- Recompute neighbors for the 4 sub-cells and any adjacent cells that bordered the original
- Update `Node.Neighbors` maps on all affected nodes

**Step 4: Resize original node**
- Update the original node's `CellID`, cell size, and coordinate bounds via `ExecOnGameLoop`
- The node's spatial grid is rebuilt for the new cell size

**Step 5: Transfers happen organically**
- `BoundarySystem` detects entities outside the new `[0, newCellSize)` bounds
- Standard transfer protocol fires: serialize → ghost on source → spawn on destination → arrival confirm
- Player routing updates via `Bridge.OnPlayerTransfer()` as transfers complete
- Replicas/proxies expire naturally via TTL on old neighbors, rebuild on new neighbors

**Step 6: Set cooldown**
- Record timestamp on all 4 sub-cells, block further auto split/merge for `Cooldown` duration

### Merge: 4 Sub-cells → Parent Cell

**Step 1: Validate**
- All 4 siblings exist at the same depth
- All 4 below merge threshold for `MergeSustain` duration
- Not on cooldown (skipped for manual console commands)

**Step 2: Pick survivor**
- Sibling with most entities becomes the survivor
- Survivor expands its bounds to parent cell size

**Step 3: Transfer entities**
- The 3 non-survivor siblings transfer all entities to the survivor via standard transfer protocol

**Step 4: Shut down empty nodes**
- Once fully drained (zero entities), the 3 nodes shut down
- Their net ID ranges are released back to `NetIDAllocator`

**Step 5: Rewire topology**
- Remove 4 sub-cells, add parent cell to `NodeOwner`
- Update neighbors of the merged cell and all adjacent cells

**Step 6: Set cooldown**
- Record timestamp on merged cell

### Client Experience

Transparent — players see no disconnection. The Coordinator's `routeEvents()` routes WebSocket events by `playerNode[connID]`, which updates as transfers complete. An optional `SE_CELL_CHANGED` engine event is emitted so clients can update debug overlays or UI if desired.

## Monitoring Goroutine

Launched by `Coordinator.Start()` when `DynamicPartitioning` is non-nil.

```
every EvalInterval (default 5s):
    for each active cell:
        rawLoad = MetricFunc(NodeLoad(cell))
        smoothedLoad = EWMA(rawLoad)  // ~30s window, using pkg/metrics primitives

        if smoothedLoad > SplitThreshold:
            increment cell's sustained-above counter
            if sustained-above >= SplitSustain AND cell size > MinCellSize AND not on cooldown:
                dispatch split
        else:
            reset sustained-above counter

        if smoothedLoad < MergeThreshold:
            increment cell's sustained-below counter
            if all 4 siblings sustained-below >= MergeSustain AND not on cooldown:
                dispatch merge
        else:
            reset sustained-below counter
```

**EWMA smoothing** on the load signal before threshold comparison prevents transient spikes from triggering premature splits. Uses the EWMA primitives already in `pkg/metrics/`.

**Merge requires all 4 siblings** below threshold for the full sustain duration. If any sibling is still hot, no merge.

**The goroutine only reads metrics and makes decisions.** Split/merge execution is dispatched to run on the appropriate game loop goroutine via `ExecOnGameLoop`. No direct ECS access from the monitor.

**Shuts down cleanly** when `Coordinator.Start()`'s context is cancelled.

## Topology Recomputation

Neighbor detection is spatial, not index-based. Two cells are neighbors if their world-space bounding boxes are adjacent (sharing an edge) or diagonally touching (sharing a corner).

### After a split of cell C

1. Remove C from topology
2. Add 4 sub-cells
3. For each sub-cell: check spatial adjacency against C's old neighbors + the other 3 siblings
4. Update old neighbors: replace C with whichever sub-cells they actually border

### After a merge into parent P

1. Remove 4 siblings from topology
2. Add P
3. P inherits all unique neighbors from the 4 siblings (minus each other)
4. Update those neighbors: replace sibling references with P

### Cross-depth neighbors

A large depth-0 cell can neighbor multiple small depth-1 cells along the same edge. The spatial adjacency check handles this naturally — no special cases needed.

```
┌────┬────┬──────────┐
│ A  │ B  │          │
│d=1 │d=1 │    E     │
├────┼────┤   d=0    │
│ C  │ D  │          │
│d=1 │d=1 │          │
└────┴────┴──────────┘
```

B and D neighbor E. A and C do not (no shared edge or corner with E). All 4 sub-cells neighbor each other internally.

Cost is proportional to affected neighbors (~8-12 cells), not total grid size.

## BoundarySystem Changes

Today `BoundarySystem` checks `[0, CellSize)` using a global constant. Changes:

- Read cell size from the node's `CellID` + `BaseCellSize` instead of the global constant
- **Destination resolution:** When an entity crosses a boundary, determine which neighbor contains the entity's world-space position by checking against neighbor world-space bounds. Small linear scan over ~4-8 neighbors.
- **Position normalization:** Same math as today, but with per-cell sizes. An entity transferring from a 4096-size cell to an 8192-size cell has its local position remapped based on world-space coordinates.

## Net ID Range Allocation

Runtime allocator with recycling, replacing the static `nodeIndex * rangeSize` scheme:

```go
type NetIDAllocator struct {
    next     uint32    // next fresh range base
    size     uint32    // range size per node (10M)
    freeList []uint32  // recycled bases from destroyed nodes
}
```

- `Allocate()` pulls from free list first, then allocates fresh
- `Release(base)` returns a range to the free list when a node is destroyed during merge
- Ranges are only released after the node is fully drained (zero entities), guaranteed by the merge flow
- Initial nodes allocated at `Build()` time consume the first N ranges

## Console Commands

Auto-registered as builtins when `DynamicPartitioning` is non-nil:

| Command | Description |
|---------|-------------|
| `cell list` | Show all active cells: ID, size, depth, node, entity count, load |
| `cell info <cellID>` | Detailed info for a specific cell |
| `cell split <cellID>` | Manually split into 4 sub-cells (bypasses cooldown + thresholds) |
| `cell merge <cellID>` | Manually merge cell + 3 siblings to parent (bypasses cooldown) |
| `cell cooldowns` | Show active cooldowns |
| `cell config` | Show current PartitionConfig values |

Cell ID format in commands: `0_0` (depth 0), `2_1_d2` (depth 2).

Example `cell list` output:

```
CELL        SIZE   DEPTH  NODE          ENTITIES  PLAYERS  LOAD
0_0         8192   0      node_0_0      142       23       0.45
2_0_d1      4096   1      node_2_0_d1   87        15       0.72
2_1_d1      4096   1      node_2_1_d1   34        8        0.31
```

## Files to Modify or Create

### New files in `pkg/`

- `pkg/universe/cell_id.go` — `CellID` type, parent/child math, node ID generation, world-space bounds
- `pkg/universe/partition.go` — `PartitionConfig`, `DefaultPartitionConfig()`, split/merge orchestration methods on Coordinator
- `pkg/universe/partition_monitor.go` — monitoring goroutine, EWMA tracking, sustained threshold logic
- `pkg/universe/partition_console.go` — `cell` command group registration
- `pkg/universe/net_id_alloc.go` — `NetIDAllocator`
- `pkg/universe/partition_test.go` — unit tests for split/merge orchestration, topology rewiring, monitor logic

### Modified files in `pkg/`

- `pkg/universe/coordinator.go` — `Config.DynamicPartitioning` field, `Build()` wires allocator + monitor, `Start()` launches monitor goroutine
- `pkg/universe/topology.go` — Replace `ComputeTopology()` with spatial-adjacency-based computation that handles mixed depths, add incremental update functions
- `pkg/universe/boundary_system.go` — Per-node cell size, destination resolution against neighbor bounds
- `pkg/universe/node.go` — Store `CellID` instead of `CellCoord`, cell-size-aware coordinate bounds
- `pkg/universe/world_base.go` — Accept `CellID`, derive cell size from depth
- `pkg/universe/node_bridge_impl.go` — Use `CellID` for neighbor info
- `pkg/universe/message.go` — No new message types needed; split/merge use existing transfer protocol
- `pkg/coords/coords.go` — Replace `CellCoord` with `CellID` (or re-export from universe), update `SetCellSize` to set base size
- `pkg/mmokit/mmokit.go` — Re-export `PartitionConfig`, `DefaultPartitionConfig`, `CellID`

### Modified files in `internal/` and `examples/`

- All callers of `CellCoord` updated to use `CellID` — grep and replace
- No game-specific logic changes needed; the feature is entirely in `pkg/`

## Verification Plan

### Unit tests

- `CellID` parent/child math: split produces correct children, merge produces correct parent
- `CellID` world-space bounds computation at various depths
- Topology: split a cell, verify neighbor relationships (including cross-depth neighbors)
- Topology: merge cells, verify neighbors collapse correctly
- Monitor: mock load snapshots, verify split/merge triggers respect thresholds, sustain durations, cooldowns
- Monitor: verify EWMA smoothing prevents transient spike triggers
- Monitor: verify merge requires all 4 siblings below threshold
- NetIDAllocator: allocate, release, re-allocate cycle
- BoundarySystem: entity crossing from small cell to large cell and vice versa

### Integration tests

- 2x2 grid, manually split one cell via console, verify entities transfer correctly
- Verify player WebSocket routing continues working through split
- Split then merge, verify entities return to single node
- Recursive split (depth 0 → 1 → 2), verify topology at depth 2
- Load-triggered split with mock metric function
- Long-running split/merge cycle to verify net ID range recycling

### Manual testing

- Run `examples/4node-basic` with `DynamicPartitioning: DefaultPartitionConfig()`
- Use `cell split` / `cell merge` console commands
- Observe entity movement across new cell boundaries
- Verify debug overlays update via `SE_CELL_CHANGED` event
- Stress test with bots to trigger automatic splits
