# Selection Model — Design

**Status:** approved
**Replaces:** the EVE-style multi-slot `TargetLock` system

## Why

Target-locking adds friction without payoff. PvE damage abilities will all be projectile/hitscan with cursor aiming (LoL/Albion feel — see `2026-05-14-skillshot-combat-design.md`). The lock system survives only to give the player an explicit "selection" for two purposes:

1. Mining: mining laser and ExtractPulse need a definite target (you don't "skill-shot" an asteroid).
2. Info display: clicking an entity shows its name, HP, type, distance.

A full lock pipeline — lock progress timer, slot management, lock-broken events, range gates, being-locked alarms — is overkill for those two needs. Replace with a single-target "Selection" component and a thin UI surface.

## Goals

- One selection per player. Left-click an entity to select; right-click to clear.
- Mining laser + ExtractPulse target whatever is selected.
- Three damage abilities that today require a lock — HomingMissile, PlasmaTorpedo, IonBurn — become cursor-pick: at fire time the server picks the nearest enemy within ~5u of the cursor, gated by ability range.
- Visible state: a subtle outline on the selected entity + an info panel listing its details. Nothing else.
- Solo-dev clean cut. No backward compat. Old persisted state from `TargetLock` is irrelevant.

## Non-goals

- Multi-target abilities (none planned).
- "Being targeted by an enemy" alarm. NPCs aim cursor-style same as players; no alarm to wire.
- PvP lock balance. PvP isn't enabled.

## Server architecture

### New component

```go
package component

type Selection struct {
    EntityNetID uint32 `mmokit:"local"`  // 0 = nothing selected
}
```

Local-only. Players carry it; cross-cell transfer carries it through `FinishTransferSpawn` (matches existing pattern).

### Removed surface

Deleted outright (no aliases):

- `gamecomp.TargetLock` + `TargetLockSlot` + all multi-slot fields
- `internal/game/system_targetlock.go`
- `internal/game/system_targetlock_multi_test.go`
- Wire messages: `LockTarget`, `SetActiveTarget`, `UnlockTarget`
- Server events: `LockProgress`, `LockComplete`, `LockReject`, `LockBroken`, anything `Lock*`
- `item.TargetingLockOn` constant value (mode = 1 becomes unused but the enum keeps it for now to avoid shifting Go const values — flag for cleanup later)
- The pre-dispatch lock gate in `AbilitySystem.Update` referencing `TargetLock`

### New wire

```go
// SelectTarget — left-click on an entity sets it as the player's
// selection; right-click sends NetID=0 to clear.
type SelectTarget struct {
    Sequence uint32
    NetID    uint32
}
```

Handler updates `player.Selection.EntityNetID`. Validation: `NetID` must reference an `Alive()` entity within AoI; out-of-range / dead → drop silently (no error event). Zero always clears.

### Ability dispatch

Add new TargetingMode:

```go
const TargetingCursorPick TargetingMode = 5
```

Reclassify in `item.go`:

| Ability | Old Mode | New Mode |
|---|---|---|
| HomingMissile | LockOn | CursorPick |
| PlasmaTorpedo | LockOn | CursorPick |
| IonBurn | LockOn | CursorPick |
| MiningBeam | LockOn | LockOn (unchanged label — but Mining uses Selection, see below) |
| ExtractPulse | LockOn | LockOn (same) |

The two mining abilities **keep** `TargetingLockOn` for now, but the dispatcher reads `Selection.EntityNetID` instead of `TargetLock.ActiveNetID`. This avoids inventing a second mode like `TargetingSelection`. Net effect: `TargetingLockOn` becomes "selection-required."

Add new dispatcher:

```go
func (s *AbilitySystem) dispatchCursorPick(action abilityAction, casterE mmokit.Entity) bool
```

Logic:
1. Read `action.aimX/aimY`.
2. Query spatial grid in radius `min(params.Range, 100)` around aim point (cap to bound the search).
3. Filter candidates: alive, `NetID != caster.NetID()`, in faction-enemy set (NPCAI for player caster), within `params.Range` of caster.
4. Pick the one with smallest distance to the aim point. Tie-break by netID.
5. If found: run the per-AbilityType effect identical to today's LockOn branch (HomingMissile → `fireProjectile` with `TargetNetID = picked.NetID()`; PlasmaTorpedo → projectile with bonus; IonBurn → hitscan damage + `StatusEffect.IonDoT` apply).
6. If not found: log + `return false` so the caller refunds the cooldown.

### NPC AI

NPCs already track aggro internally — `NPCAI.LastDamageByNetID` and per-tick proximity search drive engagement. The lock indirection (NPC asking the lock system "who am I targeting?") is just plumbing. Replace with:

- `NPCAI.TargetNetID uint32` — already conceptually there as the "engage target"; if not stored, add it. Updated each tick by the existing closest-enemy-in-aggro-range logic.
- NPC dispatch (Brawler hitscan auto-attack, Artillery cast, Lancer charge, Brawler special telegraphed line) reads `ai.TargetNetID` directly — no TargetLock lookup.

Concrete: the existing `tickEngage` body already computes `dx, dy, dist` from caster to a target. Confirm the target source and re-anchor to `ai.TargetNetID`.

### MiningBeam + ExtractPulse

Dispatcher reads `Selection.EntityNetID` instead of `TargetLock.ActiveNetID`. Pre-dispatch gate (for slots Q/W/E/R, currently checks `params.Mode == TargetingLockOn`):

```go
if params.Mode == item.TargetingLockOn {
    sel := mmokit.Get[gamecomp.Selection](player)
    if sel == nil || sel.EntityNetID == 0 { continue }
    target := mmokit.EntityByNetID(stage, sel.EntityNetID)
    if !target.Alive() { continue }
    if params.Range > 0 && !s.inRange(player.Handle(), target.Handle(), params.Range) { continue }
}
```

### What stays

- `item.AbilityTypeIonBurn`'s DoT mechanics (`StatusFortified` style apply) — only the targeting changes.
- HomingMissile projectile homing — projectile still seeks `TargetNetID`. CursorPick just sets `TargetNetID` from cursor pick instead of a locked target.
- Damage broadcast + auto-broadcast pipeline — no change.

## Client architecture

### Deleted

- `web-pixi/src/effects/lock-on-ring.ts`
- `web-pixi/src/effects/being-locked-ring.ts`
- `web-pixi/src/effects/target-highlight.ts`
- `web-pixi/src/ui/lock-hud.ts`
- `web-pixi/src/ui/lock-overlay.ts`
- `state.lockSlots`, `state.lockedTargets` (and any related fields)
- `LockTarget`, `SetActiveTarget`, `UnlockTarget` input message imports / sends
- All lock event subscribers (`onLockProgress`, etc.) in `network.ts`

### New

- `web-pixi/src/effects/selection-outline.ts` — a Pixi `Graphics` that draws a 2px outline + faint halo on the entity whose `netID === state.selectedNetID`. Color picked by entity type: 0x44ff88 for asteroids (mineable), 0xff5544 for hostile NPCs, 0xaaaacc for stations/loot/neutral. Renders in the world layer below entities.
- `web-pixi/src/ui/selection-panel.ts` — an HTML sidebar block (top-right, above existing minimap or below ship status — pick wherever fits least disruptively). Shows: icon, name (NPC archetype or asteroid type), HP bar, distance. Empty when `selectedNetID === 0`.
- `state.selectedNetID: number` — single field, default 0.

### Input

- Left-click on a clickable entity → `state.client.send(new SelectTarget({ sequence, netID }))`. Optimistically set `state.selectedNetID = netID` for instant UI feedback.
- Right-click → if `state.selectedNetID !== 0`, send `SelectTarget(0)` and clear local state. Otherwise the click continues to other right-click behaviors (move-cancel, aim-cancel).
- Tab → cycles `state.selectedNetID` across visible enemies/asteroids; sends the new selection. Same algorithm as today (visibility-ordered list) but the target field changes.

### Aim indicator

The existing `AimIndicator` (line + channel + ground previews) stays. Add one extension: for `TargetingCursorPick` mode, draw a small hover-highlight ring on whichever enemy is nearest the cursor (computed client-side from `state.entities` + `state.cursorWorldX/Y`). Gives the player visual feedback about "this is who you'd hit." No server roundtrip — purely local.

### Sound

`SoundId.TargetLock` is repurposed as the "select-click blip." Same file, played on every successful `SelectTarget` send. Tab-cycle also fires it.

## Wire summary

Removed: `LockTarget`, `SetActiveTarget`, `UnlockTarget`, every `Lock*` server event.
Added: `SelectTarget`.
Net effect: simpler wire surface. SDK regen.

## Open question — info panel placement

The HUD already has: ability bar (bottom-center), ship status (left), minimap (top-right). The selection panel slots cleanly under the minimap. If that's wrong, easy to move — the panel is HTML/CSS, not Pixi.

## Test plan

- `TestCursorPick_PicksNearestEnemyToCursor` — fire HomingMissile with cursor near NPC A, near NPC B → correct target picked each time. Cursor in empty space → no fire, cooldown refunded.
- `TestSelection_LeftClickSetsSelection` — server-side: SelectTarget handler populates Selection. Right-click clears.
- `TestMining_RequiresSelection` — pressing mining without selection: refused via dispatch gate.
- Existing tests that reference TargetLock: rewrite to use Selection where applicable; delete the ones validating multi-lock progress.

## Migration

Solo-dev, dev DB only. Server restart after deploy. No persisted lock state to migrate. Client reload picks up new wire format from regenerated SDK.

## Risks

- **NPC AI re-targeting:** the existing AI uses TargetLock for closest-enemy bookkeeping. If we move that into `NPCAI.TargetNetID`, every dispatch site that today queries `lock.ActiveNetID` becomes `ai.TargetNetID`. Touch points to audit: `tickEngage`, `tickCast`, `tickWindup`, `resolveBrawlerSpecial`, `resolveLancerCharge`. About 6-10 references.
- **Bot integration:** `internal/bot/` uses `TargetLock` for headless smoke testing. Bots need to be updated to use `SelectTarget` for mining-flavored scenarios and direct cursor coords for combat scenarios.
- **Hidden lock-dependent code:** there may be lock references in places not yet audited (e.g., status effect targeting, replication). Plan should include a final `grep -rln 'TargetLock\|LockTarget'` sweep + manual review.

## Out of scope

- Multi-selection.
- Hover tooltips (info on hover, no click required).
- Selection persistence across sessions.
- PvP balance changes.
