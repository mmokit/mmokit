package component

// ProjectileSpec describes a travel-time projectile fired by a player
// ability (or future NPC). ProjectileSystem moves it each tick via the
// entity's Velocity, performs swept collision against the spatial grid,
// and on first non-owner hit:
//   - applies Damage to the hit target via gw.ApplyDamage
//   - if SplashRadius > 0, spawns an AoEMarker with Lifetime=0 at the
//     impact point (resolves same tick via AoESystem)
//   - despawns the projectile
// On lifetime expiry without a hit, despawns silently.
//
// Homing: if TargetNetID != 0, ProjectileSystem rotates Velocity toward
// the target each tick capped by MaxTurnRate (rad/s). If the target is
// dead or out of range, TargetNetID is zeroed and the projectile
// continues on its current heading (dumb-fire).
//
// Type is purely a visual variant for the client renderer.
type ProjectileSpec struct {
	OwnerNetID   uint32  `net:"u32"`
	TargetNetID  uint32  `net:"u32"`
	Damage       float32 `net:"f32"`
	SplashRadius float32 `net:"f32"`
	SplashDamage float32 `net:"f32"`
	MaxTurnRate  float32 `net:"f32"` // rad/s; 0 = no homing
	Type         uint8   `net:"u8"`  // 0=Plasma, 1=Missile, 2=Mortar

	// PierceCount: remaining pierces before the projectile despawns on
	// hit. 0 = stop at first non-owner hit (default). >0 = pass through;
	// decrement on each hit. PiercedNetIDs prevents re-hitting the same
	// victim along the line.
	PierceCount   uint8     `net:"u8"`
	PiercedNetIDs [4]uint32 // server-only (no net tag) — already-hit victims
}

// Projectile-type visual variants. Must match the corresponding
// constants in web-pixi/src/entities/projectile.ts.
const (
	ProjectileTypePlasma  uint8 = 0
	ProjectileTypeMissile uint8 = 1
	ProjectileTypeMortar  uint8 = 2
	ProjectileTypePulse   uint8 = 3 // fast yellow tracers (PulseLaser, PulseBarrage)
	ProjectileTypeRail    uint8 = 4 // long blue-white slugs (RailShot, PiercingRound)
	ProjectileTypeIon     uint8 = 5 // purple/electric (IonOverload)
)
