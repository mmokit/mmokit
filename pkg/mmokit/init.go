package mmokit

import "github.com/zenion/mmoserver/pkg/engine"

// init wires the engine's default envelope parser to mmokit's
// ProtoEnvelopeParser. Engine doesn't import mmokit (would cycle), so
// the linkage runs in the opposite direction at package-init time.
//
// All universe.Process instances built after package init see the
// parser; tests that build engines outside the mmokit stack and never
// import mmokit get nil and the dispatcher is a no-op.
func init() {
	engine.DefaultEnvelopeParser = ProtoEnvelopeParser
}
