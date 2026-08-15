package game

import "github.com/mmokit/mmokit"

// BeamToggle is dispatched when a mining beam toggles on/off. The payload
// is purely visual — server-side state lives on the MiningLaser component.
// The framework auto-broadcasts to AoI viewers.
//
// Restored via Plan G after Plan F's manual-Enqueue deletion took the
// press-pulse VFX with it (mining beam visual still renders via
// ActiveMining replication; this restores the per-press pulse).
type BeamToggle struct {
	Caster mmokit.Entity
	Beam   uint8
	Active bool
}

// beamToggleHandler is a no-op; the broadcast IS the entire effect.
// Registered for HandleAll so the framework picks up the type for
// auto-broadcast.
func beamToggleHandler(target mmokit.Entity, msg *BeamToggle) {
	// Empty: no server-side state mutation.
}

// RegisterBeamToggleVerb wires beamToggleHandler. Call once at startup
// from GameSetup.
func RegisterBeamToggleVerb(p *mmokit.Process) {
	mmokit.HandleAll(p, beamToggleHandler)
}
