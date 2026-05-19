package game

import (
	"math"
	"math/rand/v2"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/pathfinding"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// jitterCooldown returns `base * (1 + r)` where r is uniform in
// [-jitter, +jitter]. Used by NPC archetypes to desync ability cadence
// across enemies engaging the same player — without it, every Lancer in a
// pack winds up and charges in unison, and every Artillery casts on the
// same beat. jitter is clamped to [0, 0.9] so a misconfigured value can
// never produce a non-positive cooldown.
func jitterCooldown(base, jitter float32) float32 {
	if base <= 0 {
		return base
	}
	if jitter < 0 {
		jitter = 0
	}
	if jitter > 0.9 {
		jitter = 0.9
	}
	r := (rand.Float32()*2 - 1) * jitter
	return base * (1 + r)
}

// NPCAISystem drives the per-NPC state machine each tick. Acquires
// targets via NPCAI.TargetNetID, applies
// motion policy in Engage, manages leash return, and fires weapons.
//
// Time is tracked locally via elapsedSec (monotonic dt accumulation) —
// only the deltas matter for deescalation, and the Stage/Process layer
// doesn't expose a game-time accessor. ClusterClock.Now() exists but is
// uint64 ms and serves a different purpose (cross-host stamping).
type NPCAISystem struct {
	mmokit.SystemBase
	gw         *GameWorld
	elapsedSec float32
	nearby     []mmokit.SpatialEntry // reusable scratch buffer for resolveBrawlerSpecial
	entities   mmokit.Query[struct {
		AI  *gamecomp.NPCAI
		Pos *mmokit.Position
		Vel *mmokit.Velocity
		Rot *mmokit.Rotation
	}]
}

func (s *NPCAISystem) Init() {
	s.gw = mmokit.State[GameWorld](s.Stage())
	// Prime components hit via mmokit.Has / Get inside Update's query
	// loop. Leashing is added dynamically by system_poi (not in any
	// entity bundle), so without priming the first Has[Leashing] check
	// would try to register the component while the world is locked
	// by Query.Iter and panic.
	mmokit.Prime[gamecomp.Leashing](s.Stage())
	mmokit.Prime[mmokit.Dormant](s.Stage())
}

func (s *NPCAISystem) Update(dt float32) {
	gw := s.gw
	s.elapsedSec += dt
	now := s.elapsedSec

	for e, b := range s.entities.Iter {
		ai, pos, vel, rot := b.AI, b.Pos, b.Vel, b.Rot
		self := mmokit.EntityFromECS(gw.stage, e)

		// Leashing entities are driven below; skip state-driven logic here.
		if mmokit.Has[gamecomp.Leashing](self) {
			s.tickLeash(self, ai, pos, vel, rot, dt)
			continue
		}

		// Support uses its own minimal anchor/kite/heal loop — no state
		// machine. Dispatched before the AIState switch so we skip the
		// Idle→Approach→Acquire→Engage flow entirely.
		if ai.Archetype == ArchetypeSupport {
			s.tickSupport(self, ai, pos, vel, dt)
			continue
		}

		switch ai.State {
		case AIStateIdle:
			s.tickIdle(self, ai, pos, vel, now, dt)
		case AIStateApproach:
			s.tickApproach(self, ai, pos, vel, rot, now, dt)
		case AIStateAcquire:
			s.tickAcquire(self, ai, pos, rot, now, dt)
		case AIStateEngage:
			s.tickEngage(self, ai, pos, vel, rot, now, dt)
		case AIStateCast:
			s.tickCast(self, ai, pos, vel, rot, now, dt)
		case AIStateLanceWindup:
			s.tickLanceWindup(self, ai, vel, rot, dt)
		case AIStateLanceCharge:
			s.tickLanceCharge(self, ai, pos, vel, rot, dt)
		case AIStateLanceRecover:
			s.tickLanceRecover(self, ai, vel, dt)
		}
	}
}

func (s *NPCAISystem) tickIdle(self mmokit.Entity, ai *gamecomp.NPCAI,
	pos *mmokit.Position, vel *mmokit.Velocity,
	now, dt float32,
) {
	vel.X, vel.Y = 0, 0

	target := s.findNearestEnemy(self, pos, ai.AggroRadius)
	if target == (mmokit.EntityHandle{}) {
		return
	}

	// If LockRange is unset (0) or equals AggroRadius, lock immediately
	// (legacy Brawler behavior). Otherwise transition to Approach — move
	// toward target until within LockRange, then start the lock.
	if ai.LockRange <= 0 || ai.LockRange >= ai.AggroRadius {
		s.startLockOn(self, ai, target, now)
		return
	}

	// Approach: don't lock yet. Move toward target.
	ai.State = AIStateApproach
	ai.LastCombatActivityAt = now
	tEnt := mmokit.EntityFromECS(s.gw.stage, target)
	s.gw.eng.Log.Log(CatNPCAI, "ai: %d Idle→Approach (target %d seen, will lock within %.0f)",
		self.NetID(), tEnt.NetID(), ai.LockRange)
}

// startLockOn captures the Idle→Acquire transition body — sets the NPC's
// engage target and flips state. Used by both direct-lock (Idle) and by
// Approach when the target gets close enough.
func (s *NPCAISystem) startLockOn(self mmokit.Entity, ai *gamecomp.NPCAI,
	target mmokit.EntityHandle, now float32,
) {
	tEnt := mmokit.EntityFromECS(s.gw.stage, target)
	tNetID := tEnt.NetID()
	ai.TargetNetID = tNetID
	ai.State = AIStateAcquire
	ai.LastCombatActivityAt = now
	s.gw.eng.Log.Log(CatNPCAI, "ai: %d → Acquire target=%d", self.NetID(), tNetID)
}

// tickApproach: NPC has seen a target but is not yet close enough to lock.
// Moves toward the target at full speed and rotates to face. Transitions
// to Acquire when within LockRange; falls back to Idle if the target is
// lost (moved out of aggro or died).
func (s *NPCAISystem) tickApproach(self mmokit.Entity, ai *gamecomp.NPCAI,
	pos *mmokit.Position, vel *mmokit.Velocity, rot *mmokit.Rotation,
	now, dt float32,
) {
	// Re-scan for nearest enemy (target might have moved or died).
	target := s.findNearestEnemy(self, pos, ai.AggroRadius)
	if target == (mmokit.EntityHandle{}) {
		// Lost target — back to Idle.
		ai.State = AIStateIdle
		vel.X, vel.Y = 0, 0
		return
	}

	tEnt := mmokit.EntityFromECS(s.gw.stage, target)
	tpos := mmokit.Get[mmokit.Position](tEnt)
	if tpos == nil {
		ai.State = AIStateIdle
		return
	}

	dx, dy := tpos.X-pos.X, tpos.Y-pos.Y
	dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))

	// Close enough → start the lock.
	if dist <= ai.LockRange {
		s.startLockOn(self, ai, target, now)
		return
	}

	// Move toward target at full speed; also rotate to face.
	if dist > 1e-3 {
		ux, uy := dx/dist, dy/dist
		vel.X = ux * ai.MaxSpeed
		vel.Y = uy * ai.MaxSpeed
		desired := float32(math.Atan2(float64(uy), float64(ux)))
		turnTowards(rot, desired, ai.TurnRate, dt)
	}
}

func (s *NPCAISystem) tickAcquire(self mmokit.Entity, ai *gamecomp.NPCAI,
	pos *mmokit.Position, rot *mmokit.Rotation,
	now, dt float32,
) {
	if ai.TargetNetID == 0 {
		ai.State = AIStateIdle
		s.gw.eng.Log.Log(CatNPCAI, "ai: %d Acquire→Idle (no target)", self.NetID())
		return
	}
	target := mmokit.EntityByNetID(s.gw.stage, ai.TargetNetID)
	if !target.Alive() {
		ai.State = AIStateIdle
		return
	}
	tpos := mmokit.Get[mmokit.Position](target)
	if tpos == nil {
		ai.State = AIStateIdle
		return
	}
	desired := float32(math.Atan2(float64(tpos.Y-pos.Y), float64(tpos.X-pos.X)))
	if turnTowards(rot, desired, ai.TurnRate, dt) {
		ai.State = AIStateEngage
		s.gw.eng.Log.Log(CatNPCAI, "ai: %d Acquire→Engage", self.NetID())
	}
}

func (s *NPCAISystem) tickEngage(self mmokit.Entity, ai *gamecomp.NPCAI,
	pos *mmokit.Position, vel *mmokit.Velocity, rot *mmokit.Rotation,
	now, dt float32,
) {
	if ai.TargetNetID == 0 {
		ai.State = AIStateIdle
		vel.X, vel.Y = 0, 0
		return
	}
	target := mmokit.EntityByNetID(s.gw.stage, ai.TargetNetID)
	if !target.Alive() || mmokit.Has[mmokit.Dormant](target) {
		ai.State = AIStateIdle
		vel.X, vel.Y = 0, 0
		return
	}
	if now-ai.LastCombatActivityAt > s.gw.Config.AggroDeescalationSec {
		ai.State = AIStateIdle
		ai.TargetNetID = 0
		vel.X, vel.Y = 0, 0
		s.gw.eng.Log.Log(CatNPCAI, "ai: %d Engage→Idle (deescalation)", self.NetID())
		return
	}
	tpos := mmokit.Get[mmokit.Position](target)
	if tpos == nil {
		ai.State = AIStateIdle
		return
	}

	// Periodic LOS recheck: if LOS to the target stays blocked for
	// AILosLossDropSec, drop the target and return to Idle. Cheap because
	// the raycast only fires every AILosRecheckIntervalSec — between
	// rechecks LOSLostAt holds the latched timestamp of first loss.
	if now-ai.LastLOSCheckAt >= s.gw.Config.AILosRecheckIntervalSec {
		ai.LastLOSCheckAt = now
		if hasLOSOnGrid(s.gw.Spatial,
			spatial.Vec2{X: pos.X, Y: pos.Y},
			spatial.Vec2{X: tpos.X, Y: tpos.Y},
			self.Handle()) {
			ai.LOSLostAt = 0
		} else if ai.LOSLostAt == 0 {
			ai.LOSLostAt = now
		} else if now-ai.LOSLostAt >= s.gw.Config.AILosLossDropSec {
			ai.State = AIStateIdle
			ai.TargetNetID = 0
			ai.LOSLostAt = 0
			vel.X, vel.Y = 0, 0
			s.gw.eng.Log.Log(CatNPCAI, "ai: %d Engage→Idle (LOS lost)", self.NetID())
			return
		}
	}

	// Target switching: if a damage source is closer than current target,
	// drop the lock and re-acquire on the attacker next tick. Stamped by
	// ApplyDamage in verb_damage.go; consumed (and cleared) here.
	if ai.LastDamageByNetID != 0 && ai.LastDamageByNetID != ai.TargetNetID {
		attacker := mmokit.EntityByNetID(s.gw.stage, ai.LastDamageByNetID)
		if attacker.Alive() && !mmokit.Has[mmokit.Dormant](attacker) && !mmokit.Has[gamecomp.Leashing](attacker) {
			if apos := mmokit.Get[mmokit.Position](attacker); apos != nil {
				adx, ady := apos.X-pos.X, apos.Y-pos.Y
				adist2 := adx*adx + ady*ady
				cdx, cdy := tpos.X-pos.X, tpos.Y-pos.Y
				cdist2 := cdx*cdx + cdy*cdy
				if adist2 < cdist2 {
					attackerNetID := ai.LastDamageByNetID
					ai.TargetNetID = attackerNetID
					ai.LastDamageByNetID = 0 // consume
					ai.State = AIStateAcquire
					ai.LastCombatActivityAt = now
					s.gw.eng.Log.Log(CatNPCAI, "ai: %d target-switch to %d (closer attacker)",
						self.NetID(), attackerNetID)
					return
				}
			}
		}
		ai.LastDamageByNetID = 0 // consumed (no switch)
	}

	dx, dy := tpos.X-pos.X, tpos.Y-pos.Y
	dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))

	desired := float32(math.Atan2(float64(dy), float64(dx)))
	turnTowards(rot, desired, ai.TurnRate, dt)

	switch ai.MotionPolicy {
	case MotionPathfind:
		s.tickPathfindMotion(self, ai, pos, vel, tpos)
	default:
		s.applyMotion(ai, vel, pos, tpos, dist, dx, dy)
	}

	// Artillery: paint AoE marker on target position when cooldown elapses,
	// then transition to Cast (stationary while casting). Defers spawn so
	// the new entity is created after the entities.Iter query closes.
	if ai.Archetype == ArchetypeArtillery || ai.Archetype == ArchetypeEliteArtillery {
		if ai.CastCooldown > 0 {
			ai.CastCooldown -= dt
		}
		if ai.CastCooldown <= 0 && ai.TargetNetID != 0 {
			cx, cy := tpos.X, tpos.Y
			castTime := s.gw.Config.ArtilleryCastTime
			aoeRadius := s.gw.Config.ArtilleryAoERadius
			aoeDamage := s.gw.Config.ArtilleryAoEDamage
			ownerNetID := self.NetID()
			selfNetID := self.NetID()
			aiRef := ai
			gw := s.gw
			abilityType := npcAbilityTypeFor(ai.Archetype)
			s.Commands().Defer(func() {
				marker := gw.SpawnAoEMarker(cx, cy, castTime, aoeRadius, aoeDamage, ownerNetID, FactionMaskPlayer, abilityType)
				aiRef.CastingMarkerNetID = marker.NetID()
				aiRef.CastTimeRemaining = castTime
				aiRef.CastDamageAccum = 0
				aiRef.State = AIStateCast
				gw.eng.Log.Log(CatNPCAI, "ai: %d Artillery cast START → (%.0f,%.0f) r=%.0f",
					selfNetID, cx, cy, aoeRadius)
			})
			vel.X, vel.Y = 0, 0
			return
		}
	}

	// Lancer windup trigger: when in lance range and off cooldown, snapshot
	// the player's current direction relative to us — that's where we'll
	// commit the charge after the windup. The player can dodge by moving
	// perpendicular during the 1s telegraph; sticking to the predicted
	// line gets you hit. Spawning the telegraph entity is deferred so the
	// new entity isn't created during the entities.Iter query.
	if (ai.Archetype == ArchetypeLancer || ai.Archetype == ArchetypeEliteLancer) && dist <= s.gw.Config.LancerLanceRange && ai.RecoverRemaining <= 0 {
		chargeDir := float32(math.Atan2(float64(dy), float64(dx)))
		chargeLen := s.gw.Config.LancerChargeSpeed * s.gw.Config.LancerChargeTime
		px2, py2 := pos.X, pos.Y
		halfW := s.gw.Config.LancerChargeWidth
		windup := s.gw.Config.LancerWindupTime
		ownerNetID := self.NetID()
		gw := s.gw
		aiRef := ai
		s.Commands().Defer(func() {
			marker := gw.SpawnLineTelegraph(px2, py2, chargeDir, chargeLen, halfW, windup, ownerNetID)
			aiRef.LineTelegraphNetID = marker.NetID()
		})

		ai.WindupRemaining = s.gw.Config.LancerWindupTime
		ai.ChargeDirAngle = chargeDir
		ai.ChargeHit = false
		ai.State = AIStateLanceWindup
		vel.X, vel.Y = 0, 0
		s.gw.eng.Log.Log(CatNPCAI, "ai: %d Lancer WINDUP toward %.2f rad (dist=%.0f)",
			self.NetID(), chargeDir, dist)
		return
	}

	// Brawler special: telegraphed line-cone attack. Runs alongside the
	// existing hitscan auto-attack (below). On cooldown the special triggers
	// first, halts the brawler for a visible windup, then resolves a
	// rectangle hit-check against any player inside the telegraph footprint.
	if ai.Archetype == ArchetypeBrawler || ai.Archetype == ArchetypeEliteBrawler {
		if ai.SpecialCooldown > 0 {
			ai.SpecialCooldown -= dt
		}
		if ai.SpecialWindup > 0 {
			ai.SpecialWindup -= dt
			if ai.SpecialWindup <= 0 {
				// Resolve the special: hitscan along SpecialDirAngle for
				// BrawlerSpecialLength × (2 × BrawlerSpecialHalfWidth).
				s.resolveBrawlerSpecial(self, ai, pos)
				ai.SpecialCooldown = jitterCooldown(s.gw.Config.BrawlerSpecialCooldown, s.gw.Config.NPCAttackJitter)
			}
			// While windup is active, Brawler stays still (visible commit).
			vel.X, vel.Y = 0, 0
			return
		}
		if ai.SpecialCooldown <= 0 && ai.TargetNetID != 0 && dist <= s.gw.Config.BrawlerSpecialLength {
			// Snapshot direction toward target's CURRENT position.
			chargeDir := float32(math.Atan2(float64(dy), float64(dx)))
			chargeLen := s.gw.Config.BrawlerSpecialLength
			halfW := s.gw.Config.BrawlerSpecialHalfWidth
			windup := s.gw.Config.BrawlerSpecialWindupTime
			px, py := pos.X, pos.Y
			ownerNetID := self.NetID()
			gw := s.gw
			aiRef := ai
			s.Commands().Defer(func() {
				marker := gw.SpawnLineTelegraph(px, py, chargeDir, chargeLen, halfW, windup, ownerNetID)
				aiRef.SpecialTelegraphNetID = marker.NetID()
			})
			ai.SpecialWindup = windup
			ai.SpecialDirAngle = chargeDir
			vel.X, vel.Y = 0, 0
			s.gw.eng.Log.Log(CatNPCAI, "ai: %d Brawler SPECIAL windup toward %.2f rad", self.NetID(), chargeDir)
			return
		}
	}

	// BossGuardian: check HP thresholds and spawn escort waves once per
	// crossing. Runs while engaged (lockstep with the boss's normal
	// firing cadence) — the boss is stationary so position is stable.
	if ai.Archetype == ArchetypeBossGuardian {
		s.maybeSpawnBossAdds(self, ai)
	}

	// Disruptor: applies Slow + Silence to the locked target on cooldown.
	// v1 is instant-apply when SpecialCooldown elapses and the target is
	// inside DisruptorAttackRange — the spec envisions wrapping this in a
	// telegraphed skillshot projectile (entity_line_telegraph), but the
	// simpler instant form ships first and meets the design intent.
	if ai.Archetype == ArchetypeDisruptor {
		if ai.SpecialCooldown > 0 {
			ai.SpecialCooldown -= dt
		}
		if ai.SpecialCooldown <= 0 && dist <= s.gw.Config.DisruptorAttackRange {
			s.gw.ApplyStatus(self, target, gamecomp.StatusSlow,
				s.gw.Config.DisruptorSlowDuration, s.gw.Config.DisruptorSlowFactor, 0, 0)
			s.gw.ApplyStatus(self, target, gamecomp.StatusSilence,
				s.gw.Config.DisruptorSilenceDuration, 1.0, 0, 0)
			ai.SpecialCooldown = jitterCooldown(s.gw.Config.DisruptorDebuffCooldown, s.gw.Config.NPCAttackJitter)
			ai.LastCombatActivityAt = now
			s.gw.eng.Log.Log(CatCombatAbility, "disruptor: debuff source=%d target=%d", self.NetID(), target.NetID())
		}
	}

	if dist <= ai.WeaponRange && angleDelta(rot.Angle, desired) < 0.26 /* ~15° */ {
		ai.FireCooldown -= dt
		if ai.FireCooldown <= 0 && ai.FireRate > 0 {
			ai.FireCooldown = 1.0 / ai.FireRate
			// Route through target.Send(&Damage{...}) so the framework
			// auto-broadcasts the hit to AoI viewers with target + source
			// as anchors. The client's typed-event handler (network.ts)
			// feeds it into ability-effects.ts, which renders the beam +
			// impact VFX keyed off AbilityType. Without this, NPC shots
			// were silently applying damage with no visual cue.
			msg := Damage{
				Amount:      ai.DamagePerShot,
				Slot:        0,
				AbilityType: npcAbilityTypeFor(ai.Archetype),
				Source:      self,
			}
			target.Send(&msg)
			if msg.Dealt > 0 {
				ai.LastCombatActivityAt = now
			}
		}
	}
}

// maybeSpawnBossAdds fires escort-add waves when the BossGuardian's HP
// fraction drops below each Config.BossMainAddSpawnThresholds entry.
// Each threshold fires exactly once per life (tracked in
// ai.AddSpawnThresholdsHit bitmask). Adds are Lancers spawned near the
// boss, anchored to the same dungeon + chamber so the chamber
// lifecycle correctly waits for them before flipping Cleared.
func (s *NPCAISystem) maybeSpawnBossAdds(self mmokit.Entity, ai *gamecomp.NPCAI) {
	gw := s.gw
	health := mmokit.Get[gamecomp.Health](self)
	if health == nil || health.Max <= 0 {
		return
	}
	frac := health.Current / health.Max
	thresholds := gw.Config.BossMainAddSpawnThresholds
	pos := mmokit.Get[mmokit.Position](self)
	if pos == nil {
		return
	}
	anchor := mmokit.Get[gamecomp.DungeonAnchor](self)
	// anchor may be nil for console-spawned test bosses (poiNetID=0);
	// they still spawn adds but won't be tracked in a chamber roster.

	for i, thresh := range thresholds {
		if i >= 8 {
			break // bitmask is uint8 — refuse to silently overflow
		}
		bit := uint8(1) << uint(i)
		if ai.AddSpawnThresholdsHit&bit != 0 {
			continue
		}
		if frac >= thresh {
			continue
		}
		ai.AddSpawnThresholdsHit |= bit

		// Snapshot the boss's data we need inside Defer — the surrounding
		// Iter holds a query lock, so spawning has to defer to the next
		// system boundary.
		bx, by := pos.X, pos.Y
		var dungeonNetID uint32
		var chamberID uint16
		if anchor != nil {
			dungeonNetID = anchor.DungeonNetID
			chamberID = anchor.ChamberID
		}
		bossNetID := self.NetID()
		s.Commands().Defer(func() {
			const addCount = 2
			// Spawn 2 Lancers a short distance off the boss. Small ring
			// offset (±15u) avoids the adds materializing inside the boss.
			for k := 0; k < addCount; k++ {
				angle := float64(k) * (2 * math.Pi / float64(addCount))
				ox := float32(math.Cos(angle)) * 15
				oy := float32(math.Sin(angle)) * 15
				addE := gw.SpawnNPC(bx+ox, by+oy, ArchetypeLancer, dungeonNetID, NPCSpawnModifiers{})
				// Re-tag the chamber ID so AliveNetIDs tracking + leashing
				// follow the boss's chamber. SpawnNPC sets ChamberID=0; we
				// overwrite to match the boss's chamber.
				if a := mmokit.Get[gamecomp.DungeonAnchor](addE); a != nil {
					a.ChamberID = chamberID
				}
				// If we have an owning chamber, register the add in
				// AliveNetIDs so the chamber lifecycle waits for it.
				if dungeonNetID != 0 {
					if chambers, ok := gw.dungeonChambers[dungeonNetID]; ok {
						if c, ok := chambers[chamberID]; ok {
							c.AliveNetIDs = append(c.AliveNetIDs, addE.NetID())
						}
					}
				}
			}
			gw.eng.Log.Log(CatNPCAI, "ai: %d BossGuardian add-spawn threshold=%d frac=%.2f spawned=%d",
				bossNetID, i, frac, addCount)
		})
	}
}

// resolveBrawlerSpecial fires the line-cone attack at the end of the
// windup. Hitscan along SpecialDirAngle: any player entity inside the
// rectangle (length=BrawlerSpecialLength, half-width=
// BrawlerSpecialHalfWidth) takes BrawlerSpecialDamage. Despawns the
// in-flight telegraph entity (it expires this tick anyway, but explicit
// cleanup avoids a 1-tick overlap).
func (s *NPCAISystem) resolveBrawlerSpecial(
	self mmokit.Entity, ai *gamecomp.NPCAI, pos *mmokit.Position,
) {
	if ai.SpecialTelegraphNetID != 0 {
		m := mmokit.EntityByNetID(s.gw.stage, ai.SpecialTelegraphNetID)
		if m.Alive() {
			s.Commands().Despawn(m.Handle())
		}
		ai.SpecialTelegraphNetID = 0
	}

	cfg := s.gw.Config
	cos := float32(math.Cos(float64(ai.SpecialDirAngle)))
	sin := float32(math.Sin(float64(ai.SpecialDirAngle)))

	// Search a radius covering the full beam length.
	s.nearby = s.gw.Spatial.QueryRadius(
		pos.X+cos*cfg.BrawlerSpecialLength*0.5,
		pos.Y+sin*cfg.BrawlerSpecialLength*0.5,
		cfg.BrawlerSpecialLength*0.5+cfg.BrawlerSpecialHalfWidth,
		s.nearby[:0],
	)

	ownerNetID := self.NetID()
	for _, entry := range s.nearby {
		v := mmokit.EntityFromECS(s.gw.stage, entry.Entity)
		if !v.Alive() || v.NetID() == ownerNetID {
			continue
		}
		// Only damage players for Brawler special (NPCs are friendlies).
		if !mmokit.Has[mmokit.PlayerConn](v) {
			continue
		}
		vpos := mmokit.Get[mmokit.Position](v)
		if vpos == nil {
			continue
		}
		rx, ry := vpos.X-pos.X, vpos.Y-pos.Y
		parallel := rx*cos + ry*sin
		if parallel < 0 || parallel > cfg.BrawlerSpecialLength {
			continue
		}
		perp := rx*(-sin) + ry*cos
		if perp < 0 {
			perp = -perp
		}
		if perp > cfg.BrawlerSpecialHalfWidth {
			continue
		}
		s.gw.Damage(self, v, cfg.BrawlerSpecialDamage, 0, 0, npcAbilityTypeFor(ai.Archetype))
		s.gw.eng.Log.Log(CatNPCAI, "ai: %d Brawler special HIT netID=%d dmg=%.0f",
			ownerNetID, v.NetID(), cfg.BrawlerSpecialDamage)
	}
}

// npcAbilityTypeFor maps an NPC archetype to an existing player-side
// ability type so the shared VFX dispatcher in ability-effects.ts
// renders a sensible visual. Brawler is the only archetype that fires
// hitscan in v2 — Artillery uses AoE markers, Lancer applies contact
// damage during its dash.
func npcAbilityTypeFor(archetype uint8) uint8 {
	return uint8(item.AbilityTypePulseLaser)
}

// tickCast drives an Artillery NPC during its cast window. The NPC is
// stationary; the AoEMarker entity ticks its own Lifetime down and
// AoESystem resolves the damage when it hits 0. If accumulated damage
// during the cast exceeds the interrupt threshold, the marker is
// despawned, cast state is reset, and the NPC re-enters Engage on its
// cast cooldown.
func (s *NPCAISystem) tickCast(self mmokit.Entity, ai *gamecomp.NPCAI,
	pos *mmokit.Position, vel *mmokit.Velocity, rot *mmokit.Rotation,
	now, dt float32,
) {
	vel.X, vel.Y = 0, 0

	// Interrupt: enough damage accumulated during the cast → cancel.
	if ai.CastDamageAccum >= s.gw.Config.ArtilleryInterruptDamage && ai.CastingMarkerNetID != 0 {
		marker := mmokit.EntityByNetID(s.gw.stage, ai.CastingMarkerNetID)
		if marker.Alive() {
			s.Commands().Despawn(marker.Handle())
		}
		s.gw.eng.Log.Log(CatNPCAI, "ai: %d Artillery cast INTERRUPT (accum=%.1f >= %.1f)",
			self.NetID(), ai.CastDamageAccum, s.gw.Config.ArtilleryInterruptDamage)
		ai.CastingMarkerNetID = 0
		ai.CastTimeRemaining = 0
		ai.CastDamageAccum = 0
		ai.CastCooldown = jitterCooldown(s.gw.Config.ArtilleryCastCooldown, s.gw.Config.NPCAttackJitter)
		ai.State = AIStateEngage
		return
	}

	// Tick down cast timer. When it hits 0 the marker's own Lifetime is
	// expiring this same tick and AoESystem will resolve the damage —
	// just reset cast state and re-enter Engage on cooldown.
	ai.CastTimeRemaining -= dt
	if ai.CastTimeRemaining <= 0 {
		s.gw.eng.Log.Log(CatNPCAI, "ai: %d Artillery cast COMPLETE", self.NetID())
		ai.CastingMarkerNetID = 0
		ai.CastTimeRemaining = 0
		ai.CastDamageAccum = 0
		ai.CastCooldown = jitterCooldown(s.gw.Config.ArtilleryCastCooldown, s.gw.Config.NPCAttackJitter)
		ai.State = AIStateEngage
	}
}

// tickLanceWindup holds the Lancer stationary while its line-shaped
// telegraph is visible. The NPC faces the committed charge direction;
// when the windup timer elapses, the telegraph entity is despawned (its
// Lifetime would also expire this tick, but cleaning up early avoids a
// 1-tick visual overlap with the charge dash) and the NPC transitions
// to LanceCharge.
func (s *NPCAISystem) tickLanceWindup(self mmokit.Entity, ai *gamecomp.NPCAI,
	vel *mmokit.Velocity, rot *mmokit.Rotation, dt float32,
) {
	vel.X, vel.Y = 0, 0
	rot.Angle = ai.ChargeDirAngle // hold the visual heading
	ai.WindupRemaining -= dt
	if ai.WindupRemaining > 0 {
		return
	}
	if ai.LineTelegraphNetID != 0 {
		marker := mmokit.EntityByNetID(s.gw.stage, ai.LineTelegraphNetID)
		if marker.Alive() {
			s.Commands().Despawn(marker.Handle())
		}
		ai.LineTelegraphNetID = 0
	}
	ai.ChargeRemaining = s.gw.Config.LancerChargeTime
	ai.State = AIStateLanceCharge
	s.gw.eng.Log.Log(CatNPCAI, "ai: %d Lancer CHARGE", self.NetID())
}

// tickLanceCharge sprints the Lancer in the committed charge direction.
// Any locked target within ChargeWidth of the lancer's current position
// takes ChargeDamage once per charge (ChargeHit gate). On charge end,
// the lancer enters LanceRecover (vulnerable cooldown window).
func (s *NPCAISystem) tickLanceCharge(self mmokit.Entity, ai *gamecomp.NPCAI,
	pos *mmokit.Position, vel *mmokit.Velocity, rot *mmokit.Rotation,
	dt float32,
) {
	ux := float32(math.Cos(float64(ai.ChargeDirAngle)))
	uy := float32(math.Sin(float64(ai.ChargeDirAngle)))
	vel.X = ux * s.gw.Config.LancerChargeSpeed
	vel.Y = uy * s.gw.Config.LancerChargeSpeed
	rot.Angle = ai.ChargeDirAngle

	if !ai.ChargeHit && ai.TargetNetID != 0 {
		target := mmokit.EntityByNetID(s.gw.stage, ai.TargetNetID)
		if target.Alive() {
			tpos := mmokit.Get[mmokit.Position](target)
			if tpos != nil {
				ddx, ddy := tpos.X-pos.X, tpos.Y-pos.Y
				hitR := s.gw.Config.LancerChargeWidth
				if ddx*ddx+ddy*ddy <= hitR*hitR {
					s.gw.Damage(self, target, s.gw.Config.LancerChargeDamage, 0, 0, npcAbilityTypeFor(ai.Archetype))
					ai.ChargeHit = true
					s.gw.eng.Log.Log(CatNPCAI, "ai: %d Lancer HIT target=%d dmg=%.0f",
						self.NetID(), target.NetID(), s.gw.Config.LancerChargeDamage)
				}
			}
		}
	}

	ai.ChargeRemaining -= dt
	if ai.ChargeRemaining > 0 {
		return
	}
	ai.RecoverRemaining = jitterCooldown(s.gw.Config.LancerRecoverTime, s.gw.Config.NPCAttackJitter)
	ai.State = AIStateLanceRecover
	vel.X, vel.Y = 0, 0
}

// tickLanceRecover holds the Lancer stationary during its post-charge
// cooldown. The window is the player's punish opportunity. On expiry the
// lancer returns to Engage so it can re-acquire range and wind up again.
func (s *NPCAISystem) tickLanceRecover(self mmokit.Entity, ai *gamecomp.NPCAI,
	vel *mmokit.Velocity, dt float32,
) {
	vel.X, vel.Y = 0, 0
	ai.RecoverRemaining -= dt
	if ai.RecoverRemaining <= 0 {
		ai.State = AIStateEngage
		s.gw.eng.Log.Log(CatNPCAI, "ai: %d Lancer RECOVER → Engage", self.NetID())
	}
}

func (s *NPCAISystem) applyMotion(ai *gamecomp.NPCAI, vel *mmokit.Velocity,
	pos *mmokit.Position, tpos *mmokit.Position, dist, dx, dy float32,
) {
	if dist < 1e-3 {
		vel.X, vel.Y = 0, 0
		return
	}
	ux, uy := dx/dist, dy/dist
	switch ai.MotionPolicy {
	case MotionCharge:
		// Charge until within preferred firing distance; stop there so the
		// NPC doesn't physically overlap the player and oscillate at sub-tick
		// distances. Lancer sets PreferredRange == LanceRange so it charges
		// right up to the windup trigger — that still works.
		if dist > ai.PreferredRange {
			vel.X, vel.Y = ux*ai.MaxSpeed, uy*ai.MaxSpeed
		} else {
			vel.X, vel.Y = 0, 0
		}
	case MotionStationary:
		vel.X, vel.Y = 0, 0
	}
}

// tickSupport advances a Support NPC. Support has its own minimal
// behavior — never enters Engage/Approach/Leash. Each tick:
//   - If nearest player is inside SupportRetreatDist, kite directly away.
//   - Else, drift toward the POI anchor.
//   - Tick HealCooldown. When ready, pick the lowest-HP roster ally
//     within SupportHealRange (excluding self / dead / full-HP), Heal,
//     reset HealCooldown.
//
// Movement applied by writing directly into Velocity — no Pathing or
// state machine needed for Support's behavior.
func (s *NPCAISystem) tickSupport(e mmokit.Entity, ai *gamecomp.NPCAI,
	pos *mmokit.Position, vel *mmokit.Velocity, dt float32,
) {
	gw := s.gw

	var dirX, dirY float32
	var hasDir bool

	// Find the nearest player by scanning ShipControl owners (players
	// have ShipControl; NPCs do not).
	var nearestPlayerNetID uint32
	nearestPlayerDist2 := float32(1e9)
	mmokit.ForEach2(gw.stage, func(pe mmokit.Entity, _ *gamecomp.ShipControl, ppos *mmokit.Position) {
		dx := ppos.X - pos.X
		dy := ppos.Y - pos.Y
		d2 := dx*dx + dy*dy
		if d2 < nearestPlayerDist2 {
			nearestPlayerDist2 = d2
			nearestPlayerNetID = pe.NetID()
		}
	})
	retreat2 := gw.Config.SupportRetreatDist * gw.Config.SupportRetreatDist

	if nearestPlayerNetID != 0 && nearestPlayerDist2 < retreat2 {
		pe := mmokit.EntityByNetID(gw.stage, nearestPlayerNetID)
		if ppos := mmokit.Get[mmokit.Position](pe); ppos != nil {
			fx := pos.X - ppos.X
			fy := pos.Y - ppos.Y
			n := float32(math.Sqrt(float64(fx*fx + fy*fy)))
			if n > 0.001 {
				dirX, dirY = fx/n, fy/n
				hasDir = true
			}
		}
	} else {
		if anchor := mmokit.Get[gamecomp.DungeonAnchor](e); anchor != nil && anchor.DungeonNetID != 0 {
			poi := mmokit.EntityByNetID(gw.stage, anchor.DungeonNetID)
			if poi.Alive() {
				if poiPos := mmokit.Get[mmokit.Position](poi); poiPos != nil {
					fx := poiPos.X - pos.X
					fy := poiPos.Y - pos.Y
					n := float32(math.Sqrt(float64(fx*fx + fy*fy)))
					if n > 1.0 {
						dirX, dirY = fx/n, fy/n
						hasDir = true
					}
				}
			}
		}
	}

	if hasDir {
		vel.X = dirX * gw.Config.SupportMaxSpeed
		vel.Y = dirY * gw.Config.SupportMaxSpeed
	} else {
		vel.X = 0
		vel.Y = 0
	}

	ai.HealCooldown -= dt
	if ai.HealCooldown > 0 {
		return
	}

	// Pick lowest-HP ally on the same POI within SupportHealRange.
	myPOI := uint32(0)
	if a := mmokit.Get[gamecomp.DungeonAnchor](e); a != nil {
		myPOI = a.DungeonNetID
	}
	healRange2 := gw.Config.SupportHealRange * gw.Config.SupportHealRange

	var bestTarget mmokit.Entity
	bestPct := float32(1.0)
	mmokit.ForEach2(gw.stage, func(other mmokit.Entity, _ *gamecomp.NPCAI, otherPos *mmokit.Position) {
		if other.NetID() == e.NetID() {
			return
		}
		oa := mmokit.Get[gamecomp.DungeonAnchor](other)
		if oa == nil || oa.DungeonNetID != myPOI {
			return
		}
		dx := otherPos.X - pos.X
		dy := otherPos.Y - pos.Y
		if dx*dx+dy*dy > healRange2 {
			return
		}
		h := mmokit.Get[gamecomp.Health](other)
		if h == nil || h.Current <= 0 || h.Current >= h.Max {
			return
		}
		pct := h.Current / h.Max
		if pct < bestPct {
			bestPct = pct
			bestTarget = other
		}
	})

	if bestTarget.Alive() {
		gw.Heal(e, bestTarget, gw.Config.SupportHealAmount)
		ai.HealCooldown = gw.Config.SupportHealCooldown
		gw.eng.Log.Log(CatNPCAI, "support: heal source=%d target=%d",
			e.NetID(), bestTarget.NetID())
	}
}

func (s *NPCAISystem) tickLeash(self mmokit.Entity, ai *gamecomp.NPCAI,
	pos *mmokit.Position, vel *mmokit.Velocity, rot *mmokit.Rotation, dt float32,
) {
	anchor := mmokit.Get[gamecomp.DungeonAnchor](self)
	if anchor == nil || anchor.DungeonNetID == 0 {
		// No anchor (test NPC) — clear leash.
		mmokit.RemoveComponent[gamecomp.Leashing](s.Commands(), self)
		ai.State = AIStateIdle
		vel.X, vel.Y = 0, 0
		return
	}
	poiE := mmokit.EntityByNetID(s.gw.stage, anchor.DungeonNetID)
	if !poiE.Alive() {
		mmokit.RemoveComponent[gamecomp.Leashing](s.Commands(), self)
		ai.State = AIStateIdle
		vel.X, vel.Y = 0, 0
		return
	}
	poiPos := mmokit.Get[mmokit.Position](poiE)
	poi := mmokit.Get[gamecomp.POI](poiE)
	if poiPos == nil || poi == nil {
		return
	}
	dx, dy := poiPos.X-pos.X, poiPos.Y-pos.Y
	dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))
	if dist < poi.AnchorRadius {
		mmokit.RemoveComponent[gamecomp.Leashing](s.Commands(), self)
		ai.State = AIStateIdle
		vel.X, vel.Y = 0, 0
		if h := mmokit.Get[gamecomp.Health](self); h != nil {
			h.Current = h.Max
		}
		if sh := mmokit.Get[gamecomp.Shield](self); sh != nil {
			sh.Current = sh.Max
		}
		s.gw.eng.Log.Log(CatNPCAI, "ai: %d Leash→Idle (arrived)", self.NetID())
		return
	}
	ux, uy := dx/dist, dy/dist
	vel.X, vel.Y = ux*ai.MaxSpeed*2, uy*ai.MaxSpeed*2
	desired := float32(math.Atan2(float64(uy), float64(ux)))
	turnTowards(rot, desired, ai.TurnRate*2, dt)
}

func (s *NPCAISystem) findNearestEnemy(self mmokit.Entity,
	pos *mmokit.Position, radius float32,
) mmokit.EntityHandle {
	var best mmokit.EntityHandle
	bestDist2 := radius * radius
	mmokit.ForEach2(s.gw.stage, func(e mmokit.Entity, ppos *mmokit.Position, kind *mmokit.EntityKind) {
		entE := e.Handle()
		if entE == self.Handle() {
			return
		}
		if kind.Type != gamecomp.KindShip {
			return
		}
		if mmokit.Has[mmokit.Dormant](e) {
			return
		}
		if h := mmokit.Get[gamecomp.Health](e); h == nil || h.Current <= 0 {
			return
		}
		dx, dy := ppos.X-pos.X, ppos.Y-pos.Y
		d2 := dx*dx + dy*dy
		if d2 >= bestDist2 {
			return
		}
		// LOS gate: reject candidates blocked by a LayerStatic collider
		// (walls, station hulls) between the NPC and the candidate. The
		// Euclidean distance check above stays as the primary filter; LOS
		// is an additional gate so NPCs can't aggro through dungeon walls.
		if !hasLOSOnGrid(s.gw.Spatial,
			spatial.Vec2{X: pos.X, Y: pos.Y},
			spatial.Vec2{X: ppos.X, Y: ppos.Y},
			self.Handle()) {
			return
		}
		bestDist2 = d2
		best = entE
	})
	return best
}

// turnTowards rotates rot.Angle toward desired by up to turnRate*dt.
// Returns true if the rotation is within ±15° of desired after the step.
func turnTowards(rot *mmokit.Rotation, desired, turnRate, dt float32) bool {
	diff := normalizeAngle(desired - rot.Angle)
	maxTurn := turnRate * dt
	if diff > maxTurn {
		diff = maxTurn
	} else if diff < -maxTurn {
		diff = -maxTurn
	}
	rot.Angle += diff
	return angleDelta(rot.Angle, desired) < 0.26
}

func angleDelta(a, b float32) float32 {
	return float32(math.Abs(float64(normalizeAngle(a - b))))
}

// tickPathfindMotion drives an NPC under MotionPathfind: lazily
// computes a NavGrid waypoint list to targetPos and steers along it.
// Re-paths when the target moves substantially or RepathAt elapses.
// Falls back to no-op (zero velocity) when no NavGrid is registered
// for the NPC's dungeon, when A* fails, or when Pathing is missing.
func (s *NPCAISystem) tickPathfindMotion(npcE mmokit.Entity, ai *gamecomp.NPCAI,
	pos *mmokit.Position, vel *mmokit.Velocity, targetPos *mmokit.Position,
) {
	p := mmokit.Get[gamecomp.Pathing](npcE)
	if p == nil {
		// Pathfind motion requested but no Pathing component — degrade to
		// stationary (safer than charging through walls). Operator-spawned
		// NPCs without a dungeon shouldn't get MotionPathfind anyway.
		vel.X, vel.Y = 0, 0
		return
	}
	ng, ok := s.gw.dungeonNavGrids[p.DungeonNetID]
	if !ok {
		vel.X, vel.Y = 0, 0
		return
	}
	now := s.elapsedSec
	dxT := targetPos.X - p.TargetX
	dyT := targetPos.Y - p.TargetY
	movedThresh := s.gw.Config.PathRepathTargetMovedThreshold
	targetMoved := dxT*dxT+dyT*dyT > movedThresh*movedThresh
	if len(p.WaypointsX) == 0 || now > p.RepathAt || targetMoved {
		path := pathfinding.AStar(ng,
			pathfinding.Vec2{X: pos.X, Y: pos.Y},
			pathfinding.Vec2{X: targetPos.X, Y: targetPos.Y})
		if path == nil {
			// No path — hold position rather than charge blindly.
			vel.X, vel.Y = 0, 0
			return
		}
		smoothed := pathfinding.SmoothLOS(path, losAdapter{grid: s.gw.Spatial})
		p.WaypointsX = p.WaypointsX[:0]
		p.WaypointsY = p.WaypointsY[:0]
		for _, wp := range smoothed {
			p.WaypointsX = append(p.WaypointsX, wp.X)
			p.WaypointsY = append(p.WaypointsY, wp.Y)
		}
		p.CurrentIdx = 0
		p.TargetX = targetPos.X
		p.TargetY = targetPos.Y
		p.RepathAt = now + s.gw.Config.PathRepathIntervalSec
	}
	if p.CurrentIdx >= len(p.WaypointsX) {
		vel.X, vel.Y = 0, 0
		return
	}
	wpX := p.WaypointsX[p.CurrentIdx]
	wpY := p.WaypointsY[p.CurrentIdx]
	dx := wpX - pos.X
	dy := wpY - pos.Y
	dist := float32(math.Hypot(float64(dx), float64(dy)))
	if dist < s.gw.Config.NavGridCellSize/2 {
		p.CurrentIdx++
		return
	}
	vel.X = ai.MaxSpeed * dx / dist
	vel.Y = ai.MaxSpeed * dy / dist
}

// losAdapter wraps spatial.HashGrid to satisfy pathfinding.LOSChecker —
// used by SmoothLOS to collapse waypoint chains where the NPC has a
// clear line of sight (against LayerStatic) between two points.
type losAdapter struct{ grid *spatial.HashGrid }

func (a losAdapter) Clear(p, q pathfinding.Vec2) bool {
	// Pathfinding LOS smoothing has no caster entity to ignore; the
	// waypoints are abstract positions, not entities. Pass the zero
	// handle so the raycast considers every collider on the segment.
	return hasLOSOnGrid(a.grid, spatial.Vec2{X: p.X, Y: p.Y}, spatial.Vec2{X: q.X, Y: q.Y}, mmokit.EntityHandle{})
}
