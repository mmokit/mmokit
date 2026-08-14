package component

// Selection holds the player's current selection — the entity they
// most recently left-clicked. Mining abilities (MiningBeam, ExtractPulse)
// read EntityNetID to find their target. Cursor-pick damage abilities
// (HomingMissile, PlasmaTorpedo, IonBurn) don't read this — they pick
// at fire time from cursor coords.
//
// 0 means nothing selected. Local-only — clients track their own
// selection optimistically and the server-authoritative value is
// implicit in the SelectTarget input flow.
type Selection struct {
	EntityNetID uint32 `mmokit:"local"`
	// LOSLostAt is the stage-time (elapsed seconds, system-local) at which
	// LOS to the selected entity first went blocked. 0 means LOS is clear
	// (or there is no selection). SelectionLOSSystem latches this on the
	// first occlusion and auto-clears the selection if the block persists
	// for LockLosLossBreakSec.
	LOSLostAt float32 `mmokit:"local"`
}
