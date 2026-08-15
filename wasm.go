package mmokit

import (
	"github.com/mmokit/mmokit/internal/wasmctl"
)

// The wasm system API is implemented in internal/wasmctl, which owns the
// wazero runtime, the per-process module registry and the wasm.* operator
// verbs. It cannot live in this package: the verbs are registered from
// mmokit.New, and the adapter embeds universe.SystemBase, so keeping it here
// would tangle the facade with the guest-module lifecycle.
//
// These are forwarders rather than `var X = wasmctl.X` because Go has generic
// type aliases but not generic function aliases.

// SwappableSystem is implemented by hot-loadable systems that can serialize
// their internal state across an unload/swap. The live-swap protocol snapshots
// the outgoing instance and restores it into the incoming one, so internal
// state (caches, counters, in-flight machines) survives the code swap. Game
// systems may implement it to participate in wasm.swap-style live reloads.
type SwappableSystem = wasmctl.SwappableSystem

// NewWasmSystem returns a SystemDef backed by the wasip1 reactor module at
// modulePath. T is the component column handed to the guest each tick; the
// guest sees a packed []T and may write it back when the module declares
// read-write access.
func NewWasmSystem[T any](modulePath string) SystemDef {
	return wasmctl.NewWasmSystem[T](modulePath)
}

// AddWasmSystem registers the module at path as a hot-swappable system on p,
// deriving the logical name from the filename ("tint.wasm" -> "tint"). Call
// before Build to have it boot into every cell; the wasm.load verb can add it
// to a live cell later.
func AddWasmSystem[T any](p *Process, path string) {
	wasmctl.AddWasmSystem[T](p, path)
}

// AddWasmSystemNamed is AddWasmSystem with an explicit logical name, for when
// the filename is not the name operators should type at the wasm.* verbs.
func AddWasmSystemNamed[T any](p *Process, name, path string) {
	wasmctl.AddWasmSystemNamed[T](p, name, path)
}
