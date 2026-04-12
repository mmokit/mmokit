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

// MeshCellID returns a string ID for a cell (used as cell ID).
func MeshCellID(cell CellID) string {
	return cell.NodeID()
}
