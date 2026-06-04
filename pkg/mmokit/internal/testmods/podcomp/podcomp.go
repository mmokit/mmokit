// Package podcomp holds self-contained POD components for wasm test fixtures,
// decoupled from any game's component package.
package podcomp

// Shield is a value-type component used by the shieldregen test module.
type Shield struct {
	Current        float32
	Max            float32
	RegenRate      float32
	RegenDelay     float32
	DamageCooldown float32
}
