package component

// AoESpec describes the damage payload of a telegraphed area-of-effect
// entity (AoEMarker). The marker's Lifetime ticks down each tick; when
// Remaining reaches 0, AoESystem queries the spatial grid in Radius
// around the marker's Position, applies Damage via gw.ApplyDamage to
// each non-owner entity matching FactionMask, and despawns the marker.
//
// FactionMask bits:
//   bit 0 (0x01) — can damage NPCs
//   bit 1 (0x02) — can damage Players
// A FactionMask of 0x03 hits both (e.g. Kamikaze blast hitting a
// neighboring Kamikaze + the player).
type AoESpec struct {
	Radius      float32 `net:"f32"`
	Damage      float32 `net:"f32"`
	OwnerNetID  uint32  `net:"u32"`
	FactionMask uint8   `net:"u8"`
	DamageType  uint8   `net:"u8"` // reserved for resistance system; v2 always 0
}
