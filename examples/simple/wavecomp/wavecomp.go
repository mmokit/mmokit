// Package wavecomp holds POD components shared between the simple example's
// host (main package) and its hot-swappable wasm modules. Keeping them in their
// own importable package lets both the server and a `wasip1` module reference
// the exact same struct layout (the wasm ABI).
package wavecomp

// Hue is a per-entity normalized hue in [0,1). The colorwave wasm module drives
// it; the SineWaveSystem reads it into each broadcast frame; the client renders
// it as hsl(hue*360, …). Durable per-entity color lives here (in the ECS), so a
// hot-swap of the colorwave module is seamless — no snapshot needed.
type Hue struct {
	Value float32
}
