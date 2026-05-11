# Combat POI System & EVE-Style Multi-Target Locking

**Status:** Design
**Date:** 2026-05-12

## 1. Overview

Three interlocked subsystems ship together as the first PvE content for the space game:

1. **POI (Point of Interest) framework** — a reusable server-side framework for procgen world content. First POI type is the **combat site**: a fixed location with a roster of hostile NPCs that defend it, drops a bounty crate on clear, repopulates after a cooldown.
2. **NPC AI** — three archetypes (Brawler / Sniper / Swarmer) sharing one state-machine driver, with leashing, target switching, and aggro de-escalation.
3. **Multi-target locking** — EVE-style parallel target locking with active-target selection; replaces the current single-target `TargetLock`.

The three are interdependent: NPC AI is the consumer of multi-target locking from the NPC side (with MaxSlots=1) and from the player side (focusing fire across an encounter); the POI framework anchors and respawns the NPCs. They are designed and reviewed as one feature.

## 2. Goals & Non-Goals

### Goals

- A reusable POI framework that future PvE content (mining anomalies, salvage wrecks, faction outposts, etc.) can implement.
- A "living" combat site: enemies you find, fight, clear, then return to fight again after a cooldown.
- Three NPC archetypes with archetype-distinct combat behavior off a shared codepath.
- EVE-style multi-target locking: 4 parallel locks, active target select, click to switch.
- All combat/AI numbers tunable from `GameConfig` (live tuning via the existing config console).

### Non-Goals (v1)

- POI instancing (open-world only; loot griefing is a feature, see §6.4)
- Group / party mechanics
- POI difficulty scaling to player level / ship class
- Scanner / probe discovery (POIs auto-reveal on the map)
- Mission acceptance, factions, reputation
- ECM, ScanResolution, SigRadius lock mechanics (schema-forward but hardcoded values for v1)
- Support / logi NPC archetype (deferred to v2 — see §11)
- Per-archetype NPC variant pool ("3-of-7" roster rolls, deferred to v2)
- Behavior-tree / utility AI (FSM-only for v1; data-driven transitions reserve the option, see §11)

## 3. System 1: POI Framework

### 3.1 Concept

A POI is a first-class ECS entity with `EntityKind = KindPOI`. It is replicated to clients via the standard AoI path — the marker on the player's HUD/map is just the POI entity being rendered. The POI entity persists across the full lifecycle (Active → Cleared → Cooldown → Active …) so the marker stays visible at all times.

The roster NPCs that defend a POI are separate ECS entities (extended `KindNPC`) that hold a reference back to their POI via a `POIAnchor` component.

### 3.2 Components

```go
// internal/component/components.go
type POI struct {
    Type         POIType    `net:"u8"`   // Combat for v1
    Status       POIStatus  `net:"u8"`   // Active | Cleared | Cooldown
    AnchorRadius float32                  // visual + initial-spawn radius (local-only, not replicated)
    LeashRadius  float32                  // local-only
    ClearedAt    int64                    // unix nanos; local-only
    RosterDefIdx uint16                   // index into POIConfig.Rosters; local-only
}

type POIAnchor struct {
    POINetID uint32  // local-only — used by AI for leashing
}

type Leashing struct {
    // transient marker added when an NPC is in the Leash state
    // makes NPC invulnerable + untargetable + 2× speed return
}
```

`POIType` and `POIStatus` are `uint8` enums in `internal/component/components.go`.

### 3.3 POIBundle (entity kind)

```go
type POIBundle struct {
    POI *POI
}
```

Registered via `mmokit.RegisterKind[POIBundle](coord, KindPOI, "POI", bindings)`.

### 3.4 Procgen placement

A new `GeneratePOIs(cell, stationCell mmokit.CellCoord) []POIDef` function in `internal/game/poi_gen.go`, mirroring `belts.go`:

- Deterministic FNV(cellX, cellY, "poi") seed per cell.
- **Station cell**: 1 guaranteed combat POI, placed at distance > `StationRadius + 400u` from the station and > `40u` from any belt center+radius. This is the starter arena.
- **Non-station cells**: 0 or 1 combat POI with probability `POIPerCellProbability` (default 0.3).
- Placement constraints: cell margin (200u), avoid belts (40u clearance), avoid station radius (400u clearance).

`POIDef` carries `Position`, `Type`, `RosterDefIdx`. POIs are spawned during cell bootstrap (alongside belts/asteroids) by a new `gw.SpawnPOI(def)`.

### 3.5 Lifecycle

```
Active ──(roster empty)──▶ Cleared ──(spawn loot crate)──▶ Cooldown ──(cooldown elapsed)──▶ Active
```

The `POISystem` (new tick system, runs late in the system order):

1. For each `Status=Active` POI: scan its tracked roster NetIDs; if all are dead/gone, transition to Cleared.
2. On entering Cleared: spawn one `LootCrate` at POI center with contents `baseFlux + perKillBonus * rosterSize`. Set `ClearedAt = now`. Transition to Cooldown.
3. For each `Status=Cooldown` POI: if `now - ClearedAt > cooldown`, transition to Active and respawn roster.

Roster tracking: the POI holds a `[]uint32` of currently-alive roster NetIDs in a parallel server-only map (`gw.poiRosters map[uint32][]uint32`). Updated on spawn and on NPC death (via `OnEntityRemoved` hook).

Cooldown:
- Station-cell POI: `StationCellPOIClearCooldown = 180s` (3 min — tutorial-friendly, prevents new players hitting "Cleared" repeatedly)
- All other POIs: `NonStationCellPOIClearCooldown = 600s` (10 min)

### 3.6 Roster definition

`internal/game/poi_config.go` holds a list of `RosterDef` entries:

```go
type RosterDef struct {
    Name    string
    Members []RosterMember
}

type RosterMember struct {
    Archetype NPCArchetype // Brawler | Sniper | Swarmer
    Count     int
}
```

V1 ships with one roster (`StarterArenaRoster`): 2 Brawlers, 1 Sniper, 3 Swarmers. All POIs use this roster in v1; per-POI variety is deferred to v2.

## 4. System 2: NPC AI

### 4.1 Archetypes

| Archetype | HP | Shield | Speed | MotionPolicy | PreferredRange | WeaponRange | GroupSize |
|---|---|---|---|---|---|---|---|
| **Brawler** | 400 | 200 | 6 u/s | `Charge` | 80 u | 100 u | 1 |
| **Sniper** | 150 | 100 | 8 u/s | `HoldRange` | 600 u | 800 u | 1 |
| **Swarmer** | 80 | 0 | 14 u/s | `Encircle` | 120 u | 150 u | 3 |

All values are initial defaults; all live in `GameConfig` for tuning.

### 4.2 State machine

States: `Idle | Acquire | Engage | Leash`

Reposition is **not** a separate state — kiting, charging, and encircling are different *motion policies* applied during Engage. This avoids oscillation at distance thresholds (the state machine doesn't flip between Engage and Reposition every tick at the preferred-range boundary).

Transitions:

```
Idle    ──(target in aggro radius)──▶ Acquire
Acquire ──(facing target ±15°)─────▶ Engage
Engage  ──(target dead/AoI-gone/dormant)─▶ Idle
Engage  ──(6s no damage dealt or received)─▶ Idle
Engage  ──(self dist from anchor > LeashRadius)─▶ Leash
Engage  ──(POI-wide leash trigger fires)─▶ Leash
Leash   ──(self at anchor)─────────▶ Idle
```

The NPC AI lives in a new system `NPCAISystem` in `internal/game/system_npc_ai.go`, queried over entities with `NPCAI` (new component holding state + archetype params) + `POIAnchor` + `Position` + `Velocity` + `Rotation` + `TargetLock`.

### 4.3 Acquire

Scan entities within `AggroRadius` (800u) for `KindShip` with `Health > 0` and not `Dormant`. Pick nearest. Set as TargetLock target (NPC's own lock with MaxSlots=1). Rotate toward target at archetype `TurnRate`. Transition to Engage when facing within ±15°.

### 4.4 Engage

Per-tick:

1. **Rotate** toward target at archetype turn rate.
2. **Apply motion policy** (computes desired velocity):
   - `Charge`: `vel = (targetPos - selfPos).normalize() * maxSpeed`
   - `HoldRange`: if dist < preferredRange − tolerance, `vel = (selfPos - targetPos).normalize() * maxSpeed`; if dist > preferredRange + tolerance, `vel = (targetPos - selfPos).normalize() * maxSpeed`; else `vel ≈ 0` (with tolerance 50u).
   - `Encircle`: `vel = tangent(targetPos, selfPos, ccw) * maxSpeed`; if dist not in [preferredRange ± tolerance], add radial component to converge.
3. **Fire ability** if `dist ≤ WeaponRange` AND facing target ±15° AND lock complete. Reuses `AbilitySystem`.
4. **Re-check target validity**: dead / AoI-gone / dormant → Idle.

### 4.5 Target switching

When the NPC takes damage from a non-current-target source:
- If attacker is alive AND attacker dist from NPC < current target dist from NPC → switch target to attacker (restart NPC's own lock).
- Otherwise keep current target.

Damage source is tracked via a new `LastDamageBy` (uint32 netID + timestamp) field on `NPCAI`. Damage handler updates it; AI tick reads + clears.

### 4.6 Aggro de-escalation

A 6s timer (`AggroDeescalationSec`) on `NPCAI.LastCombatActivityAt`. Reset on any damage dealt or received. If `now - LastCombatActivityAt > 6s` while in Engage: drop target, return to Idle.

This covers d/c, warps, player deaths, and "player ran away faster than NPC can chase" — all without separate code paths.

### 4.7 Leash

**Leash is always roster-wide** — there is no "this one NPC leashes alone" case. Triggers (any of):

- **Distance**: *any* roster member's distance from the POI anchor > `POI.LeashRadius` (1500u). All members leash simultaneously. Otherwise stragglers chase forever while the puller's group resets.
- **De-escalation**: 6s elapsed with no damage dealt OR received by *any* roster member (covers the "puller died / d/c / warped out" case in one rule, no separate code path).

**Behavior**:
- Add `Leashing` component to all affected NPCs (transient, removed on Idle).
- While `Leashing`: NPC is **invulnerable** (damage system early-returns) AND **untargetable** (target-lock system rejects + auto-breaks existing locks on this entity, similar to the Dormant path).
- Velocity: `vel = (anchor - selfPos).normalize() * maxSpeed * 2`.
- Rotation: face direction of travel.
- On reaching anchor (within `AnchorRadius`): remove `Leashing`, **restore Health and Shield to Max**, clear TargetLock, transition to Idle.

Restoring HP/shields on return is essential: without it, "chip → kite-out → leash → return → repeat" becomes a zero-risk farm exploit (classic MMO bug).

### 4.8 NPCAI component

```go
type NPCAI struct {
    Archetype             NPCArchetype // u8 — Brawler | Sniper | Swarmer
    State                 NPCAIState   // u8 — Idle | Acquire | Engage | Leash
    MaxSpeed              float32
    TurnRate              float32
    PreferredRange        float32
    WeaponRange           float32
    AggroRadius           float32
    MotionPolicy          MotionPolicy // u8 — Charge | HoldRange | Encircle
    LastDamageByNetID     uint32
    LastDamageAt          float32     // seconds since stage start
    LastCombatActivityAt  float32
}
```

All `*float32` defaults come from `GameConfig` at spawn time.

### 4.9 Replication

NPCs already replicate Position/Velocity/Rotation/Health/Shield. We add:

- `NPCAI.Archetype` (`net:"u8 initial"`) — for client-side sprite selection.
- `NPCAI.State` (`net:"u8"`) — clients can render state visually (e.g. Leashing = faded sprite). Optional; can defer if wire-size matters.
- `Leashing` is a component-presence flag; absence/presence is observable via standard component-add/remove replication.

## 5. System 3: Multi-Target Locking

### 5.1 Component change

Replace the existing `TargetLock` with:

```go
type TargetLock struct {
    Slots       []LockSlot
    ActiveNetID uint32  `net:"u32"`
    MaxSlots    uint8   // local-only — 4 for player ships, 1 for NPCs
    Range       float32
}

type LockSlot struct {
    TargetNetID    uint32  // local-only — replicated via LockSlotsMsg
    TargetEntity   ecs.Entity
    Progress       float32
    LockTime       float32
    Locked         bool
    // Forward-compatibility (v1 hardcoded; reserved for v2 sensor mechanics)
    ScanResolution float32
    SigRadius      float32
}
```

### 5.2 Slot rules

- Up to `MaxSlots` parallel locks; each slot progresses independently each tick.
- **5th-lock rejection**: when client sends `LockTarget` and `len(Slots) >= MaxSlots`, server responds with `LockRejectedMsg{Reason: TooManyLocks}`. Client plays an error sound; no auto-evict. (EVE behavior — explicit slot allocation; auto-evict is surprising in combat.)
- **Out-of-range cancel**: any slot whose target distance > `Range` is dropped immediately (no grace period). Matches EVE.
- **Target lifecycle**: slot dropped if target dies / leaves AoI / becomes dormant / becomes Leashing.
- **Lock-on-asteroid**: still uses faster `MiningLockTime` (current behavior); per-slot `LockTime`.

### 5.3 Wire inputs (replaces `SetLockTarget`)

```go
type LockTargetMsg     struct { NetID uint32 }
type UnlockTargetMsg   struct { NetID uint32 }
type SetActiveTargetMsg struct { NetID uint32 }
```

Server validates each:
- `LockTarget`: must be a `KindShip`/`KindNPC`/`KindAsteroid`, alive, non-dormant, in range, not already in slots, slots not full.
- `UnlockTarget`: must be in slots; removes the slot. If `ActiveNetID` was this target, run auto-fallback.
- `SetActiveTarget`: must be in slots AND `Locked=true`. Else server ignores (no error event).

### 5.4 Active-target auto rules

- When a slot completes locking (`Progress` first reaches 1.0): if `ActiveNetID == 0`, set `ActiveNetID = slot.TargetNetID`.
- When the active target's slot is dropped (any reason): set `ActiveNetID = mostRecentlyLockedSurvivingSlot.TargetNetID`, or 0 if none.
- "Most recently locked" tracked by slot append order — newest at the end.

### 5.5 Replication

- **Own-state replication** (player sees own locks): new server-to-client event `LockSlotsMsg`:

  ```go
  type LockSlotsMsg struct {
      Slots       []LockSlotWire
      ActiveNetID uint32
  }
  type LockSlotWire struct {
      TargetNetID uint32
      Progress    float32 // qnorm
      Locked      bool
  }
  ```

  Sent when slots change (add/remove/active-switch/lock-complete). Sent only to the owning player's connID.

- **Reverse-direction** (`LockedBy` "someone is locking me" ring): unchanged. Continues to show the highest-progress incoming lock against this entity.

### 5.6 Weapons / abilities

`AbilitySystem` reads `TargetLock.ActiveNetID` instead of the old `TargetLock.TargetNetID`. No other changes — all targeting logic remains the same.

For NPCs (MaxSlots=1), the single slot's target == ActiveNetID — same code path as players, no NPC-specific branch.

## 6. Wire / Client / Admin Surface

### 6.1 Wire protocol changes

| Change | Direction |
|---|---|
| New entity kind `KindPOI` | server→client (entity replication) |
| Remove `SetLockTarget` input | client→server |
| Add `LockTarget`, `UnlockTarget`, `SetActiveTarget` inputs | client→server |
| Add `LockSlotsMsg` event | server→client (per-player) |
| Add `LockRejectedMsg{Reason}` event | server→client (per-player) |

All wire format changes flow through the existing sdkgen — TS client SDK regenerates automatically via `just build`.

### 6.2 Client (`web-pixi`)

- **POI marker**: distinct sprite (e.g. red ring with skull) at POI center; rendered in-world + on the map view. Greyed-out tint when `POI.Status == Cleared`.
- **NPC archetype variants**: distinct sprites for Brawler / Sniper / Swarmer. v1 can be tints + scales of the existing ship sprite; proper sprites later.
- **Leashed NPC visual**: faded sprite + no reticle response (untargetable).
- **HUD locked-target list**: horizontal strip of small reticle icons in the corner of the HUD, one per locked slot, with the active slot highlighted (e.g. brighter border).
- **Input handlers**:
  - Click empty enemy → `LockTarget(netID)` (start locking)
  - Click locked enemy or its HUD icon → `SetActiveTarget(netID)`
  - Shift+click / right-click locked enemy or HUD icon → `UnlockTarget(netID)`
  - "Lock rejected" sound on `LockRejectedMsg`

### 6.3 Admin / console commands

New cmdsys verbs in `internal/game/commands/`:

- `poi.list` — list all POIs across cells with status + cooldown remaining
- `poi.clear <netID>` — force-clear (debug; spawns crate immediately)
- `poi.spawn <cellID> [type]` — manually spawn a POI for testing in a cell

All three follow the existing cmdsys pattern (typed Args/Result, capability gate, `cmdsys.OnLoop` for ECS access). Admin UI gets them for free via the admin command palette.

### 6.4 Loot model — single crate, contested

When a POI clears, a single `LootCrate` spawns at the POI center. Any player can pick it up (existing lootcrate pickup logic). Contesting the crate is intentional — fights over the drop are an emergent feature (Albion-style). No damage-share tracking, no per-player crates.

## 7. Tunables (`GameConfig`)

```
POI:
  StationCellPOIClearCooldown   = 180s
  NonStationCellPOIClearCooldown = 600s
  POIPerCellProbability         = 0.3
  POIPlacementMargin            = 200u
  POIBeltClearance              = 40u
  POIStationClearance           = 400u
  POIBaseClearFlux              = 500
  POIPerKillFluxBonus           = 100

Combat / AI:
  AggroDeescalationSec          = 6
  TargetSwitchEnabled           = true

Per archetype (Brawler/Sniper/Swarmer):
  HP, Shield, MaxSpeed, TurnRate
  AggroRadius, LeashRadius
  PreferredRange, WeaponRange
  MotionPolicy
  DamagePerShot, FireRate

Locking:
  PlayerMaxLockSlots = 4
  NPCMaxLockSlots    = 1
  LockOnTime         = 2.5s   (existing)
  MiningLockTime     = 1.5s   (existing)
```

## 8. Logging

New categories:
- `poi` — POI lifecycle (spawn, clear, cooldown start/end, repopulate)
- `ai` — NPC AI state transitions, target switch, leash trigger

Existing `combatLock` extends to multi-lock events.

All combat-relevant events log player + NPC NetIDs and quantities (per CLAUDE.md logging convention).

## 9. Testing

Unit tests in `internal/game/`:

- `system_npc_ai_test.go` — state transitions: Idle→Acquire→Engage→Leash→Idle for each archetype; target switching; aggro de-escalation timer; motion policy outputs.
- `system_targetlock_multi_test.go` — slot add/remove/full-rejection; parallel progress; auto-active rules; out-of-range cancel.
- `poi_lifecycle_test.go` — Active→Cleared→Cooldown→Active cycle; loot crate spawn on clear; roster respawn.
- `poi_leash_test.go` — individual leash, POI-wide leash trigger, HP/shield restore on return, invulnerability/untargetability while leashing.
- `poi_gen_test.go` — deterministic placement; respects margin/belt/station clearances; station-cell guaranteed POI.

Integration test (`examples/4node-basic`-side or similar):
- Player spawns near starter POI, kills roster, sees crate, waits cooldown, sees roster respawn.
- Player drags NPC outside leash radius, observes whole roster leash + HP/shield reset.

## 10. Implementation order

Suggested phasing (full breakdown in the implementation plan):

1. **Multi-target locking refactor first** — pure refactor of existing `TargetLock`, prerequisite for NPC AI (which uses the new component). Land + verify with existing single-target gameplay (player using only slot 0) before any AI work.
2. **NPC AI standalone** — `NPCAISystem` operates on hand-placed `SpawnNPC(x, y, archetype)` test NPCs anchored to a stub "POI" (no real POI yet). Validate all three archetypes' behaviors, leash, target-switch, de-escalation in isolation.
3. **POI framework** — `POI` entity, procgen, lifecycle system, loot drop. Wire NPC spawns to come from POI rosters instead of hand-placed.
4. **Admin / console** — `poi.list/clear/spawn` cmdsys verbs.
5. **Client surface** — POI sprite, multi-lock HUD strip, archetype sprites, leash visual.

## 11. Deferred / v2

Explicitly out of scope, but the design reserves room for each:

- **Per-archetype variant pool** (roll 3-of-7 NPC variants per spawn) — prevents anomaly-staleness. Easy add: roster definition expands to weighted-variant tables.
- **Support archetype** (logi/ECM-rat) — repairs / debuffs other roster members; motivates the multi-lock UX (priority targeting). Needed once players start asking "why bother locking more than one thing?"
- **Sensor mechanics**: `ScanResolution`, `SigRadius`, signature-based lock-time scaling. Schema already includes these fields; v2 turns them on by computing `LockTime = 40000 / (ScanResolution * asinh²(SigRadius))` per EVE.
- **Data-driven AI transition tables** — when `NPCAISystem` grows past ~5 states, lift transitions into a per-archetype config table. From there, the table is straightforwardly convertible to a behavior-tree selector. The FSM shell stays.
- **POI types beyond combat** — mining anomaly, salvage wreck, signal-source escalation, etc. The `POIType` enum + `POIBundle` framework is the extension point.
- **Group / party loot fairness** — only relevant once groups exist. Until then, contested-crate gameplay is the design.

## 12. Open questions / risks

- **Sprite work for archetypes** — v1 says "tint + scale ship sprite." That's ugly but functional. Real sprites are blocked on art; doesn't gate this feature.
- **Performance with many NPCs** — NPCAISystem is O(N_npcs × N_potential_targets) per tick for acquisition. At low counts (one POI = ~6 NPCs, a few POIs per cell), trivial. At high counts, may need spatial-hash filtering. Not v1.
- **Leash visual juddering** — invulnerable+untargetable+2× speed return may look weird on the client. May need a small "warp tunnel" visual or a smoothing pass on the client interpolation. Add to client-work iteration.
