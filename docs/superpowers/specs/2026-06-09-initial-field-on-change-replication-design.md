# On-Change Replication for `net:"initial"` Fields

**Date:** 2026-06-09
**Status:** Approved — ready for implementation plan

## Problem

Editing a replicated field tagged `net:"initial"` (e.g. an entity's `Name` via the
admin attribute editor) mutates the live server component correctly but never reaches
already-watching clients. The change is invisible in-game until the entity leaves and
re-enters the viewer's area of interest.

### Root cause

Fields tagged `net:"initial"` are written into a separate variable-length "initial data"
blob that is attached to an entity's frame **only when it first becomes visible to a
viewer**:

- [pkg/system/replication.go:719-734](../../../pkg/system/replication.go#L719-L734) —
  `initialData` is populated only in the `if isNew || bl.Acked == nil` branch (the
  full-frame / first-visibility path).
- After that, the entity flows through the **delta path**, which carries only the fixed
  quantized snapshot fields. `initial` fields are *not* part of the snapshot layout
  (they live in `rb.initials`, separate from `rb.fields` —
  [pkg/system/auto_replicator.go:593-608](../../../pkg/system/auto_replicator.go#L593-L608)),
  so they never ride a delta.

A subtle consequence: the per-entity content hash **already includes** initial fields
([auto_replicator.go:678-680](../../../pkg/system/auto_replicator.go#L678-L680)). So a
name change *does* bust the hash and run the delta encoder — but the snapshot fields are
unchanged, the encoder returns `nil`, and the frame is dropped
([replication.go:765-768](../../../pkg/system/replication.go#L765-L768)). The change is
detected and then thrown away.

### Confirmed: server state updates correctly

The attribute editor is backed by the `entity.modify` cmdsys verb
([pkg/universe/builtins_entity.go:540](../../../pkg/universe/builtins_entity.go#L540)),
which calls `SetFieldByPath` on a pointer aliasing live ECS component memory
([entity_kind.go:115](../../../pkg/universe/entity_kind.go#L115):
`reflect.NewAt(t, u.Get(entity, id)).Interface()`). The mutation persists server-side.
The defect is purely in replication propagation.

## Design

### Semantics

`net:"initial"` changes meaning from **"send once on visibility enter"** to **"send on
visibility enter AND re-send whenever the field changes."**

- Tag name is unchanged (no churn across the ~9 existing tagged component fields).
- Single unified mechanism — no second tag, no strict-once variant. No field in the
  codebase needs strict-once.
- Immutable fields (`Kind`, `Variant`, `Archetype`, `Type`, `Tier`, `EntranceMask`,
  static `Name`s) never change, so the on-change branch never fires for them → zero
  added wire cost.

### Client: no change required

The generated SDK decoder already supports this. Every full-entry passes its
`initialData` to the per-kind decoder, which decodes the field when present and
otherwise carries the prior value forward:

- [web-pixi/sdk/delta-decoder.ts:311](../../../web-pixi/sdk/delta-decoder.ts#L311) —
  full entries pass `entry.initialData` to `decodeEntity`.
- [web-pixi/sdk/delta-decoder.ts:49](../../../web-pixi/sdk/delta-decoder.ts#L49) —
  `name = initial ? decode(initial) : (existing?.name ?? "")`.

So re-emitting an entity as a full-entry-with-initialData on change updates the field
transparently. **No wire-format change, no SDK regen.**

### Server mechanism

All changes are in the replication system and the auto-replicator / baseline store.

1. **Per-replicator initial hash.** Add `InitialHash(viewer *ViewerInfo, entry spatial.Entry) uint64`
   to the `EntityReplicator` interface
   ([pkg/system/replication.go:105-107](../../../pkg/system/replication.go#L105) is where
   `InitialData` already lives). `autoReplicator` computes it by hashing only the
   `initial` fields — the same `rb.initials` set it already feeds into the combined hash.
   A replicator with no initial fields returns a sentinel/`0` and is treated as "no
   initial data" (consistent with `hasInitial() == false`).
   - Audit every `EntityReplicator` implementer (border-replica / proxy replicators,
     etc.) and provide the method. Search for implementers during planning; adding an
     interface method is a breaking change in Go and must be handled for all of them.

2. **Per-viewer last-sent initial hash.** Extend `EntityBaseline`
   ([pkg/replication/baseline.go:29](../../../pkg/replication/baseline.go#L29)) with
   `InitialHash uint64` and a `HasInitialHash bool` (or equivalent), recorded each time
   initial data is sent to that viewer for that entity.

3. **Send decision** (replacing the branch at
   [replication.go:719-789](../../../pkg/system/replication.go#L719-L789)), for an
   entity that passed the unchanged/dormancy skip:

   ```
   curInitHash := rep.InitialHash(viewer, entry)            // only if rep.hasInitial()
   initialChanged := rep.hasInitial() && (!bl.HasInitialHash || bl.InitialHash != curInitHash)

   if isNew || bl.Acked == nil:
       -> full frame + initialData            // unchanged from today; record curInitHash
   else if initialChanged:
       -> full frame + initialData            // NEW: re-send initial; covers any snapshot change too; record curInitHash
   else if isKeyframe:
       -> full frame, no initialData          // unchanged from today
   else:
       -> delta                               // unchanged from today
   ```

   - The existing combined hash (which already includes initial fields) keeps the entity
     out of the dormancy/unchanged skip when only an initial field changes — that path is
     unchanged. The separate initial hash is used *solely* to decide whether to attach
     `initialData`.
   - When the full+initial frame is emitted for `initialChanged`, record
     `bl.InitialHash = curInitHash` / `bl.HasInitialHash = true` alongside the existing
     baseline bookkeeping.

### Known limitation (v1)

In unreliable ack mode, a dropped change-frame leaves the field stale on that client
until the next change or AoI re-enter, because the stored initial hash advances on send,
not on ack. This is the **same fragility the existing enter-frame already has** (initial
data rides the best-effort full frame). `initial` fields change rarely (admin edits);
acceptable for v1. If reliable initial-field updates are wanted later, route
initial-change frames through the reliable ack path — out of scope here.

## Testing

Unit tests in `pkg/system` (alongside the existing replication tests):

1. **Change propagates.** Spawn an entity with an `initial` field and a viewer. Tick once
   (viewer enters → receives full frame with initial data). Mutate the `initial` field on
   the live component. Tick again. Assert the viewer's frame contains a **full-entry whose
   `InitialData` decodes to the new value** (not a delta, not the old value).

2. **No wasteful re-send.** With the same setup, mutate only a *snapshot* field (e.g.
   position) and tick. Assert the entity flows as a **delta with no initialData**, and the
   stored initial hash is unchanged.

3. **Immutable field is silent.** An entity whose `initial` fields never change produces
   only deltas after the enter frame — no repeated full+initial frames.

## Out of scope

- Reliable delivery of initial-field changes (noted as a v1 limitation).
- Any wire-format or SDK change.
- New tags or strict-once semantics.
