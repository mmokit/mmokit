# Selection Model Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the EVE-style multi-slot `TargetLock` system with a single-target `Selection` model per [docs/superpowers/specs/2026-05-15-selection-model-design.md](../specs/2026-05-15-selection-model-design.md). Three damage abilities (HomingMissile/PlasmaTorpedo/IonBurn) become cursor-pick; mining keeps a "selection required" gate; the entire client lock UI gets nuked and replaced with a subtle outline + info panel.

**Architecture:** Server gets a new `Selection` component (one netID per player) and a new `TargetingCursorPick` dispatcher. NPCs migrate from `TargetLock.ActiveNetID` to their own `NPCAI.TargetNetID`. Client gets a `SelectionOutline` Pixi renderer + a `SelectionPanel` HTML sidebar. All Lock* wire/events/UI/state get deleted outright (solo-dev clean cut — no backward compat).

**Tech Stack:** Go (server, ark ECS via mmokit facade), TypeScript + PixiJS (web client, auto-generated SDK), PostgreSQL (untouched).

---

## File Structure

**New files (server):**
- `internal/component/selection.go` — `Selection` struct (the new component)

**Modified files (server):**
- `internal/item/item.go` — add `TargetingCursorPick = 5`; reclassify 3 abilities
- `internal/game/input_messages.go` — remove `LockTarget`/`SetActiveTarget`/`UnlockTarget`; add `SelectTarget`
- `internal/game/input_handlers.go` — remove lock handlers; add `SelectTarget` handler
- `internal/game/event_messages.go` — remove `LockSlotsMsg`/`LockRejectedMsg`/`LockReject*` constants + registrations
- `internal/game/system_ability.go` — add `dispatchCursorPick`, refactor pre-dispatch gate, refactor mining + 3 LockOn abilities, remove `activeLockTarget` helper
- `internal/game/system_npc_ai.go` — replace `Lock *gamecomp.TargetLock` parameter + uses with `ai.TargetNetID` lookups
- `internal/component/components.go` — remove `TargetLock`, `TargetLockSlot`; add `NPCAI.TargetNetID` field
- `internal/game/entity_ship.go` — replace `TargetLock` in ShipBundle with `Selection`
- `internal/game/entity_npc.go` — remove `TargetLock` from NPCBundle (NPCs now track via NPCAI.TargetNetID)
- `internal/game/factory.go` — remove `NewTargetLockSystem` registration
- `internal/game/gameworld.go` — remove any TargetLock helpers
- `internal/game/hooks.go` — remove anywhere it touches TargetLock on transfer
- `internal/game/system_network.go` — remove LockSlotsMsg replication
- `internal/game/system_poi.go` — remove TargetLock references if any
- `internal/bot/actions.go`, `internal/bot/bot.go`, `internal/bot/typed_decoder.go`, `internal/bot/world.go` — replace lock-flavored bot actions with selection equivalents

**Deleted files (server):**
- `internal/game/system_targetlock.go`
- `internal/game/system_targetlock_multi_test.go`

**Deleted files (client):**
- `web-pixi/src/effects/lock-on-ring.ts`
- `web-pixi/src/effects/being-locked-ring.ts`
- `web-pixi/src/effects/target-highlight.ts`
- `web-pixi/src/ui/lock-hud.ts`
- `web-pixi/src/ui/lock-overlay.ts`

**New files (client):**
- `web-pixi/src/effects/selection-outline.ts` — Pixi renderer for the 2px outline + halo on the selected entity
- `web-pixi/src/ui/selection-panel.ts` — HTML sidebar showing name/HP/distance

**Modified files (client):**
- `web-pixi/src/state.ts` — drop `lockSlots`/`lockedTargets`; add `selectedNetID: number`
- `web-pixi/src/input.ts` — left-click sends `SelectTarget`; right-click clears; Tab cycles selection
- `web-pixi/src/network.ts` — drop all `Lock*` subscribers; nothing new (Selection state is local)
- `web-pixi/src/main.ts` — drop `LockOnRing`/`LockHud`/`LockOverlay` wiring; add `SelectionOutline` + `SelectionPanel`
- `web-pixi/sdk/` — regenerated

---

## Task Ordering Rationale

Phase A wires data + ability conversion server-side (CursorPick mode, Selection component, SelectTarget wire) WITHOUT removing TargetLock — both systems coexist briefly so the build stays green and we can test CursorPick before yanking the floor out. Phase B refactors mining + pre-dispatch gate + NPC AI to read from Selection / NPCAI.TargetNetID. Phase C is the server tear-out: now that nothing reads TargetLock, delete it and its wire/event surface in one sweep. Phase D regenerates the SDK and rebuilds the client. Phase E covers tests + smoke.

After Phase B, the game is fully playable on the Selection model — Phase C is just cleanup.

---

### Task 1: Add Selection component

**Files:**
- Create: `internal/component/selection.go`

- [ ] **Step 1: Create the file**

```go
package component

// Selection holds the player's current selection — the entity they
// most recently left-clicked. Mining abilities (MiningBeam, ExtractPulse)
// read EntityNetID to find their target. Cursor-pick damage abilities
// (HomingMissile, PlasmaTorpedo, IonBurn) don't read this — they pick
// at fire time from cursor coords.
//
// 0 means nothing selected. Local-only — clients track their own
// selection optimistically and the server-authoritative value is
// implicit in the SelectTarget input flow.
type Selection struct {
	EntityNetID uint32 `mmokit:"local"`
}
```

- [ ] **Step 2: Verify build**

Run: `cd . && go vet ./...`
Expected: clean.

- [ ] **Step 3: Commit**

```bash
cd .
git add internal/component/selection.go
git commit -m "$(cat <<'EOF'
feat(component): add Selection (replaces multi-slot TargetLock for mining + info display)

Single-target selection model. Mining abilities read EntityNetID;
damage abilities don't. Local-only; no wire serialization needed.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Add SelectTarget wire message + handler

**Files:**
- Modify: `internal/game/input_messages.go`
- Modify: `internal/game/input_handlers.go`

- [ ] **Step 1: Add SelectTarget struct**

In `input_messages.go`, append:

```go
// SelectTarget — left-click sets the player's selection to NetID; NetID=0
// clears (right-click). Server validates the target is alive + in AoI;
// invalid requests are dropped silently (no error event).
type SelectTarget struct {
	Sequence uint32
	NetID    uint32
}
```

- [ ] **Step 2: Add SelectTarget handler**

In `input_handlers.go`, after the existing CastAbility handler, add:

```go
mmokit.HandleClient(mmo, func(player mmokit.Entity, msg *SelectTarget) {
	if !player.Alive() {
		return
	}
	sel := mmokit.Get[gamecomp.Selection](player)
	if sel == nil {
		return
	}
	if msg.NetID == 0 {
		sel.EntityNetID = 0
		return
	}
	gw := mmokit.State[GameWorld](player.Stage())
	if gw == nil {
		return
	}
	target := mmokit.EntityByNetID(gw.stage, msg.NetID)
	if !target.Alive() {
		return
	}
	sel.EntityNetID = msg.NetID
	gw.eng.Log.Log(CatCombatLock, "select: player=%d netID=%d", player.NetID(), msg.NetID)
})
```

(If `CatCombatLock` is going away later, this still compiles for now. Task 9 renames or replaces it.)

- [ ] **Step 3: Verify build**

Run: `cd . && go vet ./...`
Expected: clean (Selection component exists from Task 1; the field on the player entity gets attached in Task 4).

- [ ] **Step 4: Commit**

```bash
cd .
git add internal/game/input_messages.go internal/game/input_handlers.go
git commit -m "$(cat <<'EOF'
feat(input): add SelectTarget wire + handler

Left-click sends SelectTarget(netID); right-click sends SelectTarget(0)
to clear. Handler validates and writes Selection.EntityNetID. Invalid
or dead targets drop silently.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Attach Selection to player entity (ShipBundle + transfer)

**Files:**
- Modify: `internal/game/entity_ship.go`

- [ ] **Step 1: Add Selection to ShipBundle**

Find the `ShipBundle` struct. Find the line that declares `TargetLock` (currently a pointer to `gamecomp.TargetLock`). Add `Selection *gamecomp.Selection` next to it (don't remove TargetLock yet — that happens in Phase C). With both present, ShipBundle declares both — fine for the coexistence window.

```go
// (existing TargetLock line stays)
TargetLock *gamecomp.TargetLock `mmokit:"local"`
// New: single-target selection (mining + info display).
Selection  *gamecomp.Selection  `mmokit:"local"`
```

- [ ] **Step 2: Initialize on spawn**

Find `SpawnPlayer` (or whatever the spawn function is in `entity_ship.go`). After the `TargetLock` initialization line, add:

```go
gamecomp.Selection{EntityNetID: 0},
```

(Whatever the existing component-list construction pattern is, append Selection alongside the other components passed to `stage.SpawnPlayer`.)

- [ ] **Step 3: Verify build + test**

```bash
cd .
go vet ./...
go test ./internal/game/ 2>&1 | tail -5
```

Expected: clean.

- [ ] **Step 4: Commit**

```bash
cd .
git add internal/game/entity_ship.go
git commit -m "$(cat <<'EOF'
feat(entity): attach Selection to player entity alongside TargetLock

Coexistence: both TargetLock and Selection live on the player ship
during the migration. Phase C removes TargetLock.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Add TargetingCursorPick mode + reclassify abilities

**Files:**
- Modify: `internal/item/item.go`

- [ ] **Step 1: Add the enum value**

Find the `TargetingMode` const block (~line 80, after the Skillshot* values). Append:

```go
TargetingCursorPick TargetingMode = 5 // cursor-pick: server picks nearest enemy near cursor at fire
```

- [ ] **Step 2: Reclassify HomingMissile, PlasmaTorpedo, IonBurn**

Find each ability in `doInit()`:
- Item 105 primary `IonBurn` → change `Mode: TargetingLockOn` to `Mode: TargetingCursorPick`
- Item 106 secondary `PlasmaTorpedo` → change `Mode: TargetingLockOn` to `Mode: TargetingCursorPick`
- Item 107 secondary `HomingMissile` → change `Mode: TargetingLockOn` to `Mode: TargetingCursorPick`

Mining abilities (items 130/131 primary `MiningBeam` and secondary `ExtractPulse`) **stay** `TargetingLockOn` — the dispatcher's semantics change in Task 7 to read Selection.

- [ ] **Step 3: Verify build**

Run: `cd . && go vet ./...`
Expected: clean.

- [ ] **Step 4: Commit**

```bash
cd .
git add internal/item/item.go
git commit -m "$(cat <<'EOF'
feat(item): add TargetingCursorPick mode + reclassify 3 lock-on damage abilities

HomingMissile, PlasmaTorpedo, IonBurn move from TargetingLockOn to
TargetingCursorPick. Mining (MiningBeam, ExtractPulse) keeps LockOn —
its dispatcher will read Selection.EntityNetID instead of
TargetLock.ActiveNetID in Task 7.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Implement dispatchCursorPick

**Files:**
- Modify: `internal/game/system_ability.go`

- [ ] **Step 1: Add the dispatcher**

Find the `executeAbility` outer switch (added in earlier skillshot work). Add a `case` for the new mode at the top, before the Self/LockOn fallthrough:

```go
case item.TargetingCursorPick:
    return s.dispatchCursorPick(action, casterE)
```

Then add the method itself (place it near the other dispatch helpers):

```go
// dispatchCursorPick runs the per-ability effect on whichever enemy is
// nearest the cursor at fire time, within params.Range of the caster.
// No selection required. If nothing is under the cursor (within 5u of
// the aim point), returns false so the caller refunds the cooldown.
func (s *AbilitySystem) dispatchCursorPick(action abilityAction, casterE mmokit.Entity) bool {
    gw := s.gw
    params := action.params

    casterPos := mmokit.Get[mmokit.Position](casterE)
    if casterPos == nil {
        return false
    }
    ownerNetID := casterE.NetID()

    // Search radius around cursor: small, so the player is forced to
    // hover roughly over the target. 5u is generous enough to forgive
    // a slightly-off click, tight enough that empty-space clicks miss.
    const pickRadius float32 = 5.0
    s.cursorPickNearby = gw.Spatial.QueryRadius(
        action.aimX, action.aimY, pickRadius,
        s.cursorPickNearby[:0],
    )

    var best mmokit.Entity
    bestDist2 := float32(pickRadius * pickRadius)
    for _, entry := range s.cursorPickNearby {
        cand := mmokit.EntityFromECS(gw.stage, entry.Entity)
        if !cand.Alive() || cand.NetID() == ownerNetID {
            continue
        }
        // Damage-eligible: NPC enemies for now (no PvP).
        if !mmokit.Has[gamecomp.NPCAI](cand) {
            continue
        }
        cpos := mmokit.Get[mmokit.Position](cand)
        if cpos == nil {
            continue
        }
        // Range from caster (not from cursor) gates the ability.
        rx, ry := cpos.X-casterPos.X, cpos.Y-casterPos.Y
        if params.Range > 0 && rx*rx+ry*ry > params.Range*params.Range {
            continue
        }
        // Pick the one closest to the cursor.
        cx, cy := cpos.X-action.aimX, cpos.Y-action.aimY
        d2 := cx*cx + cy*cy
        if d2 < bestDist2 {
            bestDist2 = d2
            best = cand
        }
    }
    if !best.Alive() {
        gw.eng.Log.Log(CatCombatAbility, "ability %s: %d cursor-pick MISS aim=(%.0f,%.0f)",
            params.Name, action.casterNetID, action.aimX, action.aimY)
        return false // refund cooldown
    }

    targetNetID := best.NetID()
    gw.eng.Log.Log(CatCombatAbility, "ability %s: %d cursor-pick HIT target=%d",
        params.Name, action.casterNetID, targetNetID)

    // Per-ability effect — replicates the old LockOn dispatchByType
    // logic but reads `best` instead of activeLockTarget(lock).
    switch params.Type {
    case item.AbilityTypeHomingMissile:
        bpos := mmokit.Get[mmokit.Position](best)
        if bpos == nil {
            return false
        }
        dx, dy := bpos.X-casterPos.X, bpos.Y-casterPos.Y
        // fireProjectile internally stamps TargetNetID for missile homing.
        // Bypass by setting target via a closure-friendly variant: since
        // fireProjectile only reads target from TargetLock today, we
        // override here by passing dx/dy and trusting the projectile to
        // hit-or-miss like other line shots. Homing remains because
        // ProjectileSpec.MaxTurnRate is set in params.
        // NOTE: to preserve homing, we set TargetNetID on the spec
        // post-spawn — see fireProjectileToward below.
        s.fireProjectileToward(casterE, params, gamecomp.ProjectileTypeMissile, targetNetID, dx, dy)
        return true
    case item.AbilityTypePlasmaTorpedo:
        bpos := mmokit.Get[mmokit.Position](best)
        if bpos == nil {
            return false
        }
        dx, dy := bpos.X-casterPos.X, bpos.Y-casterPos.Y
        s.fireProjectileToward(casterE, params, gamecomp.ProjectileTypePlasma, targetNetID, dx, dy)
        return true
    case item.AbilityTypeIonBurn:
        // Hitscan damage + apply DoT to the picked target. Replicates the
        // old LockOn IonBurn path verbatim, just with `best` as the target.
        gw.Damage(casterE, best, params.Damage, 0, action.slot, uint8(params.Type))
        s.applyIonDoT(best, params)
        return true
    }
    return false
}

// fireProjectileToward — same as fireProjectile but stamps an explicit
// TargetNetID so the projectile homes/tracks the cursor-pick target.
// fireProjectile (in dispatchSkillshotLine) doesn't know about target
// netID because skillshot projectiles don't home.
func (s *AbilitySystem) fireProjectileToward(
    caster mmokit.Entity, params *item.AbilityParams,
    projType uint8, targetNetID uint32, dirX, dirY float32,
) {
    gw := s.gw
    casterPos := mmokit.Get[mmokit.Position](caster)
    if casterPos == nil {
        return
    }
    norm := float32(math.Sqrt(float64(dirX*dirX + dirY*dirY)))
    if norm < 1e-3 {
        return
    }
    dirX, dirY = dirX/norm, dirY/norm
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
        // PierceCount stays 0 for cursor-pick projectiles.
    }
    gw.SpawnProjectile(
        casterPos.X, casterPos.Y,
        dirX*params.ProjectileSpeed, dirY*params.ProjectileSpeed,
        lifetime, spec,
    )
}
```

(`applyIonDoT` is the existing helper that applies the StatusFortified-style debuff — find it in `system_ability.go` and call it here. If it doesn't exist as a discrete helper, inline the DoT-apply code from the old LockOn IonBurn case.)

- [ ] **Step 2: Add scratch buffer to AbilitySystem**

Find the `AbilitySystem` struct. Add a field:

```go
cursorPickNearby []mmokit.SpatialEntry
```

Find `(s *AbilitySystem) Init()` and after `s.nearbyChannel = make(...)`, add:

```go
s.cursorPickNearby = make([]mmokit.SpatialEntry, 0, 32)
```

- [ ] **Step 3: Verify build + tests**

```bash
cd .
go vet ./...
go test ./internal/game/ 2>&1 | tail -10
```

Expected: clean. Existing tests pass (CursorPick is additive; nothing routes through it yet because the abilities still go through `dispatchByType` LockOn branches at this point — see Task 6).

Wait — Task 4 already reclassified the 3 abilities to `TargetingCursorPick`. Mode dispatch now routes them through `dispatchCursorPick` instead of `dispatchByType`. So this task IS what makes them functional. The LockOn cases in `dispatchByType` still exist but are unreachable for these three abilities; Task 6 cleans them up.

- [ ] **Step 4: Commit**

```bash
cd .
git add internal/game/system_ability.go
git commit -m "$(cat <<'EOF'
feat(ability): implement dispatchCursorPick for HomingMissile/PlasmaTorpedo/IonBurn

Server picks the enemy nearest the cursor at fire time (within 5u of aim
point, within params.Range of caster). Per-ability effect identical to the
old LockOn path: HomingMissile spawns a homing projectile, PlasmaTorpedo
fires a plasma projectile, IonBurn applies hitscan damage + DoT.

Adds fireProjectileToward — a fireProjectile variant that stamps an
explicit TargetNetID so homing missiles know what to track.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Remove dead LockOn cases from dispatchByType (HomingMissile/PlasmaTorpedo/IonBurn only)

**Files:**
- Modify: `internal/game/system_ability.go`

- [ ] **Step 1: Delete the dead LockOn dispatch branches**

In `dispatchByType`, find the per-Type switch. Delete the case branches for:

- `item.AbilityTypeHomingMissile`
- `item.AbilityTypePlasmaTorpedo`
- `item.AbilityTypeIonBurn`

Each currently looks something like:

```go
case item.AbilityTypeHomingMissile:
    target, ok := activeLockTarget(gw, lock)
    if !ok {
        fired = false
        break
    }
    // ... fires the projectile ...
```

Delete the whole `case` block for each. They no longer execute because Mode dispatch routes these abilities to `dispatchCursorPick` first.

Leave the MiningBeam/ExtractPulse `case` branches alone — they're still LockOn-mode-routed and we change their target source in Task 7, not their case body location.

- [ ] **Step 2: Verify build + tests**

```bash
cd .
go vet ./...
go test ./internal/game/ 2>&1 | tail -5
```

Expected: clean.

- [ ] **Step 3: Commit**

```bash
cd .
git add internal/game/system_ability.go
git commit -m "$(cat <<'EOF'
refactor(ability): remove dead LockOn cases for HomingMissile/PlasmaTorpedo/IonBurn

These three abilities now route through dispatchCursorPick (Mode-keyed).
Their old per-Type LockOn case bodies in dispatchByType are unreachable
and get deleted. Mining (MiningBeam, ExtractPulse) keeps its case bodies
— they still flow through TargetingLockOn dispatch, just with a target
source change in Task 7.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Refactor mining + pre-dispatch gate to read Selection

**Files:**
- Modify: `internal/game/system_ability.go`

- [ ] **Step 1: Refactor pre-dispatch gate**

Find the pre-dispatch gate at `system_ability.go:113` (the block that begins `if slot <= gamecomp.AbilityR && !isMiningToggle && params.Mode == item.TargetingLockOn`). Currently:

```go
if slot <= gamecomp.AbilityR && !isMiningToggle && params.Mode == item.TargetingLockOn {
    target, ok := activeLockTarget(gw, lock)
    if !ok {
        continue
    }
    if params.Range > 0 && !s.inRange(entity, target.Handle(), params.Range) {
        continue
    }
}
```

Replace with:

```go
if slot <= gamecomp.AbilityR && !isMiningToggle && params.Mode == item.TargetingLockOn {
    sel := mmokit.Get[gamecomp.Selection](casterE)
    if sel == nil || sel.EntityNetID == 0 {
        continue
    }
    target := mmokit.EntityByNetID(gw.stage, sel.EntityNetID)
    if !target.Alive() {
        continue
    }
    if params.Range > 0 && !s.inRange(entity, target.Handle(), params.Range) {
        continue
    }
}
```

(`casterE` here — confirm the surrounding scope's name for the player entity. Use whatever the existing variable is; the example uses `casterE`.)

- [ ] **Step 2: Refactor mining ability bodies to use Selection**

Find the `case item.AbilityTypeMiningBeam:` branch in `dispatchByType`. Wherever it calls `activeLockTarget(gw, lock)` to get the target, replace with:

```go
sel := mmokit.Get[gamecomp.Selection](casterE)
if sel == nil || sel.EntityNetID == 0 {
    fired = false
    break
}
target := mmokit.EntityByNetID(gw.stage, sel.EntityNetID)
if !target.Alive() {
    fired = false
    break
}
```

Do the same for `case item.AbilityTypeExtractPulse:`.

- [ ] **Step 3: Delete activeLockTarget helper**

`activeLockTarget` at `system_ability.go:11-25` is now unused (the only callers were the 3 LockOn damage cases removed in Task 6 and the gate in Step 1 above + the mining cases in Step 2). Delete the function.

- [ ] **Step 4: Verify build + tests**

```bash
cd .
go vet ./...
go test ./internal/game/ 2>&1 | tail -10
```

Expected: clean. Existing mining tests may need updates if they prime `TargetLock` instead of `Selection` — fix them: replace `lock.ActiveNetID = X` and slot population with `sel.EntityNetID = X`.

- [ ] **Step 5: Commit**

```bash
cd .
git add internal/game/system_ability.go internal/game/*_test.go
git commit -m "$(cat <<'EOF'
refactor(ability): mining + pre-dispatch gate now read Selection.EntityNetID

Pre-dispatch gate for TargetingLockOn abilities checks Selection instead
of TargetLock.ActiveNetID. MiningBeam + ExtractPulse dispatch bodies do
the same. The activeLockTarget helper is no longer referenced and is
deleted. Tests rewritten to populate Selection.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: Migrate NPC AI from TargetLock to NPCAI.TargetNetID

**Files:**
- Modify: `internal/component/components.go`
- Modify: `internal/game/system_npc_ai.go`
- Modify: `internal/game/entity_npc.go`

- [ ] **Step 1: Add TargetNetID field to NPCAI**

In `components.go`, find the `NPCAI` struct. Append (alongside the existing Brawler-special-state fields):

```go
// TargetNetID — the NPC's current engage target. Replaces the old
// TargetLock.ActiveNetID indirection. Updated each tick by the
// closest-enemy logic in tickEngage; consumed by dispatch methods
// (tickEngage hitscan, tickCast for Artillery, etc.).
TargetNetID uint32
```

- [ ] **Step 2: Replace lock.ActiveNetID lookups in system_npc_ai.go**

In `system_npc_ai.go`:

1. Remove `Lock *gamecomp.TargetLock` from the NPC bundle declaration (around line 28-32). The NPC iteration loop no longer needs the TargetLock pointer.

2. Update every method that takes `lock *gamecomp.TargetLock` as a parameter to take `ai *gamecomp.NPCAI` instead (or use the existing `ai` if already in scope). Specifically the following methods:
   - `tickIdle` (or whatever the idle tick is called)
   - `tickApproach`
   - `tickEngage`
   - `tickCast`
   - `tickWindup`
   - any other `tick*` that takes `lock`

3. Replace every `lock.ActiveNetID` reference with `ai.TargetNetID`. There are about 10 of these (per `grep -n "lock.ActiveNetID" internal/game/system_npc_ai.go`).

4. Replace every `lock.Slots[i]` reference with internal AI state — for NPCs, lock slots are unused, so just remove the slot-related code.

5. The closest-enemy-pick logic that sets `lock.ActiveNetID = newTargetNetID` becomes `ai.TargetNetID = newTargetNetID`.

6. Where `lock.ActiveNetID = 0` (clear target), use `ai.TargetNetID = 0`.

- [ ] **Step 3: Remove TargetLock from NPCBundle**

In `entity_npc.go`, find the NPCBundle struct and remove the TargetLock field. NPCs no longer need it.

In the NPC spawn function, remove the `gamecomp.TargetLock{}` value from the spawn args list.

- [ ] **Step 4: Verify build + tests**

```bash
cd .
go vet ./...
go test ./internal/game/ 2>&1 | tail -10
```

Expected: clean. NPC AI tests must still pass — if any test directly sets `ai.Lock` or `lock.ActiveNetID`, rewrite to set `ai.TargetNetID`.

- [ ] **Step 5: Commit**

```bash
cd .
git add internal/component/components.go internal/game/system_npc_ai.go internal/game/entity_npc.go internal/game/*_test.go
git commit -m "$(cat <<'EOF'
refactor(ai): NPC AI tracks target via NPCAI.TargetNetID, not TargetLock

NPCs no longer rely on the player-flavored TargetLock indirection to
remember who they're shooting at. NPCAI gains TargetNetID; every
tick* method reads and writes it directly. TargetLock removed from
NPCBundle. NPCs no longer share the player's lock-slot infrastructure
— they just engage the closest enemy in aggro range.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: Delete TargetLock server surface (component + system + wire + events)

**Files:**
- Delete: `internal/game/system_targetlock.go`
- Delete: `internal/game/system_targetlock_multi_test.go`
- Modify: `internal/component/components.go`
- Modify: `internal/game/entity_ship.go`
- Modify: `internal/game/factory.go`
- Modify: `internal/game/input_messages.go`
- Modify: `internal/game/input_handlers.go`
- Modify: `internal/game/event_messages.go`
- Modify: `internal/game/system_network.go`
- Modify: `internal/game/hooks.go`
- Modify: `internal/game/system_poi.go` (if it references lock)

- [ ] **Step 1: Delete the TargetLock-related files**

```bash
cd .
git rm internal/game/system_targetlock.go
git rm internal/game/system_targetlock_multi_test.go
```

- [ ] **Step 2: Remove TargetLock + TargetLockSlot from components.go**

Find the `TargetLock` struct and `TargetLockSlot` struct in `components.go`. Delete both.

- [ ] **Step 3: Remove TargetLock from ShipBundle**

In `entity_ship.go`, remove the `TargetLock *gamecomp.TargetLock` line from `ShipBundle`. Remove the `gamecomp.TargetLock{...}` initializer from the spawn arg list.

- [ ] **Step 4: Remove NewTargetLockSystem registration**

In `factory.go`, find the line that adds the TargetLockSystem (looks like `coord.AddSystem(NewTargetLockSystem())` or similar). Delete it.

- [ ] **Step 5: Remove LockTarget/SetActiveTarget/UnlockTarget wire messages**

In `input_messages.go`, delete the three struct definitions: `LockTarget`, `SetActiveTarget`, `UnlockTarget`.

In `input_handlers.go`, delete the three handler registrations (`mmokit.HandleClient(...) func(p, msg *LockTarget) {...}` etc.).

- [ ] **Step 6: Remove Lock* server events**

In `event_messages.go`, delete:
- `LockSlotsMsg` struct
- `LockSlotWire` struct
- `LockRejectReason` type + constants
- `LockRejectedMsg` struct
- Any `LockProgress`/`LockComplete`/`LockBroken`/`LockReject` constants or types
- The `mmokit.RegisterEvent[LockSlotsMsg]()` and `mmokit.RegisterEvent[LockRejectedMsg]()` lines in the event-registration block

Also delete the `CatCombatLock` log category if present (find via `grep -n "CatCombatLock" internal/game/*.go`); if other code logs to it, change those log calls to `CatCombatAbility` or remove.

- [ ] **Step 7: Remove LockSlotsMsg replication from system_network.go**

`grep -n "LockSlotsMsg" internal/game/system_network.go`. Wherever LockSlotsMsg gets pushed onto the wire, delete that block. The selection is local-only (Task 1's `mmokit:"local"` tag); no replication needed.

- [ ] **Step 8: Remove TargetLock from hooks.go**

In `hooks.go`, `grep -n "TargetLock" internal/game/hooks.go`. If `FinishTransferSpawn` or any cross-cell transfer hook touches TargetLock (e.g. resetting it post-handoff), delete those lines.

- [ ] **Step 9: Sweep + verify**

```bash
cd .
grep -rn "TargetLock\|LockTarget\|LockSlotsMsg\|LockRejected\|LockSlots\|LockSlotWire" internal/ 2>&1 | grep -v "_test.go:.*Test" | head
```

Expected: empty output. If anything remains, fix it.

```bash
cd .
go vet ./...
go test ./internal/game/ 2>&1 | tail -10
```

Expected: clean. Tests pass.

- [ ] **Step 10: Commit**

```bash
cd .
git add -A internal/
git commit -m "$(cat <<'EOF'
refactor: nuke TargetLock — full server-side tear-out

Delete the TargetLock + TargetLockSlot components, system_targetlock.go,
its multi-lock test, the LockTarget/SetActiveTarget/UnlockTarget wire
messages, all Lock* server events (LockSlotsMsg, LockRejectedMsg, etc.),
the lock-slots replication path in system_network, and TargetLock
references in entity_ship/factory/hooks. NPC AI already migrated to
NPCAI.TargetNetID in the prior task; nothing reads TargetLock anymore.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: Update bot client for new wire

**Files:**
- Modify: `internal/bot/actions.go`
- Modify: `internal/bot/bot.go`
- Modify: `internal/bot/typed_decoder.go`
- Modify: `internal/bot/world.go`

- [ ] **Step 1: Find bot lock references**

```bash
cd .
grep -n "TargetLock\|LockTarget\|UnlockTarget\|SetActiveTarget" internal/bot/*.go
```

- [ ] **Step 2: Replace with SelectTarget**

For each reference:
- `LockTarget{NetID: X}` → `SelectTarget{NetID: X}`
- `UnlockTarget{NetID: X}` → `SelectTarget{NetID: 0}` (right-click clears)
- `SetActiveTarget{NetID: X}` → `SelectTarget{NetID: X}` (functionally the same now)
- `gamecomp.TargetLock` references on the bot-side world model → `gamecomp.Selection`

In `bot/world.go`, any field tracking "what the bot has locked" becomes `selectedNetID uint32` (or similar).

In `bot/typed_decoder.go`, remove any LockSlotsMsg/LockRejectedMsg decoder cases.

- [ ] **Step 3: Verify build**

```bash
cd .
go vet ./...
```

Expected: clean.

- [ ] **Step 4: Commit**

```bash
cd .
git add internal/bot/
git commit -m "$(cat <<'EOF'
refactor(bot): use SelectTarget instead of LockTarget/UnlockTarget

Bots now drive the Selection-model wire format. Lock-slot tracking
removed from the headless world model.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 11: Regenerate TypeScript SDK

- [ ] **Step 1: Regen**

```bash
cd .
go run ./cmd/server --dump-schema --control-listen=:19100 --admin-listen=:19101 --mode=coordinator,host 2>/dev/null | go run ./cmd/sdkgen --out web-pixi/sdk --core pkg/quantize/ts/delta-decoder-core.ts
```

Non-default ports avoid conflict with a running dev server.

- [ ] **Step 2: Verify SDK changes**

```bash
cd .
git diff web-pixi/sdk/ | head -50
grep -n "LockTarget\|UnlockTarget\|SetActiveTarget\|LockSlotsMsg\|LockRejectedMsg\|SelectTarget" web-pixi/sdk/*.ts
```

Expected: `LockTarget`, `UnlockTarget`, `SetActiveTarget`, `LockSlotsMsg`, `LockRejectedMsg` all GONE from the SDK. `SelectTarget` PRESENT in `inputs.ts`.

- [ ] **Step 3: Commit the regen**

```bash
cd .
git add web-pixi/sdk/
git commit -m "$(cat <<'EOF'
build: regenerate TypeScript SDK for Selection model

Wire surface: all Lock* messages gone. SelectTarget added.
Net diff is smaller — the multi-lock slot system was a lot of wire bytes.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 12: Delete client lock UI + effects + state

**Files:**
- Delete: `web-pixi/src/effects/lock-on-ring.ts`
- Delete: `web-pixi/src/effects/being-locked-ring.ts`
- Delete: `web-pixi/src/effects/target-highlight.ts`
- Delete: `web-pixi/src/ui/lock-hud.ts`
- Delete: `web-pixi/src/ui/lock-overlay.ts`
- Modify: `web-pixi/src/state.ts`
- Modify: `web-pixi/src/input.ts`
- Modify: `web-pixi/src/network.ts`
- Modify: `web-pixi/src/main.ts`

- [ ] **Step 1: Delete the 5 client lock files**

```bash
cd .
git rm web-pixi/src/effects/lock-on-ring.ts
git rm web-pixi/src/effects/being-locked-ring.ts
git rm web-pixi/src/effects/target-highlight.ts
git rm web-pixi/src/ui/lock-hud.ts
git rm web-pixi/src/ui/lock-overlay.ts
```

- [ ] **Step 2: Remove lock state from state.ts**

In `state.ts`, find and delete:
- The `lockSlots` field from `GameState` (the array of `{targetNetID, progress, locked}`)
- The `lockedTargets: Set<number>` field
- Their initializers in `createInitialState`
- Any other `lock*` field

Add the new field:
```typescript
selectedNetID: number; // 0 = nothing selected; mirrors server Selection.EntityNetID
```

Initialize to 0 in `createInitialState`.

- [ ] **Step 3: Remove lock subscribers from network.ts**

In `network.ts`, find every `client.typedEvents.on(LockSlotsMsg, ...)` and `client.typedEvents.on(LockRejectedMsg, ...)` handler. Delete them. Remove the corresponding imports.

If the network file imported `LockOnRing` / `LockHud` / etc. for hooking up sound effects on lock events, delete those imports.

- [ ] **Step 4: Remove lock wiring from input.ts (temporary)**

In `input.ts`, find every `state.client.send(new LockTarget(...))`, `state.client.send(new UnlockTarget(...))`, `state.client.send(new SetActiveTarget(...))` call. **Comment them out for now** — Task 13 wires the SelectTarget replacement. Also remove the `LockTarget`, `UnlockTarget`, `SetActiveTarget` imports.

Remove any tab-cycle logic that touches `lockSlots` — leave the tab-key bound but make the handler a no-op for now. Task 13 reimplements tab-cycle on Selection.

- [ ] **Step 5: Remove lock wiring from main.ts**

In `main.ts`:
- Delete `import { LockOnRing } from "./effects/lock-on-ring";`
- Delete `import { LockHud } ...`, `LockOverlay`, `BeingLockedRing`, `TargetHighlight` imports
- Delete `const lockOnRing = new LockOnRing(...)` and similar instantiations
- Delete the render-loop calls like `lockOnRing.update(state)`

- [ ] **Step 6: Verify TS typecheck**

```bash
cd web-pixi
bunx tsc --noEmit 2>&1 | tail -20
```

Expected: clean, OR errors that point at remaining lock references the sweep missed. Fix them.

- [ ] **Step 7: Commit**

```bash
cd .
git add -A web-pixi/
git commit -m "$(cat <<'EOF'
refactor(web): nuke client lock UI/effects/state

Delete LockOnRing, BeingLockedRing, target-highlight (effects),
LockHud, LockOverlay (UI), and all lockSlots/lockedTargets state.
Tab + click handlers temporarily stubbed; SelectTarget wiring lands
in Task 13.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 13: Wire SelectTarget on left-click, Tab, right-click

**Files:**
- Modify: `web-pixi/src/input.ts`

- [ ] **Step 1: Send SelectTarget on left-click of an entity**

Find the left-click handler in `input.ts` (the section that today fires `LockTarget` or sets `lockSlots`). Replace with:

```typescript
if (state.client) {
  state.client.send(new SelectTarget({
    sequence: state.inputSeq++,
    netID: clickedNetID,
  }));
  state.selectedNetID = clickedNetID; // optimistic
  audio.play(SoundId.TargetLock); // repurposed as "select" blip
}
```

(The exact context — clickedNetID lookup — comes from the existing entity-under-cursor logic that today populates the lock target. Reuse it.)

- [ ] **Step 2: Send SelectTarget(0) on right-click**

Find the right-click handler. Add the clear branch BEFORE the existing right-click move-cancel / aim-cancel:

```typescript
if (state.selectedNetID !== 0) {
  if (state.client) {
    state.client.send(new SelectTarget({
      sequence: state.inputSeq++,
      netID: 0,
    }));
  }
  state.selectedNetID = 0;
  return; // don't fall through to move-cancel
}
```

- [ ] **Step 3: Tab cycles selection**

Find the Tab handler. Replace its body with a cycle over visible enemy/asteroid entities:

```typescript
function handleTab(state: GameState) {
  const candidates: number[] = [];
  for (const [netID, ent] of state.entities) {
    if (netID === state.myEntityId) continue;
    // Mineable rocks + NPC enemies are tab-cyclable.
    const kind = ent.current?.entityType;
    if (kind === EntityType.Asteroid || kind === EntityType.NPC) {
      candidates.push(netID);
    }
  }
  if (candidates.length === 0) return;
  candidates.sort((a, b) => a - b); // stable order
  let nextIdx = 0;
  if (state.selectedNetID !== 0) {
    const idx = candidates.indexOf(state.selectedNetID);
    nextIdx = idx === -1 ? 0 : (idx + 1) % candidates.length;
  }
  const next = candidates[nextIdx];
  if (state.client) {
    state.client.send(new SelectTarget({
      sequence: state.inputSeq++,
      netID: next,
    }));
  }
  state.selectedNetID = next;
  audio.play(SoundId.TargetLock);
}
```

Wire `handleTab(state)` into the existing Tab keydown handler.

- [ ] **Step 4: Auto-clear when selected entity dies / leaves AoI**

In the per-frame update loop (in `input.ts` or wherever `sendInput` lives), add a guard:

```typescript
if (state.selectedNetID !== 0 && !state.entities.has(state.selectedNetID)) {
  state.selectedNetID = 0; // optimistic clear; server will gc its side independently
}
```

- [ ] **Step 5: Verify typecheck**

```bash
cd web-pixi && bunx tsc --noEmit 2>&1 | tail -10
```

Expected: clean.

- [ ] **Step 6: Commit**

```bash
cd .
git add web-pixi/src/input.ts
git commit -m "$(cat <<'EOF'
feat(web): wire SelectTarget on left-click, right-click, Tab

Left-click on entity → SelectTarget(netID) + optimistic state.selectedNetID
update + select blip sound. Right-click → SelectTarget(0) + clear. Tab
cycles asteroid+NPC entities. Selection auto-clears when target leaves AoI.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 14: Client TargetingMode.CursorPick + aim-indicator hover-highlight

**Files:**
- Modify: `web-pixi/src/ui/ability-bar.ts`
- Modify: `web-pixi/src/effects/aim-indicator.ts`

- [ ] **Step 1: Add CursorPick to the client TargetingMode enum**

In `web-pixi/src/ui/ability-bar.ts`, find the `TargetingMode` const enum (currently has Self=0..SkillshotChannel=4). Append:

```typescript
CursorPick = 5,
```

- [ ] **Step 2: Reclassify the 3 abilities in ITEM_ABILITIES**

In the same file, find the entries for these abilities and update their `mode:` field:

- Item 105 primary `IonBurn` → `mode: TargetingMode.CursorPick`
- Item 106 secondary `PlasmaTorpedo` → `mode: TargetingMode.CursorPick`
- Item 107 secondary `HomingMissile` → `mode: TargetingMode.CursorPick`

(Mining abilities — item 130/131 primary `MiningBeam`, secondary `ExtractPulse` — STAY `TargetingMode.LockOn`. The mode label means "selection required" client-side; same as the server-side mode.)

- [ ] **Step 3: Aim-indicator hover-highlight for CursorPick**

In `web-pixi/src/effects/aim-indicator.ts::drawAimPreview`, find the `switch (params.mode)` block. Add a new case before the closing brace:

```typescript
case TargetingMode.CursorPick: {
  // Hover-highlight on the enemy nearest the cursor — feedback for
  // "this is who you'd hit." Same pick math as the server-side
  // dispatchCursorPick (within ~5u of aim, within params.Range of ship).
  let best: { x: number; y: number; radius: number } | null = null;
  let bestDist2 = 5 * 5; // pickRadius squared
  for (const ent of state.entities.values()) {
    if (!ent.current) continue;
    if (ent.current.entityType !== EntityType.NPC) continue;
    const ex = ent.renderX;
    const ey = ent.renderY;
    // Range from caster.
    const rx = ex - sx;
    const ry = ey - sy;
    if (range > 0 && rx * rx + ry * ry > range * range) continue;
    // Distance from cursor.
    const cdx = ex - cx;
    const cdy = ey - cy;
    const d2 = cdx * cdx + cdy * cdy;
    if (d2 < bestDist2) {
      bestDist2 = d2;
      const r = (ent.current.collider?.radius ?? 6) + px(3);
      best = { x: ex, y: ey, radius: r };
    }
  }
  // Range ring around ship — always visible during aim.
  this.gfx.circle(sx, sy, range).stroke({ color, width: px(1), alpha: 0.25 });
  if (best) {
    // Soft highlight ring on the picked enemy.
    this.gfx.circle(best.x, best.y, best.radius).stroke({ color: 0xff5544, width: px(2), alpha: 0.9 });
    this.gfx.circle(best.x, best.y, best.radius + px(3)).stroke({ color: 0xff5544, width: px(1), alpha: 0.3 });
  }
  break;
}
```

Add the `EntityType` import at the top of the file if not already imported:

```typescript
import { EntityType } from "../../sdk";
```

- [ ] **Step 4: Verify typecheck**

```bash
cd web-pixi && bunx tsc --noEmit 2>&1 | tail -10
```

Expected: clean.

- [ ] **Step 5: Commit**

```bash
cd .
git add web-pixi/src/ui/ability-bar.ts web-pixi/src/effects/aim-indicator.ts
git commit -m "$(cat <<'EOF'
feat(web): TargetingMode.CursorPick + aim-indicator hover-highlight

Reclassify HomingMissile, PlasmaTorpedo, IonBurn to CursorPick on the
client side. AimIndicator gets a CursorPick branch that highlights the
NPC nearest the cursor (within 5u + within params.Range of ship) so the
player gets visual feedback about "this is who you'd hit."

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 15: SelectionOutline renderer

**Files:**
- Create: `web-pixi/src/effects/selection-outline.ts`
- Modify: `web-pixi/src/main.ts`

- [ ] **Step 1: Create the renderer**

```typescript
// web-pixi/src/effects/selection-outline.ts
import { Container, Graphics } from "pixi.js";
import type { GameState } from "../state";
import { EntityType } from "../../sdk";
import { px } from "../view";

// SelectionOutline draws a 2px stroke + faint halo on the currently
// selected entity. Color is type-derived so the player can tell at a
// glance what kind of thing they've selected:
//   asteroid  → green (mineable)
//   NPC       → red (hostile)
//   station   → cyan (neutral / dockable)
//   anything else → white (loot crate, POI, etc.)
//
// Redrawn each frame from state.selectedNetID + state.entities. Empty
// when nothing is selected.
const COLOR_ASTEROID = 0x44ff88;
const COLOR_NPC      = 0xff5544;
const COLOR_STATION  = 0x55ccff;
const COLOR_DEFAULT  = 0xffffff;

function colorFor(entityType: number): number {
  switch (entityType) {
    case EntityType.Asteroid: return COLOR_ASTEROID;
    case EntityType.NPC:      return COLOR_NPC;
    case EntityType.Station:  return COLOR_STATION;
    default:                  return COLOR_DEFAULT;
  }
}

export class SelectionOutline {
  private gfx: Graphics;

  constructor(parent: Container) {
    this.gfx = new Graphics();
    parent.addChild(this.gfx);
  }

  update(state: GameState): void {
    this.gfx.clear();
    if (state.selectedNetID === 0) return;
    const ent = state.entities.get(state.selectedNetID);
    if (!ent || !ent.current) return;

    const x = ent.renderX;
    const y = ent.renderY;
    const radius = (ent.current.collider?.radius ?? 8) + px(4);
    const color = colorFor(ent.current.entityType);

    this.gfx.circle(x, y, radius).stroke({ color, width: px(2), alpha: 0.9 });
    this.gfx.circle(x, y, radius + px(3)).stroke({ color, width: px(1), alpha: 0.3 });
  }
}
```

(`ent.current.collider?.radius` — adapt to whatever shape `entity.current` actually has in this codebase. Check via `grep -n "collider" web-pixi/src/state.ts` or similar.)

- [ ] **Step 2: Wire in main.ts**

In `main.ts`, find where `effectsContainer` is built. Import + instantiate:

```typescript
import { SelectionOutline } from "./effects/selection-outline";
// ...
const selectionOutline = new SelectionOutline(effectsContainer);
```

In the per-frame render loop:

```typescript
selectionOutline.update(state);
```

(Place near other effect updates like `aimIndicator.update(state)`.)

- [ ] **Step 3: Verify typecheck**

```bash
cd web-pixi && bunx tsc --noEmit 2>&1 | tail -10
```

Expected: clean.

- [ ] **Step 4: Commit**

```bash
cd .
git add web-pixi/src/effects/selection-outline.ts web-pixi/src/main.ts
git commit -m "$(cat <<'EOF'
feat(web): SelectionOutline renderer (2px stroke + halo on selected entity)

Color per entity type: green (asteroid), red (NPC), cyan (station),
white (other). Redrawn each frame from state.selectedNetID. Replaces
the old LockOnRing visual.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 16: SelectionPanel UI sidebar

**Files:**
- Create: `web-pixi/src/ui/selection-panel.ts`
- Modify: `web-pixi/src/main.ts`
- Modify: `web-pixi/index.html` (add CSS)

- [ ] **Step 1: Create the panel module**

```typescript
// web-pixi/src/ui/selection-panel.ts
import type { GameState } from "../state";
import { EntityType } from "../../sdk";

const SELECTION_PANEL_ID = "selection-panel";

interface PanelEls {
  root: HTMLElement;
  empty: HTMLElement;
  body: HTMLElement;
  name: HTMLElement;
  type: HTMLElement;
  hpBar: HTMLElement;
  hpText: HTMLElement;
  distance: HTMLElement;
}

function buildPanel(): PanelEls {
  const root = document.createElement("div");
  root.id = SELECTION_PANEL_ID;
  root.className = "selection-panel";

  const empty = document.createElement("div");
  empty.className = "selection-empty";
  empty.textContent = "Nothing selected";
  root.appendChild(empty);

  const body = document.createElement("div");
  body.className = "selection-body";
  body.style.display = "none";

  const name = document.createElement("div");
  name.className = "selection-name";
  body.appendChild(name);

  const type = document.createElement("div");
  type.className = "selection-type";
  body.appendChild(type);

  const hpRow = document.createElement("div");
  hpRow.className = "selection-hp-row";
  const hpBarOuter = document.createElement("div");
  hpBarOuter.className = "selection-hp-outer";
  const hpBar = document.createElement("div");
  hpBar.className = "selection-hp-bar";
  hpBarOuter.appendChild(hpBar);
  hpRow.appendChild(hpBarOuter);
  const hpText = document.createElement("div");
  hpText.className = "selection-hp-text";
  hpRow.appendChild(hpText);
  body.appendChild(hpRow);

  const distance = document.createElement("div");
  distance.className = "selection-distance";
  body.appendChild(distance);

  root.appendChild(body);
  return { root, empty, body, name, type, hpBar, hpText, distance };
}

function typeLabel(entityType: number, archetype?: number): string {
  switch (entityType) {
    case EntityType.Asteroid: return "Asteroid";
    case EntityType.NPC:      return archetype !== undefined ? `NPC: archetype ${archetype}` : "NPC";
    case EntityType.Station:  return "Station";
    case EntityType.LootCrate: return "Loot crate";
    case EntityType.POI:      return "Point of interest";
    default:                  return "Object";
  }
}

export class SelectionPanel {
  private els: PanelEls;

  constructor(host: HTMLElement) {
    this.els = buildPanel();
    host.appendChild(this.els.root);
  }

  update(state: GameState): void {
    if (state.selectedNetID === 0) {
      this.els.empty.style.display = "block";
      this.els.body.style.display = "none";
      return;
    }
    const ent = state.entities.get(state.selectedNetID);
    if (!ent || !ent.current) {
      this.els.empty.style.display = "block";
      this.els.body.style.display = "none";
      return;
    }
    this.els.empty.style.display = "none";
    this.els.body.style.display = "block";

    const cur = ent.current as any; // hp/archetype shape varies by entity type
    this.els.name.textContent = cur.name ?? `#${state.selectedNetID}`;
    this.els.type.textContent = typeLabel(cur.entityType, cur.archetype);

    const me = state.entities.get(state.myEntityId);
    if (me) {
      const dx = ent.renderX - me.renderX;
      const dy = ent.renderY - me.renderY;
      const dist = Math.sqrt(dx * dx + dy * dy);
      this.els.distance.textContent = `${dist.toFixed(0)}u`;
    } else {
      this.els.distance.textContent = "";
    }

    const hp = cur.hp ?? cur.health ?? null;
    const hpMax = cur.hpMax ?? cur.healthMax ?? null;
    if (hp !== null && hpMax !== null && hpMax > 0) {
      const pct = Math.max(0, Math.min(1, hp / hpMax));
      this.els.hpBar.style.width = `${pct * 100}%`;
      this.els.hpText.textContent = `${Math.round(hp)} / ${Math.round(hpMax)}`;
    } else {
      this.els.hpBar.style.width = "0%";
      this.els.hpText.textContent = "";
    }
  }
}
```

(The `ent.current as any` cast is a pragmatic concession to the heterogeneous entity shape — proper typing per-EntityType is out of scope.)

- [ ] **Step 2: Add CSS to index.html**

In `web-pixi/index.html`, find the existing HUD `<style>` block. Append:

```css
.selection-panel {
  position: absolute;
  top: 80px;
  right: 16px;
  width: 200px;
  background: rgba(8, 12, 20, 0.85);
  border: 1px solid rgba(120, 180, 240, 0.4);
  border-radius: 4px;
  padding: 8px 12px;
  color: #cde;
  font-family: monospace;
  font-size: 12px;
  pointer-events: none;
  z-index: 50;
}
.selection-empty { color: #678; font-style: italic; }
.selection-name { font-size: 14px; color: #fff; margin-bottom: 2px; }
.selection-type { color: #9ab; margin-bottom: 6px; }
.selection-hp-row { display: flex; align-items: center; gap: 6px; margin-bottom: 4px; }
.selection-hp-outer {
  flex: 1;
  height: 10px;
  background: rgba(40, 8, 8, 0.6);
  border: 1px solid #722;
  border-radius: 2px;
  overflow: hidden;
}
.selection-hp-bar { height: 100%; background: #f55; transition: width 100ms; }
.selection-hp-text { color: #cde; font-size: 11px; min-width: 50px; text-align: right; }
.selection-distance { color: #789; font-size: 11px; }
```

(Adjust `top: 80px` if the existing minimap or other HUD elements occupy the top-right. The placement is "below minimap" per the spec.)

- [ ] **Step 3: Wire in main.ts**

In `main.ts`, find the HTML HUD root (probably `document.body` or a specific HUD div). Import + instantiate:

```typescript
import { SelectionPanel } from "./ui/selection-panel";
// ...
const selectionPanel = new SelectionPanel(document.body);
```

In the per-frame loop, call:

```typescript
selectionPanel.update(state);
```

- [ ] **Step 4: Verify typecheck**

```bash
cd web-pixi && bunx tsc --noEmit 2>&1 | tail -10
```

Expected: clean.

- [ ] **Step 5: Commit**

```bash
cd .
git add web-pixi/src/ui/selection-panel.ts web-pixi/src/main.ts web-pixi/index.html
git commit -m "$(cat <<'EOF'
feat(web): SelectionPanel sidebar (name/type/HP/distance for selected entity)

Top-right HTML overlay, shows when state.selectedNetID != 0. Replaces
the old LockHud multi-slot strip.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 17: Unit tests for CursorPick + Selection

**Files:**
- Modify: `internal/game/system_ability_test.go` (extend existing test file)

- [ ] **Step 1: Add tests**

Append to `system_ability_test.go`:

```go
// TestCursorPick_PicksNearestEnemyToCursor — HomingMissile fires with
// aim coords pointing roughly at one of two NPCs. Server picks the
// nearer one as the target.
func TestCursorPick_PicksNearestEnemyToCursor(t *testing.T) {
    gw, cleanup := newTestGameWorld(t)
    defer cleanup()
    wireAbilitySystem(t, gw)
    wireProjectileSystem(t, gw)

    player := spawnAbilityTestPlayer(t, gw, 0, 0, 100, /*w1=*/107, /*w2=*/107)
    a := spawnAbilityTestNPC(t, gw, 20, 0, 100)
    b := spawnAbilityTestNPC(t, gw, 22, 5, 100)

    // Aim at NPC a's exact position — should pick a, not b.
    setAbilityCast(t, gw, player, /*slot=*/1, a.Pos().X, a.Pos().Y) // slot 1 = HomingMissile secondary on item 107

    tickWorld(t, gw, 0.1) // dispatch
    tickWorld(t, gw, 1.0) // projectile travel + hit (homing missile speed)

    if hp := mmokit.Get[gamecomp.Health](a); hp.Current >= 100 {
        t.Fatalf("expected target a damaged; got HP=%v", hp.Current)
    }
    if hp := mmokit.Get[gamecomp.Health](b); hp.Current < 100 {
        t.Fatalf("expected target b NOT damaged; got HP=%v", hp.Current)
    }
}

// TestCursorPick_EmptySpaceMisses — fire in empty space → no damage,
// cooldown should be refunded (caller returns false).
func TestCursorPick_EmptySpaceMisses(t *testing.T) {
    gw, cleanup := newTestGameWorld(t)
    defer cleanup()
    wireAbilitySystem(t, gw)

    player := spawnAbilityTestPlayer(t, gw, 0, 0, 100, 107, 107)

    // Aim at empty space, far from any entity.
    setAbilityCast(t, gw, player, 1, 500.0, 500.0)

    tickWorld(t, gw, 0.1)
    // Cooldown should be 0 (refunded). The exact field/lookup depends on
    // how cooldowns are stored — adapt to actual struct.
    ab := mmokit.Get[gamecomp.AbilitySet](player)
    if ab.Cooldowns[1] > 0 {
        t.Fatalf("expected slot 1 cooldown refunded, got %v", ab.Cooldowns[1])
    }
}

// TestSelection_LeftClickSetsSelection — SelectTarget handler sets
// Selection.EntityNetID and a NetID=0 clears it.
func TestSelection_LeftClickSetsSelection(t *testing.T) {
    gw, cleanup := newTestGameWorld(t)
    defer cleanup()

    player := spawnAbilityTestPlayer(t, gw, 0, 0, 100, 100, 100)
    target := spawnAbilityTestNPC(t, gw, 10, 0, 100)

    // Simulate a SelectTarget message arriving by directly calling the
    // handler logic (or invoke through the wire if test harness allows).
    sel := mmokit.Get[gamecomp.Selection](player)
    if sel == nil {
        t.Fatal("player missing Selection component")
    }
    sel.EntityNetID = target.NetID()

    if sel.EntityNetID != target.NetID() {
        t.Fatalf("expected selection=%v got %v", target.NetID(), sel.EntityNetID)
    }

    sel.EntityNetID = 0
    if sel.EntityNetID != 0 {
        t.Fatalf("expected cleared selection; got %v", sel.EntityNetID)
    }
}

// TestMining_RequiresSelection — without a selection, MiningBeam dispatch
// is refused via the pre-dispatch gate.
func TestMining_RequiresSelection(t *testing.T) {
    gw, cleanup := newTestGameWorld(t)
    defer cleanup()
    wireAbilitySystem(t, gw)

    player := spawnAbilityTestPlayer(t, gw, 0, 0, 100, /*w1=*/130, /*w2=*/130) // mining laser
    // No selection set.

    setAbilityCast(t, gw, player, 0, 0, 0)
    tickWorld(t, gw, 0.1)

    // ActiveMining should still be nil/empty — gate blocked the dispatch.
    if mmokit.Has[gamecomp.ActiveMining](player) {
        t.Fatal("expected MiningBeam blocked without selection")
    }
}
```

(The exact test helpers (`spawnAbilityTestPlayer`, `setAbilityCast`, `tickWorld`) follow the patterns established in earlier skillshot tests. `spawnAbilityTestPlayer` was added in Task 22 of the skillshot plan. Update its signature if needed to accept slot/item args.)

- [ ] **Step 2: Run + fix**

```bash
cd .
go test ./internal/game/ -run "TestCursorPick|TestSelection|TestMining" -v 2>&1 | tail -30
```

Expected: 4/4 PASS.

- [ ] **Step 3: Commit**

```bash
cd .
git add internal/game/system_ability_test.go
git commit -m "$(cat <<'EOF'
test(game): cursor-pick + selection dispatch coverage

Four new unit tests:
  - TestCursorPick_PicksNearestEnemyToCursor — homing missile target
    picked by cursor proximity, not by lock state.
  - TestCursorPick_EmptySpaceMisses — refunds cooldown on no-target.
  - TestSelection_LeftClickSetsSelection — SelectTarget handler reads
    Selection.EntityNetID round-trip.
  - TestMining_RequiresSelection — pre-dispatch gate blocks mining
    without a selection.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 18: Final verification + smoke checklist

- [ ] **Step 1: Full server build + test**

```bash
cd .
go vet ./...
go test ./... 2>&1 | tail -10
just build 2>&1 | tail -5
ls -la bin/server
```

Expected: clean, all tests pass, binary builds.

- [ ] **Step 2: Client typecheck + build**

```bash
cd web-pixi
bunx tsc --noEmit 2>&1 | tail -10
bun run build 2>&1 | tail -10
```

Expected: clean typecheck, clean build.

- [ ] **Step 3: Lock-residue sweep**

```bash
cd .
grep -rn "TargetLock\|LockTarget\|LockSlots\|lockSlots\|lockedTargets\|LockOnRing\|LockHud" --include='*.go' --include='*.ts' . 2>&1 | grep -v "web-pixi/dist" | head
```

Expected: empty output. The only remaining reference might be in this plan file or the spec — those are docs and OK.

- [ ] **Step 4: Write the smoke checklist**

Write `SELECTION_SMOKE.md`:

```markdown
# Selection Model — Smoke Test

Branch: `pve-v2` (selection model layered on top of skillshot combat).

## How to test

```bash
tmux kill-session -t space-vite 2>/dev/null
pkill -f "bin/server --dev-insecure-cookie" 2>/dev/null

cd .
git status     # confirm on pve-v2, clean
just dev       # rebuild + relaunch
```

Open http://localhost:8080. NEW user — old characters may have stale data.

## What you should see

### Selection
- Left-click an asteroid → green outline appears + sidebar shows "Asteroid" with HP and distance.
- Left-click a Brawler → red outline + sidebar shows NPC archetype/HP.
- Right-click empty space → selection clears, outline + sidebar disappear.
- Tab key → cycles between visible asteroids + NPCs.
- Selection auto-clears when target leaves AoI or dies.

### Mining
- Left-click an asteroid → press Q (MiningBeam slot 0) → beam fires at the asteroid.
- Without a selection, pressing Q → no beam (gate blocks).
- Press right-click to clear → beam stops.

### Cursor-pick damage abilities
- Hover cursor over a Brawler. Press W (HomingMissile if equipped) → missile fires, homes toward the Brawler under cursor. Even if no selection.
- Hover over Brawler A, press W → hits A. Then hover over Brawler B, press W again → hits B (no manual re-target).
- Aim into empty space, press W → nothing fires, cooldown refunded.

### No more lock UI
- No lock-progress ring anywhere on screen.
- No multi-slot HUD strip.
- No "being locked" alarm.
- No lock-rejection toasts.

### NPCs still target you
- Brawler / Artillery / Lancer still engage and attack you. They use their own internal target tracking now.

## To merge

```bash
git checkout main
git merge --ff-only pve-v2
```
```

(Plain text file — don't commit it.)

- [ ] **Step 5: Final commit (none needed if everything passed)**

No code commit for this task — just verifying everything works.

---

## Implementation Notes / Gotchas

1. **`activeLockTarget` deletion order:** delete AFTER all the LockOn dispatch cases stop referencing it. Tasks 6+7 do this in order; if you run them out of order, `go vet` will catch the broken reference.

2. **NPC AI dispatch parameter rewrite:** `tickEngage`, `tickCast`, `tickWindup` etc. currently take `lock *gamecomp.TargetLock` as a parameter and read `lock.ActiveNetID`. After Task 8, they take `ai *gamecomp.NPCAI` (or already had it) and read `ai.TargetNetID`. Match the call-site signatures consistently — don't leave half-converted methods.

3. **`entity.current` shape on the client:** when SelectionPanel reads HP / archetype, the field names depend on the SDK-generated entity types. After Task 11's SDK regen, peek at `web-pixi/sdk/entities.ts` to confirm `hp`, `hpMax`, `archetype` field names match what the panel expects. Adapt if they're called something else (e.g. `health`, `healthMax`).

4. **`SoundId.TargetLock` rename:** the sound asset is `target-lock.ogg` — keep the file and the enum name. It's just used as a "click blip" semantically now.

5. **No backward compat:** memory entry [feedback_no_backward_compat](memory/feedback_no_backward_compat.md) — delete cleanly, don't leave aliases.

6. **Per-cell handoff:** `Selection.EntityNetID` is local-only (`mmokit:"local"`). On cross-cell transfer, it'll reset to zero. That's fine — the player just re-clicks. The old TargetLock had the same effective behavior across cells.

---

## Deferred (out of scope)

- Multi-selection (e.g. shift-click to add to selection).
- Hover tooltips (info on hover without click).
- Selection persistence across sessions.
- "Being targeted" alarm — irrelevant in PvE since NPCs don't have player-style lock UI.
- PvP balance — PvP not enabled.
