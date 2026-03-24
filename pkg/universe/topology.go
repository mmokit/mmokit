// Package universe provides generic multi-node server meshing infrastructure
// for 2D authoritative game servers. Includes topology computation, sector
// identification, and inter-node message types.
package universe

import (
	"fmt"

	"github.com/zenion/mmoserver/pkg/coords"
)

// Topology holds the neighbor relationships between sectors.
type Topology struct {
	Neighbors map[coords.SectorCoord][]coords.SectorCoord
}

// ComputeTopology builds 8-connected neighbor relationships for a set of sectors.
func ComputeTopology(sectors []coords.SectorCoord) Topology {
	sectorSet := make(map[coords.SectorCoord]bool, len(sectors))
	for _, s := range sectors {
		sectorSet[s] = true
	}

	neighbors := make(map[coords.SectorCoord][]coords.SectorCoord, len(sectors))
	for _, s := range sectors {
		var adj []coords.SectorCoord
		for dx := int32(-1); dx <= 1; dx++ {
			for dy := int32(-1); dy <= 1; dy++ {
				if dx == 0 && dy == 0 {
					continue
				}
				neighbor := coords.SectorCoord{SX: s.SX + dx, SY: s.SY + dy}
				if sectorSet[neighbor] {
					adj = append(adj, neighbor)
				}
			}
		}
		neighbors[s] = adj
	}
	return Topology{Neighbors: neighbors}
}

// SectorID returns a string ID for a sector coordinate (used as node ID).
func SectorID(s coords.SectorCoord) string {
	return fmt.Sprintf("node_%d_%d", s.SX, s.SY)
}
