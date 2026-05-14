# Skillshot Combat Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Convert player damage abilities to MOBA-style skillshots (aimed line + ground-target + cursor-aimed channel) per [docs/superpowers/specs/2026-05-14-skillshot-combat-design.md](../specs/2026-05-14-skillshot-combat-design.md). Lock-on stays for utility/heavy abilities (HomingMissile, PlasmaTorpedo, IonBurn, MiningBeam). Brawler gets a second attack — a telegraphed line-cone — so player↔NPC combat language stays symmetric.

**Architecture:** Add `TargetingMode` to `AbilityParams`. Extend `CastAbility` wire with `AimX/AimY`. Five dispatch paths in `AbilitySystem.executeAbility` keyed on `TargetingMode`. Pierce mechanic on Projectile. Rename LanceTelegraph→LineTelegraph (reused by Lancer + Brawler). Client side: aim-state machine + new `aim-indicator.ts` + per-slot quickcast settings.

**Tech Stack:** Go (server, ark ECS via mmokit facade), TypeScript + PixiJS (web client, auto-generated SDK), PostgreSQL (untouched here).

---

## File Structure

**New files (server):**
- None — all server changes extend existing files

**Modified files (server):**
- `internal/item/item.go` — `TargetingMode` enum, `GroundCastDelay` field, per-ability classification
- `internal/game/input_messages.go` — extend `CastAbility`, add `ChannelAim`
- `internal/game/input_handlers.go` — pass aim coords to AbilitySystem; handle `ChannelAim`
- `internal/component/channeling.go` — add `AimX`/`AimY` fields
- `internal/component/projectile.go` — add `PierceCount` field, `PiercedNetIDs` array
- `internal/game/system_ability.go` — refactor dispatch to switch on TargetingMode; new Line/Ground/Channel dispatches; aim-aware channel tick
- `internal/game/system_projectile.go` — handle PierceCount on hit
- `internal/component/components.go` — rename `KindLanceTelegraph` → `KindLineTelegraph`; add Brawler NPCAI fields
- `internal/component/lance.go` → `internal/component/line_telegraph.go` (file rename with content unchanged except type names)
- `internal/game/entity_lance.go` → `internal/game/entity_line_telegraph.go` (file rename)
- `internal/game/entity_kinds.go` — rename binding to LineTelegraph
- `internal/game/npc_archetype.go` — Brawler special config in archetype defaults
- `internal/game/config.go` — Brawler special config fields + defaults + ConfigVersion bump
- `internal/game/system_npc_ai.go` — Lancer windup uses renamed types; Brawler special trigger + resolve

**New files (client):**
- `web-pixi/src/effects/aim-indicator.ts` — aim-state machine + per-mode preview visuals
- `web-pixi/src/ui/quickcast-settings.ts` — settings panel section + localStorage

**Modified files (client):**
- `web-pixi/src/input.ts` — route ability presses through aim-state machine; send ChannelAim updates
- `web-pixi/src/state.ts` — `aimingSlot`, `quickcastMask` fields
- `web-pixi/src/network.ts` — wire the new ChannelAim send path
- `web-pixi/src/main.ts` — wire `aim-indicator.ts` into render loop
- `web-pixi/src/entities/lance-telegraph.ts` → `web-pixi/src/entities/line-telegraph.ts` (file rename)
- `web-pixi/src/entities/entity-manager.ts` — case rename
- `web-pixi/sdk/` — regenerated

---

## Task Ordering Rationale

Phase A wires data structures (TargetingMode + new fields). Phase B refactors server dispatch to use them. Phase C ships LineTelegraph rename + Brawler dual-attack. Phase D regenerates the SDK and builds the client. Phase E covers tests + final smoke.

**Player can press abilities and they fire reasonably for the existing modes (Self/LockOn) after Phase B; full skillshot flow is testable after Phase D.**

---

### Task 1: Add TargetingMode enum + per-ability classification

**Files:**
- Modify: `internal/item/item.go`

- [ ] **Step 1: Add the enum**

After the `AbilityType` constants block (around line 72), add:

```go
// TargetingMode describes how an ability resolves its target. The
// per-ability targeting model — see docs/superpowers/specs/
// 2026-05-14-skillshot-combat-design.md for the full design.
type TargetingMode uint8

const (
	TargetingSelf             TargetingMode = 0 // no aim, no target (default — abilities missing classification will Self-cast, visible bug)
	TargetingLockOn           TargetingMode = 1 // requires active TargetLock
	TargetingSkillshotLine    TargetingMode = 2 // direction from caster toward cursor; fires Projectile
	TargetingSkillshotGround  TargetingMode = 3 // cursor position; drops AoEMarker
	TargetingSkillshotChannel TargetingMode = 4 // held; beam tracks cursor direction
)
```

- [ ] **Step 2: Add fields to AbilityParams**

In the existing `AbilityParams` struct, add two new fields. Find the existing fields and append:

```go
// PVE v3 — skillshot combat
Mode             TargetingMode // how this ability resolves its target
GroundCastDelay  float32       // SkillshotGround: seconds the AoE marker waits before detonating (~0.6s for player abilities)
```

- [ ] **Step 3: Classify every existing ability**

In `item.go`'s `doInit()` function, add `Mode:` to every `AbilityParams` entry. Match the spec's table:

For item 100 (Pulse Laser Array):
- Primary `PulseLaser` → `Mode: item.TargetingSkillshotLine`
- Secondary `PulseBarrage` → `Mode: item.TargetingSkillshotLine`

For item 101 (Railgun System):
- Primary `RailShot` → `Mode: item.TargetingSkillshotLine`
- Secondary `PiercingRound` → `Mode: item.TargetingSkillshotLine`

For item 105 (Ion Array):
- Primary `IonBurn` → `Mode: item.TargetingLockOn`
- Secondary `IonOverload` → `Mode: item.TargetingSkillshotLine`

For item 106 (Plasma System):
- Primary `PlasmaBolt` → `Mode: item.TargetingSkillshotLine`
- Secondary `PlasmaTorpedo` → `Mode: item.TargetingLockOn`

For item 107 (Plasma Cannon):
- Primary `PlasmaShot` → `Mode: item.TargetingSkillshotLine`
- Secondary `HomingMissile` → `Mode: item.TargetingLockOn`

For item 108 (Beam-Mortar Battery):
- Primary `SustainedBeam` → `Mode: item.TargetingSkillshotChannel`
- Secondary `MortarShell` → `Mode: item.TargetingSkillshotGround`, `GroundCastDelay: 0.6`

For items 110 (Standard Shield) + 111 (Hardened Shield):
- Primary `EmergencyShield`/`HardenedShield` → `Mode: item.TargetingSelf`

For items 120 (Standard Thruster) + 121 (Micro Warp Drive):
- Primary `Afterburner`/`MicroWarp` → `Mode: item.TargetingSelf`

For items 130 (Mining Laser) + 131 (Deep Core Mining Laser):
- Primary `MiningBeam` → `Mode: item.TargetingLockOn`
- Secondary `ExtractPulse` → `Mode: item.TargetingLockOn`

(File names: the actual paths use `item.` prefix because callers reference these constants from outside the package. Inside `item.go` itself, the const reference is unqualified — e.g. `Mode: TargetingSkillshotLine` not `Mode: item.TargetingSkillshotLine`.)

- [ ] **Step 4: Verify build**

Run: `cd . && go vet ./...`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
cd .
git add internal/item/item.go
git commit -m "$(cat <<'EOF'
feat(item): add TargetingMode + classify every ability

Adds the TargetingMode enum (Self/LockOn/SkillshotLine/SkillshotGround/
SkillshotChannel) and an AbilityParams.Mode field. Classifies every
existing AbilityType per the skillshot-combat spec — basic damage
weapons (PulseShot, PlasmaBolt, etc.) become SkillshotLine; MortarShell
becomes SkillshotGround; SustainedBeam becomes SkillshotChannel; heavy
single-target abilities (HomingMissile, PlasmaTorpedo, IonBurn) stay
LockOn; shield/thruster stay Self; mining stays LockOn.

GroundCastDelay added for SkillshotGround abilities — the AoE marker's
lifetime in seconds.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Player ability tuning per spec §6

**Files:**
- Modify: `internal/item/item.go`

The spec §6 tuning table shifts a few abilities to compensate for the new aiming requirement (skillshots reward the player's aim instead of always-hits). Update the AbilityParams entries:

- [ ] **Step 1: Adjust PulseLaser, PulseBarrage**

Find the item 100 (Pulse Laser Array) entries. Adjust:

- `PulseLaser`: `Range: 30.0, Cooldown: 1.0` (already set in prior commits) — verify
- `PulseBarrage`: change `Damage: 50.0, Range: 50.0, Cooldown: 5.0` → `Damage: 20.0, Range: 40.0, Cooldown: 4.0` (3 sub-projectiles at 20 each = 60 burst, reachable through aim)

- [ ] **Step 2: Adjust RailShot, PiercingRound**

Item 101:
- `RailShot`: change `Damage: 35, Range: 33.3, Cooldown: 6.0` → `Damage: 50, Range: 60, Cooldown: 3.0` (pierces 2 — long-range piercer)
- `PiercingRound`: change `Damage: 50, BonusDamage: 20, Range: 26.7, Cooldown: 10.0` → `Damage: 40, BonusDamage: 15, Range: 35, Cooldown: 8.0` (pierces every target in line)

- [ ] **Step 3: Adjust IonOverload, PlasmaBolt**

Item 105: `IonOverload`: change `Damage: 40, Range: 20.0, Cooldown: 12.0` → `Damage: 40, Range: 30, Cooldown: 8.0`.

Item 106: `PlasmaBolt`: change `Damage: 20, Range: 23.3, Cooldown: 4.0` → `Damage: 25, Range: 35, Cooldown: 3.0`.

(PlasmaTorpedo, IonBurn — leave as-is; they remain LockOn.)

- [ ] **Step 4: Adjust PlasmaShot, MortarShell**

Item 107 primary: `PlasmaShot` already at `Damage: 30, Range: 40, Cooldown: 1.0` (from prior PVE v2 rebalance) — verify, no change.

Item 108 secondary: `MortarShell` currently at `Damage: 60, Range: 40, Cooldown: 6, SplashRadius: 6, SplashDamage: 40` — leave as-is. (The aim model is what changes, not the numbers.)

- [ ] **Step 5: Verify**

Run: `cd . && go vet ./... && go test ./internal/game/ 2>&1 | tail -10`
Expected: clean. Tests that asserted specific damage/cooldown values may need adjustment — update test assertions to match new values where appropriate.

- [ ] **Step 6: Commit**

```bash
cd .
git add internal/item/item.go
git commit -m "$(cat <<'EOF'
tune(item): skillshot tuning per spec §6 — buff cooldowns/range, balance damage

PulseBarrage: 50/5s → 20×3/4s (60 burst aimed)
RailShot: 35/6s → 50/3s, range 33.3 → 60 (pierces 2)
PiercingRound: 50+20/10s → 40+15/8s, range 27 → 35 (pierces all)
IonOverload: 40/12s → 40/8s, range 20 → 30
PlasmaBolt: 20/4s → 25/3s, range 23 → 35

Skillshot abilities reward aim — pricier when you whiff, snappier when
you land. Lock-on (HomingMissile, PlasmaTorpedo, IonBurn) and the
4 PVE v2 weapons unchanged from prior rebalance.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Extend CastAbility wire with AimX/AimY + add ChannelAim message

**Files:**
- Modify: `internal/game/input_messages.go`

- [ ] **Step 1: Extend CastAbility**

Find the existing struct (around line 43):

```go
type CastAbility struct {
	Sequence uint32
	Slot     uint8
}
```

Replace with:

```go
// CastAbility — discrete ability press. Slot identifies which equipped
// ability fired. AimX/AimY carry the cursor world position; meaning
// depends on the ability's TargetingMode:
//   Self            — ignored
//   LockOn          — ignored (server reads TargetLock.ActiveNetID)
//   SkillshotLine   — direction = (AimX - shipX, AimY - shipY), normalized
//   SkillshotGround — drop the AoE at (AimX, AimY), clamped to ability range
//   SkillshotChannel — initial aim point; updates via ChannelAim while held
type CastAbility struct {
	Sequence uint32
	Slot     uint8
	AimX     float32
	AimY     float32
}
```

- [ ] **Step 2: Add ChannelAim message**

Append after `CastAbility`:

```go
// ChannelAim — cursor update for an in-flight SkillshotChannel ability.
// Client sends one per tick while the channel is held; server applies
// to the player's Channeling component AimX/AimY fields.
type ChannelAim struct {
	Sequence uint32
	Slot     uint8
	AimX     float32
	AimY     float32
}
```

- [ ] **Step 3: Verify build**

Run: `cd . && go vet ./...`
Expected: clean.

- [ ] **Step 4: Commit**

```bash
cd .
git add internal/game/input_messages.go
git commit -m "$(cat <<'EOF'
feat(input): extend CastAbility with AimX/AimY + add ChannelAim message

CastAbility now carries cursor world coords so skillshot abilities can
fire in the player's aim direction. Semantics per TargetingMode: Line
uses (AimX - shipX, AimY - shipY) as the direction vector; Ground drops
the AoE at (AimX, AimY); Self/LockOn ignore the aim fields.

ChannelAim is the in-flight cursor update for a SkillshotChannel
(SustainedBeam) — client streams ~20 Hz while the channel is held; the
server applies it to the player's Channeling component each tick.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Extend Channeling component with aim coords

**Files:**
- Modify: `internal/component/channeling.go`

- [ ] **Step 1: Add fields**

Replace the existing struct:

```go
type Channeling struct {
	SlotID        uint8   `mmokit:"local"`
	RemainingTime float32 `mmokit:"local"`
	NextTickIn    float32 `mmokit:"local"`
	TargetNetID   uint32  `mmokit:"local"`
}
```

With:

```go
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
```

- [ ] **Step 2: Verify build**

Run: `cd . && go vet ./...`
Expected: clean.

- [ ] **Step 3: Commit**

```bash
cd .
git add internal/component/channeling.go
git commit -m "$(cat <<'EOF'
feat(component): add AimX/AimY to Channeling for skillshot channels

SustainedBeam is becoming a SkillshotChannel — the beam tracks the
cursor direction instead of a locked target. Channeling now stores the
current cursor world coords; tickChannels reads them to compute the
hitscan line each tick. TargetNetID kept for compatibility / future
LockOn channels.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Extend ProjectileSpec with PierceCount

**Files:**
- Modify: `internal/component/projectile.go`

- [ ] **Step 1: Add fields**

Find the existing struct and add `PierceCount` + `PiercedNetIDs`:

```go
type ProjectileSpec struct {
	OwnerNetID    uint32  `net:"u32"`
	TargetNetID   uint32  `net:"u32"`
	Damage        float32 `net:"f32"`
	SplashRadius  float32 `net:"f32"`
	SplashDamage  float32 `net:"f32"`
	MaxTurnRate   float32 `net:"f32"`
	Type          uint8   `net:"u8"`

	// PierceCount: remaining pierces before the projectile despawns on
	// hit. 0 = stop at first non-owner hit (default). >0 = pass through;
	// decrement on each hit. PiercedNetIDs prevents re-hitting the same
	// victim along the line.
	PierceCount   uint8     `net:"u8"`
	PiercedNetIDs [4]uint32 // server-only (no net tag) — already-hit victims
}
```

- [ ] **Step 2: Verify build**

Run: `cd . && go vet ./...`
Expected: clean.

- [ ] **Step 3: Commit**

```bash
cd .
git add internal/component/projectile.go
git commit -m "$(cat <<'EOF'
feat(component): add PierceCount + PiercedNetIDs to ProjectileSpec

Line skillshots can pierce multiple targets. PierceCount=0 (default)
keeps the existing "stop at first hit" behavior. PierceCount>0 (e.g. 2
for RailShot, 99 for PiercingRound) decrements per non-owner hit;
PiercedNetIDs[] prevents re-hitting the same victim along the line
(matches Ezreal Q / LoL line-pierce semantics).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: ProjectileSystem honors PierceCount

**Files:**
- Modify: `internal/game/system_projectile.go`

- [ ] **Step 1: Read existing hit logic**

Open the file. Find the impact block — looks for a `victim.Alive()` check followed by `gw.Damage(...)`. Around line 90-130. Note the current structure: on hit, applies damage, optionally spawns splash AoE, despawns.

- [ ] **Step 2: Add pierce handling**

Replace the impact block with pierce-aware logic. The exact existing block was:

```go
if victim.Alive() {
    caster := mmokit.EntityByNetID(gw.stage, spec.OwnerNetID)
    if caster.Alive() {
        gw.Damage(caster, victim, spec.Damage, 0, 0, projectileAbilityType(spec.Type))
    } else {
        gw.ApplyDamage(victim, spec.Damage, spec.OwnerNetID)
    }
    if spec.SplashRadius > 0 {
        mask := factionMaskFromOwner(gw, spec.OwnerNetID)
        gw.SpawnAoEMarker(pos.X, pos.Y, 0, spec.SplashRadius, spec.SplashDamage, spec.OwnerNetID, mask)
    }
    gw.eng.Log.Log(CatCombatHit, "projectile: hit netID=%d dmg=%.0f splash=%.0f",
        victim.NetID(), spec.Damage, spec.SplashRadius)
    s.Commands().Despawn(proj.Handle())
    continue
}
```

Replace with:

```go
if victim.Alive() {
    victimNetID := victim.NetID()

    // Skip already-pierced victims so the same target can't take
    // multiple hits from one line shot.
    alreadyHit := false
    for _, id := range spec.PiercedNetIDs {
        if id == victimNetID {
            alreadyHit = true
            break
        }
    }
    if !alreadyHit {
        caster := mmokit.EntityByNetID(gw.stage, spec.OwnerNetID)
        if caster.Alive() {
            gw.Damage(caster, victim, spec.Damage, 0, 0, projectileAbilityType(spec.Type))
        } else {
            gw.ApplyDamage(victim, spec.Damage, spec.OwnerNetID)
        }
        if spec.SplashRadius > 0 {
            mask := factionMaskFromOwner(gw, spec.OwnerNetID)
            px2, py2 := pos.X, pos.Y
            splashR, splashD := spec.SplashRadius, spec.SplashDamage
            ownerID := spec.OwnerNetID
            s.Commands().Defer(func() {
                gw.SpawnAoEMarker(px2, py2, 0, splashR, splashD, ownerID, mask)
            })
        }
        gw.eng.Log.Log(CatCombatHit, "projectile: hit netID=%d dmg=%.0f pierce=%d",
            victimNetID, spec.Damage, spec.PierceCount)

        if spec.PierceCount > 0 {
            // Record + continue. Append to first free slot in PiercedNetIDs.
            for i := range spec.PiercedNetIDs {
                if spec.PiercedNetIDs[i] == 0 {
                    spec.PiercedNetIDs[i] = victimNetID
                    break
                }
            }
            spec.PierceCount--
            // Don't despawn — the projectile keeps flying.
            continue
        }

        s.Commands().Despawn(proj.Handle())
        continue
    }
}
```

(Note: `Spec` is `*gamecomp.ProjectileSpec` via the query bundle, so mutating `spec.PierceCount` and `spec.PiercedNetIDs[i]` writes back to ECS storage. Confirm by checking the bundle definition in the system's `entities` query.)

- [ ] **Step 3: Verify build**

Run: `cd . && go vet ./internal/game/...`
Expected: clean.

- [ ] **Step 4: Verify tests still pass**

Run: `cd . && go test ./internal/game/ -run TestProjectile -v 2>&1 | tail -15`
Expected: existing projectile tests pass (default `PierceCount=0` preserves single-hit behavior).

- [ ] **Step 5: Commit**

```bash
cd .
git add internal/game/system_projectile.go
git commit -m "$(cat <<'EOF'
feat(game): ProjectileSystem honors PierceCount and tracks pierced victims

On hit: if PierceCount==0, apply damage + despawn (existing). If
PierceCount>0, apply damage, record the victim in PiercedNetIDs to
prevent re-hits, decrement PierceCount, and continue flying. Same
victim cannot be pierced twice on the same projectile.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Set PierceCount on RailShot + PiercingRound at spawn

**Files:**
- Modify: `internal/game/system_ability.go`

- [ ] **Step 1: Find fireProjectile**

Look at the existing `fireProjectile` method (around line 411-470). It already constructs a `ProjectileSpec` but doesn't set PierceCount. The new dispatch (Task 8) will pass pierce info via a different code path, so for THIS task we just teach `fireProjectile` to read from a new `pierceCount` arg.

- [ ] **Step 2: Add pierceCount parameter to fireProjectile**

Change the signature:

```go
func (s *AbilitySystem) fireProjectile(
    caster mmokit.Entity, params *item.AbilityParams, projType uint8,
) {
```

To:

```go
func (s *AbilitySystem) fireProjectile(
    caster mmokit.Entity, params *item.AbilityParams, projType uint8, pierceCount uint8,
) {
```

Update the spec construction inside to pass it:

```go
spec := gamecomp.ProjectileSpec{
    OwnerNetID:   caster.NetID(),
    TargetNetID:  targetNetID,
    Damage:       params.Damage,
    SplashRadius: params.SplashRadius,
    SplashDamage: params.SplashDamage,
    MaxTurnRate:  params.HomingMaxTurnRate,
    Type:         projType,
    PierceCount:  pierceCount,
}
```

- [ ] **Step 3: Update existing call sites**

Find every existing `s.fireProjectile(...)` call (3 of them, for PlasmaShot/MortarShell/HomingMissile dispatch cases) and append `, 0` as the new argument. Existing behavior preserved.

- [ ] **Step 4: Verify build**

Run: `cd . && go vet ./...`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
cd .
git add internal/game/system_ability.go
git commit -m "$(cat <<'EOF'
feat(ability): plumb pierceCount through fireProjectile

Preparation for SkillshotLine dispatch: fireProjectile now accepts a
pierceCount argument so the per-ability dispatch can request pierce
behavior (RailShot=2, PiercingRound=99, all others=0). All existing
call sites pass 0 — no behavior change yet.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Refactor executeAbility to switch on TargetingMode

**Files:**
- Modify: `internal/game/system_ability.go`

- [ ] **Step 1: Read the existing executeAbility structure**

Open the file. `executeAbility` (around line 174) is a single big switch on `params.Type` — one case per AbilityType. We need to ADD an outer dispatch by `params.Mode` while preserving the existing per-Type behavior for Self and LockOn modes (which use the AbilityType discrimination today).

- [ ] **Step 2: Add the action.aim fields**

Find the `abilityAction` struct (around line 65 — search for `type abilityAction struct`). Add aim fields:

```go
type abilityAction struct {
    caster      mmokit.EntityHandle
    casterNetID uint32
    slot        uint8
    params      *item.AbilityParams
    abilities   *gamecomp.AbilitySet
    aimX        float32 // PVE v3: cursor world coords from CastAbility wire msg
    aimY        float32
}
```

In the `Update` loop, find where `abilityAction` is appended to `s.deferred` (around line 119) and add the aim fields:

```go
s.deferred = append(s.deferred, abilityAction{
    caster:      entity,
    casterNetID: casterNetID,
    slot:        slot,
    params:      params,
    abilities:   abilities,
    aimX:        input.LastCastAimX, // populated by the CastAbility handler — see Task 8
    aimY:        input.LastCastAimY,
})
```

(`input.LastCastAimX/Y` are new fields on `PlayerInput` — added in Task 8.)

- [ ] **Step 3: Wrap the existing per-Type switch in a per-Mode dispatch**

Find the existing `switch params.Type {` block in `executeAbility`. The current shape:

```go
func (s *AbilitySystem) executeAbility(action abilityAction) bool {
    gw := s.gw
    entity := action.caster
    casterE := mmokit.EntityFromECS(gw.stage, entity)

    if !casterE.Alive() {
        return false
    }

    lock := mmokit.Get[gamecomp.TargetLock](casterE)
    params := action.params

    fired := true

    switch params.Type {
    // ... ~12 cases ...
    }
    return fired
}
```

Restructure to dispatch by Mode FIRST, then fall through to the per-Type body for Self/LockOn modes only. For SkillshotLine/Ground/Channel, we'll add dedicated dispatches in Tasks 9-11.

Replace the whole body of `executeAbility` with:

```go
func (s *AbilitySystem) executeAbility(action abilityAction) bool {
    gw := s.gw
    casterE := mmokit.EntityFromECS(gw.stage, action.caster)
    if !casterE.Alive() {
        return false
    }
    params := action.params

    switch params.Mode {
    case item.TargetingSkillshotLine:
        return s.dispatchSkillshotLine(action, casterE)
    case item.TargetingSkillshotGround:
        return s.dispatchSkillshotGround(action, casterE)
    case item.TargetingSkillshotChannel:
        return s.dispatchSkillshotChannel(action, casterE)
    }

    // TargetingSelf and TargetingLockOn fall through to the existing
    // per-Type dispatch — these abilities have bespoke handlers (shield
    // buffs, thruster effects, mining beam toggle, lock-on hitscan/DoT,
    // homing-missile-with-lock, plasma torpedo) that are too varied for
    // a generic dispatch.
    return s.dispatchByType(action, casterE)
}
```

- [ ] **Step 4: Rename the existing switch body to dispatchByType**

The existing `switch params.Type { ... }` body — extract it into a new method:

```go
func (s *AbilitySystem) dispatchByType(action abilityAction, casterE mmokit.Entity) bool {
    gw := s.gw
    lock := mmokit.Get[gamecomp.TargetLock](casterE)
    params := action.params
    fired := true

    switch params.Type {
    // ... PASTE existing case block here verbatim ...
    }
    return fired
}
```

(The whole existing switch body moves into this new method. Make sure variables `gw`, `lock`, `params`, `fired` are scoped to the function body.)

- [ ] **Step 5: Add stub dispatch methods**

Add empty stubs that we'll fill in Tasks 9-11:

```go
func (s *AbilitySystem) dispatchSkillshotLine(action abilityAction, casterE mmokit.Entity) bool {
    return false // implemented in Task 9
}

func (s *AbilitySystem) dispatchSkillshotGround(action abilityAction, casterE mmokit.Entity) bool {
    return false // implemented in Task 10
}

func (s *AbilitySystem) dispatchSkillshotChannel(action abilityAction, casterE mmokit.Entity) bool {
    return false // implemented in Task 11
}
```

- [ ] **Step 6: Verify build**

Run: `cd . && go vet ./...`
Expected: clean (assuming Task 8 hasn't added `input.LastCastAim*` yet, expect ONE error for those undefined fields. If so, temporarily comment out the new aim-field append in Step 2 — uncomment in Task 8).

If Step 2 doesn't compile, leave aimX/aimY unset (default zero) in the deferred append for now; Task 8 plumbs them.

- [ ] **Step 7: Commit**

```bash
cd .
git add internal/game/system_ability.go
git commit -m "$(cat <<'EOF'
refactor(ability): switch on TargetingMode before falling through to per-Type dispatch

Adds an outer Mode-keyed switch in executeAbility. Skillshot modes
(Line/Ground/Channel) get their own dispatch helpers — currently stubs
returning false. Self and LockOn fall through to the existing per-Type
switch body, which is extracted into dispatchByType to keep the shape
clear. No behavior change yet; the existing abilities all still run
through dispatchByType.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: Plumb AimX/AimY through CastAbility handler to PlayerInput

**Files:**
- Modify: `internal/component/components.go`
- Modify: `internal/game/input_handlers.go`

- [ ] **Step 1: Add LastCastAim fields to PlayerInput**

Find the `PlayerInput` struct in `components.go`. Add aim fields:

```go
type PlayerInput struct {
    // ... existing fields ...
    
    // PVE v3: cursor world coords from the most recent CastAbility press.
    // AbilitySystem reads these when dispatching skillshot abilities and
    // resets them to 0 each tick after dispatch (matching the
    // AbilityCast bitmask reset pattern). Local-only — never on the wire.
    LastCastAimX float32 `mmokit:"local"`
    LastCastAimY float32 `mmokit:"local"`
}
```

(If `mmokit:"local"` isn't already used on PlayerInput, use whatever the existing local-only convention is — could just be no `net:` tag.)

- [ ] **Step 2: Update the CastAbility handler**

Open `input_handlers.go`. Find the existing `CastAbility` handler (around line 168):

```go
mmokit.HandleClient(mmo, func(player mmokit.Entity, msg *CastAbility) {
    // ... existing body ...
})
```

Inside the handler body, after the existing `input.AbilityCast |= 1 << msg.Slot` line (whatever the exact line is — find it), also stash the aim:

```go
input.LastCastAimX = msg.AimX
input.LastCastAimY = msg.AimY
```

- [ ] **Step 3: Reset aim after dispatch**

In `system_ability.go::Update`, find where `input.AbilityCast = 0` is reset at the end of each entity's iteration (around line 128). Right after that line:

```go
input.AbilityCast = 0
input.LastCastAimX = 0
input.LastCastAimY = 0
```

- [ ] **Step 4: Uncomment / wire the aim fields in deferred append**

If Task 7 left the deferred append without aim fields, fix it now. The append should read:

```go
s.deferred = append(s.deferred, abilityAction{
    caster:      entity,
    casterNetID: casterNetID,
    slot:        slot,
    params:      params,
    abilities:   abilities,
    aimX:        input.LastCastAimX,
    aimY:        input.LastCastAimY,
})
```

- [ ] **Step 5: Verify build + tests**

```bash
cd .
go vet ./...
go test ./internal/game/ -v 2>&1 | tail -15
```

Expected: clean. Existing tests still pass (aim coords default to 0 in tests).

- [ ] **Step 6: Commit**

```bash
cd .
git add internal/component/components.go internal/game/input_handlers.go internal/game/system_ability.go
git commit -m "$(cat <<'EOF'
feat(input): plumb CastAbility AimX/AimY through PlayerInput to dispatch

Adds LastCastAimX/Y as local-only fields on PlayerInput. CastAbility
handler stashes the cursor coords on press; AbilitySystem reads them
into abilityAction.aimX/Y at deferred-action time; the skillshot
dispatch helpers (still stubs) consume them in Tasks 9-11. Fields
reset each tick alongside AbilityCast.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: Implement dispatchSkillshotLine

**Files:**
- Modify: `internal/game/system_ability.go`

- [ ] **Step 1: Read the existing fireProjectile to confirm the signature**

The current `fireProjectile` (after Task 6) takes `(caster, params, projType, pierceCount)`. It always uses `casterRot.Angle` for direction unless there's an active lock. For skillshot mode, we want to use the cursor coords from `action.aimX/aimY` instead.

- [ ] **Step 2: Refactor fireProjectile to accept an explicit aim direction**

Change the signature again (this time the FINAL form):

```go
func (s *AbilitySystem) fireProjectile(
    caster mmokit.Entity, params *item.AbilityParams, projType uint8,
    pierceCount uint8, dirX, dirY float32,
) {
    gw := s.gw
    casterPos := mmokit.Get[mmokit.Position](caster)
    if casterPos == nil {
        return
    }

    // dirX/dirY should already be normalized by the caller; if not (e.g. cursor on
    // top of ship), fall back to caster facing.
    norm := float32(math.Sqrt(float64(dirX*dirX + dirY*dirY)))
    if norm < 1e-3 {
        if rot := mmokit.Get[mmokit.Rotation](caster); rot != nil {
            dirX = float32(math.Cos(float64(rot.Angle)))
            dirY = float32(math.Sin(float64(rot.Angle)))
        } else {
            return // can't fire without a direction
        }
    } else {
        dirX, dirY = dirX/norm, dirY/norm
    }

    var targetNetID uint32
    if projType == gamecomp.ProjectileTypeMissile {
        if lock := mmokit.Get[gamecomp.TargetLock](caster); lock != nil {
            if target, ok := activeLockTarget(gw, lock); ok {
                targetNetID = target.NetID()
            }
        }
    }

    lifetime := float32(0)
    if params.ProjectileSpeed > 0 {
        lifetime = params.Range / params.ProjectileSpeed
    }

    spec := gamecomp.ProjectileSpec{
        OwnerNetID:   caster.NetID(),
        TargetNetID:  targetNetID,
        Damage:       params.Damage,
        SplashRadius: params.SplashRadius,
        SplashDamage: params.SplashDamage,
        MaxTurnRate:  params.HomingMaxTurnRate,
        Type:         projType,
        PierceCount:  pierceCount,
    }
    gw.SpawnProjectile(
        casterPos.X, casterPos.Y,
        dirX*params.ProjectileSpeed, dirY*params.ProjectileSpeed,
        lifetime, spec,
    )
}
```

(Remove the old LockOn-based direction-derivation logic. For homing missiles in LockOn mode, the dispatch path computes the direction toward the locked target separately — see Step 4.)

- [ ] **Step 3: Update existing call sites**

Find all callers of `fireProjectile` (in the dispatchByType switch — search for `s.fireProjectile`). Each is a `case AbilityTypeFoo:` that needs to pass the right direction.

For HomingMissile (LockOn — the only LockOn projectile), the dispatch is:

```go
case item.AbilityTypeHomingMissile:
    if lock := mmokit.Get[gamecomp.TargetLock](casterE); lock != nil {
        if target, ok := activeLockTarget(gw, lock); ok {
            tpos := mmokit.Get[mmokit.Position](target)
            cpos := mmokit.Get[mmokit.Position](casterE)
            if tpos != nil && cpos != nil {
                dx, dy := tpos.X-cpos.X, tpos.Y-cpos.Y
                s.fireProjectile(casterE, params, gamecomp.ProjectileTypeMissile, 0, dx, dy)
                gw.eng.Log.Log(CatCombatAbility, "ability %s: %d HomingMissile fired", params.Name, action.casterNetID)
                return true
            }
        }
    }
    return false // no lock — refund cooldown
```

The OTHER fireProjectile callers (PlasmaShot, MortarShell in dispatchByType) will be REMOVED because they're moving to dispatchSkillshotLine and dispatchSkillshotGround respectively. Delete those `case AbilityTypePlasmaShot` and `case AbilityTypeMortarShell` blocks from dispatchByType entirely — they're now handled by the Mode switch.

- [ ] **Step 4: Implement dispatchSkillshotLine**

Replace the stub:

```go
func (s *AbilitySystem) dispatchSkillshotLine(action abilityAction, casterE mmokit.Entity) bool {
    gw := s.gw
    params := action.params

    casterPos := mmokit.Get[mmokit.Position](casterE)
    if casterPos == nil {
        return false
    }
    dx := action.aimX - casterPos.X
    dy := action.aimY - casterPos.Y

    // Choose pierce count per AbilityType (per spec table).
    var pierceCount uint8
    var projType uint8 = gamecomp.ProjectileTypePlasma // default visual variant
    switch params.Type {
    case item.AbilityTypeRailShot:
        pierceCount = 2
    case item.AbilityTypePiercingRound:
        pierceCount = 99
        projType = gamecomp.ProjectileTypeMissile // distinct visual
    case item.AbilityTypePlasmaShot:
        projType = gamecomp.ProjectileTypePlasma
    // All others (PulseLaser, PulseBarrage, RailShot, PlasmaBolt, IonOverload):
    // default Plasma visual + pierceCount 0
    }

    // PulseBarrage: 3 sub-projectiles in a small cone (±10°).
    if params.Type == item.AbilityTypePulseBarrage {
        s.fireProjectile(casterE, params, projType, pierceCount, dx, dy)
        // Rotate ±10° (~0.1745 rad)
        spread := float32(0.1745)
        cos, sin := float32(math.Cos(float64(spread))), float32(math.Sin(float64(spread)))
        dxL := cos*dx - sin*dy
        dyL := sin*dx + cos*dy
        dxR := cos*dx + sin*dy
        dyR := -sin*dx + cos*dy
        s.fireProjectile(casterE, params, projType, pierceCount, dxL, dyL)
        s.fireProjectile(casterE, params, projType, pierceCount, dxR, dyR)
    } else {
        s.fireProjectile(casterE, params, projType, pierceCount, dx, dy)
    }

    gw.eng.Log.Log(CatCombatAbility, "ability %s: %d skillshot fired aim=(%.0f,%.0f) pierce=%d",
        params.Name, action.casterNetID, action.aimX, action.aimY, pierceCount)
    return true
}
```

- [ ] **Step 5: Verify build + tests**

```bash
cd .
go vet ./...
go test ./internal/game/ -v 2>&1 | tail -15
```

Expected: clean. Tests may show PlasmaShot dispatch tests now fail (they were testing the old LockOn-targeted path). Update those tests to use the new aim-based dispatch — call `executeAbility` with `action.aimX/Y` populated rather than `lock.ActiveNetID`.

- [ ] **Step 6: Commit**

```bash
cd .
git add internal/game/system_ability.go
git commit -m "$(cat <<'EOF'
feat(ability): implement dispatchSkillshotLine for aimed projectiles

PulseLaser, PulseBarrage, RailShot, PiercingRound, IonOverload,
PlasmaBolt, and PlasmaShot all now route through dispatchSkillshotLine:
direction comes from (aimX - shipX, aimY - shipY) instead of a locked
target. RailShot pierces 2, PiercingRound pierces 99, PulseBarrage
fires 3 projectiles in a ±10° cone, everything else fires one.

HomingMissile keeps its LockOn dispatch (lock-on smart missile).
fireProjectile signature changes to accept an explicit (dirX, dirY) —
the caller owns the targeting decision.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: Implement dispatchSkillshotGround

**Files:**
- Modify: `internal/game/system_ability.go`

- [ ] **Step 1: Replace the stub**

```go
func (s *AbilitySystem) dispatchSkillshotGround(action abilityAction, casterE mmokit.Entity) bool {
    gw := s.gw
    params := action.params

    casterPos := mmokit.Get[mmokit.Position](casterE)
    if casterPos == nil {
        return false
    }

    // Clamp cursor to ability range.
    dx := action.aimX - casterPos.X
    dy := action.aimY - casterPos.Y
    dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))
    aimX, aimY := action.aimX, action.aimY
    if dist > params.Range && dist > 0 {
        scale := params.Range / dist
        aimX = casterPos.X + dx*scale
        aimY = casterPos.Y + dy*scale
    }

    // GroundCastDelay: time the marker lives before exploding.
    delay := params.GroundCastDelay
    if delay <= 0 {
        delay = 0.6 // fallback default
    }

    px, py := aimX, aimY
    radius := params.SplashRadius
    damage := params.SplashDamage
    ownerNetID := casterE.NetID()
    mask := factionMaskFromOwner(gw, ownerNetID)

    s.Commands().Defer(func() {
        gw.SpawnAoEMarker(px, py, delay, radius, damage, ownerNetID, mask)
    })

    gw.eng.Log.Log(CatCombatAbility, "ability %s: %d ground-cast at (%.0f,%.0f) r=%.0f delay=%.1fs",
        params.Name, action.casterNetID, px, py, radius, delay)
    return true
}
```

- [ ] **Step 2: Remove the old MortarShell case from dispatchByType**

In the per-Type switch (in `dispatchByType`), find and DELETE the `case item.AbilityTypeMortarShell:` block. MortarShell now flows through Mode dispatch entirely.

- [ ] **Step 3: Verify build + tests**

```bash
cd . && go vet ./...
cd . && go test ./internal/game/ 2>&1 | tail -10
```

Expected: clean.

- [ ] **Step 4: Commit**

```bash
cd .
git add internal/game/system_ability.go
git commit -m "$(cat <<'EOF'
feat(ability): implement dispatchSkillshotGround for cursor-targeted AoEs

MortarShell now flows through dispatchSkillshotGround: cursor coords
clamped to params.Range, AoEMarker spawned at the clamped position
with lifetime = params.GroundCastDelay (~0.6s). AoESystem handles
detonation as today.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 11: Implement dispatchSkillshotChannel + aim-aware tickChannels

**Files:**
- Modify: `internal/game/system_ability.go`
- Modify: `internal/game/input_handlers.go`

- [ ] **Step 1: Replace the dispatchSkillshotChannel stub**

```go
func (s *AbilitySystem) dispatchSkillshotChannel(action abilityAction, casterE mmokit.Entity) bool {
    gw := s.gw
    params := action.params

    // Already channeling? No-op.
    if mmokit.Has[gamecomp.Channeling](casterE) {
        return false
    }

    mmokit.AddComponent(s.Commands(), casterE.Handle(), gamecomp.Channeling{
        SlotID:        action.slot,
        RemainingTime: params.ChannelDuration,
        NextTickIn:    0,
        AimX:          action.aimX,
        AimY:          action.aimY,
    })

    gw.eng.Log.Log(CatCombatAbility, "ability %s: %d channel START aim=(%.0f,%.0f) duration=%.1fs",
        params.Name, action.casterNetID, action.aimX, action.aimY, params.ChannelDuration)

    // Returning false keeps the cooldown at 0 — channel end (in tickChannels) sets it.
    return false
}
```

- [ ] **Step 2: Remove the old SustainedBeam case from dispatchByType**

Delete `case item.AbilityTypeSustainedBeam:` block from the per-Type switch — Mode dispatch handles it now.

- [ ] **Step 3: Refactor tickChannels to use aim coords**

Find `tickChannels` (around line 492). The current logic looks up `target := mmokit.EntityByNetID(gw.stage, ch.TargetNetID)` and applies damage to that entity. Replace with a hitscan along the aim direction.

The new tick body (inside the `mmokit.ForEach1[gamecomp.Channeling]` loop):

```go
caster := mmokit.EntityFromECS(gw.stage, h)
if !caster.Alive() {
    ends = append(ends, endCandidate{h, ch.SlotID})
    return
}
ch.RemainingTime -= dt
ch.NextTickIn -= dt
if ch.RemainingTime <= 0 {
    ends = append(ends, endCandidate{h, ch.SlotID})
    return
}

casterPos := mmokit.Get[mmokit.Position](caster)
casterRot := mmokit.Get[mmokit.Rotation](caster)
if casterPos == nil || casterRot == nil {
    ends = append(ends, endCandidate{h, ch.SlotID})
    return
}

// Aim direction from cursor coords.
dx := ch.AimX - casterPos.X
dy := ch.AimY - casterPos.Y
aimNorm := float32(math.Sqrt(float64(dx*dx + dy*dy)))
if aimNorm < 1e-3 {
    // Cursor on top of ship → no clear aim; skip tick but don't end.
    return
}
aimAngle := float32(math.Atan2(float64(dy), float64(dx)))

// Player can be rotated separately from cursor — drop the channel if
// the player's facing is way off the aim direction (full 180°). Mild
// wiggle is allowed (BeamHalfArcRad is the per-tick check).
arcDelta := angleDelta(casterRot.Angle, aimAngle)
if arcDelta > params.BeamHalfArcRad {
    ends = append(ends, endCandidate{h, ch.SlotID})
    return
}

if ch.NextTickIn > 0 {
    return
}

// Resolve params from the equipment slot.
equip := mmokit.Get[gamecomp.Equipment](caster)
if equip == nil {
    ends = append(ends, endCandidate{h, ch.SlotID})
    return
}
params := resolveAbilityParams(equip, ch.SlotID)
if params == nil {
    ends = append(ends, endCandidate{h, ch.SlotID})
    return
}

// Hitscan along the aim line — find first non-owner entity within Range.
s.nearbyChannel = gw.Spatial.QueryRadius(
    casterPos.X+dx/aimNorm*params.Range*0.5, // midpoint of the beam line
    casterPos.Y+dy/aimNorm*params.Range*0.5,
    params.Range*0.5+8, // search radius covering the full beam length
    s.nearbyChannel[:0],
)

ownerNetID := caster.NetID()
var victim mmokit.Entity
var victimDist float32 = params.Range + 1
for _, entry := range s.nearbyChannel {
    e := mmokit.EntityFromECS(gw.stage, entry.Entity)
    if !e.Alive() || e.NetID() == ownerNetID {
        continue
    }
    if !mmokit.Has[gamecomp.NPCAI](e) && !mmokit.Has[mmokit.PlayerConn](e) {
        continue
    }
    // Project entity onto the beam line; require both:
    //   - parallel distance ≤ Range
    //   - perpendicular distance ≤ beam half-width (use 4u)
    epos := mmokit.Get[mmokit.Position](e)
    if epos == nil {
        continue
    }
    rx, ry := epos.X-casterPos.X, epos.Y-casterPos.Y
    parallel := rx*dx/aimNorm + ry*dy/aimNorm   // along beam
    if parallel < 0 || parallel > params.Range {
        continue
    }
    // Perpendicular: |rx*nx - ry*nx| where (nx, ny) is the unit normal.
    nx, ny := -dy/aimNorm, dx/aimNorm
    perp := rx*nx + ry*ny
    if perp < 0 {
        perp = -perp
    }
    if perp > 4 {
        continue
    }
    if parallel < victimDist {
        victimDist = parallel
        victim = e
    }
}

if victim.Alive() {
    gw.ApplyDamage(victim, params.Damage, ownerNetID)
    gw.eng.Log.Log(CatCombatHit, "channel: hit netID=%d dmg=%.0f", victim.NetID(), params.Damage)
}
ch.NextTickIn = 1.0 / params.ChannelTickRate
```

(Add a `nearbyChannel []mmokit.SpatialEntry` field on `AbilitySystem` to avoid re-allocating the slice each tick.)

- [ ] **Step 4: Add ChannelAim handler**

In `input_handlers.go`, after the CastAbility handler, add:

```go
mmokit.HandleClient(mmo, func(player mmokit.Entity, msg *ChannelAim) {
    ch := mmokit.Get[gamecomp.Channeling](player)
    if ch == nil {
        return // not channeling — drop silently
    }
    if ch.SlotID != msg.Slot {
        return // mismatched slot — drop
    }
    ch.AimX = msg.AimX
    ch.AimY = msg.AimY
})
```

- [ ] **Step 5: Verify build + tests**

```bash
cd . && go vet ./...
cd . && go test ./internal/game/ 2>&1 | tail -10
```

Expected: clean.

- [ ] **Step 6: Commit**

```bash
cd .
git add internal/game/system_ability.go internal/game/input_handlers.go
git commit -m "$(cat <<'EOF'
feat(ability): SustainedBeam becomes SkillshotChannel

dispatchSkillshotChannel starts the channel with AimX/AimY from
CastAbility. tickChannels hitscans along the aim direction each tick:
finds the first non-owner entity within params.Range and ±4u of the
beam line, applies params.Damage. Channel ends on timeout, caster
death, or facing>BeamHalfArcRad off the aim direction.

ChannelAim message handler updates the player's Channeling component
each tick so the beam tracks the cursor while held.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 12: Rename LanceTelegraph → LineTelegraph

**Files:** Many — this is a global rename.

- [ ] **Step 1: Rename component file**

```bash
cd .
git mv internal/component/lance.go internal/component/line_telegraph.go
git mv internal/game/entity_lance.go internal/game/entity_line_telegraph.go
git mv web-pixi/src/entities/lance-telegraph.ts web-pixi/src/entities/line-telegraph.ts
```

- [ ] **Step 2: Rename types in Go files**

Across the codebase, rename:
- `LanceTelegraphSpec` → `LineTelegraphSpec`
- `LanceTelegraphBundle` → `LineTelegraphBundle`
- `KindLanceTelegraph` → `KindLineTelegraph`
- `SpawnLanceTelegraph` → `SpawnLineTelegraph`
- Function name `(gw *GameWorld) SpawnLanceTelegraph` keeps caller's signature; just rename

Use grep + sed:

```bash
cd .
grep -rln 'LanceTelegraph\|KindLanceTelegraph\|SpawnLanceTelegraph' --include='*.go' | \
    xargs sed -i 's/LanceTelegraphSpec/LineTelegraphSpec/g; s/LanceTelegraphBundle/LineTelegraphBundle/g; s/KindLanceTelegraph/KindLineTelegraph/g; s/SpawnLanceTelegraph/SpawnLineTelegraph/g'
```

- [ ] **Step 3: Update comments referring to Lancer-only telegraphs**

In `internal/game/entity_line_telegraph.go` and `internal/component/line_telegraph.go`, update the doc comments to say the entity is shared between Lancer's charge windup and Brawler's special. Replace any "Lancer NPC" wording with "Lancer or Brawler" or just "telegraphed line attack."

- [ ] **Step 4: Verify Go build + tests**

```bash
cd .
go vet ./...
go test ./internal/game/ 2>&1 | tail -10
```

Expected: clean.

- [ ] **Step 5: Update client renderer name**

In `web-pixi/src/entities/line-telegraph.ts`, rename the function:

```typescript
export function createLineTelegraphDisplay(): EntityDisplayObject {
```

(Was `createLanceTelegraphDisplay`.)

- [ ] **Step 6: Update entity-manager registration**

In `web-pixi/src/entities/entity-manager.ts`, update the import + switch case:

```typescript
import { createLineTelegraphDisplay } from "./line-telegraph";
// ...
case EntityType.LineTelegraph:
    return createLineTelegraphDisplay();
```

(After SDK regen in Task 16, `EntityType.LineTelegraph` will exist.)

- [ ] **Step 7: Commit**

```bash
cd .
git add -A internal/ web-pixi/src/
git commit -m "$(cat <<'EOF'
refactor: rename LanceTelegraph → LineTelegraph (now used by Brawler too)

Same entity kind (wire byte 8) — only the names change. Brawler is
about to gain a telegraphed line-cone special that uses this same
rectangle visualization, so the name "Lance" is misleading.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 13: Brawler special config + NPCAI fields

**Files:**
- Modify: `internal/component/components.go`
- Modify: `internal/game/config.go`
- Modify: `internal/game/npc_archetype.go`

- [ ] **Step 1: Add NPCAI fields**

In `components.go`, append to `NPCAI`:

```go
// PVE v3: Brawler special-attack state. SpecialCooldown ticks down each
// frame between specials; SpecialWindup > 0 means a telegraph is in
// flight; SpecialDirAngle is the locked aim direction; SpecialTelegraphNetID
// is the in-flight LineTelegraph entity netID so we can despawn it on death.
SpecialCooldown       float32
SpecialWindup         float32
SpecialDirAngle       float32
SpecialTelegraphNetID uint32
```

- [ ] **Step 2: Add GameConfig fields**

In `config.go`, append to the `GameConfig` struct:

```go
BrawlerSpecialCooldown   float32 `json:"brawler_special_cooldown"`
BrawlerSpecialWindupTime float32 `json:"brawler_special_windup_time"`
BrawlerSpecialLength     float32 `json:"brawler_special_length"`
BrawlerSpecialHalfWidth  float32 `json:"brawler_special_half_width"`
BrawlerSpecialDamage     float32 `json:"brawler_special_damage"`
```

In the `DefaultGameConfig` block:

```go
BrawlerSpecialCooldown:   6.0,
BrawlerSpecialWindupTime: 0.8,
BrawlerSpecialLength:     50,
BrawlerSpecialHalfWidth:  5,
BrawlerSpecialDamage:     25,
```

Also bump the `ConfigVersion` constant: find `const ConfigVersion = 7` and change to `8`.

- [ ] **Step 3: Verify build**

Run: `cd . && go vet ./...`
Expected: clean.

- [ ] **Step 4: Commit**

```bash
cd .
git add internal/component/components.go internal/game/config.go
git commit -m "$(cat <<'EOF'
feat(game): Brawler special-attack config + NPCAI fields

Telegraphed line-cone Brawler attack — auto-attack stays, special
fires every 6s with a 0.8s windup along a 50u × 10u rectangle for
25 damage. NPCAI tracks SpecialCooldown/SpecialWindup/SpecialDirAngle
+ a netID handle for the in-flight telegraph entity.

ConfigVersion 7 → 8 to invalidate saved configs (new defaults won't
overwrite stored values otherwise).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 14: Implement Brawler special tickEngage logic

**Files:**
- Modify: `internal/game/system_npc_ai.go`

- [ ] **Step 1: Add the special trigger in tickEngage**

In `tickEngage`, after the existing Artillery cast-trigger block AND after the Lancer windup-trigger block AND after the Kamikaze proximity-trigger block (which no longer exists post-PVE v2; just go in the Brawler-relevant spot), add a Brawler-specific block. Position it BEFORE the existing hitscan-fire block so the special trigger gets a chance first:

```go
if ai.Archetype == ArchetypeBrawler {
    if ai.SpecialCooldown > 0 {
        ai.SpecialCooldown -= dt
    }
    if ai.SpecialWindup > 0 {
        ai.SpecialWindup -= dt
        if ai.SpecialWindup <= 0 {
            // Resolve the special: hitscan along SpecialDirAngle for
            // BrawlerSpecialLength × (2 × BrawlerSpecialHalfWidth).
            s.resolveBrawlerSpecial(self, ai, pos)
            ai.SpecialCooldown = s.gw.Config.BrawlerSpecialCooldown
        }
        // While windup is active, Brawler stays still (visible commit).
        vel.X, vel.Y = 0, 0
        return
    }
    if ai.SpecialCooldown <= 0 && lock.ActiveNetID != 0 && dist <= s.gw.Config.BrawlerSpecialLength {
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
```

- [ ] **Step 2: Implement resolveBrawlerSpecial**

Add the method:

```go
// resolveBrawlerSpecial — fires the line-cone attack at the end of the
// windup. Hitscan along SpecialDirAngle: any non-Brawler entity inside
// the rectangle (length=BrawlerSpecialLength, half-width=
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
```

(`s.nearby` is the existing reused scratch slice on `NPCAISystem`. If it doesn't exist as a field, add it: `nearby []mmokit.SpatialEntry`.)

- [ ] **Step 3: Verify build + tests**

```bash
cd . && go vet ./...
cd . && go test ./internal/game/ 2>&1 | tail -10
```

Expected: clean.

- [ ] **Step 4: Commit**

```bash
cd .
git add internal/game/system_npc_ai.go
git commit -m "$(cat <<'EOF'
feat(game): Brawler dual-attack — auto-attack + telegraphed line-cone special

Brawler now alternates: existing hitscan auto-attack continues at 1Hz,
and every 6s it winds up a 0.8s telegraphed line-cone attack via the
LineTelegraph entity. On windup expiry, resolves a rectangle hit-check
(length=50u, half-width=5u) and applies BrawlerSpecialDamage (25) to
any player inside the rectangle. Sidestepping perpendicular to the
telegraph during the 0.8s windup dodges the special.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 15: Regenerate TypeScript SDK

- [ ] **Step 1: Regenerate**

```bash
cd .
go run ./cmd/server --dump-schema --control-listen=:19100 --admin-listen=:19101 --mode=coordinator,host 2>/dev/null | go run ./cmd/sdkgen --out web-pixi/sdk --core pkg/quantize/ts/delta-decoder-core.ts
```

(Non-default ports avoid conflict with the user's running dev server.)

- [ ] **Step 2: Verify SDK changes**

```bash
cd .
git status web-pixi/sdk/
grep -n "LineTelegraph\|ChannelAim\|aimX\|aimY" web-pixi/sdk/*.ts | head -10
```

Expected: `web-pixi/sdk/entityType.ts` has `LineTelegraph: 8` (renamed from LanceTelegraph). `web-pixi/sdk/inputs.ts` has both `CastAbility` (now with aimX/aimY) and `ChannelAim`.

- [ ] **Step 3: Commit the regen**

```bash
cd .
git add web-pixi/sdk/
git commit -m "$(cat <<'EOF'
build: regenerate TypeScript SDK for skillshot wire changes

CastAbility gains aimX/aimY. New ChannelAim message. EntityType.LanceTelegraph
renamed to EntityType.LineTelegraph (same wire byte 8).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 16: Client — send aim coords on CastAbility

**Files:**
- Modify: `web-pixi/src/input.ts`
- Modify: `web-pixi/src/state.ts`

- [ ] **Step 1: Add aim state to GameState**

In `state.ts`, find the GameState interface (or type) and add:

```typescript
aimingSlot: number; // 0 = not aiming; 1..6 = currently aiming this slot index
quickcastMask: number; // bitmask: bit N = slot N+1 is quickcast (skip aim-confirm)
cursorWorldX: number; // live cursor world coords (updated by input.ts each mousemove)
cursorWorldY: number;
```

Initialize in the state factory:
```typescript
aimingSlot: 0,
quickcastMask: parseInt(localStorage.getItem("skillshot.quickcast") ?? "0", 10),
cursorWorldX: 0,
cursorWorldY: 0,
```

- [ ] **Step 2: Track cursor world position**

In `input.ts`, find the existing mousemove handler (search for `mousemove`). If one exists, add this to it; otherwise add a new handler near the other mouse handlers:

```typescript
canvas.addEventListener("mousemove", (e) => {
  const rect = canvas.getBoundingClientRect();
  const sx = e.clientX - rect.left;
  const sy = e.clientY - rect.top;
  // Convert to world coords via the camera. The exact API depends on
  // the existing camera implementation — look for screenToWorld or
  // similar in view.ts / camera.ts. If none exists, compute manually:
  //   worldX = camera.x + (sx - canvas.width/2) / zoom();
  //   worldY = camera.y + (sy - canvas.height/2) / zoom();
  const world = screenToWorld(sx, sy); // or inline the math
  state.cursorWorldX = world.x;
  state.cursorWorldY = world.y;
});
```

(Look at how `view.ts` or `camera.ts` exposes screen→world transforms. Reuse the existing helper.)

- [ ] **Step 3: Send AimX/AimY on CastAbility**

Find every existing `state.client.send(new CastAbility(...))` call in `input.ts`. Update to include aim:

```typescript
state.client.send(new CastAbility({
  sequence: nextSequence(state),
  slot,
  aimX: state.cursorWorldX,
  aimY: state.cursorWorldY,
}));
```

- [ ] **Step 4: Verify TS typecheck**

```bash
cd web-pixi
bunx tsc --noEmit 2>&1 | tail -10
```

Expected: clean.

- [ ] **Step 5: Commit**

```bash
cd .
git add web-pixi/src/input.ts web-pixi/src/state.ts
git commit -m "$(cat <<'EOF'
feat(web): send aim coords with CastAbility; track cursor world pos

GameState gains aimingSlot, quickcastMask, cursorWorldX/Y. mousemove
handler keeps the cursor world coords up to date. CastAbility sends
those coords on every press — server reads them for skillshot
direction/position.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 17: Client — aim-state machine in input.ts

**Files:**
- Modify: `web-pixi/src/input.ts`

- [ ] **Step 1: Add the aim-state machine**

Find the existing ability-press handler — the section that converts keypresses to `state.abilityPresses |= ABILITY_KEYS[e.code]`. Wrap it with the aim-state machine:

```typescript
// Ability keys (press, not hold)
if (!state.isDead && ABILITY_KEYS[e.code] !== undefined) {
  const bit = ABILITY_KEYS[e.code];
  const slot = Math.log2(bit); // 0..5 → Q/W/E/R/D/F
  e.preventDefault();
  handleAbilityPress(state, slot);
}
```

Add the helper:

```typescript
// handleAbilityPress drives the aim-state machine for one slot.
// - Self / LockOn / on-cooldown / quickcast-mode → fire immediately
// - Skillshot mode → enter aim-state, show indicator
// - Same-slot press while aiming → fire (release equivalent)
// - Different-slot press while aiming → swap aim to new slot
function handleAbilityPress(state: GameState, slot: number) {
  const params = abilityParamsForSlot(state, slot);
  if (!params) return; // empty slot

  // Cooldown gate (visual handled by ability-bar.ts; server enforces).
  const cd = state.abilityCooldowns.get(slot);
  if (cd && cd.remaining > 0) return;

  const isSkillshot = params.mode === TargetingMode.SkillshotLine
                   || params.mode === TargetingMode.SkillshotGround
                   || params.mode === TargetingMode.SkillshotChannel;
  const quickcast = (state.quickcastMask & (1 << slot)) !== 0;

  // Press while already aiming this slot → fire (release-equivalent).
  if (state.aimingSlot === slot + 1) {
    fireSkillshot(state, slot);
    state.aimingSlot = 0;
    return;
  }

  // Non-skillshot OR quickcast OR same-slot press → fire immediately.
  if (!isSkillshot || quickcast) {
    fireSkillshot(state, slot);
    state.aimingSlot = 0;
    return;
  }

  // Enter aim-state for skillshot.
  state.aimingSlot = slot + 1;
}

// fireSkillshot emits the CastAbility with current cursor coords.
function fireSkillshot(state: GameState, slot: number) {
  if (!state.client) return;
  state.client.send(new CastAbility({
    sequence: nextSequence(state),
    slot,
    aimX: state.cursorWorldX,
    aimY: state.cursorWorldY,
  }));
}
```

- [ ] **Step 2: Handle key release for aim-confirm**

For aim-confirm mode, release of the same key commits the cast. Add a keyup handler (or extend an existing one):

```typescript
document.addEventListener("keyup", (e) => {
  if (!state.aimingSlot) return;
  if (ABILITY_KEYS[e.code] === undefined) return;
  const bit = ABILITY_KEYS[e.code];
  const slot = Math.log2(bit);
  if (state.aimingSlot === slot + 1) {
    fireSkillshot(state, slot);
    state.aimingSlot = 0;
  }
});
```

- [ ] **Step 3: Cancel on right-click / Escape**

Wire right-click and Escape to cancel aim-state:

```typescript
// In existing right-click handler (button === 2):
if (state.aimingSlot) {
  state.aimingSlot = 0;
  return; // don't process as move
}

// In existing Escape handler:
if (state.aimingSlot) {
  state.aimingSlot = 0;
  return;
}
```

- [ ] **Step 4: abilityParamsForSlot helper**

Add a helper that resolves the AbilityParams for the player's current loadout. The existing code in `ability-bar.ts` has `ITEM_ABILITIES` — extend the AbilityInfo type to include `mode: TargetingMode`:

In `ability-bar.ts`:

```typescript
export interface AbilityInfo {
  name: string;
  title: string;
  desc: string;
  stats: string[];
  range: number;
  mode: TargetingMode; // NEW
  color?: string;
}

export const enum TargetingMode {
  Self = 0,
  LockOn = 1,
  SkillshotLine = 2,
  SkillshotGround = 3,
  SkillshotChannel = 4,
}
```

Populate `mode:` on every entry in `ITEM_ABILITIES`. Match the spec's table from §1:

- Item 100 PulseLaser / PulseBarrage → `SkillshotLine`
- Item 101 RailShot / PiercingRound → `SkillshotLine`
- Item 105 IonBurn → `LockOn`; IonOverload → `SkillshotLine`
- Item 106 PlasmaBolt → `SkillshotLine`; PlasmaTorpedo → `LockOn`
- Item 107 PlasmaShot → `SkillshotLine`; HomingMissile → `LockOn`
- Item 108 SustainedBeam → `SkillshotChannel`; MortarShell → `SkillshotGround`
- Item 110/111 EmergencyShield/HardenedShield → `Self`
- Item 120/121 Afterburner/MicroWarp → `Self`
- Item 130/131 MiningBeam → `LockOn`; ExtractPulse → `LockOn`

Export `abilityParamsForSlot`:

```typescript
export function abilityParamsForSlot(state: GameState, slot: number): AbilityInfo | null {
  return getAbilityForSlot(state, slot);
}
```

(`getAbilityForSlot` already exists in `ability-bar.ts`; this just exports it under a more specific name. Or use the existing name in `input.ts` if it's already exported.)

- [ ] **Step 5: Verify typecheck**

```bash
cd web-pixi && bunx tsc --noEmit 2>&1 | tail -10
```

Expected: clean.

- [ ] **Step 6: Commit**

```bash
cd .
git add web-pixi/src/input.ts web-pixi/src/ui/ability-bar.ts
git commit -m "$(cat <<'EOF'
feat(web): aim-state machine for skillshot abilities

Press key → if skillshot mode + not quickcast → enter aim-state
(state.aimingSlot=slot+1). Press same key again or release the key →
fire CastAbility with current cursor coords. Different key → swap aim
to new slot. Right-click or Escape → cancel.

ITEM_ABILITIES entries get a `mode:` field matching the server-side
TargetingMode classification.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 18: Client — aim-indicator renderer

**Files:**
- Create: `web-pixi/src/effects/aim-indicator.ts`
- Modify: `web-pixi/src/main.ts`

- [ ] **Step 1: Create the renderer**

```typescript
// web-pixi/src/effects/aim-indicator.ts
import { Container, Graphics } from "pixi.js";
import { abilityParamsForSlot, TargetingMode } from "../ui/ability-bar";
import type { GameState } from "../state";
import { getShip } from "../entity-accessors";
import { px } from "../view";

const SLOT_COLORS: Record<number, number> = {
  0: 0x4488ff, 1: 0x4488ff, // Q/W weapon1
  2: 0xaa44ff, 3: 0xaa44ff, // E/R weapon2
  4: 0x44ff88,              // D shield
  5: 0xffff44,              // F thruster
};

// AimIndicator renders the active aim preview for whichever slot is
// being aimed. One container, redrawn each frame from state.aimingSlot
// + state.cursorWorldX/Y. Returns the Pixi container so main.ts can
// add it to the world layer.
export function createAimIndicator(state: GameState): { container: Container; update(): void } {
  const container = new Container();
  const gfx = new Graphics();
  container.addChild(gfx);

  return {
    container,
    update() {
      gfx.clear();
      if (state.aimingSlot === 0) return;
      const slot = state.aimingSlot - 1;
      const params = abilityParamsForSlot(state, slot);
      if (!params) return;
      const me = state.myEntityId ? state.entities.get(state.myEntityId) : null;
      if (!me) return;
      const myShip = getShip(me);

      const sx = me.renderX;
      const sy = me.renderY;
      const cx = state.cursorWorldX;
      const cy = state.cursorWorldY;
      const dx = cx - sx;
      const dy = cy - sy;
      const dist = Math.sqrt(dx * dx + dy * dy);
      const range = params.range || 30;
      const color = SLOT_COLORS[slot] ?? 0xffffff;

      switch (params.mode) {
        case TargetingMode.SkillshotLine:
        case TargetingMode.SkillshotChannel: {
          if (dist < 1e-3) return;
          // Ray from ship out range units in cursor direction.
          const nx = dx / dist;
          const ny = dy / dist;
          const ex = sx + nx * range;
          const ey = sy + ny * range;
          // Beam: thick line with bright stroke + dimmer fill rectangle (width = 4u).
          const beamHalfWidth = 2;
          const px2 = -ny * beamHalfWidth;
          const py2 = nx * beamHalfWidth;
          gfx.poly([
            sx + px2, sy + py2,
            ex + px2, ey + py2,
            ex - px2, ey - py2,
            sx - px2, sy - py2,
          ]).fill({ color, alpha: 0.15 });
          gfx.moveTo(sx, sy).lineTo(ex, ey).stroke({ color, width: px(2), alpha: 0.85 });
          break;
        }
        case TargetingMode.SkillshotGround: {
          // Circle at cursor, clamped to range. Also draw a faint range ring around ship.
          const clamped = dist > range && dist > 0 ? range / dist : 1;
          const tx = sx + dx * clamped;
          const ty = sy + dy * clamped;
          const splashR = (params as any).splashRadius || 6;
          gfx.circle(tx, ty, splashR).fill({ color: 0xff4422, alpha: 0.25 });
          gfx.circle(tx, ty, splashR).stroke({ color: 0xff6644, width: px(2), alpha: 0.85 });
          gfx.circle(sx, sy, range).stroke({ color, width: px(1), alpha: 0.25 });
          break;
        }
        // Self / LockOn shouldn't be aimed — handleAbilityPress fires
        // those immediately, never entering aim-state. Defensive: no-op.
      }
    },
  };
}
```

- [ ] **Step 2: Add splashRadius to ITEM_ABILITIES**

In `web-pixi/src/ui/ability-bar.ts`, add a `splashRadius?: number` field to `AbilityInfo` and populate for ground/splash abilities:

- MortarShell (item 108 secondary): `splashRadius: 6`
- HomingMissile (item 107 secondary): `splashRadius: 3` (informational, won't show in aim since it's LockOn)

- [ ] **Step 3: Wire the indicator in main.ts**

Find where the main scene is set up. After the entity container is created and added, add the aim indicator:

```typescript
const aimIndicator = createAimIndicator(state);
worldContainer.addChild(aimIndicator.container);
// In the per-frame render loop, after entity updates:
aimIndicator.update();
```

(Match the existing render-loop structure. Likely there's an existing pattern of `entityManager.update()` calls per frame.)

- [ ] **Step 4: Verify typecheck**

```bash
cd web-pixi && bunx tsc --noEmit 2>&1 | tail -10
```

Expected: clean.

- [ ] **Step 5: Commit**

```bash
cd .
git add web-pixi/src/effects/aim-indicator.ts web-pixi/src/ui/ability-bar.ts web-pixi/src/main.ts
git commit -m "$(cat <<'EOF'
feat(web): aim-indicator renderer (line/channel ray + ground circle)

aim-indicator.ts watches state.aimingSlot + cursor pos and draws the
appropriate preview each frame: a beam line from the ship for
SkillshotLine/Channel; a clamped damage circle at the cursor + range
ring around ship for SkillshotGround. Color from slot.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 19: Client — ChannelAim streaming while channeling

**Files:**
- Modify: `web-pixi/src/input.ts`
- Modify: `web-pixi/src/state.ts`

- [ ] **Step 1: Track active channel slot client-side**

In `state.ts`, add:

```typescript
channelingSlot: number; // 0 = not channeling; 1..6 = slot index + 1
channelEndsAt: number;  // performance.now() ms when the channel auto-ends
```

Initialize to 0.

When `fireSkillshot` is called for a `SkillshotChannel` ability, set:

```typescript
if (params.mode === TargetingMode.SkillshotChannel) {
  state.channelingSlot = slot + 1;
  state.channelEndsAt = performance.now() + (params.channelDuration ?? 3000);
}
```

Add `channelDuration?: number` field to `AbilityInfo` and populate for SustainedBeam (item 108 primary): `channelDuration: 3000` (ms).

- [ ] **Step 2: Stream ChannelAim updates**

In input.ts, add a per-frame tick handler (or piggyback on an existing one). For each animation frame:

```typescript
function tickChannelAim(state: GameState) {
  if (!state.channelingSlot || !state.client) return;
  if (performance.now() >= state.channelEndsAt) {
    state.channelingSlot = 0;
    return;
  }
  state.client.send(new ChannelAim({
    sequence: nextSequence(state),
    slot: state.channelingSlot - 1,
    aimX: state.cursorWorldX,
    aimY: state.cursorWorldY,
  }));
}
```

Call `tickChannelAim(state)` from the main render loop (in `main.ts`), throttled to ~20 Hz (every 50ms):

```typescript
let lastChannelAimTime = 0;
// In render loop:
const now = performance.now();
if (now - lastChannelAimTime > 50) {
  tickChannelAim(state);
  lastChannelAimTime = now;
}
```

- [ ] **Step 3: Verify typecheck**

```bash
cd web-pixi && bunx tsc --noEmit 2>&1 | tail -10
```

Expected: clean.

- [ ] **Step 4: Commit**

```bash
cd .
git add web-pixi/src/input.ts web-pixi/src/state.ts web-pixi/src/main.ts
git commit -m "$(cat <<'EOF'
feat(web): stream ChannelAim updates during SustainedBeam channel

While state.channelingSlot is set, client sends ChannelAim every 50ms
with the current cursor world coords. Server's tickChannels reads
those coords each tick to update the beam line direction.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 20: Client — Quickcast settings UI + persistence

**Files:**
- Create: `web-pixi/src/ui/quickcast-settings.ts`
- Modify: `web-pixi/src/ui/index.ts` (or whatever the settings panel root is — search for an existing "settings" UI)

- [ ] **Step 1: Find the settings panel**

Search for an existing settings panel:

```bash
grep -rn "settings\|Settings\|preferences" web-pixi/src/ui/ 2>/dev/null | head
```

If a settings panel exists, add the quickcast section to it. If not, the simplest path is to add a minimal settings UI element. For the smoke test, hardcoding the mask in localStorage works too — just make sure the bit-toggle UX is reachable.

- [ ] **Step 2: Build the UI**

```typescript
// web-pixi/src/ui/quickcast-settings.ts
import type { GameState } from "../state";

const SLOT_KEYS = ["Q", "W", "E", "R", "D", "F"];

export function buildQuickcastSettings(state: GameState): HTMLElement {
  const root = document.createElement("div");
  root.className = "quickcast-settings";
  const title = document.createElement("h3");
  title.textContent = "Quickcast (skip aim-confirm)";
  root.appendChild(title);

  for (let i = 0; i < 6; i++) {
    const label = document.createElement("label");
    label.style.marginRight = "8px";
    const checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    checkbox.checked = (state.quickcastMask & (1 << i)) !== 0;
    checkbox.addEventListener("change", () => {
      if (checkbox.checked) {
        state.quickcastMask |= 1 << i;
      } else {
        state.quickcastMask &= ~(1 << i);
      }
      localStorage.setItem("skillshot.quickcast", String(state.quickcastMask));
    });
    label.appendChild(checkbox);
    label.appendChild(document.createTextNode(SLOT_KEYS[i]));
    root.appendChild(label);
  }

  return root;
}
```

- [ ] **Step 3: Mount in the settings panel**

Find where the existing settings panel is constructed and add a call to `buildQuickcastSettings(state)` to append the section.

- [ ] **Step 4: Verify typecheck**

```bash
cd web-pixi && bunx tsc --noEmit 2>&1 | tail -10
```

Expected: clean.

- [ ] **Step 5: Commit**

```bash
cd .
git add web-pixi/src/ui/quickcast-settings.ts web-pixi/src/ui/
git commit -m "$(cat <<'EOF'
feat(web): per-slot quickcast settings panel + localStorage persistence

Six checkboxes (Q/W/E/R/D/F) — checked = skip aim-confirm, press
fires immediately at current cursor. State stored in
localStorage under "skillshot.quickcast" as a 6-bit mask.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 21: Unit tests for new dispatch paths

**Files:**
- Modify: `internal/game/system_ability_test.go` (or create if missing)

- [ ] **Step 1: Look at existing tests for the pattern**

```bash
cd .
ls internal/game/system_ability_test.go 2>/dev/null
grep -l "TestAbility\|TestProjectile" internal/game/*_test.go
```

Reuse the existing test harness (probably `newTestGameWorld` etc., as used in earlier PVE v2 tests).

- [ ] **Step 2: Add tests**

```go
// TestSkillshotLine_AimedAtTargetHits — fire PulseLaser as a
// SkillshotLine with aim coords pointing at an NPC. Projectile spawns
// and damages the NPC on the next tick.
func TestSkillshotLine_AimedAtTargetHits(t *testing.T) {
    gw, cleanup := newTestGameWorld(t)
    defer cleanup()

    player := spawnTestPlayer(t, gw, 0, 0, 100)
    target := spawnTestNPC(t, gw, 20, 0, 100) // 20u east

    // Simulate a CastAbility for PulseLaser (slot 0). Set aim to target pos.
    // (Use whatever ECS-injection pattern the existing tests use to
    //  populate input.AbilityCast + input.LastCastAimX/Y on the player entity.)
    setAbilityCast(t, gw, player, 0, target.Pos().X, target.Pos().Y)

    tickWorld(t, gw, 0.1) // dispatch happens
    tickWorld(t, gw, 0.5) // projectile travels + hits

    if hp := mmokit.Get[gamecomp.Health](target); hp.Current >= 100 {
        t.Fatalf("expected target damaged, got HP=%v", hp.Current)
    }
}

// TestSkillshotGround_DropsAoEAtCursor — MortarShell as
// SkillshotGround drops an AoEMarker at the cursor location, clamped
// to ability range.
func TestSkillshotGround_DropsAoEAtCursor(t *testing.T) {
    gw, cleanup := newTestGameWorld(t)
    defer cleanup()

    player := spawnTestPlayer(t, gw, 0, 0, 100)
    target := spawnTestNPC(t, gw, 30, 0, 100)

    // Equip MortarShell on weapon2 secondary (slot 3) — depends on how
    // the test harness assigns equipment. Cast at target's position.
    equipMortarShell(t, gw, player)
    setAbilityCast(t, gw, player, 3, target.Pos().X, target.Pos().Y)

    tickWorld(t, gw, 0.1)
    // AoEMarker has 0.6s GroundCastDelay, so tick past that.
    tickWorld(t, gw, 0.7)

    if hp := mmokit.Get[gamecomp.Health](target); hp.Current >= 100 {
        t.Fatalf("expected MortarShell splash to damage target, got HP=%v", hp.Current)
    }
}

// TestProjectilePierce_HitsTwoTargets — a RailShot-style PierceCount=2
// projectile damages two NPCs and despawns on the third.
func TestProjectilePierce_HitsTwoTargets(t *testing.T) {
    gw, cleanup := newTestGameWorld(t)
    defer cleanup()

    t1 := spawnTestNPC(t, gw, 10, 0, 100)
    t2 := spawnTestNPC(t, gw, 20, 0, 100)
    t3 := spawnTestNPC(t, gw, 30, 0, 100)

    gw.SpawnProjectile(0, 0, 100, 0, 0.5, gamecomp.ProjectileSpec{
        OwnerNetID:  0,
        Damage:      30,
        PierceCount: 2,
    })

    tickWorld(t, gw, 0.4) // covers 0→40u at 100 u/s

    if hp := mmokit.Get[gamecomp.Health](t1); hp.Current >= 100 {
        t.Errorf("expected t1 damaged: HP=%v", hp.Current)
    }
    if hp := mmokit.Get[gamecomp.Health](t2); hp.Current >= 100 {
        t.Errorf("expected t2 damaged: HP=%v", hp.Current)
    }
    if hp := mmokit.Get[gamecomp.Health](t3); hp.Current >= 100 {
        t.Errorf("expected t3 damaged (PierceCount=2 means 3 hits): HP=%v", hp.Current)
    }
}
```

(Adapt to the actual test-helper signatures. `setAbilityCast` is a new helper — implement it to set both `input.AbilityCast |= 1<<slot` and `input.LastCastAimX/Y`. `equipMortarShell` populates the player's Equipment to put item 108 in weapon2.)

- [ ] **Step 3: Run + fix**

```bash
cd .
go test ./internal/game/ -run TestSkillshot -v 2>&1 | tail -20
```

Expected: 3/3 PASS.

- [ ] **Step 4: Commit**

```bash
cd .
git add internal/game/system_ability_test.go
git commit -m "$(cat <<'EOF'
test(game): skillshot dispatch + projectile pierce

Three new unit tests:
  - TestSkillshotLine_AimedAtTargetHits: PulseLaser fires along aim
    vector, projectile hits target downrange.
  - TestSkillshotGround_DropsAoEAtCursor: MortarShell drops AoEMarker
    at cursor, target takes splash damage after the GroundCastDelay.
  - TestProjectilePierce_HitsTwoTargets: PierceCount=2 projectile
    damages 3 targets in a line and stops on the third.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 22: Final verification + smoke checklist

- [ ] **Step 1: Full Go build + test**

```bash
cd .
go vet ./...
go test ./... 2>&1 | tail -15
go build -o /tmp/skillshot-server ./cmd/server
ls -la /tmp/skillshot-server
```

Expected: clean build, all tests pass.

- [ ] **Step 2: Client typecheck + build**

```bash
cd web-pixi
bunx tsc --noEmit 2>&1 | tail -10
bun run build 2>&1 | tail -10
```

Expected: clean typecheck, clean build.

- [ ] **Step 3: Write the smoke checklist**

Write `SKILLSHOT_SMOKE.md`:

```markdown
# Skillshot Combat — Smoke Test

Branch: `pve-v2` (skillshot combat layered on top of PVE v2).

## How to test

```bash
# Stop the current dev server (running old binary)
tmux kill-session -t space-vite 2>/dev/null
pkill -f "bin/server --dev-insecure-cookie" 2>/dev/null

cd .
git status     # confirm on pve-v2, clean
just dev       # rebuild + relaunch
```

Open http://localhost:8080. NEW user (existing characters have old save).

## What you should see

### Aim-state
- Press **Q** with PulseLaser equipped → aim indicator (blue beam) appears, follows cursor.
- Press **Q** again → projectile fires along the beam direction.
- Press **E** (different slot) while aiming Q → aim switches to E's indicator (no fire).
- Press **Esc** or **right-click** while aiming → cancel, no fire.
- Toggle quickcast for Q in settings → press Q fires immediately (no aim phase).

### Per-mode visuals
- **SkillshotLine** (PulseLaser, RailShot, PlasmaShot, etc.): blue/purple beam from ship.
- **SkillshotGround** (MortarShell): red circle at cursor, clamped to range; range ring around ship.
- **SkillshotChannel** (SustainedBeam): beam from ship, tracks cursor while held.
- **LockOn** (HomingMissile, PlasmaTorpedo, IonBurn): no aim phase — fires immediately if locked.
- **Self** (EmergencyShield, Afterburner): no aim phase — fires immediately.

### Combat
- Press Q at a Brawler 30u away → PulseLaser projectile fires along the line; damages whatever it hits.
- Press R (PiercingRound, requires Railgun item) → pierces through multiple targets.
- Press E (MortarShell) → click + drop AoE at cursor; damages all NPCs inside.
- Press R while holding SustainedBeam item → channel beam, drag cursor to sweep across targets.

### Brawler dual-attack
- Watch a Brawler at range for 6+ seconds → red rectangle telegraph appears on the ground.
- After 0.8s, the beam fires along the rectangle.
- Sidestep perpendicular during the 0.8s windup → dodge the special.

## To merge

```bash
git checkout main
git merge --ff-only pve-v2
```
```

(Plain text file — don't commit it.)

- [ ] **Step 4: Final commit (none needed if everything passed)**

No code commit for this task — just verifying everything works.

---

## Implementation Notes / Gotchas

1. **`mmokit:"local"` on PlayerInput** — check whether the existing fields use that tag or no tag at all. Match the existing convention.
2. **`screenToWorld` helper** — find the existing camera transform in `view.ts` / `camera.ts`. Don't invent a new one.
3. **`nextSequence(state)`** — the existing input layer has a sequence counter. Use it.
4. **`splashRadius` typing** — `(params as any).splashRadius` in `aim-indicator.ts` is a temporary cast; if you have time, add `splashRadius?: number` to `AbilityInfo` properly.
5. **TargetingMode mapping in the SDK** — the SDK doesn't emit Go-side enums for `AbilityParams.Mode`, only the ItemDef registry. The TS side just hardcodes `TargetingMode` as a const enum (matching the Go const block). If the values drift, server vs client out of sync — but with the spec's small enum (5 values), this is low-risk.
6. **`ConfigVersion: 8`** — bump in Task 13 invalidates any persisted GameConfig JSON. If the user has a saved config, it gets the new defaults.
7. **Existing `dispatchSkillshotChannel` returns false** — that means the cooldown is NOT set at start. Channel-end (in tickChannels) writes the cooldown via `ab.Cooldowns[ch.SlotID] = params.Cooldown`. Make sure this still works after the channel ends naturally.

---

## Deferred (out of scope per spec)

- Combo / cancel-cast / animation cancel
- Smart cast variants (smart-cast-self)
- Targeting keybind rebinder (Q/W/E/R/D/F is hardcoded)
- NPC SkillshotGround attacks (Artillery already does ground-target AoE in its own FSM path)
- Per-slot quickcast hold-modifier (e.g. shift+Q = aim-confirm even if Q is quickcast)
- PvP balance pass
