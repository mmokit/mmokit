package component

// Channeling tracks an in-flight channeled ability (e.g. SustainedBeam).
// Local-only — never serialized to clients nor transferred across cells.
// The client derives "I am channeling slot N" from the player's current
// input state; the server uses this component to gate per-tick damage
// and arc checks until the channel ends.
type Channeling struct {
	SlotID        uint8   `mmokit:"local"`
	RemainingTime float32 `mmokit:"local"` // seconds left in channel window
	NextTickIn    float32 `mmokit:"local"` // seconds until next damage tick
	TargetNetID   uint32  `mmokit:"local"`
}
