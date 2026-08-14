package main

import "github.com/mmokit/mmokit/pkg/mmokit"

const (
	TickRate     int     = 20
	CellsX       uint32  = 2
	CellsY       uint32  = 2
	CellSize     float32 = 2000.0
	AoIRadius    float32 = 1000.0
	PlayerRadius float32 = 20.0

	// Entity types
	KindPlayer uint8 = 1
	KindBot    uint8 = 2
)

// DefaultTint is the spawn-time entity color — the same blue the renderer
// used before tint replication existed. The tint wasm module overwrites it
// every tick while loaded; this is what you see when the module is unloaded.
var DefaultTint = mmokit.Tint{R: 85, G: 136, B: 204}
