// Package universe provides generic multi-node server meshing infrastructure
// for 2D authoritative game servers. Includes topology computation, cell
// identification, and inter-node message types.
package universe

import (
	"fmt"

	"github.com/zenion/mmoserver/pkg/coords"
)

// Topology holds the neighbor relationships between cells.
type Topology struct {
	Neighbors map[coords.CellCoord][]coords.CellCoord
}

// ComputeTopology builds 8-connected neighbor relationships for a set of cells.
func ComputeTopology(cells []coords.CellCoord) Topology {
	cellSet := make(map[coords.CellCoord]bool, len(cells))
	for _, s := range cells {
		cellSet[s] = true
	}

	neighbors := make(map[coords.CellCoord][]coords.CellCoord, len(cells))
	for _, s := range cells {
		var adj []coords.CellCoord
		for dx := int32(-1); dx <= 1; dx++ {
			for dy := int32(-1); dy <= 1; dy++ {
				if dx == 0 && dy == 0 {
					continue
				}
				neighbor := coords.CellCoord{CellX: s.CellX + dx, CellY: s.CellY + dy}
				if cellSet[neighbor] {
					adj = append(adj, neighbor)
				}
			}
		}
		neighbors[s] = adj
	}
	return Topology{Neighbors: neighbors}
}

// MeshNodeID returns a string ID for a cell coordinate (used as node ID).
func MeshNodeID(s coords.CellCoord) string {
	return fmt.Sprintf("node_%d_%d", s.CellX, s.CellY)
}
