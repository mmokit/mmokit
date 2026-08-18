package universe

import "github.com/mmokit/mmokit/pkg/system"

// Dimension is the spatial profile a process simulates in — aliased from
// pkg/system, which owns the declaration because it owns the bindings the
// profile selects. Aliased, never mirrored: a parallel enum plus a cast at the
// package boundary is two sources of truth for one value, which is the defect
// CE-010 part A deleted from the cell geometry.
type Dimension = system.Dimension

// Dimension profiles. Dimension3D is selectable and not yet implemented; see
// system.EngineBindingsFor.
const (
	Dimension2D = system.Dimension2D
	Dimension3D = system.Dimension3D
)
