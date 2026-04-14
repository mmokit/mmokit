// Package universe provides generic multi-node server meshing infrastructure
// for 2D authoritative game servers. Includes topology computation, cell
// identification, and inter-node message types.
package universe

// Topology holds the neighbor relationships between cells.
type Topology struct {
	Neighbors map[CellID][]CellID
}

// ComputeTopology builds neighbor relationships for a set of cells.
// Supports mixed-depth cells by using spatial adjacency (world-space bounds).
func ComputeTopology(cells []CellID, baseCellSize float32) Topology {
	neighbors := make(map[CellID][]CellID, len(cells))
	for _, a := range cells {
		var adj []CellID
		for _, b := range cells {
			if AreAdjacent(a, b, baseCellSize) {
				adj = append(adj, b)
			}
		}
		neighbors[a] = adj
	}
	return Topology{Neighbors: neighbors}
}

// AllCells returns all cell IDs in the topology.
func (t *Topology) AllCells() []CellID {
	cells := make([]CellID, 0, len(t.Neighbors))
	for cell := range t.Neighbors {
		cells = append(cells, cell)
	}
	return cells
}

// UpdateAfterSplit updates the topology after a cell splits into 4 children.
// The parent cell is removed and replaced by its 4 children. Neighbors of the
// parent are updated to reference whichever children they are adjacent to.
func (t *Topology) UpdateAfterSplit(parent CellID, children [4]CellID, baseCellSize float32) {
	// Collect the parent's old neighbors
	oldNeighbors := t.Neighbors[parent]
	delete(t.Neighbors, parent)

	// Add children with their neighbor lists
	for _, child := range children {
		var adj []CellID
		// Children are neighbors of each other
		for _, sibling := range children {
			if AreAdjacent(child, sibling, baseCellSize) {
				adj = append(adj, sibling)
			}
		}
		// Check adjacency with parent's old neighbors
		for _, old := range oldNeighbors {
			if AreAdjacent(child, old, baseCellSize) {
				adj = append(adj, old)
			}
		}
		t.Neighbors[child] = adj
	}

	// Update old neighbors: replace parent reference with adjacent children
	for _, old := range oldNeighbors {
		filtered := t.Neighbors[old][:0]
		for _, n := range t.Neighbors[old] {
			if n != parent {
				filtered = append(filtered, n)
			}
		}
		for _, child := range children {
			if AreAdjacent(old, child, baseCellSize) {
				filtered = append(filtered, child)
			}
		}
		t.Neighbors[old] = filtered
	}
}

// UpdateAfterMerge updates the topology after 4 sibling cells merge into a parent.
// The 4 children are removed and replaced by the parent. Neighbors of the children
// (excluding siblings) are updated to reference the parent.
func (t *Topology) UpdateAfterMerge(children [4]CellID, parent CellID, baseCellSize float32) {
	// Collect all unique external neighbors (not siblings)
	childSet := make(map[CellID]bool, 4)
	for _, c := range children {
		childSet[c] = true
	}

	externalNeighbors := make(map[CellID]bool)
	for _, child := range children {
		for _, n := range t.Neighbors[child] {
			if !childSet[n] {
				externalNeighbors[n] = true
			}
		}
		delete(t.Neighbors, child)
	}

	// Add parent with external neighbors
	var adj []CellID
	for n := range externalNeighbors {
		if AreAdjacent(parent, n, baseCellSize) {
			adj = append(adj, n)
		}
	}
	t.Neighbors[parent] = adj

	// Update external neighbors: replace child references with parent
	for n := range externalNeighbors {
		filtered := t.Neighbors[n][:0]
		for _, nb := range t.Neighbors[n] {
			if !childSet[nb] {
				filtered = append(filtered, nb)
			}
		}
		filtered = append(filtered, parent)
		t.Neighbors[n] = filtered
	}
}

// RebuildNeighborsFor incrementally recomputes neighbor entries for the given
// set of cell IDs plus their immediate neighbors. Used by the cell-transfer
// commit path to avoid a full O(N^2) rebuild after every split/merge/migrate.
//
// For each affected cell: its entire Neighbors list is rebuilt by rescanning
// the full Topology.Neighbors key set for adjacency. The "immediate neighbors"
// expansion ensures that cells whose adjacency TO an affected cell has changed
// also get their slots rebuilt — otherwise we'd leave dangling references to
// cells that no longer exist, or miss new entries.
//
// Correctness invariant (enforced by TestRebuildNeighborsFor_MatchesFullRebuild):
// for any topology T and affected set A, RebuildNeighborsFor(A) produces the
// same Neighbors map as ComputeTopology(AllCells, baseCellSize).Neighbors
// AS LONG AS all cells in T are mutually consistent (which the commit path
// guarantees by calling UpdateAfterSplit/UpdateAfterMerge first).
//
// Mixed-depth topologies are handled via the shared AreAdjacent helper.
func (t *Topology) RebuildNeighborsFor(affected []CellID, baseCellSize float32) {
	if len(affected) == 0 || t.Neighbors == nil {
		return
	}

	// Collect the expanded frontier: affected ∪ (neighbors-of-affected).
	// We use a set so duplicates collapse; order doesn't matter for correctness
	// because each cell is rebuilt from scratch against the full key set.
	frontier := make(map[CellID]struct{}, len(affected)*4)
	for _, c := range affected {
		frontier[c] = struct{}{}
		// Include the OLD neighbors so they get recomputed too — after a
		// split/merge they may have lost or gained edges.
		for _, n := range t.Neighbors[c] {
			frontier[n] = struct{}{}
		}
	}

	// Snapshot the full key set once — all present cells are candidates
	// for adjacency checks.
	allKeys := make([]CellID, 0, len(t.Neighbors))
	for k := range t.Neighbors {
		allKeys = append(allKeys, k)
	}

	// Rebuild the frontier cells.
	for cell := range frontier {
		if _, present := t.Neighbors[cell]; !present {
			// Cell was removed (e.g. the parent of a split) — drop any
			// stale entry. UpdateAfterSplit already did this, so this
			// branch is defensive.
			delete(t.Neighbors, cell)
			continue
		}
		adj := make([]CellID, 0, 8)
		for _, other := range allKeys {
			if other == cell {
				continue
			}
			if AreAdjacent(cell, other, baseCellSize) {
				adj = append(adj, other)
			}
		}
		t.Neighbors[cell] = adj
	}
}

// MeshCellID returns a string ID for a cell (used as cell ID).
func MeshCellID(cell CellID) string {
	return cell.NodeID()
}
