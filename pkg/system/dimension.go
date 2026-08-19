package system

import (
	"fmt"

	"github.com/mlange-42/ark/ecs"
)

// Dimension is the spatial profile a process simulates in. It is chosen once,
// at construction, and never changes for the life of the process.
//
// It selects BEHAVIOUR — which engine bindings replicate, and later which
// systems, narrow-phase tests and validators run — and never selects TYPES.
// One component set serves both profiles. The alternative, sibling Position3
// -style types, was rejected for a concrete reason: framework code that
// statically names component.Position (the ecs.NewFilterN sites in
// cell_transfer_executor.go, border_replication.go and viewer_source.go, plus
// five query-bundle sites) would silently match zero entities in a 3D world. A
// 3D cell split would serialize nothing, with no error, and none of the five
// integrity invariants would catch it — four assert map consistency over cell
// identifiers and the fifth asserts netID uniqueness, which zero entities
// satisfies trivially. A single type set makes that failure structurally
// impossible rather than lint-guarded.
//
// Declared here, in pkg/system, because this is the layer that owns the
// bindings the profile selects. pkg/universe aliases it rather than mirroring
// it: a second declaration plus a cast at the boundary is exactly the
// two-sources-of-truth defect CE-010 part A spent five days deleting.
type Dimension uint8

const (
	// Dimension2D replicates position as two f32 world coordinates. The only
	// profile implemented today.
	Dimension2D Dimension = iota

	// Dimension3D is declared, selectable, and not yet implemented — see
	// EngineBindingsFor. It exists as a value so the selection mechanism has
	// something to reject loudly rather than something to silently round down
	// to 2D, which would ship exactly the "valid bytes decoded into the wrong
	// shape" failure a schema fingerprint exists to prevent.
	Dimension3D
)

// String returns a stable, human-readable name. Used by diagnostics and by the
// panic message when an unimplemented profile is selected.
func (d Dimension) String() string {
	switch d {
	case Dimension2D:
		return "2d"
	case Dimension3D:
		return "3d"
	default:
		return "unknown"
	}
}

// EngineBindingSet is the engine-level replication bindings one dimension
// profile contributes to every entity kind — the first and, today, only member
// of the profile. Systems, narrow phase and validators join it as the 3D work
// lands; the point of naming the set now is that the selection happens in one
// place instead of at each future call site.
type EngineBindingSet struct {
	// Dimension names the profile this set implements.
	Dimension Dimension

	// Bindings builds the engine-level ComponentBinding for one ECS world.
	// Signature matches EngineBindings, which is the 2D implementation.
	Bindings func(w *ecs.World, velScale, sizeScale, cellSize float32) ComponentBinding
}

// EngineBindingsFor returns the binding set for d.
//
// Panics for a dimension with no implementation. That is deliberate: this runs
// at process construction, where a panic names the config field to change,
// and the alternative — falling back to 2D — produces a server that encodes
// two coordinates while its operator believes it encodes three. Nothing
// downstream can detect that: TypeIDOf hashes reflect.Type.String(), and one
// component set across profiles means a 2D and a 3D Position hash identically.
func EngineBindingsFor(d Dimension) EngineBindingSet {
	switch d {
	case Dimension2D:
		return EngineBindingSet{Dimension: Dimension2D, Bindings: EngineBindings}
	case Dimension3D:
		panic("system.EngineBindingsFor: the 3d profile is not implemented yet — " +
			"the core types are widened (phase 1) but no 3D binding set emits Z, " +
			"and QAngle still sits outside EngineBindings so orientation is not " +
			"dimension-selected (see docs/roadmap.md §7.5 phase 2)")
	default:
		panic(fmt.Sprintf("system.EngineBindingsFor: unknown dimension %d", uint8(d)))
	}
}
