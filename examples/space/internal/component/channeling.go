package component

// Channeling tracks an in-flight channeled ability. PVE v3 skillshot
// channels (SustainedBeam) store the cursor aim point in AimX/AimY and
// hitscan along that direction each tick. TargetNetID is kept for
// completeness — no LockOn channel ability exists today, but the field
// makes the structural option available.
type Channeling struct {
	SlotID        uint8   `mmokit:"local"`
	RemainingTime float32 `mmokit:"local"`
	NextTickIn    float32 `mmokit:"local"`
	TargetNetID   uint32  `mmokit:"local"`
	AimX          float32 `mmokit:"local"`
	AimY          float32 `mmokit:"local"`
}
