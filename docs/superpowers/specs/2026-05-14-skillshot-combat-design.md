# Skillshot Combat Design

**Status:** Design  
**Date:** 2026-05-14  
**Builds on:** [2026-05-13-pve-v2-design.md](2026-05-13-pve-v2-design.md)

## Problem

PVE v2 introduced telegraphed AoEs and projectile weapons but kept the same combat input model: **lock a target, press a key, the ability resolves on the locked entity**. There's no aiming. Player skill comes from cooldown rotation and positioning, not from where the cursor points.

The user wants combat to feel more like League of Legends / Albion Online — **skillshots** the player aims with the cursor, with visual indicators showing what they're about to commit to. Lock-on stays for high-impact "I'm picking this target" abilities, but the moment-to-moment damage comes from aimed line shots and ground-target AoEs.

## Goals

- **Aimed combat as the default.** Most damage abilities require the player to point the cursor and predict target movement. Lock-on is reserved for utility, DoT, and big-commit nukes.
- **Honest indicators.** Every skillshot shows exactly where it will land — line, cone, or circle. No hidden geometry.
- **Per-slot quickcast.** Aim-confirm by default (press → preview → release/click → fire). Quickcast (press = fire immediately at current cursor) opt-in per slot via settings.
- **NPC parity.** Brawlers also get a telegraphed special (line-cone) so the player has something to dodge from every archetype, matching the player's new aim language.
- **Server-authoritative aim.** Cursor coords flow with the cast; server validates range and snaps; can't be cheated.

## Non-Goals

- **No new ability content** beyond what's already in the item registry. The 12 existing AbilityTypes get re-classified; this spec doesn't add new weapons.
- **No combo / cancel-cast / animation cancel mechanics.** Each ability is one press → one cast.
- **No keybind remapping UI** for which key is which slot. Quickcast toggle is the only new setting.
- **No PvP rebalance.** All numbers are tuned for PvE; PvP balance is out of scope.
- **No NPC ability-system unification** beyond the Brawler dual-attack. Artillery and Lancer stay on their bespoke FSM paths.

---

## Architecture Overview

Four pieces, in dependency order:

```
┌─────────────────────────────────┐
│ AbilityParams.TargetingMode     │  (new field on item.AbilityParams)
│   Self | Targeted | Line |      │
│   Ground | Channel              │
└──────────────┬──────────────────┘
               │ consumed by
               ▼
┌─────────────────────────────────┐    ┌─────────────────────────────────┐
│ Wire: CastAbility{slot, aimX,   │    │ Client: aim-indicator.ts        │
│       aimY, aimDirX, aimDirY}   │◄───┤  per-slot aim state + preview   │
└──────────────┬──────────────────┘    │  visuals + quickcast settings   │
               │ dispatched by         └─────────────────────────────────┘
               ▼
┌─────────────────────────────────┐
│ Server: AbilitySystem           │
│   switch on TargetingMode →     │
│   validate aim → spawn entity   │
└──────────────┬──────────────────┘
               │
       ┌───────┼───────┐
       ▼       ▼       ▼
   Projectile  AoEMarker  Channeling
   (line)      (ground)   (channel-line)
```

---

## 1. TargetingMode

New field on `internal/item/item.go::AbilityParams`:

```go
// TargetingMode is how an ability resolves its target.
type TargetingMode uint8

const (
    TargetingSelf            TargetingMode = 0 // no aim, no target
    TargetingLockOn          TargetingMode = 1 // requires active TargetLock
    TargetingSkillshotLine   TargetingMode = 2 // aim direction; fires Projectile
    TargetingSkillshotGround TargetingMode = 3 // aim position; drops AoEMarker
    TargetingSkillshotChannel TargetingMode = 4 // held; beam tracks aim direction
)
```

Each `AbilityParams` entry in the registry declares its `TargetingMode`. The default value `0` is `Self` — abilities that haven't been classified intentionally end up as Self casts (visible bug, easy to catch).

### Per-ability classification

| Ability ID | Name | New Mode | Notes |
|---|---|---|---|
| 1 | PulseLaser | `SkillshotLine` | Basic line skillshot; the "auto-attack" replacement |
| 2 | PulseBarrage | `SkillshotLine` | 3 sub-projectiles in a small cone (impl: 3 separate projectile spawns spread ±10°) |
| 3 | RailShot | `SkillshotLine` | Long range, fast projectile, `PierceCount=2` |
| 4 | PiercingRound | `SkillshotLine` | `PierceCount=99` (pierces every target along the line) |
| 5 | IonBurn | `LockOn` | DoT debuff stays lock-on |
| 6 | IonOverload | `SkillshotLine` | |
| 7 | PlasmaBolt | `SkillshotLine` | |
| 8 | PlasmaTorpedo | `LockOn` | Heavy nuke stays lock-on |
| 9 | PlasmaShot | `SkillshotLine` | Already projectile; the targeting mode change is what unlocks "aim it" |
| 10 | HomingMissile | `LockOn` | Lock-on smart missile |
| 11 | SustainedBeam | `SkillshotChannel` | Beam tracks cursor instead of locked target |
| 12 | MortarShell | `SkillshotGround` | Cursor-target AoE — classic MOBA ground-targeted nuke |
| 20 | EmergencyShield | `Self` | |
| 21 | HardenedShield | `Self` | |
| 30 | Afterburner | `Self` | |
| 31 | MicroWarp | `Self` | |
| 40 | MiningBeam | `LockOn` | Asteroid target |
| 41 | ExtractPulse | `LockOn` | |

---

## 2. Wire Protocol

### `CastAbility` extended

Current:
```go
type CastAbility struct {
    Slot     uint8
    Sequence uint16
}
```

New:
```go
type CastAbility struct {
    Slot     uint8
    Sequence uint16
    // Aim point in world coords. Semantics depend on the ability's
    // TargetingMode at the slot:
    //   Self            — ignored
    //   LockOn          — ignored (server reads TargetLock.ActiveNetID)
    //   SkillshotLine   — direction vector = (AimX - shipX, AimY - shipY)
    //   SkillshotGround — drop AoE at (AimX, AimY), clamped to range
    //   SkillshotChannel — initial aim point; client sends updates each tick
    //                      via a separate ChannelAim message while held
    AimX float32
    AimY float32
}
```

### `ChannelAim` (new message)

Client streams cursor updates while channeling a `SkillshotChannel` ability:

```go
type ChannelAim struct {
    Slot uint8  // which channel to update
    AimX float32
    AimY float32
}
```

Server applies to the `Channeling` component's `AimX/Y` fields. Rate-limited client-side to ~20 Hz (one update per server tick) to avoid spam.

### `Channeling` component extended

```go
type Channeling struct {
    SlotID        uint8   `mmokit:"local"`
    RemainingTime float32 `mmokit:"local"`
    NextTickIn    float32 `mmokit:"local"`
    AimX          float32 `mmokit:"local"` // NEW
    AimY          float32 `mmokit:"local"` // NEW
    // TargetNetID kept for compatibility with non-skillshot channels (none
    // currently, but the structural option avoids re-add later)
    TargetNetID uint32 `mmokit:"local"`
}
```

### `ProjectileSpec` extended

```go
type ProjectileSpec struct {
    OwnerNetID    uint32  `net:"u32"`
    TargetNetID   uint32  `net:"u32"`
    Damage        float32 `net:"f32"`
    SplashRadius  float32 `net:"f32"`
    SplashDamage  float32 `net:"f32"`
    MaxTurnRate   float32 `net:"f32"`
    Type          uint8   `net:"u8"`
    PierceCount   uint8   `net:"u8"` // NEW: remaining pierces; 0 = stop at first hit
    PiercedNetIDs [4]uint32 // NEW: server-only, no net tag — already-pierced victims (max 4)
}
```

When a projectile hits a victim:
- If `PierceCount == 0`: apply damage, despawn (current behavior).
- If `PierceCount > 0`: apply damage, decrement `PierceCount`, record victim in `PiercedNetIDs[]`, continue. Same victim cannot be pierced twice on the same projectile (the `PiercedNetIDs` check).
- For PiercingRound (`PierceCount = 99` at spawn): effectively pierces every target on the line until lifetime expires or `PierceCount` decrements past `PiercedNetIDs` capacity.

---

## 3. Server Dispatch

`AbilitySystem.executeAbility` gets a top-level switch on `params.TargetingMode`:

```go
func (s *AbilitySystem) executeAbility(action abilityAction) bool {
    params := action.params
    switch params.TargetingMode {
    case item.TargetingSelf:
        return s.dispatchSelf(action)
    case item.TargetingLockOn:
        return s.dispatchLockOn(action)
    case item.TargetingSkillshotLine:
        return s.dispatchSkillshotLine(action)
    case item.TargetingSkillshotGround:
        return s.dispatchSkillshotGround(action)
    case item.TargetingSkillshotChannel:
        return s.dispatchSkillshotChannel(action)
    }
    return false
}
```

Each dispatch function owns one mode's flow.

### `dispatchSelf`

No aim, no target. The existing Self abilities (shield, thruster) — flow unchanged from current behavior.

### `dispatchLockOn`

Same as today's "locked target required" flow. Requires `TargetLock.ActiveNetID`, validates range against the locked entity's position. Refunds cooldown if no lock. Used by HomingMissile, PlasmaTorpedo, IonBurn, MiningBeam, ExtractPulse.

### `dispatchSkillshotLine`

```
1. Get caster position
2. Compute direction = (action.aimX - casterPos.X, action.aimY - casterPos.Y), normalized.
   If direction magnitude < epsilon, fall back to caster facing.
3. Spawn one or more Projectile entities at casterPos with velocity =
   direction × params.ProjectileSpeed.
   - PulseBarrage spawns 3 projectiles with ±10° spread.
   - All others spawn 1.
4. Set PierceCount based on ability:
   - PiercingRound: PierceCount = 99
   - RailShot:     PierceCount = 2
   - All others:   PierceCount = 0
5. Lifetime = params.Range / params.ProjectileSpeed.
```

The Projectile + ProjectileSystem already handle collision and damage; this just changes the spawn parameters and adds pierce handling on hit.

### `dispatchSkillshotGround`

```
1. Get caster position
2. Compute distance = ‖(action.aimX, action.aimY) - casterPos‖
3. Clamp aim point to ability range:
   if distance > params.Range:
     scale = params.Range / distance
     aimX = casterX + (aimX - casterX) × scale
     aimY = casterY + (aimY - casterY) × scale
4. Spawn AoEMarker at (aimX, aimY) with Lifetime = params.GroundCastDelay (NEW
   AbilityParams field; ~0.6s for MortarShell so a player crossing the AoE
   has a moment to dodge). The visible "is about to explode" window matches
   the same field used by AoESystem to gate damage application.
5. AoESystem resolves on expiry as today.
```

For MortarShell, the player no longer has to lock a target — just point and drop.

### `dispatchSkillshotChannel`

```
1. Reject if caster already has a Channeling component (prevent double-channel).
2. Spawn a Channeling component with SlotID = action.slot, RemainingTime =
   params.ChannelDuration, AimX/AimY = action.aimX/Y, NextTickIn = 0.
3. Return false (no immediate cooldown — applied on channel end).
```

`tickChannels` updates: target resolution changes from "look up entity by TargetNetID" to "compute target along aim direction." Per tick:
```
1. Cursor direction = normalize(AimX - casterX, AimY - casterY).
2. Determine if caster's facing is within params.BeamHalfArcRad of the cursor direction
   (still allows the player to "wiggle" their cursor while channeling without dropping
   the beam, but a hard 180° flip drops it).
3. Hitscan along the line from caster forward params.Range. The **first** non-owner
   entity intersected gets params.Damage every NextTickIn period — single-target per
   tick, matching the existing SustainedBeam behavior. Multi-target sweeps would
   require a separate ability mode.
4. If no target hit: tick still passes, channel continues (beam visible in empty space).
   If channel duration expires OR caster dies: end channel, set cooldown.
```

The `ChannelAim` message updates `Channeling.AimX/AimY` between ticks so the player can drag the beam across targets.

---

## 4. Client Aim Indicators

New file: `web-pixi/src/effects/aim-indicator.ts`.

A single component instance lives at the game scene level. Each tick it reads `state.aimingSlot` (the currently aimed slot, or 0 if none) and renders the appropriate preview using PixiJS Graphics.

### Aim state machine (client-side)

```
state.aimingSlot: number = 0  // 0 = not aiming; 1..6 = currently aiming this slot

Press ability key (Q/W/E/R/D/F):
  if state.aimingSlot === slot: fire immediately (toggle-off equivalent)
  elif state.aimingSlot !== 0: cancel previous aim, then start aiming new slot
  else: start aiming this slot

  If the slot's ability has TargetingMode in {Self, LockOn} OR quickcast is enabled
  for this slot OR the slot is on cooldown: fire immediately (don't enter aim-state).

Release ability key OR press the same key again OR left-click while aiming:
  → send CastAbility with the current cursor position
  → state.aimingSlot = 0

Press another ability key while aiming:
  → state.aimingSlot = new slot (replace, don't double-fire)

Press Escape OR right-click while aiming:
  → state.aimingSlot = 0 (cancel, no fire)
```

### Per-mode visuals

- **SkillshotLine**: ray from ship muzzle along (cursor - shipPos) direction, length = `params.Range`, width = 2 × the ProjectileSystem hit-radius constant (currently 4u; lives in `internal/game/system_projectile.go`, not per-ability). Faint glow + bright outline. Color from slot.
- **SkillshotGround**: circle at cursor (clamped to `params.Range` from ship), radius = `params.SplashRadius`. Filled red/orange. Additionally, a thin range-ring around the ship shows the max cast distance.
- **SkillshotChannel**: same ray as SkillshotLine; updates each frame as cursor moves. Subtle pulsing.
- **Cooldown overlay**: when aiming a slot that's on cooldown, the indicator is greyed-out with a remaining-time number. Pressing fires nothing (no queue).

### Quickcast settings

A new settings panel section (the existing settings UI is in `web-pixi/src/ui/`):

```
Skillshot Cast Mode
  ☐ Q  ☐ W  ☐ E  ☐ R  ☐ D  ☐ F
  (checked = quickcast: press fires immediately)
```

State stored in `localStorage` under `"skillshot.quickcast"` as a 6-bit mask. Default: all off (aim-confirm everywhere).

---

## 5. Brawler Dual-Attack

`NPCAI` extension: a second cooldown/timer for the special attack.

```go
type NPCAI struct {
    // ... existing fields ...
    SpecialCooldown   float32 // PVE v3: time until next special attack (Brawler line-cone)
    SpecialWindup     float32 // PVE v3: >0 = winding up the special
    SpecialDirAngle   float32 // PVE v3: direction the line-cone is aimed
    SpecialTelegraphNetID uint32 // PVE v3: in-flight telegraph entity for the special
}
```

New `GameConfig` fields:

```go
BrawlerSpecialCooldown   float32 // seconds between line-cone attacks
BrawlerSpecialWindupTime float32 // 0.8s — visible telegraph window
BrawlerSpecialLength     float32 // 50u — line-cone length
BrawlerSpecialHalfWidth  float32 // 5u — half-width of cone at tip
BrawlerSpecialDamage     float32 // higher than auto-attack (~25)
```

### Behavior in `tickEngage`

```
After existing hitscan fire block:

1. SpecialCooldown -= dt
2. If SpecialCooldown <= 0 AND SpecialWindup <= 0 AND target locked + in range:
   - Snapshot direction toward target's CURRENT position.
   - Spawn LineTelegraph (rename of LanceTelegraph for generality) with:
       - direction = snapshot
       - length = BrawlerSpecialLength
       - halfWidth = BrawlerSpecialHalfWidth
       - lifetime = BrawlerSpecialWindupTime
   - Set SpecialWindup = BrawlerSpecialWindupTime, store telegraph netID.
3. If SpecialWindup > 0:
   - SpecialWindup -= dt
   - If SpecialWindup <= 0:
       - Resolve: hitscan check along the line from Brawler position in SpecialDirAngle.
         Each non-Brawler entity inside the rectangle takes BrawlerSpecialDamage.
       - Despawn the telegraph (it expires anyway, but explicit cleanup).
       - SpecialCooldown = BrawlerSpecialCooldown.
```

### LanceTelegraph → LineTelegraph (rename)

The `LanceTelegraph` entity kind already does exactly what Brawler's special needs: position + rotation + length + halfWidth + lifetime telegraph rectangle. Rename:

- `KindLanceTelegraph` → `KindLineTelegraph`
- `LanceTelegraphBundle` → `LineTelegraphBundle`
- `LanceTelegraphSpec` → `LineTelegraphSpec`
- `LanceTelegraphEntity` (client SDK) → `LineTelegraphEntity` (regenerated)
- `entity_lance.go` stays the filename (LineTelegraph is used by both Lancer + Brawler now); update the comment block.
- Lancer windup code keeps the same call site, just imports the renamed types.

The rename is a wire-stable swap (same KindID byte = 8). Aesthetically the entity now serves both Lancer's charge AND Brawler's special — name should reflect that.

---

## 6. Tuning

### Player abilities (cooldown + damage shifts where the new mode demands)

| Ability | Mode | Range | Damage | Cooldown | Notes |
|---|---|---|---|---|---|
| PulseLaser | Line | 30 | 30 | 1.0s | Basic skillshot |
| PulseBarrage | Line × 3 | 40 | 20 each | 4.0s | 3-shot cone |
| RailShot | Line (pierce 2) | 60 | 50 | 3.0s | Long-range piercer |
| PiercingRound | Line (pierce-all) | 35 | 40 + 15 vs unshielded | 8.0s | Big finisher line |
| IonBurn | LockOn DoT | 30 | DoT 6/s × 4s | 6.0s | unchanged |
| IonOverload | Line | 30 | 40 | 8.0s | |
| PlasmaBolt | Line | 35 | 25 | 3.0s | |
| PlasmaTorpedo | LockOn | 40 | 60 + 30 vs unshielded | 12.0s | Lock-on nuke |
| PlasmaShot | Line | 40 | 30 | 1.0s | |
| HomingMissile | LockOn | 50 | 60 + 20 splash | 8.0s | unchanged |
| SustainedBeam | Channel | 30 | 4/tick × 10/s | 4s after channel | Beam tracks cursor |
| MortarShell | Ground | 40 | 60 + 40 splash (6u) | 6.0s | Cursor drops AoE |

### NPC: Brawler dual-attack defaults

```
BrawlerSpecialCooldown:   6.0  // s between specials
BrawlerSpecialWindupTime: 0.8  // visible telegraph
BrawlerSpecialLength:     50   // u
BrawlerSpecialHalfWidth:  5    // u (cone-like, widens at tip — visual hint only;
                               //  hit check is a uniform rectangle)
BrawlerSpecialDamage:     25
```

Brawler auto-attack damage stays at 8 DPS (1 Hz × 8 dmg). Special adds ~4 DPS averaged. Total Brawler DPS to player ≈ 12 with dodgeable upside.

---

## 7. Cooldown + Aim Edge Cases

- **Aiming a slot that goes on cooldown mid-aim**: aim continues to render (greyed). On release, server rejects (cooldown not 0), client gets no confirmation. State.aimingSlot resets locally.
- **Channel cast while aiming another slot**: starting a channel exits aim-state on any other slot.
- **Death while aiming**: aim-state resets on isDead transition.
- **Docked while aiming**: aim-state resets on dock transition.
- **Tab cycling while aiming**: tab cycling does NOT cancel aim-state. The current target lock is just metadata for `LockOn` abilities, not for skillshots.

---

## 8. Testing

### Unit (Go)

- `internal/game/system_ability_test.go` (new): per-mode dispatch produces the expected effect.
  - `Self` cast applies the status effect with no aim.
  - `LockOn` cast rejects without lock; resolves to lock target's pos with lock.
  - `SkillshotLine` cast spawns a Projectile with correct velocity vector.
  - `SkillshotGround` cast spawns an AoEMarker at clamped position.
  - `SkillshotChannel` cast adds a Channeling component with the aim coords.
- `internal/game/system_projectile_test.go`: extend `TestProjectile_PierceCount` — a projectile with `PierceCount=2` damages two NPCs and despawns on the third.
- `internal/game/encounter_test.go`: extend the Brawler test to assert the special telegraph fires on cooldown.

### Integration (existing tests)

The fairness invariant test still applies but only to AoEs (Artillery, Kamikaze-was). Brawler's special is `FairnessEffort` — base speed × windup ≥ halfWidth × 0.4 → 6 × 0.8 = 4.8u ≥ 5 × 0.4 = 2u. Passes.

### Manual smoke

- `just dev` → log in → equip the new weapons.
- Press Q with PulseLaser → should enter aim-state, show line indicator.
- Move cursor → indicator follows cursor.
- Release Q → projectile fires along line.
- Toggle quickcast for Q in settings → press Q → fires immediately at cursor.
- Press E (MortarShell) → aim circle appears at cursor, clamped to range ring.
- Click left or release E → AoE drops.
- Press R (SustainedBeam) → channel starts, beam tracks cursor.

---

## 9. Rollout

- **No backward compat.** Solo dev, no live users. `CastAbility` wire format changes; client and server update together.
- **`ConfigVersion` bump.** Adds Brawler special config fields.
- **SDK regen** required (new entity kind name, new fields on CastAbility, new ChannelAim message).
- **Single PR.** Player and server are tightly coupled on the new aim semantics; ship together.

---

## 10. Out of Scope (deferred)

- **Combo / cancel-cast**: cancelling an animation by starting another, or chaining abilities. Modern.
- **Smart cast variants**: LoL's "smart cast self" (e.g. Cleanse on Q + S keypress = always self). Add later if useful.
- **Targeting keybind rebinder**: settings UI to change which key is which slot.
- **NPC SkillshotGround attacks**: Artillery already does this in a different way (AoE marker on player snapshot); not unifying yet.
- **Per-slot quickcast hold-modifier**: e.g. shift+Q = aim-confirm even if Q is in quickcast mode. Defer.
- **PVP balance**: all numbers are tuned for PvE only; PVP pass is a separate effort.

---

## Implementation Order (rough)

1. Add `TargetingMode` enum + field + classify all abilities.
2. Extend `CastAbility` wire message with `AimX`/`AimY`. Update client send-sites.
3. Extend `AbilitySystem.executeAbility` with the 5-way dispatch (Self/LockOn already exist; Line/Ground/Channel are new branches).
4. Extend `ProjectileSpec` with `PierceCount`; update ProjectileSystem hit logic.
5. Rename `LanceTelegraph` → `LineTelegraph` across codebase.
6. Add Brawler special config + tickEngage branch + LineTelegraph hitscan resolve.
7. Add `ChannelAim` message + extend `Channeling` component + update `tickChannels` to read aim coords.
8. Client: `aim-indicator.ts` + aim-state machine + per-mode visuals.
9. Client: quickcast settings + persistence.
10. SDK regen.
11. Tests + smoke.
