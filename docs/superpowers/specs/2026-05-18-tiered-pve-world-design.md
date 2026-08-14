# Tiered PvE World Design

**Date**: 2026-05-18
**Status**: Design — awaiting plan + implementation

## Goal

Make the world feel alive between major POIs by adding a continuous tier-based PvE content gradient anchored at the station. Today, ~70% of cells have no content; the player rides for ~2 minutes between encounters. After this change, density scales with distance from the station (dense near, sparse far), and mob difficulty + reward scale up as the player pushes outward.

This is a v1 PvE-only feature. PvP zones, dynamic events, derelicts, and roaming NPCs are explicitly out of scope.

## Non-goals

- New POI archetypes (derelicts, distress beacons, salvage POIs).
- Roaming / wandering NPCs between POIs.
- Time-driven dynamic events (pirate raids, distress beacons, world bosses on timers).
- PvP zones, full-loot, safezone enforcement.
- Cell-size changes — content density is fixed inside the existing 8192u cells.
- Hidden caches, treasure maps, world bosses.

## Architecture

A **tier** is a function of world position, completely decoupled from cell partitioning:

```
tier(pos) = clamp(floor(dist(pos, stationCenter) / TierWidth), 0, 2)
```

Three tiers, concentric radial bands centered on the station:

| Tier | Inner radius | Density | Roster shape | Stat mul | Reward mul | Cooldown |
|------|-------------:|---------|--------------|---------:|-----------:|---------:|
| T1   | 0u           | 3–5 POIs/cell | Small skirmish (1–2 mobs) | 1.0× | 1.0× | 180s |
| T2   | 16384u       | 1–2 POIs/cell | Medium warband (4–5 mobs, possibly Disruptor) | 1.5× | 2.5× | 300s |
| T3   | 32768u       | 0–1 POIs/cell | Heavy battalion / Elite Anchor (7–10 mobs, Support, BossGuardian) | 2.5× | 6.0× | 900s |

Tier boundaries are invisible to gameplay: no hard gates, no warnings. The player learns by losing fights. Optional client debug overlay (toggled by the existing Backquote dev overlay) renders tier rings.

The station cell stays special — the existing testsite dungeon owns it, plus the T1 ring naturally surrounds it via distance-based tier computation.

## Data model

### TierDef table

`internal/game/poi_config.go` gains:

```go
type TierDef struct {
    Tier            uint8     // 1..3
    InnerRadius     float32   // world units; outer is next tier's inner
    POIsPerCell     [2]int    // [min, max] inclusive
    StatMultiplier  float32   // applied to HP / damage / shield at NPC spawn
    FluxRewardMul   float32   // applied to POIBaseClearFlux + PerKillFluxBonus
    Rosters         []uint16  // roster def indices eligible at this tier
    CooldownSec     int32     // repopulation cooldown per tier
}

var tierTable = []TierDef{
    {1, 0,     [2]int{3, 5}, 1.0, 1.0, []uint16{SmallSkirmishIdx, StarterArenaIdx},   180},
    {2, 16384, [2]int{1, 2}, 1.5, 2.5, []uint16{MediumWarbandIdx, DisruptorCellIdx},  300},
    {3, 32768, [2]int{0, 1}, 2.5, 6.0, []uint16{HeavyBattalionIdx, EliteAnchorIdx},   900},
}
```

The legacy `NonStationCellPOIClearCooldown` config field is preserved but is now a fallback only (e.g. for testsite dungeon or game-mode overrides). Tier cooldown table takes precedence at runtime.

### New roster defs

Added to `rosters` in `poi_config.go`. The original `"Starter Arena"` is retained but renamed conceptually to T1/T2 baseline; new entries:

- **SmallSkirmish** (T1): 1 Lancer + 1 Brawler
- **StarterArena** (T1 fallback / T2 lower-end): 1 Artillery + 2 Brawlers + 3 Lancers (existing)
- **MediumWarband** (T2): 2 Lancer + 2 Brawler + 1 Artillery
- **DisruptorCell** (T2): 1 Disruptor + 2 Lancer + 1 Brawler
- **HeavyBattalion** (T3): 3 Brawler + 2 Artillery + 2 EliteLancer
- **EliteAnchor** (T3): 1 BossGuardian + 1 Support + 2 EliteBrawler + 2 EliteArtillery

Each roster is named by its index constant for compile-time safety.

### POI component

`gamecomp.POI` gains a `Tier uint8` field, serialized with `net:"initial"` (sent once on visibility enter; tier is immutable per POI).

### POIDef

`poi_gen.go` POIDef gains a `Tier uint8` field; `SpawnPOI` accepts and stores it.

## Tier function

New file `internal/game/tier.go`:

```go
// TierForCellCenter computes the tier of a cell's center.
// Used by POI generation to roll POI count.
func TierForCellCenter(cell, stationCell mmokit.CellCoord) uint8 { ... }

// tierForDist computes tier from a raw world-space distance from the station.
// Used per-POI after placement to determine roster/stat/reward.
func tierForDist(d float32) uint8 {
    for i := len(tierTable) - 1; i >= 0; i-- {
        if d >= tierTable[i].InnerRadius { return tierTable[i].Tier }
    }
    return 1
}

// distFromStation returns the absolute world-space distance from a position
// to the station center (StationCell origin + StationPOIOffset).
func distFromStation(cell mmokit.CellCoord, localX, localY float32,
                     stationCell mmokit.CellCoord) float32 { ... }
```

## POI generation

`internal/game/poi_gen.go` rewritten:

```go
func GeneratePOIs(cell, stationCell mmokit.CellCoord,
                  cfg *GameConfig, belts []AsteroidBelt) []POIDef {

    // Station cell is dungeon-owned, no POIs.
    if cell == stationCell {
        return nil
    }

    // Cell-tier (coarse) drives POI density rolling.
    cellTier := TierForCellCenter(cell, stationCell)
    td := tierForDef(cellTier)

    rng := rand.New(rand.NewPCG(hashCell(cell, "poi"), 1))
    n := td.POIsPerCell[0] + rng.IntN(td.POIsPerCell[1] - td.POIsPerCell[0] + 1)
    if n == 0 {
        return nil
    }

    defs := make([]POIDef, 0, n)
    for i := 0; i < n; i++ {
        x, y, ok := placePOI(rng, cfg, belts, defs)
        if !ok {
            continue
        }
        // Per-POI tier from the POI's actual world position, not the cell.
        worldDist := distFromStation(cell, x, y, stationCell)
        poiTier := tierForDist(worldDist)
        roster := pickRosterForTier(rng, poiTier)
        defs = append(defs, POIDef{X: x, Y: y, Type: 0, RosterIdx: roster, Tier: poiTier})
    }
    return defs
}
```

### Placement constraints

`placePOI` is a new helper (lifted from the current inline rejection-sampling loop), extended with:

- Existing: belt clearance (`POIBeltClearance`), cell margin (`POIPlacementMargin`).
- New: `POIIntraCellClearance` (~1000u) — reject if the candidate is closer than this to any previously-placed POI in the same cell.
- Existing: station clearance (`POIStationClearance`) for cells near the station cell.

`pickRosterForTier(rng, tier)` picks one of `tierTable[tier].Rosters` uniformly. Future iterations can add weights.

### Cell tier vs POI tier (rationale)

Cell-tier is computed from cell-center distance — too coarse to use for roster/stat assignment when a cell straddles a tier boundary. Per-POI tier handles boundaries: a cell straddling T1/T2 rolls its *count* from one cell-tier but its individual POIs may end up T1 or T2 based on actual position. This softens the boundary visually and mechanically — no abrupt difficulty wall.

### Stat scaling at NPC spawn

`NPCSpawnModifiers` already exists in `entity_npc.go` with an `Elite bool` flag that multiplies HP/Damage/Speed via `Config.BossSolo*Multiplier`. This design extends it with explicit multiplier fields so tier-based scaling composes cleanly with the existing Elite path:

```go
type NPCSpawnModifiers struct {
    Elite    bool     // existing — multiplies via BossSolo* multipliers
    Main     bool     // existing — BossGuardian flag
    HPMul    float32  // NEW — tier-based; multiplied AFTER Elite scaling. Zero = 1.0×.
    DmgMul   float32  // NEW
    ShieldMul float32 // NEW
}
```

`spawnPOIRoster` reads `poiTier` from the `POIDef` and passes `tierTable[poiTier].StatMultiplier` into all three Mul fields. `SpawnNPC` applies these multipliers after the existing Elite scaling so an `Elite + tier-multiplier` stacks correctly (T3 EliteLancer = base × BossSoloHPMul × T3.StatMul = 1.0 × 1.5 × 2.5).

### Reward scaling on POI clear

`system_poi.go::onClear` multiplies the existing flux calculation by `tierTable[poi.Tier].FluxRewardMul`:

```go
bounty := int32(float32(gw.Config.POIBaseClearFlux + gw.Config.POIPerKillFluxBonus*int32(rosterCount))
              * tierTable[poi.Tier].FluxRewardMul)
```

Tier-aware cooldown: `poiCooldownSec(gw, poi.Tier)` replaces the current station/non-station binary.

## New archetypes

### Support (T3-only — EliteAnchor)

**Constant**: `ArchetypeSupport`

**Stats** (config):
```go
SupportHP             float32
SupportShield         float32
SupportMaxSpeed       float32   // ~5u/s — slow, stays back
SupportTurnRate       float32
SupportHealRange      float32   // ~200u
SupportHealAmount     float32   // ~40 HP
SupportHealCooldown   float32   // 4s
SupportRetreatDist    float32   // 60u — kite if player closes inside
```

**AI behavior** (`system_npc_ai.go` gains a Support state branch):

1. On spawn: pick the highest-HP same-roster member as "anchor"; orbit it at ~80u.
2. Every tick: if any player is within `SupportRetreatDist`, kite away (move opposite from nearest player). Otherwise, return toward anchor.
3. Every `SupportHealCooldown` seconds: scan same-roster members within `SupportHealRange`. Pick the one with lowest HP%. If picked target HP < 100%, fire `verb_heal` to that target for `SupportHealAmount` HP. Emit a beam event for client visualization (re-uses beam-clip event infra from commit `17a080b`).

**`verb_heal`**: new verb file `verb_heal.go`. Mirrors `verb_damage.go` but adds to `Health.Current` (capped at `Health.Max`). Tested in `verb_heal_test.go`.

**Threat priority**: player AI does not auto-target Support — the player must manually click it. This is the intended pressure.

### Disruptor (T2 + T3 — DisruptorCell, EliteAnchor)

**Constant**: `ArchetypeDisruptor`

**Stats** (config):
```go
DisruptorHP                 float32
DisruptorShield             float32
DisruptorMaxSpeed           float32   // ~7u/s
DisruptorTurnRate           float32
DisruptorAttackRange        float32   // ~150u (between Brawler 60u and Artillery 300u)
DisruptorDebuffCooldown     float32   // 6s
DisruptorSlowDuration       float32   // 3s
DisruptorSlowFactor         float32   // 0.5 (50% movement reduction)
DisruptorSilenceDuration    float32   // 2s
```

**AI behavior**: same Engage/Approach/Leash as existing Brawler, kept at medium range (`DisruptorAttackRange`).

**Debuff ability**: every `DisruptorDebuffCooldown` seconds, cast a slow skillshot at current target via existing skillshot system. On hit, applies two status effects through `StatusEffectSystem`:

- **StatusSlow** (NEW — `gamecomp.StatusType` enum currently has IonBurn/Fortified/Afterburner/ShieldRegen; Slow is a new value): `DisruptorSlowFactor` movement multiplier for `DisruptorSlowDuration`. Implementation mirrors `StatusAfterburner` (also a speed multiplier, in the opposite direction).
- **StatusSilence** (NEW): disables player ability casts for `DisruptorSilenceDuration`. New value on the same enum. Cast-time check added to ability dispatch path.

**Skillshot semantics**: slow projectile, dodgeable. Uses existing line-telegraph entity. Renders via existing skillshot UI.

### EliteX variants (T3)

`ArchetypeEliteLancer`, `ArchetypeEliteBrawler`, `ArchetypeEliteArtillery`. Behavior identical to base archetype; stat multipliers stacked on top of tier multiplier (e.g. base × tier × elite = 1.0 × 2.5 × 1.3 = 3.25× HP for an EliteLancer in T3). No new AI code. May get one extra ability later but v1 is stat-only differentiation.

## Wire format

`gamecomp.POI` gains `Tier uint8` with `net:"initial"`. The SDK regenerates automatically via `just build` / `just space-sdk`. No hand-patching.

Schema-runtime invariant (per memory `feedback_wire_format_schema_runtime_match`): `Tier` is unconditionally included in the EngineBindingsConfig. No runtime branching.

## Web client

`web-pixi/src/entities/poi.ts` (or wherever POI rendering lives):

- Tier badge color on POI marker:
  - T1 = `#5bd078` (green)
  - T2 = `#e8b53b` (amber)
  - T3 = `#d04545` (red)
- Tier label text on hover or select.
- Marker radius scales with roster size (read from existing POI metadata) — small/medium/large visual cue.

Optional debug overlay (Backquote toggle, commit `e922843`):

- Two semi-transparent circles at radii `tierTable[1].InnerRadius` and `tierTable[2].InnerRadius` from the station's world position.
- Helps designers verify boundary placement during smoke testing.

NPC AI changes (Support, Disruptor) need no client-side rendering work beyond what's already there:

- Support's heal beam re-uses the beam event infra → existing client beam renderer handles it.
- Disruptor debuffs render via existing status-icon UI (no new icons needed for `StatusSilence` if the design accepts re-using the generic "disabled" icon, or add one new icon — minor work).

## Config

New config fields in `internal/game/config.go`:

```go
// Tier system
TierWidth                  float32  // 16384 — unused at runtime (table is authoritative),
                                    // kept for designer reference
POIIntraCellClearance      float32  // 1000

// Support
SupportHP, SupportShield, SupportMaxSpeed, SupportTurnRate float32
SupportHealRange, SupportHealAmount, SupportHealCooldown   float32
SupportRetreatDist                                          float32

// Disruptor
DisruptorHP, DisruptorShield, DisruptorMaxSpeed, DisruptorTurnRate     float32
DisruptorAttackRange, DisruptorDebuffCooldown                           float32
DisruptorSlowDuration, DisruptorSlowFactor                              float32
DisruptorSilenceDuration                                                float32

// Elite multipliers (stacked on tier multiplier)
EliteStatMultiplier        float32  // ~1.3 — applied to EliteX archetypes
```

## Testing

### Unit

- **`poi_gen_test.go`**: extend with tier cases:
  - T1 cells (near station) yield 3–5 POIs.
  - T2 cells yield 1–2.
  - T3 cells yield 0–1.
  - Boundary cells produce a mix of tiers for individual POIs.
  - Station cell still returns nil.
- **`tier_test.go`** (new): `tierForDist` correct at each boundary (epsilon below/above). `TierForCellCenter` consistent with `tierForDist` at cell midpoints.
- **`verb_heal_test.go`** (new): adds HP, caps at Max, emits the heal beam event, respects target alive check.
- **`npc_archetype_support_test.go`** (new): Support picks lowest-HP roster ally, ignores out-of-range allies, doesn't heal self, respects cooldown, kites away from close player.
- **`npc_archetype_disruptor_test.go`** (new): casts skillshot at target on cooldown, on hit applies Slow + Silence, telegraph fires correctly.
- **`verb_status_test.go`** (existing): extend with `StatusSlow` (movement multiplier applied + expires) and `StatusSilence` cases (blocks ability cast + expires).

### Integration

- Spawn POI at T3 distance from station, verify roster uses EliteAnchor archetype mix.
- Spawn POI at T1, kill roster, assert loot crate flux ≈ `POIBaseClearFlux × 1.0 × (1 + rosterCount × PerKillBonus%)`.
- Spawn same-roster at T3 distance, assert reward ≈ 6× T1 amount.
- Verify Support inside a roster heals injured ally and measurably increases TTK vs. same roster without Support.

### Smoke

- `just dev`, scout T1 ring (lots of small camps), T2 ring (mid-sized fights with occasional Disruptor encounters), T3 ring (rare destination POIs with Support + BossGuardian).
- Verify tier badge colors on POI markers.
- Verify cell-straddling cells contain mixed-tier POIs.
- Confirm Backquote debug overlay renders the two tier rings.
- Confirm `cell list` and `dungeon list` still work; no regression in existing combat/dungeon flows.

## Risks and open questions

- **Reward inflation**: 6× T3 reward multiplier × possible large roster could swing flux income up faster than the economy is balanced for. Mitigation: integration test asserts upper bound; tunable via `FluxRewardMul` per-tier; can dial down post-smoke.
- **Performance with denser POIs**: T1 cells could have 5 POIs × ~5 roster size = 25 NPCs per cell, plus existing systems. Should be well within current handling (each POI roster is ~6 NPCs today, scattered across all non-station cells). Monitor `perf` console output during smoke.
- **`StatusSilence` interaction with abilities**: existing ability system needs a check at cast time. Need to confirm no other status currently does this — easy add but it's a new code path.
- **Support targeting**: relies on roster cohesion. If roster members spread far apart due to combat, Support might fail to find a heal target. Acceptable behavior — Support just waits — but worth checking the heal radius matches typical roster spread (~200u vs spread radius 25–40u in current rosters).
- **Tier function and dynamic partitioning**: tier uses world-space distance, so cell splits don't change a POI's tier mid-life. Correct by construction; covered by integration test.

## Implementation order (rough)

1. Tier infrastructure (`tier.go`, `TierDef` table, `POI.Tier` field + wire export).
2. POI generation rewrite (`poi_gen.go`) — start emitting tiered POIs.
3. Stat + reward scaling at spawn / clear.
4. New rosters (`poi_config.go` additions).
5. `verb_heal` + Support archetype + AI branch.
6. `StatusSilence` + Disruptor archetype.
7. Web client tier badges + optional debug overlay.
8. Test passes throughout; smoke at the end.
