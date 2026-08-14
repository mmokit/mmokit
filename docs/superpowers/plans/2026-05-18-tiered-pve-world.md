# Tiered PvE World Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a distance-from-station tier system that scales POI density (gradient: dense inner → sparse outer), mob difficulty (stat multipliers + Elite variants), reward, and cooldown across 3 tiers — plus two new archetypes (Support healer, Disruptor with Slow + Silence) to give T2/T3 distinctive encounter shapes.

**Architecture:** Tier is a pure function of world-space distance from the station, completely decoupled from cell partitioning. `TierDef` table in `poi_config.go` is the single authoritative knob. POI generation rolls density from cell-tier (coarse) but each POI's roster/stat/reward uses per-POI tier (fine, computed from POI position) so boundary cells naturally mix tiers. NPC stat scaling extends the existing `NPCSpawnModifiers` (Elite path stays untouched).

**Tech Stack:** Go (server-side ECS via mmokit/ark), TypeScript/PixiJS (web client), pgx (no DB schema changes).

**Spec:** `docs/superpowers/specs/2026-05-18-tiered-pve-world-design.md`

---

## File Structure

**Server (new):**
- `internal/game/tier.go` — TierDef table accessor, `tierForDist`, `TierForCellCenter`, `distFromStation`
- `internal/game/tier_test.go` — boundary tests
- `internal/game/verb_heal.go` — Heal verb (mirrors verb_damage.go)
- `internal/game/verb_heal_test.go`
- `internal/game/npc_archetype_support_test.go`
- `internal/game/npc_archetype_disruptor_test.go`

**Server (modified):**
- `internal/game/poi_config.go` — TierDef struct, tierTable, new roster constants + defs
- `internal/game/poi_gen.go` — multi-POI per cell + tier-aware roster + Tier field on POIDef
- `internal/game/entity_poi.go` — SpawnPOI accepts tier; spawnPOIRoster passes tier multiplier
- `internal/game/system_poi.go` — tier-aware reward multiplier + cooldown
- `internal/game/entity_npc.go` — NPCSpawnModifiers gains HPMul/DmgMul/ShieldMul
- `internal/game/npc_archetype.go` — Elite + Support + Disruptor archetype constants + defaults
- `internal/game/system_npc_ai.go` — Support behavior branch (anchor + kite + heal cadence)
- `internal/game/system_statuseffect.go` — handle StatusSlow + StatusSilence
- `internal/game/system_ship_dynamics.go` — apply StatusSlow movement multiplier
- `internal/game/system_ability.go` — gate ability cast on StatusSilence
- `internal/game/config.go` — Support, Disruptor, Elite, POIIntraCellClearance config fields
- `internal/component/components.go` — POI.Tier field, StatusSlow + StatusSilence enum values
- `internal/game/poi_gen_test.go` — extend with tier cases
- `internal/game/verb_status_test.go` — extend with Slow + Silence cases
- `internal/game/game.go` (or wherever verbs register) — register heal verb

**Client:**
- `web-pixi/src/entities/poi.ts` — tier-based color + size
- `web-pixi/src/ui/dev-overlay.ts` — debug tier rings

---

## Task 1: Add TierDef table + tier functions

**Files:**
- Create: `internal/game/tier.go`
- Create: `internal/game/tier_test.go`
- Modify: `internal/game/poi_config.go` (add TierDef struct + tierTable at end)
- Modify: `internal/game/config.go` (add `TierWidth` + `POIIntraCellClearance`)

- [ ] **Step 1: Write the failing tier test**

Create `internal/game/tier_test.go`:

```go
package game

import (
	"testing"

	"github.com/mmokit/mmokit/pkg/coords"
	"github.com/mmokit/mmokit/pkg/mmokit"
)

func TestTierForDist_Boundaries(t *testing.T) {
	tests := []struct {
		name string
		dist float32
		want uint8
	}{
		{"origin", 0, 1},
		{"just inside T1", 100, 1},
		{"just below T2 boundary", 16383, 1},
		{"exactly T2 boundary", 16384, 2},
		{"middle of T2", 25000, 2},
		{"just below T3 boundary", 32767, 2},
		{"exactly T3 boundary", 32768, 3},
		{"deep T3", 100000, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tierForDist(tt.dist)
			if got != tt.want {
				t.Fatalf("tierForDist(%v) = %d, want %d", tt.dist, got, tt.want)
			}
		})
	}
}

func TestDistFromStation_AdjacentCells(t *testing.T) {
	station := mmokit.CellCoord{CellX: 0, CellY: 0}
	stationCenterX := coords.CellSize / 2 // station center is mid-cell by default

	// Position is at local (centerX, centerY) of a cell 1 to the east of station.
	got := distFromStation(mmokit.CellCoord{CellX: 1, CellY: 0}, stationCenterX, coords.CellSize/2, station)
	want := coords.CellSize // exactly one cell apart on X
	if absf(got-want) > 1 {
		t.Fatalf("distFromStation eastNeighbor = %v, want ~%v", got, want)
	}
}

func TestTierForCellCenter_StationAndNeighbors(t *testing.T) {
	station := mmokit.CellCoord{CellX: 0, CellY: 0}

	if got := TierForCellCenter(station, station); got != 1 {
		t.Fatalf("station cell tier = %d, want 1", got)
	}
	if got := TierForCellCenter(mmokit.CellCoord{CellX: 1, CellY: 0}, station); got != 1 {
		t.Fatalf("immediate neighbor tier = %d, want 1", got)
	}
	if got := TierForCellCenter(mmokit.CellCoord{CellX: 3, CellY: 0}, station); got != 2 {
		t.Fatalf("3-cells-out tier = %d, want 2", got)
	}
	if got := TierForCellCenter(mmokit.CellCoord{CellX: 5, CellY: 0}, station); got != 3 {
		t.Fatalf("5-cells-out tier = %d, want 3", got)
	}
}

func absf(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}
```

- [ ] **Step 2: Run test to verify it fails to compile**

Run: `go test ./internal/game/ -run TestTierForDist_Boundaries -count=1`
Expected: COMPILE ERROR (`tierForDist`, `distFromStation`, `TierForCellCenter` undefined)

- [ ] **Step 3: Add TierDef struct + tierTable to `internal/game/poi_config.go`**

Append to `internal/game/poi_config.go`:

```go
// Roster index constants used by the tier table. Existing rosters slice
// is indexed in declaration order — these constants pin the indices for
// readable tierTable entries.
const (
	StarterArenaIdx uint16 = 0
	// SmallSkirmishIdx, MediumWarbandIdx, DisruptorCellIdx,
	// HeavyBattalionIdx, EliteAnchorIdx are added as their rosters land
	// in later tasks.
)

// TierDef describes the difficulty / density / reward shape of a single
// tier. Tiers form concentric radial bands centered on the station.
// Boundary tier is computed per-POI from its world position (not the
// cell), so cells that straddle a boundary naturally produce mixed-tier
// content.
type TierDef struct {
	Tier           uint8     // 1..3
	InnerRadius    float32   // world units; outer is the next tier's InnerRadius
	POIsPerCell    [2]int    // [min, max] inclusive
	StatMultiplier float32   // multiplies HP / damage / shield at NPC spawn
	FluxRewardMul  float32   // multiplies POIBaseClearFlux + PerKillFluxBonus on clear
	Rosters        []uint16  // roster def indices eligible at this tier
	CooldownSec    int32     // repopulation cooldown
}

// tierTable is the single source of truth for tier behavior. Modify
// this table to retune; nothing else should hard-code tier values.
//
// Roster lists evolve as new rosters land in later tasks; v1 entries
// here use StarterArenaIdx for all tiers as a placeholder so tier
// generation works end-to-end before the new rosters exist.
var tierTable = []TierDef{
	{Tier: 1, InnerRadius: 0, POIsPerCell: [2]int{3, 5}, StatMultiplier: 1.0, FluxRewardMul: 1.0, Rosters: []uint16{StarterArenaIdx}, CooldownSec: 180},
	{Tier: 2, InnerRadius: 16384, POIsPerCell: [2]int{1, 2}, StatMultiplier: 1.5, FluxRewardMul: 2.5, Rosters: []uint16{StarterArenaIdx}, CooldownSec: 300},
	{Tier: 3, InnerRadius: 32768, POIsPerCell: [2]int{0, 1}, StatMultiplier: 2.5, FluxRewardMul: 6.0, Rosters: []uint16{StarterArenaIdx}, CooldownSec: 900},
}

// tierDef returns the TierDef for a tier (1-indexed). Out-of-range
// returns tier 1 (defensive — should never happen in practice).
func tierDef(tier uint8) TierDef {
	if tier >= 1 && int(tier) <= len(tierTable) {
		return tierTable[tier-1]
	}
	return tierTable[0]
}
```

- [ ] **Step 4: Create `internal/game/tier.go`**

```go
package game

import (
	"math"

	"github.com/mmokit/mmokit/pkg/coords"
	"github.com/mmokit/mmokit/pkg/mmokit"
)

// tierForDist returns the tier of a world-space distance from the
// station. Walks tierTable from outermost inward; first band whose
// InnerRadius is <= dist wins.
func tierForDist(d float32) uint8 {
	for i := len(tierTable) - 1; i >= 0; i-- {
		if d >= tierTable[i].InnerRadius {
			return tierTable[i].Tier
		}
	}
	return tierTable[0].Tier
}

// distFromStation returns the absolute world-space distance from the
// given cell-local position to the station center. Station center is
// the station cell's local center (CellSize/2, CellSize/2) plus the
// configured StationPOIOffset (zero in the default config). Caller
// passes the station cell explicitly so this function doesn't depend
// on GameConfig.
func distFromStation(cell mmokit.CellCoord, localX, localY float32,
	stationCell mmokit.CellCoord) float32 {

	dCellX := float32(cell.CellX-stationCell.CellX) * coords.CellSize
	dCellY := float32(cell.CellY-stationCell.CellY) * coords.CellSize
	// Station local center is (CellSize/2, CellSize/2); StationPOIOffset
	// is read in TierForCellCenter (config-aware). This helper uses the
	// nominal center.
	stationLocalX := coords.CellSize / 2
	stationLocalY := coords.CellSize / 2
	dx := dCellX + localX - stationLocalX
	dy := dCellY + localY - stationLocalY
	return float32(math.Sqrt(float64(dx*dx + dy*dy)))
}

// TierForCellCenter returns the tier of a cell's center. Used by POI
// generation to roll POI count (coarse — final per-POI tier uses
// tierForDist on the POI's actual world position).
func TierForCellCenter(cell, stationCell mmokit.CellCoord) uint8 {
	centerLocal := coords.CellSize / 2
	d := distFromStation(cell, centerLocal, centerLocal, stationCell)
	return tierForDist(d)
}
```

- [ ] **Step 5: Add config fields**

Modify `internal/game/config.go`. Find the POI block (around line 155-167) and add:

```go
	POIIntraCellClearance          float32 `json:"poi_intra_cell_clearance"`
```

And the default in `DefaultConfig()` (around line 306):

```go
		POIIntraCellClearance:          1000,
```

(`TierWidth` is not added to config — `tierTable` is authoritative.)

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/game/ -run "TestTier|TestDistFromStation" -count=1 -v`
Expected: PASS for all three tests.

- [ ] **Step 7: Commit**

```bash
git add internal/game/tier.go internal/game/tier_test.go internal/game/poi_config.go internal/game/config.go
git commit -m "tier: add TierDef table + tierForDist/distFromStation/TierForCellCenter"
```

---

## Task 2: Add Tier field to POI component + POIDef + SpawnPOI signature

**Files:**
- Modify: `internal/component/components.go` (POI struct)
- Modify: `internal/game/poi_gen.go` (POIDef + GeneratePOIs return)
- Modify: `internal/game/entity_poi.go` (SpawnPOI signature)
- Modify: `internal/game/system_poi.go` (no behavior change yet, but compiles)

- [ ] **Step 1: Find the POI component definition**

Run: `grep -n "type POI struct" internal/component/components.go`
Note the line, then read the surrounding 20 lines.

- [ ] **Step 2: Add Tier field to gamecomp.POI**

In `internal/component/components.go`, locate `type POI struct` and add:

```go
type POI struct {
	Type         uint8       `net:"initial"`
	Status       POIStatus   `net:"u8"`
	AnchorRadius float32     `net:"initial"`
	LeashRadius  float32     `net:"initial"`
	RosterDefIdx uint16      `net:"initial"`
	Tier         uint8       `net:"initial"` // NEW — 1..3, immutable per POI
	ClearedAt    int64       // server time; not networked
}
```

(Keep existing fields exactly as written; add `Tier` with the `net:"initial"` tag so it's sent once on visibility enter. Place it just before `ClearedAt`.)

- [ ] **Step 3: Add Tier to POIDef**

In `internal/game/poi_gen.go` modify the `POIDef` struct:

```go
type POIDef struct {
	X, Y      float32
	Type      uint8
	RosterIdx uint16
	Tier      uint8 // NEW — 1..3
}
```

- [ ] **Step 4: Update existing POIDef construction in `GeneratePOIs`**

The current `poi_gen.go` returns `[]POIDef{{X: x, Y: y, Type: 0, RosterIdx: 0}}` (line ~57). Update it to:

```go
		return []POIDef{{X: x, Y: y, Type: 0, RosterIdx: 0, Tier: 1}}
```

(All existing POIs become Tier 1 by default until Task 5 rewrites generation.)

- [ ] **Step 5: Update SpawnPOI signature**

In `internal/game/entity_poi.go`:

```go
func (gw *GameWorld) SpawnPOI(x, y float32, poiType uint8, rosterIdx uint16, tier uint8) uint32 {
	e := gw.stage.Spawn(
		mmokit.Position{X: x, Y: y},
		mmokit.EntityKind{Type: gamecomp.KindPOI},
		gamecomp.POI{
			Type:         poiType,
			Status:       gamecomp.POIStatusActive,
			AnchorRadius: gw.Config.POIAnchorRadius,
			LeashRadius:  gw.Config.POILeashRadius,
			RosterDefIdx: rosterIdx,
			Tier:         tier, // NEW
		},
	)
	// ... rest of body unchanged
}
```

And update the `spawnPOIs` caller (same file, ~line 16) to pass `d.Tier`:

```go
	for _, d := range defs {
		gw.SpawnPOI(d.X, d.Y, d.Type, d.RosterIdx, d.Tier)
	}
```

- [ ] **Step 6: Update test callers of SpawnPOI**

Run: `grep -rn "SpawnPOI(" internal/game --include="*.go" | grep -v entity_poi.go`
For each match, add `, 1` as the last argument (tier=1).

Also check `internal/game/commands/poi.go`:
Run: `grep -n "SpawnPOI(" internal/game/commands/poi.go`
Update each call site to pass tier (use `1` as default for now or compute via `game.TierForCellCenter` if appropriate — for the console verb, accept a `--tier=N` flag, default 1).

- [ ] **Step 7: Compile + run all game tests**

Run: `go vet ./internal/game/...`
Expected: clean.
Run: `go test ./internal/game/ -count=1`
Expected: PASS (no behavior change yet).

- [ ] **Step 8: Commit**

```bash
git add internal/component/components.go internal/game/poi_gen.go internal/game/entity_poi.go internal/game/commands/poi.go
# also any test files modified
git commit -m "tier: add Tier field to POI component + POIDef + SpawnPOI"
```

---

## Task 3: Extend NPCSpawnModifiers with stat multiplier fields

**Files:**
- Modify: `internal/game/entity_npc.go` (NPCSpawnModifiers struct + SpawnNPC body)

- [ ] **Step 1: Write a failing test**

Create new file `internal/game/entity_npc_modifiers_test.go`:

```go
package game

import (
	"testing"

	gamecomp "github.com/mmokit/mmokit/internal/component"
	"github.com/mmokit/mmokit/pkg/mmokit"
)

func TestSpawnNPC_StatMultipliersScaleHP(t *testing.T) {
	gw := newTestGameWorld(t) // uses existing test harness in testutil_test.go
	baseHP := gw.Config.BrawlerHP

	npc := gw.SpawnNPC(0, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{
		HPMul: 2.5,
	})
	h := mmokit.Get[gamecomp.Health](npc)
	if h == nil {
		t.Fatal("no Health on spawned NPC")
	}
	if absf(h.Max-baseHP*2.5) > 0.1 {
		t.Fatalf("Health.Max = %v, want %v", h.Max, baseHP*2.5)
	}
}

func TestSpawnNPC_StatMultipliersZero_DefaultsToOne(t *testing.T) {
	gw := newTestGameWorld(t)
	baseHP := gw.Config.BrawlerHP

	npc := gw.SpawnNPC(0, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{})
	h := mmokit.Get[gamecomp.Health](npc)
	if absf(h.Max-baseHP) > 0.1 {
		t.Fatalf("zero-modifier Health.Max = %v, want %v", h.Max, baseHP)
	}
}

func TestSpawnNPC_StatMultipliersStackWithElite(t *testing.T) {
	gw := newTestGameWorld(t)
	baseHP := gw.Config.BrawlerHP
	eliteMul := gw.Config.BossSoloHPMultiplier
	tierMul := float32(2.0)

	npc := gw.SpawnNPC(0, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{
		Elite: true,
		HPMul: tierMul,
	})
	h := mmokit.Get[gamecomp.Health](npc)
	expected := baseHP * eliteMul * tierMul
	if absf(h.Max-expected) > 0.1 {
		t.Fatalf("elite+tier HP = %v, want %v", h.Max, expected)
	}
}
```

(`newTestGameWorld` already exists in `testutil_test.go`. If the signature differs, adjust accordingly — the test harness already supports SpawnNPC.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/game/ -run TestSpawnNPC_StatMultipliers -count=1 -v`
Expected: COMPILE ERROR (`HPMul` undefined on `NPCSpawnModifiers`).

- [ ] **Step 3: Extend NPCSpawnModifiers**

In `internal/game/entity_npc.go`, modify the struct:

```go
type NPCSpawnModifiers struct {
	Elite     bool    // existing — multiplies HP/Damage/Speed via Config.BossSolo* multipliers
	Main      bool    // existing — BossGuardian flag
	HPMul     float32 // NEW — additional HP multiplier; zero treated as 1.0
	DmgMul    float32 // NEW — additional damage multiplier; zero treated as 1.0
	ShieldMul float32 // NEW — additional shield multiplier; zero treated as 1.0
}
```

- [ ] **Step 4: Apply multipliers in SpawnNPC**

In `internal/game/entity_npc.go`, find the Elite scaling block (around line 51-55):

```go
	if mods.Elite {
		d.HP *= gw.Config.BossSoloHPMultiplier
		d.MaxSpeed *= gw.Config.BossSoloSpeedMultiplier
		d.DamagePerShot *= gw.Config.BossSoloDmgMultiplier
	}
```

Add immediately after it (still before the abilities cooldown jitter logic):

```go
	// Tier / explicit multipliers stack on top of Elite scaling.
	if mods.HPMul > 0 {
		d.HP *= mods.HPMul
	}
	if mods.DmgMul > 0 {
		d.DamagePerShot *= mods.DmgMul
	}
	// Shield multiplier is applied below where shield is initialized;
	// look up the shield assignment and multiply when present. If the
	// archetype doesn't currently set d.Shield, also extend
	// ArchetypeDefaults to include Shield and assign similarly.
	if mods.ShieldMul > 0 {
		d.Shield *= mods.ShieldMul
	}
```

If `ArchetypeDefaults` doesn't have a `Shield` field today, add one and populate it per archetype in `archetypeDefaults` (Brawler 60, Artillery 40, Lancer 20 — values from `config.go` defaults at line 244-266). This requires extending the struct in `npc_archetype.go`. If you find `Shield` is already inlined elsewhere (e.g. read directly from config inside SpawnNPC), apply `ShieldMul` at that exact assignment instead. The intent: shield value applied to the `gamecomp.Shield` component on the spawned NPC gets multiplied.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/game/ -run TestSpawnNPC_StatMultipliers -count=1 -v`
Expected: PASS for all three.

- [ ] **Step 6: Run full game test suite to confirm no regression**

Run: `go test ./internal/game/ -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/game/entity_npc.go internal/game/entity_npc_modifiers_test.go internal/game/npc_archetype.go
git commit -m "tier: extend NPCSpawnModifiers with HP/Dmg/Shield multipliers"
```

---

## Task 4: Rewrite POI generation for multi-POI + intra-cell clearance

**Files:**
- Modify: `internal/game/poi_gen.go` (extract placePOI helper; main loop emits N POIs)
- Modify: `internal/game/poi_gen_test.go` (extend with multi-POI cases)

- [ ] **Step 1: Write failing test for multi-POI generation**

Append to `internal/game/poi_gen_test.go` (file already exists — confirm with `ls`):

```go
func TestGeneratePOIs_T1CellYieldsMultiplePOIs(t *testing.T) {
	// Cell adjacent to station — should be T1 (3-5 POIs).
	station := mmokit.CellCoord{CellX: 0, CellY: 0}
	cell := mmokit.CellCoord{CellX: 1, CellY: 0}
	cfg := DefaultConfig()
	defs := GeneratePOIs(cell, station, cfg, nil)
	if len(defs) < 3 || len(defs) > 5 {
		t.Fatalf("T1 cell POIs = %d, want 3..5", len(defs))
	}
}

func TestGeneratePOIs_T3CellYieldsFewPOIs(t *testing.T) {
	station := mmokit.CellCoord{CellX: 0, CellY: 0}
	cell := mmokit.CellCoord{CellX: 5, CellY: 0} // ~40k units out, T3
	cfg := DefaultConfig()
	defs := GeneratePOIs(cell, station, cfg, nil)
	if len(defs) > 1 {
		t.Fatalf("T3 cell POIs = %d, want 0..1", len(defs))
	}
}

func TestGeneratePOIs_IntraCellClearance(t *testing.T) {
	station := mmokit.CellCoord{CellX: 0, CellY: 0}
	cell := mmokit.CellCoord{CellX: 1, CellY: 0}
	cfg := DefaultConfig()
	defs := GeneratePOIs(cell, station, cfg, nil)
	for i, a := range defs {
		for j, b := range defs {
			if i >= j {
				continue
			}
			dx := a.X - b.X
			dy := a.Y - b.Y
			d2 := dx*dx + dy*dy
			min2 := cfg.POIIntraCellClearance * cfg.POIIntraCellClearance
			if d2 < min2 {
				t.Fatalf("POIs %d,%d too close: %v < %v", i, j, d2, min2)
			}
		}
	}
}

func TestGeneratePOIs_StationCellReturnsNil(t *testing.T) {
	station := mmokit.CellCoord{CellX: 0, CellY: 0}
	cfg := DefaultConfig()
	defs := GeneratePOIs(station, station, cfg, nil)
	if defs != nil {
		t.Fatalf("station cell defs = %v, want nil", defs)
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/game/ -run "TestGeneratePOIs_T1Cell|TestGeneratePOIs_T3Cell|TestGeneratePOIs_IntraCell|TestGeneratePOIs_StationCell" -count=1 -v`
Expected: First two fail (current code returns ≤1 POI); IntraCellClearance passes vacuously; StationCellReturnsNil passes.

- [ ] **Step 3: Rewrite GeneratePOIs**

Replace the body of `GeneratePOIs` in `internal/game/poi_gen.go` with:

```go
func GeneratePOIs(cell, stationCell mmokit.CellCoord, cfg *GameConfig, belts []AsteroidBelt) []POIDef {
	if cell == stationCell {
		return nil
	}

	cellTier := TierForCellCenter(cell, stationCell)
	td := tierDef(cellTier)

	h := fnv.New64a()
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(cell.CellX))
	binary.LittleEndian.PutUint32(buf[4:8], uint32(cell.CellY))
	copy(buf[8:12], []byte("poi_"))
	h.Write(buf)
	rng := rand.New(rand.NewPCG(h.Sum64(), 1))

	n := td.POIsPerCell[0]
	span := td.POIsPerCell[1] - td.POIsPerCell[0]
	if span > 0 {
		n += rng.IntN(span + 1)
	}
	if n == 0 {
		return nil
	}

	defs := make([]POIDef, 0, n)
	for i := 0; i < n; i++ {
		x, y, ok := placePOI(rng, cfg, belts, defs)
		if !ok {
			continue
		}
		worldDist := distFromStation(cell, x, y, stationCell)
		poiTier := tierForDist(worldDist)
		roster := pickRosterForTier(rng, poiTier)
		defs = append(defs, POIDef{X: x, Y: y, Type: 0, RosterIdx: roster, Tier: poiTier})
	}
	return defs
}

// placePOI rejection-samples a position inside the cell, respecting belt
// clearance, placement margin, and clearance from already-placed POIs in
// the same cell. Returns (x, y, true) on success, (0, 0, false) after
// the attempt budget is exhausted.
func placePOI(rng *rand.Rand, cfg *GameConfig, belts []AsteroidBelt, existing []POIDef) (float32, float32, bool) {
	margin := cfg.POIPlacementMargin
	usable := coords.CellSize - margin*2
	intraMin2 := cfg.POIIntraCellClearance * cfg.POIIntraCellClearance

	for attempt := 0; attempt < 30; attempt++ {
		x := margin + rng.Float32()*usable
		y := margin + rng.Float32()*usable

		bad := false
		for _, b := range belts {
			dx := x - b.CenterX
			dy := y - b.CenterY
			if dx*dx+dy*dy < (b.Radius+cfg.POIBeltClearance)*(b.Radius+cfg.POIBeltClearance) {
				bad = true
				break
			}
		}
		if bad {
			continue
		}

		for _, p := range existing {
			dx := x - p.X
			dy := y - p.Y
			if dx*dx+dy*dy < intraMin2 {
				bad = true
				break
			}
		}
		if bad {
			continue
		}

		return x, y, true
	}
	return 0, 0, false
}

// pickRosterForTier returns one of the rosters eligible at the given tier.
// Currently uniform selection; weights can be added later.
func pickRosterForTier(rng *rand.Rand, tier uint8) uint16 {
	td := tierDef(tier)
	if len(td.Rosters) == 0 {
		return StarterArenaIdx
	}
	return td.Rosters[rng.IntN(len(td.Rosters))]
}
```

Make sure imports include `"math"` is removed if unused (the inline sqrt is gone — placePOI uses squared distances).

- [ ] **Step 4: Run tier-aware POI gen tests**

Run: `go test ./internal/game/ -run "TestGeneratePOIs" -count=1 -v`
Expected: PASS for all four.

- [ ] **Step 5: Run full game test suite**

Run: `go test ./internal/game/ -count=1`
Expected: PASS (other tests must not regress).

- [ ] **Step 6: Commit**

```bash
git add internal/game/poi_gen.go internal/game/poi_gen_test.go
git commit -m "tier: tier-aware POI generation (multi-POI + intra-cell clearance + per-POI tier)"
```

---

## Task 5: Wire tier stat multipliers into POI roster spawning

**Files:**
- Modify: `internal/game/entity_poi.go` (`spawnPOIRoster` passes tier multiplier)

- [ ] **Step 1: Write failing test**

Append to `internal/game/poi_gen_test.go`:

```go
func TestSpawnPOI_AppliesTierStatMultiplier(t *testing.T) {
	gw := newTestGameWorld(t)
	baseHP := gw.Config.BrawlerHP

	// Spawn a tier-3 POI at the origin (tier label is what matters for
	// the test — placement isn't required to match tier in the test).
	netID := gw.SpawnPOI(100, 100, 0, StarterArenaIdx, 3)
	_ = netID

	// Walk roster NPCs; verify Brawler members have HP scaled by T3 mul.
	t3Mul := tierDef(3).StatMultiplier
	expected := baseHP * t3Mul

	var brawlerFound bool
	mmokit.ForEach1(gw.stage, func(e mmokit.Entity, ai *gamecomp.NPCAI) {
		if ai.Archetype != ArchetypeBrawler {
			return
		}
		h := mmokit.Get[gamecomp.Health](e)
		if h == nil {
			return
		}
		if absf(h.Max-expected) > 0.1 {
			t.Fatalf("T3 Brawler Health.Max = %v, want %v", h.Max, expected)
		}
		brawlerFound = true
	})
	if !brawlerFound {
		t.Fatal("no Brawler in spawned roster — fixture issue")
	}
}
```

(Note: `NPCAI.Archetype` may be named differently — verify via `grep "Archetype" internal/component/components.go`. If the field name differs, adjust the test.)

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/game/ -run TestSpawnPOI_AppliesTierStatMultiplier -count=1 -v`
Expected: FAIL — tier mul not yet wired.

- [ ] **Step 3: Wire tier mul in spawnPOIRoster**

In `internal/game/entity_poi.go`, modify `spawnPOIRoster`:

```go
func (gw *GameWorld) spawnPOIRoster(cx, cy float32, poiNetID uint32, rosterIdx uint16, tier uint8) {
	rng := rand.New(rand.NewPCG(uint64(poiNetID), uint64(rosterIdx)))
	roster := rosterForIdx(rosterIdx)
	mul := tierDef(tier).StatMultiplier
	for _, m := range roster.Members {
		for i := 0; i < m.Count; i++ {
			angle := rng.Float64() * (2 * math.Pi)
			r := rng.Float32() * m.SpreadRadius
			ox := r * float32(math.Cos(angle))
			oy := r * float32(math.Sin(angle))
			gw.SpawnNPC(cx+ox, cy+oy, m.Archetype, poiNetID, NPCSpawnModifiers{
				HPMul:     mul,
				DmgMul:    mul,
				ShieldMul: mul,
			})
		}
	}
}
```

Update the caller in `SpawnPOI` (same file):

```go
	gw.spawnPOIRoster(x, y, poiNetID, rosterIdx, tier)
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/game/ -run TestSpawnPOI_AppliesTierStatMultiplier -count=1 -v`
Expected: PASS.

Run: `go test ./internal/game/ -count=1`
Expected: PASS overall.

- [ ] **Step 5: Commit**

```bash
git add internal/game/entity_poi.go internal/game/poi_gen_test.go
git commit -m "tier: apply tier stat multiplier to roster NPCs at spawn"
```

---

## Task 6: Tier-aware reward + cooldown on POI clear

**Files:**
- Modify: `internal/game/system_poi.go` (`onClear` reward, `poiCooldownSec` tier-aware)

- [ ] **Step 1: Write failing tests**

Create new test file `internal/game/system_poi_tier_test.go`:

```go
package game

import (
	"testing"

	gamecomp "github.com/mmokit/mmokit/internal/component"
	"github.com/mmokit/mmokit/internal/item"
	"github.com/mmokit/mmokit/pkg/mmokit"
)

func TestPOIClear_T1RewardUnscaled(t *testing.T) {
	gw := newTestGameWorld(t)
	poiNetID := gw.SpawnPOI(100, 100, 0, StarterArenaIdx, 1)
	clearPOIRoster(t, gw, poiNetID)
	// Run one POISystem tick to detect clear.
	tickGW(gw, 0.05)

	crate := findLootCrateNear(gw, 100, 100)
	if crate == nil {
		t.Fatal("no loot crate after clear")
	}
	bounty := lootCrateAmount(crate, item.CreditsItemID)
	rosterCount := totalRosterMembers(StarterArenaIdx)
	expected := gw.Config.POIBaseClearFlux + gw.Config.POIPerKillFluxBonus*int32(rosterCount)
	if absInt32(bounty-expected) > 1 {
		t.Fatalf("T1 bounty = %d, want %d", bounty, expected)
	}
}

func TestPOIClear_T3RewardScaledBy6x(t *testing.T) {
	gw := newTestGameWorld(t)
	poiNetID := gw.SpawnPOI(100, 100, 0, StarterArenaIdx, 3)
	clearPOIRoster(t, gw, poiNetID)
	tickGW(gw, 0.05)

	crate := findLootCrateNear(gw, 100, 100)
	if crate == nil {
		t.Fatal("no loot crate after clear")
	}
	bounty := lootCrateAmount(crate, item.CreditsItemID)
	rosterCount := totalRosterMembers(StarterArenaIdx)
	base := gw.Config.POIBaseClearFlux + gw.Config.POIPerKillFluxBonus*int32(rosterCount)
	expected := int32(float32(base) * 6.0)
	tolerance := int32(2) // float rounding
	if absInt32(bounty-expected) > tolerance {
		t.Fatalf("T3 bounty = %d, want ~%d", bounty, expected)
	}
}
```

Helper functions to add at end of the same file (or to `testutil_test.go`):

```go
// clearPOIRoster kills every roster member of the given POI by zeroing
// HP. Caller must run a POISystem tick afterwards to detect the clear.
func clearPOIRoster(t *testing.T, gw *GameWorld, poiNetID uint32) {
	t.Helper()
	ids := gw.poiRosters[poiNetID]
	if len(ids) == 0 {
		t.Fatal("POI has empty roster — wrong tier setup?")
	}
	for _, nid := range ids {
		e := mmokit.EntityByNetID(gw.stage, nid)
		if !e.Alive() {
			continue
		}
		h := mmokit.Get[gamecomp.Health](e)
		if h != nil {
			h.Current = 0
		}
	}
}

func findLootCrateNear(gw *GameWorld, x, y float32) mmokit.Entity {
	var found mmokit.Entity
	mmokit.ForEach2(gw.stage, func(e mmokit.Entity, _ *gamecomp.LootCrate, pos *mmokit.Position) {
		dx := pos.X - x
		dy := pos.Y - y
		if dx*dx+dy*dy < 100*100 {
			found = e
		}
	})
	return found
}

func lootCrateAmount(crate mmokit.Entity, itemID uint32) int32 {
	lc := mmokit.Get[gamecomp.LootCrate](crate)
	if lc == nil {
		return 0
	}
	for _, it := range lc.Items {
		if it.ItemID == itemID {
			return it.Quantity
		}
	}
	return 0
}

func totalRosterMembers(idx uint16) int {
	r := rosterForIdx(idx)
	n := 0
	for _, m := range r.Members {
		n += m.Count
	}
	return n
}

func absInt32(x int32) int32 {
	if x < 0 {
		return -x
	}
	return x
}
```

(If `LootCrate.Items` has a different shape — e.g. a `map[uint32]int32` rather than a slice — adapt `lootCrateAmount`. Check `internal/component/components.go` for the actual definition. `tickGW(gw, dt)` already exists in `testutil_test.go` or similar.)

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./internal/game/ -run TestPOIClear_ -count=1 -v`
Expected: T1 passes (no reward scaling exists yet, default is 1x); T3 fails.

- [ ] **Step 3: Apply tier reward + cooldown in system_poi.go**

In `internal/game/system_poi.go`, locate `onClear` (around the bounty calculation):

```go
func (s *POISystem) onClear(poi *gamecomp.POI, pos *mmokit.Position, poiNetID uint32) {
	gw := s.gw
	roster := rosterForIdx(poi.RosterDefIdx)
	rosterCount := 0
	for _, m := range roster.Members {
		rosterCount += m.Count
	}
	rewardMul := tierDef(poi.Tier).FluxRewardMul
	if rewardMul <= 0 {
		rewardMul = 1.0
	}
	base := gw.Config.POIBaseClearFlux + gw.Config.POIPerKillFluxBonus*int32(rosterCount)
	bounty := int32(float32(base) * rewardMul)
	gw.SpawnLootCrate(pos.X, pos.Y, map[uint32]int32{
		item.CreditsItemID: bounty,
	})

	poi.Status = gamecomp.POIStatusCooldown
	poi.ClearedAt = time.Now().UnixNano()
	gw.eng.Log.Log(CatPOI, "poi: cleared netID=%d tier=%d bounty=%d roster=%s",
		poiNetID, poi.Tier, bounty, roster.Name)
}
```

Locate `poiCooldownSec` at the bottom of the file and replace:

```go
func poiCooldownSec(gw *GameWorld, tier uint8) int32 {
	if gw.RootCell == gw.Config.StationCell {
		return gw.Config.StationCellPOIClearCooldown
	}
	td := tierDef(tier)
	if td.CooldownSec > 0 {
		return td.CooldownSec
	}
	return gw.Config.NonStationCellPOIClearCooldown
}
```

Find `tickCooldown` (same file) and update the call:

```go
func (s *POISystem) tickCooldown(poi *gamecomp.POI, pos *mmokit.Position, poiNetID uint32, now int64) bool {
	cooldownSec := poiCooldownSec(s.gw, poi.Tier)
	elapsed := now - poi.ClearedAt
	return elapsed >= int64(cooldownSec)*int64(time.Second)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/game/ -run TestPOIClear_ -count=1 -v`
Expected: PASS for both.

Run: `go test ./internal/game/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/game/system_poi.go internal/game/system_poi_tier_test.go
git commit -m "tier: scale POI reward + cooldown by tier"
```

---

## Task 7: Add new roster defs (T1-T2 only — without new archetypes yet)

**Files:**
- Modify: `internal/game/poi_config.go` (add SmallSkirmish, MediumWarband)

- [ ] **Step 1: Define new rosters**

In `internal/game/poi_config.go`, replace the existing const block:

```go
const (
	StarterArenaIdx   uint16 = 0
	SmallSkirmishIdx  uint16 = 1
	MediumWarbandIdx  uint16 = 2
	// DisruptorCellIdx, HeavyBattalionIdx, EliteAnchorIdx land in later tasks.
)
```

Replace the `rosters` slice:

```go
var rosters = []RosterDef{
	{
		Name: "Starter Arena",
		Members: []RosterMember{
			{Archetype: ArchetypeArtillery, Count: 1, SpreadRadius: 40},
			{Archetype: ArchetypeBrawler, Count: 2, SpreadRadius: 25},
			{Archetype: ArchetypeLancer, Count: 3, SpreadRadius: 30},
		},
	},
	{
		Name: "Small Skirmish",
		Members: []RosterMember{
			{Archetype: ArchetypeLancer, Count: 1, SpreadRadius: 25},
			{Archetype: ArchetypeBrawler, Count: 1, SpreadRadius: 20},
		},
	},
	{
		Name: "Medium Warband",
		Members: []RosterMember{
			{Archetype: ArchetypeLancer, Count: 2, SpreadRadius: 30},
			{Archetype: ArchetypeBrawler, Count: 2, SpreadRadius: 25},
			{Archetype: ArchetypeArtillery, Count: 1, SpreadRadius: 40},
		},
	},
}
```

Update tierTable T1 and T2 to use the new rosters:

```go
var tierTable = []TierDef{
	{Tier: 1, InnerRadius: 0, POIsPerCell: [2]int{3, 5}, StatMultiplier: 1.0, FluxRewardMul: 1.0, Rosters: []uint16{SmallSkirmishIdx, StarterArenaIdx}, CooldownSec: 180},
	{Tier: 2, InnerRadius: 16384, POIsPerCell: [2]int{1, 2}, StatMultiplier: 1.5, FluxRewardMul: 2.5, Rosters: []uint16{MediumWarbandIdx, StarterArenaIdx}, CooldownSec: 300},
	{Tier: 3, InnerRadius: 32768, POIsPerCell: [2]int{0, 1}, StatMultiplier: 2.5, FluxRewardMul: 6.0, Rosters: []uint16{StarterArenaIdx}, CooldownSec: 900},
}
```

(T3 keeps StarterArena until EliteAnchor lands in Task 14.)

- [ ] **Step 2: Run all game tests to confirm nothing regressed**

Run: `go test ./internal/game/ -count=1`
Expected: PASS. Tier tests, POI gen tests, and reward tests all green.

- [ ] **Step 3: Commit**

```bash
git add internal/game/poi_config.go
git commit -m "tier: add Small Skirmish + Medium Warband rosters for T1/T2"
```

---

## Task 8: Add Elite archetype variants

**Files:**
- Modify: `internal/game/npc_archetype.go` (constants + archetypeDefaults branches)
- Modify: `internal/game/config.go` (EliteStatMultiplier field)

- [ ] **Step 1: Write failing test**

Append to `internal/game/entity_npc_modifiers_test.go`:

```go
func TestSpawnNPC_EliteLancer_StatsAreScaled(t *testing.T) {
	gw := newTestGameWorld(t)
	baseHP := gw.Config.LancerHP

	npc := gw.SpawnNPC(0, 0, ArchetypeEliteLancer, 0, NPCSpawnModifiers{})
	h := mmokit.Get[gamecomp.Health](npc)
	expected := baseHP * gw.Config.EliteStatMultiplier
	if absf(h.Max-expected) > 0.1 {
		t.Fatalf("EliteLancer HP = %v, want %v", h.Max, expected)
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/game/ -run TestSpawnNPC_EliteLancer -count=1 -v`
Expected: FAIL (undefined `ArchetypeEliteLancer`).

- [ ] **Step 3: Add archetype constants**

In `internal/game/npc_archetype.go`, extend the const block (currently Brawler=0, Artillery=1, Lancer=2, BossGuardian=3):

```go
const (
	ArchetypeBrawler        uint8 = 0
	ArchetypeArtillery      uint8 = 1
	ArchetypeLancer         uint8 = 2
	ArchetypeBossGuardian   uint8 = 3
	ArchetypeEliteBrawler   uint8 = 4
	ArchetypeEliteArtillery uint8 = 5
	ArchetypeEliteLancer    uint8 = 6
)
```

In `archetypeDefaults`, add cases that re-use base defaults with the Elite multiplier:

```go
	case ArchetypeEliteBrawler:
		d := archetypeDefaults(cfg, ArchetypeBrawler)
		d.HP *= cfg.EliteStatMultiplier
		d.DamagePerShot *= cfg.EliteStatMultiplier
		d.MaxSpeed *= cfg.EliteStatMultiplier
		return d
	case ArchetypeEliteArtillery:
		d := archetypeDefaults(cfg, ArchetypeArtillery)
		d.HP *= cfg.EliteStatMultiplier
		d.DamagePerShot *= cfg.EliteStatMultiplier
		d.MaxSpeed *= cfg.EliteStatMultiplier
		return d
	case ArchetypeEliteLancer:
		d := archetypeDefaults(cfg, ArchetypeLancer)
		d.HP *= cfg.EliteStatMultiplier
		d.DamagePerShot *= cfg.EliteStatMultiplier
		d.MaxSpeed *= cfg.EliteStatMultiplier
		return d
```

Note recursion — make sure your `archetypeDefaults` is structured to handle these calls cleanly. If recursion creates issues, inline the base call into a small helper or duplicate the base values per archetype with multipliers pre-applied.

Update the kind-name switch (lines ~140-150) for any sprite/label code that uses archetype names:

```go
	case ArchetypeEliteArtillery: return "elite_artillery"
	case ArchetypeEliteBrawler:   return "elite_brawler"
	case ArchetypeEliteLancer:    return "elite_lancer"
```

- [ ] **Step 4: Add config field**

In `internal/game/config.go`, find the NPC stats block (where BrawlerHP etc. live, around line 240) and add:

```go
	EliteStatMultiplier float32 `json:"elite_stat_multiplier"`
```

In `DefaultConfig`, set:

```go
		EliteStatMultiplier: 1.3,
```

- [ ] **Step 5: Run test**

Run: `go test ./internal/game/ -run TestSpawnNPC_EliteLancer -count=1 -v`
Expected: PASS.

Run: `go test ./internal/game/ -count=1`
Expected: PASS overall.

- [ ] **Step 6: Commit**

```bash
git add internal/game/npc_archetype.go internal/game/config.go internal/game/entity_npc_modifiers_test.go
git commit -m "tier: add EliteBrawler/EliteArtillery/EliteLancer archetypes"
```

---

## Task 9: Add verb_heal (mirrors verb_damage)

**Files:**
- Create: `internal/game/verb_heal.go`
- Create: `internal/game/verb_heal_test.go`
- Modify: wherever verbs register (likely `internal/game/game.go` or `factory.go`)

- [ ] **Step 1: Write failing test**

Create `internal/game/verb_heal_test.go`:

```go
package game

import (
	"testing"

	gamecomp "github.com/mmokit/mmokit/internal/component"
	"github.com/mmokit/mmokit/pkg/mmokit"
)

func TestHeal_RestoresHP(t *testing.T) {
	gw := newTestGameWorld(t)
	target := gw.SpawnNPC(0, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{})
	h := mmokit.Get[gamecomp.Health](target)
	h.Current = h.Max / 2

	caster := gw.SpawnNPC(50, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{})

	gw.Heal(caster, target, 30)
	tickGW(gw, 0.05) // allow verb to dispatch

	got := mmokit.Get[gamecomp.Health](target).Current
	want := h.Max/2 + 30
	if absf(got-want) > 0.1 {
		t.Fatalf("HP after heal = %v, want %v", got, want)
	}
}

func TestHeal_CapsAtMax(t *testing.T) {
	gw := newTestGameWorld(t)
	target := gw.SpawnNPC(0, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{})
	h := mmokit.Get[gamecomp.Health](target)
	h.Current = h.Max - 5

	caster := gw.SpawnNPC(50, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{})
	gw.Heal(caster, target, 100)
	tickGW(gw, 0.05)

	got := mmokit.Get[gamecomp.Health](target).Current
	if absf(got-h.Max) > 0.1 {
		t.Fatalf("HP after over-heal = %v, want max=%v", got, h.Max)
	}
}

func TestHeal_DeadTargetNoOp(t *testing.T) {
	gw := newTestGameWorld(t)
	target := gw.SpawnNPC(0, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{})
	mmokit.Get[gamecomp.Health](target).Current = 0

	caster := gw.SpawnNPC(50, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{})
	gw.Heal(caster, target, 30) // must not raise from 0

	tickGW(gw, 0.05)

	got := mmokit.Get[gamecomp.Health](target).Current
	if got != 0 {
		t.Fatalf("dead target healed: HP = %v, want 0", got)
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/game/ -run TestHeal -count=1 -v`
Expected: COMPILE ERROR (`gw.Heal` undefined).

- [ ] **Step 3: Create verb_heal.go**

```go
package game

import (
	gamecomp "github.com/mmokit/mmokit/internal/component"
	"github.com/mmokit/mmokit/pkg/mmokit"
)

// Heal is a typed cross-cell-aware healing message. Applied to a target
// via mmokit.Send; handler runs on the authoritative cell.
type Heal struct {
	Amount float32
	Source mmokit.Entity
	Target mmokit.Entity
}

func healHandler(target mmokit.Entity, msg *Heal) {
	msg.Target = target

	h := mmokit.Get[gamecomp.Health](target)
	if h == nil {
		return
	}
	if h.Current <= 0 {
		// Don't revive dead targets.
		return
	}
	prev := h.Current
	h.Current += msg.Amount
	if h.Current > h.Max {
		h.Current = h.Max
	}

	gw := gameWorldOfEntity(target)
	if gw == nil {
		return
	}
	gw.eng.Log.Log(CatCombatAbility, "heal: source=%d -> target=%d amount=%.1f (%.1f -> %.1f)",
		msg.Source.NetID(), target.NetID(), msg.Amount, prev, h.Current)
}

// RegisterHealVerb wires healHandler onto every Stage owned by p.
func RegisterHealVerb(p *mmokit.Process) {
	mmokit.HandleAll(p, healHandler)
}

// Heal is the game-side helper. Mirrors gw.Damage.
func (gw *GameWorld) Heal(caster, target mmokit.Entity, amount float32) {
	if !target.Alive() {
		return
	}
	target.Send(&Heal{
		Amount: amount,
		Source: caster,
	})
}
```

- [ ] **Step 4: Register the heal verb at startup**

Find where `RegisterDamageVerb` is called (likely in `internal/game/game.go` or `factory.go`):

Run: `grep -rn "RegisterDamageVerb" internal/game --include="*.go" | grep -v _test.go`

Add a `RegisterHealVerb(p)` call immediately after the `RegisterDamageVerb(p)` call.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/game/ -run TestHeal -count=1 -v`
Expected: PASS for all three.

- [ ] **Step 6: Commit**

```bash
git add internal/game/verb_heal.go internal/game/verb_heal_test.go internal/game/game.go
# (or factory.go — whichever you modified)
git commit -m "heal: add verb_heal (cross-cell heal verb mirroring verb_damage)"
```

---

## Task 10: Add Support archetype constants + config + defaults

**Files:**
- Modify: `internal/game/npc_archetype.go` (constant + defaults case)
- Modify: `internal/game/config.go` (Support stat fields)

- [ ] **Step 1: Add Support config fields**

In `internal/game/config.go`, add to the NPC stats section:

```go
	SupportHP             float32 `json:"support_hp"`
	SupportShield         float32 `json:"support_shield"`
	SupportMaxSpeed       float32 `json:"support_max_speed"`
	SupportTurnRate       float32 `json:"support_turn_rate"`
	SupportHealRange      float32 `json:"support_heal_range"`
	SupportHealAmount     float32 `json:"support_heal_amount"`
	SupportHealCooldown   float32 `json:"support_heal_cooldown"`
	SupportRetreatDist    float32 `json:"support_retreat_dist"`
```

In `DefaultConfig`:

```go
		SupportHP:           100,
		SupportShield:       40,
		SupportMaxSpeed:     5,
		SupportTurnRate:     1.2,
		SupportHealRange:    200,
		SupportHealAmount:   40,
		SupportHealCooldown: 4.0,
		SupportRetreatDist:  60,
```

- [ ] **Step 2: Add archetype constant + defaults**

In `internal/game/npc_archetype.go`, extend the const block:

```go
	ArchetypeSupport uint8 = 7
```

Add to `archetypeDefaults`:

```go
	case ArchetypeSupport:
		return ArchetypeDefaults{
			HP:            cfg.SupportHP,
			Shield:        cfg.SupportShield,
			MaxSpeed:      cfg.SupportMaxSpeed,
			TurnRate:      cfg.SupportTurnRate,
			DamagePerShot: 0, // Support deals no damage
			// AttackRange unused; Support targets allies via separate code path.
		}
```

Update the name switch:

```go
	case ArchetypeSupport: return "support"
```

- [ ] **Step 3: Compile**

Run: `go vet ./internal/game/...`
Expected: clean.

Run: `go test ./internal/game/ -count=1`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/game/npc_archetype.go internal/game/config.go
git commit -m "support: add archetype constant + stats config (no behavior yet)"
```

---

## Task 11: Support AI behavior (anchor + kite + heal cadence)

**Files:**
- Modify: `internal/game/system_npc_ai.go` (Support branch in tick logic)
- Create: `internal/game/npc_archetype_support_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/game/npc_archetype_support_test.go`:

```go
package game

import (
	"testing"

	gamecomp "github.com/mmokit/mmokit/internal/component"
	"github.com/mmokit/mmokit/pkg/mmokit"
)

func TestSupport_HealsLowestHPRosterAlly(t *testing.T) {
	gw := newTestGameWorld(t)
	poiNetID := gw.SpawnPOI(500, 500, 0, StarterArenaIdx, 1)
	// Spawn Support attached to the same POI.
	support := gw.SpawnNPC(500, 500, ArchetypeSupport, poiNetID, NPCSpawnModifiers{})
	_ = support

	// Find two roster members; damage one to half HP.
	var injured, healthy mmokit.Entity
	mmokit.ForEach1(gw.stage, func(e mmokit.Entity, _ *gamecomp.NPCAI) {
		if e == support {
			return
		}
		if !injured.Alive() {
			injured = e
		} else if !healthy.Alive() {
			healthy = e
		}
	})
	if !injured.Alive() || !healthy.Alive() {
		t.Skip("not enough roster members for test")
	}
	mmokit.Get[gamecomp.Health](injured).Current = mmokit.Get[gamecomp.Health](injured).Max / 2
	prevInjuredHP := mmokit.Get[gamecomp.Health](injured).Current
	prevHealthyHP := mmokit.Get[gamecomp.Health](healthy).Current

	// Run enough ticks to clear the Support heal cooldown.
	for i := 0; i < 100; i++ {
		tickGW(gw, gw.Config.SupportHealCooldown/50)
	}

	gotInjured := mmokit.Get[gamecomp.Health](injured).Current
	gotHealthy := mmokit.Get[gamecomp.Health](healthy).Current
	if gotInjured <= prevInjuredHP {
		t.Fatalf("injured HP %v not increased from %v", gotInjured, prevInjuredHP)
	}
	if absf(gotHealthy-prevHealthyHP) > 0.1 {
		t.Fatalf("healthy ally HP changed: %v != %v (support should pick lowest)", gotHealthy, prevHealthyHP)
	}
}

func TestSupport_NoTargetWhenAllAtFull(t *testing.T) {
	gw := newTestGameWorld(t)
	poiNetID := gw.SpawnPOI(500, 500, 0, StarterArenaIdx, 1)
	support := gw.SpawnNPC(500, 500, ArchetypeSupport, poiNetID, NPCSpawnModifiers{})

	// Snapshot all HP before ticks.
	hpBefore := map[uint32]float32{}
	mmokit.ForEach1(gw.stage, func(e mmokit.Entity, _ *gamecomp.NPCAI) {
		if e == support {
			return
		}
		hpBefore[e.NetID()] = mmokit.Get[gamecomp.Health](e).Current
	})

	for i := 0; i < 100; i++ {
		tickGW(gw, gw.Config.SupportHealCooldown/50)
	}

	mmokit.ForEach1(gw.stage, func(e mmokit.Entity, _ *gamecomp.NPCAI) {
		if e == support {
			return
		}
		got := mmokit.Get[gamecomp.Health](e).Current
		want := hpBefore[e.NetID()]
		if absf(got-want) > 0.1 {
			t.Fatalf("full-HP ally %d changed: %v -> %v", e.NetID(), want, got)
		}
	})
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./internal/game/ -run TestSupport -count=1 -v`
Expected: FAIL — Support tick logic doesn't exist.

- [ ] **Step 3: Add `HealCooldown` field to NPCAI**

In `internal/component/components.go`, inside the `type NPCAI struct` body (any logical position — group with other cooldowns near `SpecialCooldown` at ~line 396), add:

```go
	// PVE-tier (Support): countdown between heal casts. Independent of
	// SpecialCooldown so Support doesn't interfere with Brawler/Disruptor
	// state. Defaults to 0 on spawn — first heal fires as soon as a
	// target appears.
	HealCooldown float32
```

- [ ] **Step 4: Add Support tick in NPCAI system**

Open `internal/game/system_npc_ai.go`. Read it once end-to-end to identify the main per-NPC tick function (look for `switch ai.State` or a top-level for-each that calls `tickEngage`, `tickApproach`, etc.).

Add a new method on `*NPCAISystem`:

```go
// tickSupport advances a Support NPC. Support has its own minimal
// behavior — it never enters Engage/Approach/Leash. Each tick:
//   - If nearest player is inside SupportRetreatDist, kite directly
//     away from them.
//   - Else, drift toward the POI anchor position.
//   - Tick HealCooldown. When ready, pick the lowest-HP roster ally
//     within SupportHealRange (excluding self / dead / full-HP), Heal
//     them, reset HealCooldown.
//
// Movement is applied by writing directly into the entity's Velocity
// component — Support is simple enough that no Pathing component or
// state machine is required.
func (s *NPCAISystem) tickSupport(e mmokit.Entity, ai *gamecomp.NPCAI,
	pos *mmokit.Position, vel *mmokit.Velocity, dt float32) {

	gw := s.gw

	// 1. Decide a target heading (dirX, dirY). Either kite-away or drift-to-anchor.
	var dirX, dirY float32
	var hasDir bool

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
		// Drift toward anchor (POI center).
		if anchor := mmokit.Get[gamecomp.DungeonAnchor](e); anchor != nil && anchor.DungeonNetID != 0 {
			poi := mmokit.EntityByNetID(gw.stage, anchor.DungeonNetID)
			if poi.Alive() {
				if poiPos := mmokit.Get[mmokit.Position](poi); poiPos != nil {
					fx := poiPos.X - pos.X
					fy := poiPos.Y - pos.Y
					n := float32(math.Sqrt(float64(fx*fx + fy*fy)))
					if n > 1.0 { // dead-zone — don't oscillate at the anchor
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

	// 2. Heal cadence.
	ai.HealCooldown -= dt
	if ai.HealCooldown > 0 {
		return
	}

	myPOI := uint32(0)
	if a := mmokit.Get[gamecomp.DungeonAnchor](e); a != nil {
		myPOI = a.DungeonNetID
	}
	healRange2 := gw.Config.SupportHealRange * gw.Config.SupportHealRange

	var bestTarget mmokit.Entity
	bestPct := float32(1.0)
	mmokit.ForEach2(gw.stage, func(other mmokit.Entity, _ *gamecomp.NPCAI, otherPos *mmokit.Position) {
		if other == e {
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
```

In the main NPCAI update loop, dispatch to `tickSupport` for Support archetype BEFORE the existing per-state dispatch so Support skips Engage/Approach/Leash entirely. The exact signature depends on how the existing loop iterates — match it. If the existing loop iterates a Query with `(pos, vel, rot, ai)` bundle, just add at the top of the loop body:

```go
		if ai.Archetype == ArchetypeSupport {
			s.tickSupport(e, ai, pos, vel, dt)
			continue
		}
```

(If `CatNPCAI` doesn't exist, check the actual log category name — `grep -n "CatNPC" internal/game/logcat.go`. The current code uses `CatPOI` for POI-system logs; for NPC AI choose whichever existing category fits — `CatCombatAbility` if it ends up nearer combat events.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/game/ -run TestSupport -count=1 -v`
Expected: PASS for both.

Run: `go test ./internal/game/ -count=1`
Expected: PASS overall (other NPCAI tests must not regress).

- [ ] **Step 5: Commit**

```bash
git add internal/game/system_npc_ai.go internal/game/npc_archetype_support_test.go internal/component/components.go
git commit -m "support: AI behavior (anchor / kite / heal lowest-HP ally)"
```

---

## Task 12: Add StatusSlow and StatusSilence enum values

**Files:**
- Modify: `internal/component/components.go` (enum extension)
- Modify: `internal/game/system_statuseffect.go` (apply effects)
- Modify: `internal/game/system_ship_dynamics.go` (movement multiplier for Slow)
- Modify: `internal/game/system_ability.go` (block cast on Silence)
- Modify: `internal/game/verb_status_test.go` (extend tests)

- [ ] **Step 1: Extract `EffectiveSpeedMul` helper (makes Slow directly testable)**

Add to `internal/game/system_ship_dynamics.go` (top of the file after imports):

```go
// EffectiveSpeedMul returns the combined movement multiplier from all
// active status effects on the entity (Afterburner amplifies, Slow
// attenuates). Returns 1.0 if se is nil. Exported so tests can verify
// the combination directly without simulating motion.
func EffectiveSpeedMul(se *gamecomp.StatusEffects) float32 {
	mul := float32(1.0)
	if se == nil {
		return mul
	}
	if af := se.Get(gamecomp.StatusAfterburner); af != nil {
		mul *= af.Value
	}
	if sl := se.Get(gamecomp.StatusSlow); sl != nil {
		mul *= sl.Value
	}
	return mul
}
```

- [ ] **Step 2: Write failing tests**

Append to `internal/game/verb_status_test.go`:

```go
func TestEffectiveSpeedMul_NoEffects(t *testing.T) {
	got := EffectiveSpeedMul(nil)
	if got != 1.0 {
		t.Fatalf("nil StatusEffects = %v, want 1.0", got)
	}
	se := &gamecomp.StatusEffects{}
	if got := EffectiveSpeedMul(se); got != 1.0 {
		t.Fatalf("empty StatusEffects = %v, want 1.0", got)
	}
}

func TestEffectiveSpeedMul_SlowOnly(t *testing.T) {
	se := &gamecomp.StatusEffects{}
	se.Add(gamecomp.StatusEffect{Type: gamecomp.StatusSlow, Duration: 5, Value: 0.5})
	got := EffectiveSpeedMul(se)
	if absf(got-0.5) > 0.001 {
		t.Fatalf("slow mul = %v, want 0.5", got)
	}
}

func TestEffectiveSpeedMul_AfterburnerAndSlowStack(t *testing.T) {
	se := &gamecomp.StatusEffects{}
	se.Add(gamecomp.StatusEffect{Type: gamecomp.StatusAfterburner, Duration: 5, Value: 2.5})
	se.Add(gamecomp.StatusEffect{Type: gamecomp.StatusSlow, Duration: 5, Value: 0.5})
	got := EffectiveSpeedMul(se)
	want := float32(2.5 * 0.5) // 1.25
	if absf(got-want) > 0.001 {
		t.Fatalf("combined mul = %v, want %v", got, want)
	}
}

func TestStatusSilence_BlocksAbilityCast(t *testing.T) {
	gw := newTestGameWorld(t)
	p := spawnTestPlayer(t, gw, "silence_test")
	se := mmokit.Get[gamecomp.StatusEffects](p)
	se.Add(gamecomp.StatusEffect{Type: gamecomp.StatusSilence, Duration: 5.0, Value: 1})

	abilities := mmokit.Get[gamecomp.AbilitySet](p)
	cdBefore := abilities.Cooldowns[0]

	// Dispatch a cast on slot 0. Use whatever the existing test pattern
	// is for invoking the ability system from a test — most likely the
	// inbound input message handler. If a `gw.TryCastAbility(p, slot)`
	// helper does not exist, write one in entity_ship.go that mirrors
	// the body of the ability-cast input handler, so this test has a
	// clean entry point. The helper must return true if the cast was
	// dispatched (cooldown deducted) and false if it was rejected.
	ok := gw.TryCastAbility(p, 0)
	tickGW(gw, 0.05)

	if ok {
		t.Fatal("ability cast accepted under StatusSilence")
	}
	cdAfter := mmokit.Get[gamecomp.AbilitySet](p).Cooldowns[0]
	if absf(cdAfter-cdBefore) > 0.001 {
		t.Fatalf("cooldown changed despite silence: %v -> %v", cdBefore, cdAfter)
	}
}
```

(`spawnTestPlayer` must spawn a ship with `ShipControl`, `Health`, `AbilitySet`, `StatusEffects` components and return the `mmokit.Entity`. If `testutil_test.go` doesn't already have it, add one — model it on how existing tests in `system_npc_ai_test.go` already spawn an attacker. `gw.TryCastAbility(p, slot) bool` is a thin wrapper around the existing cast pipeline — implement it as a helper that calls the same internal cast function the input handler uses, so the test entry point matches production code path.)

- [ ] **Step 3: Run tests to verify failure**

Run: `go test ./internal/game/ -run "TestEffectiveSpeedMul|TestStatusSilence" -count=1 -v`
Expected: COMPILE ERROR (`StatusSlow`/`StatusSilence` undefined).

- [ ] **Step 4: Add enum values**

In `internal/component/components.go`, extend the `StatusType` const block:

```go
const (
	StatusNone        StatusType = 0
	StatusIonBurn     StatusType = 1
	StatusFortified   StatusType = 2
	StatusAfterburner StatusType = 3
	StatusShieldRegen StatusType = 4
	StatusSlow        StatusType = 5 // NEW — movement multiplier (Value < 1.0)
	StatusSilence     StatusType = 6 // NEW — disables ability casts (Value unused)
)
```

- [ ] **Step 5: Apply Slow in ship dynamics**

`system_ship_dynamics.go` line 72-74 currently reads Afterburner inline:

```go
		maxSpeed := ship.MaxSpeed
		if se := mmokit.Get[gamecomp.StatusEffects](entity); se != nil {
			if eff := se.Get(gamecomp.StatusAfterburner); eff != nil {
```

Replace that inline check with a call to `EffectiveSpeedMul`:

```go
		maxSpeed := ship.MaxSpeed
		se := mmokit.Get[gamecomp.StatusEffects](entity)
		maxSpeed *= EffectiveSpeedMul(se)
```

Remove the now-unused inner block that was multiplying by Afterburner. Check line ~200 of the same file too (second Afterburner reference) — if it's a different code path (e.g. visual-effect emission rather than speed), leave it alone; otherwise replace with the helper.

- [ ] **Step 6: Block ability casts on Silence**

In `internal/game/system_ability.go`, locate the ability cast dispatch — search for `Cooldowns[slot]` or `AbilitySet`. The check goes at the very top of the cast-eligibility flow, before cooldown deduction:

```go
	if se := mmokit.Get[gamecomp.StatusEffects](caster); se != nil && se.Has(gamecomp.StatusSilence) {
		return // silently drop the cast
	}
```

Then implement `gw.TryCastAbility(p mmokit.Entity, slot uint8) bool` as a thin wrapper that runs the same cast-eligibility check (Silence + cooldown gate) used by the input handler. The handler likely calls an internal `castAbility` or `dispatchCast` method; expose a test-friendly entry point that returns false on Silence and on cooldown.

- [ ] **Step 7: Run tests**

Run: `go test ./internal/game/ -run "TestEffectiveSpeedMul|TestStatusSilence" -count=1 -v`
Expected: PASS for all four.

Run: `go test ./internal/game/ -count=1`
Expected: PASS overall.

- [ ] **Step 8: Commit**

```bash
git add internal/component/components.go internal/game/system_ship_dynamics.go internal/game/system_ability.go internal/game/verb_status_test.go internal/game/entity_ship.go
git commit -m "status: add StatusSlow + StatusSilence enum + EffectiveSpeedMul helper"
```

---

## Task 13: Add Disruptor archetype + ability

**Files:**
- Modify: `internal/game/npc_archetype.go` (Disruptor constant + defaults)
- Modify: `internal/game/config.go` (Disruptor stats)
- Modify: `internal/game/system_npc_ai.go` (Disruptor cast cadence)
- Create: `internal/game/npc_archetype_disruptor_test.go`

- [ ] **Step 1: Add config fields**

In `internal/game/config.go`:

```go
	DisruptorHP                 float32 `json:"disruptor_hp"`
	DisruptorShield             float32 `json:"disruptor_shield"`
	DisruptorMaxSpeed           float32 `json:"disruptor_max_speed"`
	DisruptorTurnRate           float32 `json:"disruptor_turn_rate"`
	DisruptorAttackRange        float32 `json:"disruptor_attack_range"`
	DisruptorDebuffCooldown     float32 `json:"disruptor_debuff_cooldown"`
	DisruptorSlowDuration       float32 `json:"disruptor_slow_duration"`
	DisruptorSlowFactor         float32 `json:"disruptor_slow_factor"`
	DisruptorSilenceDuration    float32 `json:"disruptor_silence_duration"`
```

Defaults:

```go
		DisruptorHP:              90,
		DisruptorShield:          30,
		DisruptorMaxSpeed:        7,
		DisruptorTurnRate:        1.4,
		DisruptorAttackRange:     150,
		DisruptorDebuffCooldown:  6.0,
		DisruptorSlowDuration:    3.0,
		DisruptorSlowFactor:      0.5,
		DisruptorSilenceDuration: 2.0,
```

- [ ] **Step 2: Add Disruptor constant + defaults**

In `internal/game/npc_archetype.go`:

```go
	ArchetypeDisruptor uint8 = 8
```

Add to `archetypeDefaults`:

```go
	case ArchetypeDisruptor:
		return ArchetypeDefaults{
			HP:            cfg.DisruptorHP,
			Shield:        cfg.DisruptorShield,
			MaxSpeed:      cfg.DisruptorMaxSpeed,
			TurnRate:      cfg.DisruptorTurnRate,
			DamagePerShot: 0,
			AttackRange:   cfg.DisruptorAttackRange,
		}
```

Update name switch:

```go
	case ArchetypeDisruptor: return "disruptor"
```

- [ ] **Step 3: Write failing test for Disruptor ability**

Create `internal/game/npc_archetype_disruptor_test.go`:

```go
package game

import (
	"testing"

	gamecomp "github.com/mmokit/mmokit/internal/component"
	"github.com/mmokit/mmokit/pkg/mmokit"
)

func TestDisruptor_DebuffAppliesSlowAndSilence(t *testing.T) {
	gw := newTestGameWorld(t)
	disruptor := gw.SpawnNPC(0, 0, ArchetypeDisruptor, 0, NPCSpawnModifiers{})
	player := spawnTestPlayer(t, gw, "disruptor_target")

	// Move player into Disruptor attack range and aggro the disruptor.
	mmokit.Get[mmokit.Position](player).X = gw.Config.DisruptorAttackRange / 2
	if ai := mmokit.Get[gamecomp.NPCAI](disruptor); ai != nil {
		ai.TargetNetID = player.NetID()
		ai.State = AIStateEngage
	}

	// Tick long enough for one cast (cooldown + projectile flight + hit).
	for i := 0; i < 200; i++ {
		tickGW(gw, gw.Config.DisruptorDebuffCooldown/20)
	}

	se := mmokit.Get[gamecomp.StatusEffects](player)
	if se == nil {
		t.Fatal("no StatusEffects on player")
	}
	if !se.Has(gamecomp.StatusSlow) {
		t.Fatal("Slow not applied")
	}
	if !se.Has(gamecomp.StatusSilence) {
		t.Fatal("Silence not applied")
	}
}
```

- [ ] **Step 4: Run test to verify failure**

Run: `go test ./internal/game/ -run TestDisruptor -count=1 -v`
Expected: FAIL — no Disruptor cast logic.

- [ ] **Step 5: Add Disruptor cast cadence**

In `internal/game/system_npc_ai.go`, hook the Disruptor cast into the existing per-archetype tick. Find where Artillery and Brawler have their special ability cadence (likely `tickEngage` checks `ai.Archetype` for cooldown reads).

Add a Disruptor case that fires the debuff:

```go
	case ArchetypeDisruptor:
		ai.SpecialCooldown -= dt
		if ai.SpecialCooldown <= 0 && targetInRange(ai, e, pos, gw.Config.DisruptorAttackRange) {
			target := mmokit.EntityByNetID(gw.stage, ai.TargetNetID)
			if target.Alive() {
				gw.ApplyStatus(e, target, gamecomp.StatusSlow,
					gw.Config.DisruptorSlowDuration, gw.Config.DisruptorSlowFactor, 0, 0)
				gw.ApplyStatus(e, target, gamecomp.StatusSilence,
					gw.Config.DisruptorSilenceDuration, 1.0, 0, 0)
				ai.SpecialCooldown = gw.Config.DisruptorDebuffCooldown
				gw.eng.Log.Log(CatCombatAbility, "disruptor: debuff source=%d target=%d", e.NetID(), target.NetID())
			}
		}
```

The actual integration depends on the existing AI structure. If the test fails because of skillshot/projectile expectations (Disruptor needs a telegraphed projectile per the spec, not an instant apply), enhance with `entity_line_telegraph.go` + an `entity_projectile` that on collision applies both statuses. For v1 of this plan, an instant-apply at cooldown is acceptable; visual telegraph can land in a follow-up. Note in the commit message which simplification was chosen.

`SpecialCooldown` field: check that `gamecomp.NPCAI.SpecialCooldown` exists — `grep -n "SpecialCooldown" internal/component/components.go`. If not, add a `DebuffCooldown float32` field to `NPCAI` and use it here.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/game/ -run TestDisruptor -count=1 -v`
Expected: PASS.

Run: `go test ./internal/game/ -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/game/npc_archetype.go internal/game/config.go internal/game/system_npc_ai.go internal/game/npc_archetype_disruptor_test.go internal/component/components.go
git commit -m "disruptor: archetype + debuff ability (Slow + Silence on cooldown)"
```

---

## Task 14: Add DisruptorCell, HeavyBattalion, EliteAnchor rosters

**Files:**
- Modify: `internal/game/poi_config.go`

- [ ] **Step 1: Add roster constants and defs**

Replace the const block in `poi_config.go`:

```go
const (
	StarterArenaIdx    uint16 = 0
	SmallSkirmishIdx   uint16 = 1
	MediumWarbandIdx   uint16 = 2
	DisruptorCellIdx   uint16 = 3
	HeavyBattalionIdx  uint16 = 4
	EliteAnchorIdx     uint16 = 5
)
```

Append three new entries to `rosters`:

```go
	{
		Name: "Disruptor Cell",
		Members: []RosterMember{
			{Archetype: ArchetypeDisruptor, Count: 1, SpreadRadius: 30},
			{Archetype: ArchetypeLancer, Count: 2, SpreadRadius: 30},
			{Archetype: ArchetypeBrawler, Count: 1, SpreadRadius: 25},
		},
	},
	{
		Name: "Heavy Battalion",
		Members: []RosterMember{
			{Archetype: ArchetypeBrawler, Count: 3, SpreadRadius: 30},
			{Archetype: ArchetypeArtillery, Count: 2, SpreadRadius: 40},
			{Archetype: ArchetypeEliteLancer, Count: 2, SpreadRadius: 35},
		},
	},
	{
		Name: "Elite Anchor",
		Members: []RosterMember{
			{Archetype: ArchetypeBossGuardian, Count: 1, SpreadRadius: 0},
			{Archetype: ArchetypeSupport, Count: 1, SpreadRadius: 25},
			{Archetype: ArchetypeEliteBrawler, Count: 2, SpreadRadius: 30},
			{Archetype: ArchetypeEliteArtillery, Count: 2, SpreadRadius: 40},
		},
	},
```

- [ ] **Step 2: Update tierTable to reference new rosters**

```go
var tierTable = []TierDef{
	{Tier: 1, InnerRadius: 0, POIsPerCell: [2]int{3, 5}, StatMultiplier: 1.0, FluxRewardMul: 1.0, Rosters: []uint16{SmallSkirmishIdx, StarterArenaIdx}, CooldownSec: 180},
	{Tier: 2, InnerRadius: 16384, POIsPerCell: [2]int{1, 2}, StatMultiplier: 1.5, FluxRewardMul: 2.5, Rosters: []uint16{MediumWarbandIdx, DisruptorCellIdx}, CooldownSec: 300},
	{Tier: 3, InnerRadius: 32768, POIsPerCell: [2]int{0, 1}, StatMultiplier: 2.5, FluxRewardMul: 6.0, Rosters: []uint16{HeavyBattalionIdx, EliteAnchorIdx}, CooldownSec: 900},
}
```

- [ ] **Step 3: Run all tests**

Run: `go test ./internal/game/ -count=1`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/game/poi_config.go
git commit -m "tier: add DisruptorCell, HeavyBattalion, EliteAnchor rosters for T2/T3"
```

---

## Task 15: Wire POI tier into wire format + regenerate SDK

**Files:**
- (Tier field on POI is already declared with `net:"initial"` from Task 2.)
- Regenerate SDK; verify Tier field appears in TS.

- [ ] **Step 1: Regenerate SDK**

Run: `just space-sdk`
Expected: regenerates files under `web-pixi/sdk/`.

- [ ] **Step 2: Verify Tier field is exported**

Run: `grep -n "tier" web-pixi/sdk/entities/poi.ts`
Expected: `tier: number` field present in `POIEntity` interface.

If missing, check the POI bundle registration (e.g. via `grep -n "POIBundle\|RegisterKind.*POI" internal/game --include="*.go"`) — confirm POIBundle includes the `POI` component with no `mmokit:"local"` tag.

- [ ] **Step 3: Run typescript build to check downstream compile**

Run: `cd web-pixi && bun run check 2>&1 | head -20` (or `bun run build`)
Expected: clean.

- [ ] **Step 4: Commit if any regen produced changes**

```bash
git add web-pixi/sdk
git commit -m "sdk: regenerate with POI.Tier field"
```

(If `just space-sdk` produces no diff, the spec's wire change is already in place from Task 2; this task is verification-only.)

---

## Task 16: Client tier badge rendering

**Files:**
- Modify: `web-pixi/src/entities/poi.ts`

- [ ] **Step 1: Replace single color with tier-driven palette**

Replace the contents of `web-pixi/src/entities/poi.ts`:

```typescript
import { Container, Graphics, Text } from "pixi.js";
import type { POIEntity } from "../../sdk/index.js";
import type { ClientEntity, EntityDisplayObject } from "../types";
import { px } from "../view";

const POI_STATUS_ACTIVE = 0;
const POI_STATUS_CLEARED = 1;

const POI_TYPE_NAMES: Record<number, string> = {
  0: "POI",
  1: "Anomaly",
  2: "Distress",
  3: "Convoy",
};

// Tier color palette. Matches the spec design section.
const TIER_COLORS: Record<number, number> = {
  1: 0x5bd078, // green
  2: 0xe8b53b, // amber
  3: 0xd04545, // red
};

// Tier-relative size scaling. T3 markers are visually larger to convey
// "destination" content; T1 markers are intentionally small.
const TIER_RADIUS_MUL: Record<number, number> = {
  1: 0.7,
  2: 1.0,
  3: 1.4,
};

function colorForTier(tier: number): number {
  return TIER_COLORS[tier] ?? TIER_COLORS[1];
}

function radiusMulForTier(tier: number): number {
  return TIER_RADIUS_MUL[tier] ?? 1.0;
}

export function createPoiDisplay(): EntityDisplayObject {
  const container = new Container();
  const ring = new Graphics();
  container.addChild(ring);

  const label = new Text({
    text: "POI",
    style: { fontFamily: "monospace", fontSize: 11, fontWeight: "bold", fill: 0xffffff },
  });
  label.anchor.set(0.5, 1);
  label.scale.set(px(1), px(1));
  container.addChild(label);

  const subLabel = new Text({
    text: "",
    style: { fontFamily: "monospace", fontSize: 9, fill: 0xffffff },
  });
  subLabel.anchor.set(0.5, 0);
  subLabel.scale.set(px(1), px(1));
  container.addChild(subLabel);

  let lastStatus = -1;
  let lastType = -1;
  let lastRadius = 0;
  let lastTier = -1;

  function redraw(status: number, type: number, radius: number, tier: number) {
    const active = status === POI_STATUS_ACTIVE;
    const tierColor = colorForTier(tier);
    const baseColor = active ? tierColor : 0x666666;
    const baseAlpha = active ? 1.0 : 0.4;
    const r = radius * radiusMulForTier(tier);

    ring.clear();
    ring.circle(0, 0, r).stroke({ color: baseColor, width: px(2), alpha: baseAlpha });
    ring.circle(0, 0, r * 0.65).stroke({ color: baseColor, width: px(1), alpha: baseAlpha * 0.4 });
    const tickInner = r * 0.85;
    const tickOuter = r * 1.05;
    for (let i = 0; i < 4; i++) {
      const a = (i / 4) * Math.PI * 2;
      ring.moveTo(Math.cos(a) * tickInner, Math.sin(a) * tickInner)
          .lineTo(Math.cos(a) * tickOuter, Math.sin(a) * tickOuter)
          .stroke({ color: baseColor, width: px(1.5), alpha: baseAlpha });
    }
    ring.circle(0, 0, px(2)).fill({ color: baseColor, alpha: baseAlpha });

    const name = POI_TYPE_NAMES[type] ?? `POI #${type}`;
    label.text = `${name.toUpperCase()} T${tier}`;
    label.style.fill = baseColor;
    label.alpha = baseAlpha;
    label.position.set(0, -r - px(4));

    const statusText = status === POI_STATUS_ACTIVE
      ? ""
      : status === POI_STATUS_CLEARED
        ? "CLEARED"
        : "COOLDOWN";
    subLabel.text = statusText;
    subLabel.style.fill = baseColor;
    subLabel.alpha = baseAlpha * 0.8;
    subLabel.position.set(0, r + px(4));
  }

  return {
    container,
    update(ent: ClientEntity, _isMe: boolean, now: number) {
      const e = ent.current as POIEntity;
      const radius = Math.max(e.radius || 60, 60);
      const tier = e.tier || 1;
      if (
        e.status !== lastStatus ||
        e.type !== lastType ||
        radius !== lastRadius ||
        tier !== lastTier
      ) {
        lastStatus = e.status;
        lastType = e.type;
        lastRadius = radius;
        lastTier = tier;
        redraw(e.status, e.type, radius, tier);
      }

      if (e.status === POI_STATUS_ACTIVE) {
        container.alpha = 0.75 + 0.25 * Math.sin(now * 0.003);
      } else {
        container.alpha = 1.0;
      }
    },
    destroy() {
      container.destroy({ children: true });
    },
  };
}
```

- [ ] **Step 2: Build the client**

Run: `cd web-pixi && bun run build`
Expected: build succeeds.

- [ ] **Step 3: Manual smoke**

`just dev` (or check with a previously-running session), connect with browser, scout outward. Verify:
- Inner-ring POIs render green and small.
- Mid-ring POIs render amber and medium.
- Far-ring POIs render red and large.

(This task does not have an automated test — pure rendering.)

- [ ] **Step 4: Commit**

```bash
git add web-pixi/src/entities/poi.ts
git commit -m "client: tier-colored POI markers (green/amber/red) + size scaling"
```

---

## Task 17: Client debug tier rings overlay

**Files:**
- Modify: `web-pixi/src/ui/dev-overlay.ts`

- [ ] **Step 1: Locate the dev overlay**

Run: `grep -n "drawCellGrid\|drawCellOverlay\|Backquote" web-pixi/src/ui/dev-overlay.ts | head -10`

Find the function that renders the existing cell debug overlay (toggled by Backquote per commit `e922843`).

- [ ] **Step 2: Add tier ring rendering**

Inside the dev-overlay rendering function, after existing overlay code, add:

```typescript
// Tier rings — concentric circles centered on the station position.
// Tier 1/2 boundary = TIER_WIDTH_1, Tier 2/3 boundary = TIER_WIDTH_2.
// Match server tierTable[].InnerRadius (Task 1 of plan).
const TIER_WIDTH_1 = 16384;
const TIER_WIDTH_2 = 32768;

function drawTierRings(g: Graphics, stationWorldX: number, stationWorldY: number) {
  // The two boundary rings as dashed-equivalent strokes (Pixi v8 has no
  // built-in dashed strokes; use semi-transparent solid).
  g.circle(stationWorldX, stationWorldY, TIER_WIDTH_1)
    .stroke({ color: 0xe8b53b, width: px(2), alpha: 0.25 });
  g.circle(stationWorldX, stationWorldY, TIER_WIDTH_2)
    .stroke({ color: 0xd04545, width: px(2), alpha: 0.25 });
}
```

Wire `drawTierRings(g, stationX, stationY)` into the existing overlay render — `stationX` and `stationY` should be available from the same state that already drives the existing cell overlay (look for how the station marker is rendered).

- [ ] **Step 3: Build + smoke**

Run: `cd web-pixi && bun run build`
Expected: clean.

Manual: open the game, press Backquote, verify two faint rings centered on the station.

- [ ] **Step 4: Commit**

```bash
git add web-pixi/src/ui/dev-overlay.ts
git commit -m "client: dev-overlay tier rings (T1/T2 + T2/T3 boundaries)"
```

---

## Task 18: Integration test — Support presence raises roster TTK

**Files:**
- Create: `internal/game/poi_support_integration_test.go`

- [ ] **Step 1: Write the integration test**

```go
package game

import (
	"testing"

	gamecomp "github.com/mmokit/mmokit/internal/component"
	"github.com/mmokit/mmokit/pkg/mmokit"
)

// TestPOI_SupportIncreasesTTK spawns two T1 POIs side-by-side: one with
// Support added, one without. A scripted attacker damages each roster
// at a fixed rate; the Support-bearing roster should take measurably
// longer to fully die because Support heals members.
func TestPOI_SupportIncreasesTTK(t *testing.T) {
	gw := newTestGameWorld(t)

	// POI A: plain Starter Arena.
	poiA := gw.SpawnPOI(500, 500, 0, StarterArenaIdx, 1)
	// POI B: same roster + 1 Support attached.
	poiB := gw.SpawnPOI(2500, 500, 0, StarterArenaIdx, 1)
	gw.SpawnNPC(2500, 500, ArchetypeSupport, poiB, NPCSpawnModifiers{})

	tickA := simulateDPS(t, gw, poiA, /*dps=*/ 30)
	tickB := simulateDPS(t, gw, poiB, /*dps=*/ 30)

	if tickB <= tickA {
		t.Fatalf("Support roster TTK %d not greater than control %d", tickB, tickA)
	}
	t.Logf("control TTK=%d ticks, support TTK=%d ticks (delta=%d)", tickA, tickB, tickB-tickA)
}

// simulateDPS applies dps damage per second across the roster of poiNetID
// (split among living members) until roster is fully dead. Returns ticks
// elapsed.
func simulateDPS(t *testing.T, gw *GameWorld, poiNetID uint32, dps float32) int {
	t.Helper()
	const dt float32 = 0.05
	for i := 0; i < 10000; i++ {
		alive := []mmokit.Entity{}
		for _, nid := range gw.poiRosters[poiNetID] {
			e := mmokit.EntityByNetID(gw.stage, nid)
			if !e.Alive() {
				continue
			}
			h := mmokit.Get[gamecomp.Health](e)
			if h == nil || h.Current <= 0 {
				continue
			}
			alive = append(alive, e)
		}
		if len(alive) == 0 {
			return i
		}
		per := dps * dt / float32(len(alive))
		for _, e := range alive {
			h := mmokit.Get[gamecomp.Health](e)
			h.Current -= per
		}
		tickGW(gw, dt)
	}
	t.Fatal("simulateDPS exceeded 10k ticks")
	return 0
}
```

- [ ] **Step 2: Run integration test**

Run: `go test ./internal/game/ -run TestPOI_SupportIncreasesTTK -count=1 -v`
Expected: PASS (Support roster takes more ticks to die).

- [ ] **Step 3: Commit**

```bash
git add internal/game/poi_support_integration_test.go
git commit -m "tier: integration test — Support measurably increases roster TTK"
```

---

## Task 19: Final smoke + post-implementation tuning

**Files:**
- (None — manual smoke + potential follow-up tuning commits.)

- [ ] **Step 1: Full server-side test pass**

Run: `go test ./... -count=1`
Expected: PASS for the whole repo.

- [ ] **Step 2: Vet + lint**

Run: `go vet ./...`
Expected: clean.

Run: `just lint-no-ark` (from CLAUDE.md — enforces game/ doesn't import ark/ecs)
Expected: clean.

- [ ] **Step 3: Build the binary**

Run: `just build`
Expected: binary in `bin/`.

- [ ] **Step 4: Smoke under `just dev`**

Start the server: `just dev` (or have the user start it; do not leave background processes per project preference).

- Connect with browser.
- Inspect inner ring: small green T1 POI markers, multiple per cell, ~30s travel between encounters.
- Push out to ~16k units (T2 boundary): markers turn amber; rosters include Disruptor variants — verify a Slow + Silence application during combat.
- Push to ~32k units (T3): markers turn red, larger, sparse; the EliteAnchor POI contains a BossGuardian + Support. Confirm Support visually retreats from your close approach and heals an ally between attacks.
- Press Backquote: verify two tier rings render around the station.
- Clear a T1 POI: verify Flux drop ≈ baseline.
- Clear a T3 POI: verify Flux drop ~6× baseline.

- [ ] **Step 5: Adjust if reward / pacing feels off**

If T3 rewards feel too high or T1 cadence feels wrong, tune `tierTable` values in `poi_config.go` and commit separately. No new structural code changes.

- [ ] **Step 6: Final commit (if tuning happened)**

```bash
git add internal/game/poi_config.go
git commit -m "tier: tune density / reward multipliers based on smoke"
```

---

## Notes for the implementer

- **TDD discipline**: each task starts with a failing test. If a test would be impossible to write before the implementation (rare), write the implementation as a thin stub first, then the test, then fix the stub — never skip.
- **No bg processes**: do not leave `just dev` / `just distributed` running; ask the user to start them for smoke. (`feedback_no_leftover_processes` memory.)
- **No worktrees**: this work is sequential. Don't open a worktree. (`feedback_no_worktrees` memory.)
- **No backward-compat aliases**: when extending NPCSpawnModifiers or POI, don't preserve old type names; update callers directly. (`feedback_no_backward_compat`.)
- **mmokit-only imports from game**: `internal/game/` imports only `pkg/mmokit`, never `pkg/component` directly — except aliased as `gamecomp` for the internal component package per existing convention. (`feedback_mmokit_facade_only`.)
- **Logging**: every new server-side game-logic addition (Support heal, Disruptor debuff, POI clear with tier) must log via `gw.eng.Log.Log(CatXxx, ...)`. (`feedback_logging`.)
- **Generated files**: never hand-edit anything under `web-pixi/sdk/`. Always regen via `just space-sdk`. (`feedback_no_handpatching_generated`.)
- **Proto + wire**: this design adds no new proto fields. POI.Tier rides the existing typed-reflection-codec auto-replicator. No `buf generate` needed.
- **Position quantization**: tier ring radii are world-space distances; they're used as thresholds, not transmitted. No quantization concern. (`feedback_position_quantization`.)
- **Cross-cell transfers**: POI.Tier is on the POI component which is fully replicated — tier survives cell splits/merges by construction. No special handoff handling needed. (`feedback_local_only_spawn_config` does not apply here — Tier is `net:"initial"`, serialized.)
- **Test fixture caveats**: if a test helper (e.g. `newTestGameWorld`, `tickGW`, `spawnTestPlayer`) doesn't yet exist in the harness for the shape the test needs, extend `testutil_test.go` with a minimal version first. Reuse the pattern other test files use.
