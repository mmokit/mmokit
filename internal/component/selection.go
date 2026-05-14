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
}
