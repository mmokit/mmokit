package mmokit

import (
	pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

// Wire primitives re-exported from pkg/universe. They are defined there
// because universe is the layer that decodes and routes the bytes; they are
// aliased here so game code keeps importing mmokit alone.

// TypeIDOf returns the stable wire identifier for a registered Go message
// type — fnv32a(reflect.Type.String()). See pkguniverse.TypeIDOf for the
// full contract, including why the ID survives moving a package between
// directories but breaks on renaming the package or the type.
var TypeIDOf = pkguniverse.TypeIDOf

// ExtractAnchors returns deduped NetIDs of every Entity-typed field in
// msgPtr plus the receiver. Anchors drive the AoI filter at broadcast
// drain time.
var ExtractAnchors = pkguniverse.ExtractAnchors

// RouteKind classifies where a typed-op handler runs in the cluster:
// RouteGatewayLocal on the gateway with no cell forwarding,
// RoutePlayerCell on the cell currently owning the player's entity.
type RouteKind = pkguniverse.RouteKind

// Route kinds. Passed as the first argument to RegisterOp.
const (
	RouteGatewayLocal = pkguniverse.RouteGatewayLocal
	RoutePlayerCell   = pkguniverse.RoutePlayerCell
)

// TypedOpEntry is the registry record for one typed-op binding, created by
// RegisterOp[Req, Res] and looked up by request typeID by the dispatcher.
type TypedOpEntry = pkguniverse.TypedOpEntry

// WireRegistry is a process's client-facing message registry: the types
// clients may send, the types the server may send back, and the typed-op
// bindings. Reach one with Process.Wire() or Stage.Wire().
type WireRegistry = pkguniverse.WireRegistry

// WireSource is anything that can name the registry a frame belongs to — a
// *Process or a *Stage. Frame builders take one rather than a *Process so a
// caller holding either can pass what it has.
type WireSource interface {
	Wire() *WireRegistry
}

// Dimension is the spatial profile a process simulates in, set once via
// Config.Dimension. It selects behaviour — which engine bindings replicate,
// and later which systems and validators run — and never selects types.
type Dimension = pkguniverse.Dimension

// Dimension profiles. Dimension3D is selectable and not yet implemented:
// constructing a process with it panics rather than falling back to 2D.
const (
	Dimension2D = pkguniverse.Dimension2D
	Dimension3D = pkguniverse.Dimension3D
)
