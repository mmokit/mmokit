package component

// LineTelegraphSpec describes a line-shaped telegraph drawn during a
// telegraphed line attack (Lancer charge windup, Brawler line-cone
// windup, etc.). The telegraph covers a rectangle from Position pointing
// along the entity's Rotation, of given Length and half-Width. Lifetime
// ticks the windup duration; the owner despawns the marker itself when
// transitioning out of the windup state.
type LineTelegraphSpec struct {
	Length     float32 `net:"f32"`
	HalfWidth  float32 `net:"f32"`
	OwnerNetID uint32  `net:"u32"`
}
