# MergeCell Entity Transfer & State Fix

## Problem

When `MergeCell` merges 4 subcells back into a parent cell, three bugs cause entity visibility loss, ghost toggling, and hitching:

1. **Non-survivor entities are destroyed** — the 3 non-survivor nodes are shut down without transferring their entities to the survivor. Entities from those subcells simply vanish.
2. **Survivor's WorldBase is never updated** — `Node.ID` and `Node.Cell` are updated on the coordinator side, but `WorldBase.cell` and `WorldBase.nodeID` remain stale (pointing to the old subcell). This causes `BoundarySystem` to use wrong bounds, triggering false transfers.
3. **Player routing incomplete** — only the survivor's old node ID is remapped in `c.playerNode`. Players on non-survivor nodes are orphaned.

### Symptoms

- Entities from different subcells can't see each other after merge (non-survivor entities gone)
- Entities toggle between local and ghost state forever (BoundarySystem detects out-of-bounds using stale subcell bounds, transfers to "self" under mismatched node IDs)
- Hitching at old subcell boundary lines (stale `LocalBounds()` creates invisible walls)

## Design

Mirror the proven `SplitCell` entity transfer pattern in reverse. The fix modifies only `MergeCell` in `pkg/universe/partition.go`.

### Execution Order

```
1. Validate (existing — no change)
2. Find survivor (existing — no change)
3. Serialize entities from non-survivor nodes (NEW)
   - For each non-survivor node, run a closure on its game loop
   - Serialize all real entities (exclude Ghost, Replica, Proxy)
   - Remap positions: offset by (oldOrigin - survivorOrigin) so they arrive in survivor-local coords
   - Migrate player sessions on source node (Transition → Remove)
   - Collect {data, netID, connID} per entity
4. Update topology under write lock (existing — no change)
   - Additionally: remap ALL non-survivor player connIDs to new survivor node ID
5. Call UpdateCellBounds on survivor's game loop (NEW)
   - Schedule via PendingAdminCmds
   - Passes parent cell and parent cell size
   - WorldBase remaps survivor's own entity positions, updates b.cell, b.nodeID, CellCoord components
   - Wait for completion (channel + timeout)
6. Start delivering transfers to survivor inbox (NEW)
   - Send MsgTransfer for each serialized entity from non-survivors
7. Shut down non-survivor nodes (existing — moved after transfers delivered)
8. Cooldowns + OnTopologyChanged callback (existing — no change)
```

### Position Remapping

Entities always use base-cell-local coordinates. A subcell at depth 1 has `LocalBounds` that are a subregion of the root cell (e.g. `[4096, 8192)` for the right half). When transferring to the survivor:

- The survivor's `UpdateCellBounds` call handles remapping its own entities from subcell-local to parent-local coordinates (this already works — it computes the origin offset and shifts all positions).
- Non-survivor entities need their positions offset before serialization so they arrive in the survivor's NEW coordinate space (parent-local). The offset is: `nonSurvivorOrigin - parentOrigin` in world space.

Since entities use base-cell coordinates and `UpdateCellBounds` will shift the survivor to parent-cell space, the non-survivor entities just need the standard world-space offset applied.

### Player Routing

In the write-lock section, iterate `c.playerNode` and remap any connID pointing to a non-survivor node ID to the new survivor node ID. This ensures WebSocket routing delivers messages to the correct node after merge.

### Error Handling

- If serialization times out on a non-survivor node (5s), log warning and skip that node's entities (same pattern as SplitCell)
- If UpdateCellBounds times out on survivor (5s), log error but continue — topology is already updated, entities will self-correct on next boundary check

### Files Changed

- `pkg/universe/partition.go` — `MergeCell` function only

### Testing

- Existing `pkg/universe/` tests cover split/merge topology
- Manual test: split a cell, move entities into different subcells, merge, verify all entities present and visible in merged cell with no ghost toggling
