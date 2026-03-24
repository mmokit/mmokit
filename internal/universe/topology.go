package universe

import pkguniverse "github.com/zenion/mmoserver/pkg/universe"

// Re-export generic topology types from pkg/universe.
type Topology = pkguniverse.Topology

var (
	ComputeTopology = pkguniverse.ComputeTopology
	SectorID        = pkguniverse.SectorID
)
