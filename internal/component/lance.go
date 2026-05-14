package component

// LanceTelegraphSpec describes a line-shaped telegraph drawn by a
// Lancer NPC during its charge windup. The lance covers a rectangle
// from Position pointing along the entity's Rotation, of given Length
// and half-Width. Lifetime ticks the windup duration; the lancer
// despawns the marker itself when transitioning to the charge state.
type LanceTelegraphSpec struct {
	Length     float32 `net:"f32"`
	HalfWidth  float32 `net:"f32"`
	OwnerNetID uint32  `net:"u32"`
}
