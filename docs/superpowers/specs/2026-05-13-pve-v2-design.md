# PVE v2: Telegraphed Combat + Travel-Time Weapons

**Status:** Design  
**Date:** 2026-05-13  
**Supersedes parts of:** [2026-05-12-combat-poi-system-design.md](2026-05-12-combat-poi-system-design.md)

## Problem

The v1 combat-POI system (yesterday's spec) ships three NPC archetypes — Brawler, Sniper, Swarmer — driven by an FSM with hitscan-only damage. Two issues block this from being fun:

1. **Sniper and Swarmer kite/encircle behaviors aren't interactive.** The Sniper holds at preferred range and flees if you close, the Swarmer orbits without committing. Both create movement noise without forcing a player decision.
2. **All weapons are instant-hit hitscan.** There's no leading, no dodging, no counter-play. Pressing the cooldown is the only verb.
3. **Procedural POI placement** in the station cell can stick the encounter 4000–6000u from the station, gating fresh-build engagement behind a long flight.

PVE v2 rebuilds the encounter feel around two coupled ideas:

- **Telegraphed AoE as the reactive primitive.** Enemies broadcast their intent; players move out of the area. Fair-by-design — every telegraph is dodgeable by a player who notices and reacts.
- **Travel-time projectiles** for the player. Leading a shot, dodging a missile, dropping a mortar on grouped enemies. The same `AoEMarker` primitive serves both NPC casts and player splash damage.

## Goals

- Replace boring AI behaviors (kite, encircle) with reactive ones (telegraphed AoE, charge-and-detonate).
- Add **interactive counter-play** at two skill axes: spatial (dodge AoE) and prioritization (focus-fire kamikazes / interrupt artillery casts).
- Add **four travel-time / projectile weapons** for players: PlasmaShot, HomingMissile, SustainedBeam, MortarShell.
- Move the station-cell POI to a deterministic, close, accessible anchor.
- Keep everything fair-by-design: codified as a runtime invariant so future archetypes can't silently break it.

## Non-Goals

- Heat / energy / ammo systems. Cooldowns remain the only resource gate.
- Point defense, mines, EMP/disruptor weapons. Natural follow-ups; explicitly out-of-scope for v2.
- Phase mechanics / mini-bosses / multi-stage encounters. Single-tier roster only.
- Healer / support archetype. Pack composition stays 3 roles.
- DB migrations. NPC and POI state remains ephemeral.

## Architecture Overview

Five distinct pieces, in dependency order:

```
                ┌──────────────────────┐
                │  AoEMarker entity    │  (new)
                │  + AoESystem         │
                └──────────┬───────────┘
                           │ spawned by
        ┌──────────────────┼──────────────────┐
        │                  │                  │
┌───────▼────────┐ ┌───────▼────────┐ ┌──────▼───────────┐
│ Artillery NPC  │ │ Kamikaze NPC   │ │ Projectile splash│
│ (cast→spawn)   │ │ (detonate)     │ │ on impact        │
└────────────────┘ └────────────────┘ └──────────┬───────┘
                                                 │ uses
                                          ┌──────▼───────┐
                                          │ Projectile   │  (new)
                                          │ + ProjSystem │
                                          └──────┬───────┘
                                                 │ fired by
                                          ┌──────▼───────┐
                                          │ AbilitySystem│  (existing)
                                          │ + 4 new types│
                                          └──────────────┘
```

- The `AoEMarker` primitive is the only new "damage at a delay" abstraction. NPCs and player splash damage both ride on it.
- The `Projectile` entity carries travel-time damage; on impact it either applies direct damage or spawns an instant-lifetime `AoEMarker` for splash.
- Existing systems (`NPCAISystem`, `AbilitySystem`, `TargetLockSystem`, `ApplyDamage`) get new branches but no structural changes.

---

## 1. POI Placement (Station Cell)

**Current:** [internal/game/poi_gen.go](../../../internal/game/poi_gen.go) `GeneratePOIs` runs procgen for all cells. Station cell gets one POI guaranteed but positioned randomly within `(margin, CellSize - margin)`, subject to `POIStationClearance` (400u) and belt-clearance constraints. Effective distance from station ranges 400–5000+ units.

**Change:** For the station cell only, replace procgen with a fixed deterministic anchor at `(StationLocalX + StationPOIOffsetX, StationLocalY + StationPOIOffsetY)`. Non-station cells keep the existing procgen pipeline unchanged.

**Defaults (new `GameConfig` fields):**

```go
StationPOIOffsetX float32 = 0      // "north" in screen-coord convention
StationPOIOffsetY float32 = -1100  // ~90s travel at base 6 u/s, ~30s with Afterburner
```

**Resolved position:** `(8100, 7000)` — directly above the station, well inside cell (0,0) (CellSize=8192, margin=200 still respected).

**Implementation:** Branch in `GeneratePOIs`: if `cell == stationCell`, return `[]POIDef{{X: StationLocalX + cfg.StationPOIOffsetX, Y: StationLocalY + cfg.StationPOIOffsetY, Type: 0, RosterIdx: 0}}` and skip the loop entirely. Belt-clearance and station-clearance are not enforced here — the offset is authored, not random. (If the offset is ever set badly enough to collide with the station entity, that's a config bug, not a runtime concern.)

---

## 2. AI Archetype Changes

### Remove

- `NPCAIArchetypeSniper` and `MotionPolicyHoldRange` — deleted from [internal/game/npc_archetype.go](../../../internal/game/npc_archetype.go) and [internal/game/system_npc_ai.go](../../../internal/game/system_npc_ai.go).
- `NPCAIArchetypeSwarmer` and `MotionPolicyEncircle` — same.

The enum is renumbered from 1; no `reserved` placeholders (per project convention).

### Keep

- `NPCAIArchetypeBrawler` and `MotionPolicyCharge` unchanged. Already the most readable enemy.

### Add: `NPCAIArchetypeArtillery`

| Field | Value | Notes |
|---|---|---|
| HP / Shield | 250 / 150 | Squishier than Brawler |
| Speed | 4 u/s | Slowest archetype |
| Aggro radius | 1000 | Engages players from far |
| Leash radius | 1500 | Returns to anchor if pulled past this |
| Weapon range | 1000 | AoE-only; no direct hitscan |
| AoE radius | 50 | Tunable: `ArtilleryAoERadius` |
| AoE damage | 50 | Tunable: `ArtilleryAoEDamage` |
| Cast time | 3.5s | Tunable: `ArtilleryCastTime`. Satisfies fairness invariant — see §7. |
| Cast cooldown | 3.0s after detonation | Tunable: `ArtilleryCastCooldown` |
| Interrupt threshold | 25 damage during cast | Tunable: `ArtilleryInterruptDamage` |

**Behavior (states added to existing FSM):**

- `Idle` → `Acquire` → `Cast` → `Cooldown` → `Cast` → ... ; `Leash` reachable from any state.
- `Cast` entry: snapshot the locked target's current world position. Spawn an `AoEMarker` entity at that position with `Lifetime = ArtilleryCastTime` and the AoE spec.
- `Cast` exit (lifetime expiry handled by AoESystem on the marker, not the NPC). The NPC just transitions to `Cooldown` when its own cast timer fires, with no further work.
- **Interrupt:** the Artillery tracks cumulative damage taken since `Cast` entry. If it crosses `ArtilleryInterruptDamage`, the NPC dispatches a "cancel my marker" command (despawn the AoEMarker entity by owner+spawn-tick lookup) and transitions to `Cooldown` immediately.

Positioning: stationary during `Cast`. Between casts, Artillery only moves to *close* distance — if the target is outside weapon range (1000u), it approaches at 4 u/s. **It never retreats** under any condition (no "kite" or "maintain distance" behavior — that's what made Sniper bad). If the player closes to melee, Artillery just tanks it and keeps casting at point-blank.

### Add: `NPCAIArchetypeKamikaze`

| Field | Value | Notes |
|---|---|---|
| HP / Shield | 60 / 0 | One-shot territory for strong weapons |
| Speed | 16 u/s | Fastest archetype |
| Aggro radius | 800 | Standard |
| Detonation trigger range | 30 | Within this radius → beep |
| Beep duration | 0.5s | Tunable: `KamikazeBeepTime` |
| Detonation AoE radius | 60 | Tunable: `KamikazeAoERadius` |
| Detonation AoE damage | 40 | Tunable: `KamikazeAoEDamage` |
| Death-while-beeping | does NOT detonate | Critical to focus-fire counter-play |

**Behavior:**

- `Idle` → `Acquire` → `Engage(Charge)` → `Beep` → `Detonate` → despawn.
- `Engage(Charge)` uses the existing `MotionPolicyCharge` — direct approach, no orbit.
- `Beep` entry (triggered when within 30u of target): NPC freezes in place (speed = 0), 0.5s timer starts. Client-side telegraph: red pulsing ring expanding from the NPC at the future AoE radius.
- `Detonate`: spawn an `AoEMarker` at the kamikaze's current position with `Lifetime = 0` — AoESystem runs later this same tick and resolves the explosion. Despawn the kamikaze.
- If killed during any state before `Detonate`: just despawn. No AoE marker spawned. **This is the design intent — incentivizes focus-fire.**

### Updated starter POI roster

Replaces today's `2 Brawler + 1 Sniper + 3 Swarmer` in [internal/game/poi_config.go](../../../internal/game/poi_config.go):

```
1 Artillery + 2 Brawlers + 3 Kamikazes
```

Tactical puzzle for the player: focus Kamikazes (squishiest, biggest immediate spike threat) → focus Artillery (each interrupt = one fewer AoE you have to dodge) → finish Brawlers (slow, predictable).

---

## 3. Telegraphed AoE Primitive

### New entity kind: `AoEMarker`

Registered via `mmokit.RegisterKind[AoEMarkerBundle](coord, KindAoEMarker, "AoEMarker", bindings)`.

**Bundle:**

```go
type AoEMarkerBundle struct {
    Position *comp.Position
    Lifetime *comp.Lifetime
    AoESpec  *gamecomp.AoESpec
}
```

**`AoESpec` component (new, in `internal/component/`):**

```go
type AoESpec struct {
    Radius      float32  // resolved AoE radius
    Damage      float32  // damage applied at expiry
    OwnerNetID  uint32   // attribution
    FactionMask uint8    // who can be hit (NPC=1, Player=2, Both=3)
    DamageType  uint8    // for future resistance system; v2: always 0=normal
}
```

`net:` tags for wire serialization: `Radius=qnorm`, `Damage=auto`, `OwnerNetID=auto`, `FactionMask=auto`, `DamageType=auto`. `Lifetime` already has a net encoding — client uses it to drive the cast-progress visual.

### New system: `AoESystem` (in `pkg/system/`)

Tick logic (runs after Network, before Lifetime flush):

1. Iterate all entities with `(Position, Lifetime, AoESpec)`.
2. For each, if `Lifetime.Remaining <= 0`:
   - Query spatial grid for all entities within `AoESpec.Radius` of `Position`.
   - Filter by `FactionMask` (NPCs only damage players, player AoEs only damage NPCs, unless mask is `Both` — Kamikazes hitting each other).
   - Apply `AoESpec.Damage` via existing `gw.Damage(...)` path so shields/buffs/logging all flow normally.
   - Queue the AoEMarker entity for despawn via `Commands().Despawn(...)`.
3. No movement logic — markers are stationary.

The system is registered in [internal/game/factory.go](../../../internal/game/factory.go) **after** `ProjectileSystem` (so projectile-impact splash markers resolve same-tick), before Lifetime flush.

### Cancellation API

`StagedAoEMarker` helper attached to the NPC during its `Cast` state lets the NPC find and despawn its in-flight marker on interrupt:

```go
// On Cast entry:
ai.CastingMarker = stage.Spawn(... AoEMarker components ...).Handle()

// On interrupt:
if stage.World().Alive(ai.CastingMarker) {
    stage.Commands().Despawn(ai.CastingMarker)
}
ai.CastingMarker = mmokit.EntityHandle{} // zero
```

### Client-side rendering

- New entity type in TS SDK (auto-generated from `RegisterKind`).
- web-pixi renderer reads `Lifetime.Remaining / Lifetime.Total` to compute progress 0→1.
- Visual: outer dashed ring at full `Radius` (intent), inner filled circle growing 0 → `Radius` (commitment), color depends on `OwnerNetID` faction (red/orange for hostile, blue/green for friendly).
- Cancelled cast → entity vanishes (server despawn). Client just removes it on the next entity-removed event. No special "interrupted" effect for v2.

---

## 4. Projectile Infrastructure

### New entity kind: `Projectile`

```go
type ProjectileBundle struct {
    Position       *comp.Position
    Velocity       *comp.Velocity
    Lifetime       *comp.Lifetime
    ProjectileSpec *gamecomp.ProjectileSpec
}
```

**`ProjectileSpec` component:**

```go
type ProjectileSpec struct {
    OwnerNetID    uint32   // attribution + collision-self-skip
    TargetNetID   uint32   // 0 = not homing; non-zero = homing
    Damage        float32  // direct impact damage
    SplashRadius  float32  // 0 = single-target
    SplashDamage  float32  // 0 = no splash
    MaxTurnRate   float32  // rad/sec; only used if TargetNetID != 0
    Type          uint8    // visual variant (Plasma=0, Missile=1, Mortar=2)
}
```

### New system: `ProjectileSystem`

Tick logic:

1. For each `(Position, Velocity, ProjectileSpec)`:
   - **Homing update** (if `TargetNetID != 0`): look up target entity. If alive, compute desired heading toward target, rotate `Velocity` toward it capped by `MaxTurnRate * dt`. If target is dead or gone, zero out `TargetNetID` (drops to dumb-fire — keeps current velocity).
   - **Move:** `Position += Velocity * dt`.
   - **Collision:** spatial-grid query at `Position` with small radius (use projectile sprite radius, ~3u). Skip the owner. First hit:
     - Apply `Damage` to the hit entity.
     - If `SplashRadius > 0`: spawn an `AoEMarker` at impact with `Lifetime = 0` (instant), `Radius = SplashRadius`, `Damage = SplashDamage`, `FactionMask` derived from owner faction.
     - Despawn projectile.
   - **Lifetime expiry without hit:** just despawn (silent).

Registered in `factory.go` **before** `AoESystem` — so splash markers spawned by projectile impacts this tick get resolved by `AoESystem` later the same tick. Reads more naturally than a 1-tick delay.

---

## 5. New Player Weapons

Four new values in the `AbilityType` enum, all wired into `AbilitySystem.Update` in [internal/game/system_ability.go](../../../internal/game/system_ability.go).

### PlasmaShot (projectile cannon)

- Slot: Weapon1 (primary or secondary)
- Cooldown: 1.5s
- Range: 700u (max lifetime distance)
- Behavior: spawn a `Projectile` entity at the player's position, velocity pointed at `TargetLock.ActiveNetID`'s **current** position (or muzzle-forward if no lock), speed 700 u/s, damage 25, no splash, no homing.
- Lifetime: `700 / 700 = 1.0s`.
- Skill expression: leading the shot when target is moving sideways.

### HomingMissile

- Slot: Weapon2 (heavy)
- Cooldown: 8s
- Range: 1500u (max lifetime distance, ~5s flight)
- Behavior: requires `TargetLock.ActiveNetID` to be set when fired (rejected with cooldown not consumed if no lock). Spawn projectile with initial speed 200 u/s (radial outward from player) that accelerates linearly to 350 u/s over 1s. `MaxTurnRate ≈ 2.09 rad/s` (120°/s). `TargetNetID = ActiveNetID`.
- Flight time: 1500u ÷ 350 u/s ≈ 4.3s at cruise (or shorter on accelerating ramp).
- Damage: 80 direct + 20 splash, splash radius 30u.
- Counter-play: not "outrun" (Kamikaze at 16 u/s can't outpace 350 u/s missile). Actual counter-play is **threat awareness** — players see the missile spawn and can drift sideways to force the turn-cap to under-track, OR they let it hit and trade the 100 damage for a positional swap.

Note: missile acceleration is implemented as a closure in `ProjectileSystem`'s tick — if `Type == Missile` and current speed < 350, scale velocity by accel factor. Avoids a new "acceleration" component for one weapon.

### SustainedBeam

- Slot: Weapon1
- Cost: 4s cooldown after channel ends
- Range: 500u
- Channel: up to 3.0s while button held.
- Behavior: while channeling, every 0.1s server tick (every 2 game ticks at 20 Hz) apply 4 damage to `TargetLock.ActiveNetID` IF target is within 500u AND within ±30° of player facing. Out-of-arc → channel ends, cooldown starts. Target dies or out of range → channel ends.
- New local-only component: `Channeling{ SlotID, RemainingTime, Target }` on the player entity.
- Cooldown applied to the slot only on channel end (whether by player release, target loss, or 3s max channel hit).

### MortarShell

- Slot: Weapon2
- Cooldown: 6s
- Range: 600u (max lifetime distance, ~1.5s flight)
- Behavior: spawn projectile aimed at lock or muzzle-forward, speed 400 u/s, `Type = Mortar`. On impact:
  - 80 direct damage (single-target on whatever it hit, if anything).
  - Spawn AoEMarker at impact with 80u radius, 60 damage, `Lifetime = 0` — AoESystem runs later this same tick and resolves the splash.
- Player's symmetric answer to Artillery: lob it into a Kamikaze cluster.

---

## 6. Tuning Summary

### Enemy stat budget (starter POI: 1 + 2 + 3 = 6 NPCs)

| Archetype | Count | HP | Shield | Speed | Range | Cast/Detonate | DPS (sustained) |
|---|---|---|---|---|---|---|---|
| Brawler | 2 | 400 | 200 | 6 | 100 (melee) | n/a | 25 |
| Artillery | 1 | 250 | 150 | 4 | 1000 (AoE) | 3.5s cast, 50dmg/50r | 7.7 (burst) |
| Kamikaze | 3 | 60 | 0 | 16 | 30 (proximity) | 0.5s beep, 40dmg/60r | one-shot |

### Player stat baseline (unchanged)

- ~600 HP / 200 shield, base speed 6 u/s, Afterburner 12 u/s.

### Player weapon comparison

| Ability | Slot | DPS | Burst | Range | Cooldown | Skill axis |
|---|---|---|---|---|---|---|
| (existing hitscan ~25dmg) | W1/W2 | 16-20 | n/a | 500-800 | 1-2s | Cooldown rotation |
| PlasmaShot | W1 | ~16 | 25 | 700 | 1.5s | Leading |
| HomingMissile | W2 | ~10 | 100 | 1500 | 8s | Target lock + waiting |
| SustainedBeam | W1 | 40 (while channel) | n/a | 500 | 3s channel + 4s cd | Tracking |
| MortarShell | W2 | ~23 | 140 (splash+direct) | 600 | 6s | Predicting clusters |

### TTK matrix (single weapon, single target, no shields)

- Brawler (400 HP): ~25s with PlasmaShot, ~10s with SustainedBeam, ~5s with two Missiles
- Artillery (250 HP): ~16s with PlasmaShot, but **interruptible** — one well-timed mortar (60 splash) cancels the cast even if the Artillery survives.
- Kamikaze (60 HP): ~4s with PlasmaShot, 1 missile direct hit, 1 mortar splash.

### Expected encounter time

~45–90 seconds of active combat to clear the starter POI. Long enough to feel like a fight; short enough that a wipe isn't punishing.

---

## 7. Fair-by-Design Invariant

Codified in `internal/game/`:

```go
// FairnessFactor describes how dodgeable a telegraph is supposed to be:
//   1.0 — strictly dodgeable: base-speed player at marker center clears the radius
//   0.4 — dodgeable with effort: base-speed gets you partway, Afterburner clears it
//   0.0 — not spatially dodgeable: counter-play is to kill or interrupt before resolution
type FairnessFactor float32

const (
    FairnessStrict   FairnessFactor = 1.0
    FairnessEffort   FairnessFactor = 0.4
    FairnessFocusFire FairnessFactor = 0.0
)

// In each archetype def, declare the intended fairness mode:
ArchetypeArtillery: { ..., Fairness: FairnessEffort, ... }
ArchetypeKamikaze:  { ..., Fairness: FairnessFocusFire, ... }
```

**Test (`internal/game/archetype_fairness_test.go`):**

```go
for _, arch := range AllArchetypes() {
    if arch.AoESpec == nil { continue }
    if arch.Fairness == FairnessFocusFire { continue } // not a spatial test
    required := arch.AoESpec.Radius * float32(arch.Fairness)
    achievable := PlayerBaseSpeed * arch.CastTime
    if achievable < required {
        t.Errorf("%s: fairness=%v requires %vu of movement during cast, "+
            "but base speed %v × cast %vs only gives %vu",
            arch.Name, arch.Fairness, required, PlayerBaseSpeed, arch.CastTime, achievable)
    }
}
```

With v2 numbers:

- Artillery: required = 50 × 0.4 = 20u; achievable = 6 × 3.5 = **21u** → passes with 1u margin. (This is why Artillery cast is 3.5s, not 3.0s.)
- Kamikaze: `FairnessFocusFire`, skipped (not a spatial-dodge test).

The test forces *any* archetype-config change to either satisfy the invariant or explicitly downgrade its fairness tag — no silent regressions. If during playtest the margin feels too tight, the principled levers are: shrink AoE radius, lengthen cast time, or downgrade the fairness tag with explicit justification.

---

## 8. Wire / Schema Impact

- **Two new entity kinds** registered via `mmokit.RegisterKind`: `KindAoEMarker`, `KindProjectile`. Auto-flow into the TS SDK on `just space-sdk`.
- **Two new components**: `AoESpec`, `ProjectileSpec` (in `internal/component/`). Use existing `net:` tag conventions.
- **One new local-only component**: `Channeling` (player-side beam state). Tagged `mmokit:"local"` — not serialized.
- **Four new `AbilityType` enum values**: `AbilityTypePlasmaShot`, `AbilityTypeHomingMissile`, `AbilityTypeSustainedBeam`, `AbilityTypeMortarShell`. Existing `AbilitySet.Slots[]` schema is unchanged.
- **NPC archetype enum renumbered** — `Sniper` and `Swarmer` removed; `Brawler`, `Artillery`, `Kamikaze` get values 1, 2, 3. No reserved placeholders.
- **No proto changes.** All wire flow uses the typed reflection codec via `mmokit.RegisterKind`.

---

## 9. Testing

### Unit (no engine spin-up)

- `pkg/system/aoe_system_test.go`: marker expiry applies damage to entities in radius, respects FactionMask, attribution flows to `OwnerNetID`.
- `pkg/system/projectile_system_test.go`: linear motion, swept collision picks first hit, homing turns at `MaxTurnRate`, splash spawns instant AoEMarker.
- `internal/game/archetype_fairness_test.go`: invariant test (above).

### Integration (real stage, real systems)

- `internal/game/encounter_test.go`: spawn the starter POI roster, spawn a bot with a fixed loadout (2× PlasmaShot, 1× MortarShell, 1× HomingMissile), tick the world for N seconds, assert bot survives ≥ X% of trials. Provides regression coverage for tuning churn.

### Manual smoke

- `just dev` for the space game.
- Fresh build → spawn at station → fly to `(8100, 7000)` → engage.
- Tab through each new weapon, verify projectile sprites render, AoE telegraphs render, beam channel ends out-of-arc.
- Reset POI by walking out of disengage radius and back; verify respawn loop is intact.

---

## 10. Rollout

- **No backward compat.** Solo dev, no users. Sniper/Swarmer code paths deleted; archetype enum renumbered from 1.
- **No DB migration.** NPC and POI state is ephemeral.
- **Single commit / merge.** The pieces are coupled enough (AoEMarker feeds three callers) that splitting would leave dead code in interim states. `just space-sdk` regen + `just build` in the merge commit.
- **Implementation order** (will be detailed in the plan): infrastructure first (AoEMarker + Projectile + their systems), then the four new abilities (player-side), then the two new archetypes (NPC-side), then POI placement change, then tuning + tests last. Each step compilable in isolation.

---

## 11. Out-of-Scope / Deferred

- **Point defense:** passive turret shooting down enemy missiles. Natural follow-up once Artillery + Missiles ship — needs the AI to fire missiles back, which v2 does not.
- **Mine layer / EMP / disruptor:** future utility weapons; same infrastructure already in place.
- **Multi-stage encounters / mini-boss:** phase mechanics, summons-at-low-HP, etc. Defer to v3.
- **Healer / support archetype:** considered and rejected for v2 scope.
- **Loot rewards from POI clears:** existing loot crate system already works; v2 doesn't change it. Tuning of drop rates is separate work.
- **NPC weapon variety:** current Artillery has *only* AoE, Brawler has *only* melee hitscan, Kamikaze has *only* suicide. Future archetypes might mix.
