package mmokit

import (
	"github.com/zenion/mmoserver/pkg/engine"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// init wires:
//
//  1. The engine's default envelope parser to mmokit's ProtoEnvelopeParser.
//     Engine doesn't import mmokit (would cycle), so the linkage runs in
//     the opposite direction at package-init time.
//  2. The universe-side BroadcastHooks callbacks that let Stage.maybeBroadcast
//     consult the mmokit-owned broadcast registry (eligibility, typeID,
//     anchor extraction) without an import cycle.
//
// All universe.Process instances built after package init see both wirings;
// tests that build engines/stages outside the mmokit stack and never import
// mmokit get nil hooks and the auto-broadcast path is a silent no-op.
func init() {
	engine.DefaultEnvelopeParser = ProtoEnvelopeParser

	pkguniverse.BroadcastHooks.Eligible = brIsRegistered
	pkguniverse.BroadcastHooks.TypeIDOf = TypeIDOf
	pkguniverse.BroadcastHooks.ExtractAnchors = func(msgPtr any, targetNetID uint32, stage *pkguniverse.Stage) []uint32 {
		return ExtractAnchors(msgPtr, EntityByNetID(stage, targetNetID))
	}
}
