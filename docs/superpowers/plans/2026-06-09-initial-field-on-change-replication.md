# On-Change Replication for `net:"initial"` Fields — Implementation Plan

> **For agentic workers:** Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `net:"initial"` fields re-replicate to already-watching clients when they change (e.g. renaming an entity via the admin attribute editor), not just on first visibility.

**Architecture:** Server-only change. Add an initial-fields-only hash to the replicator, track the last-sent initial hash per viewer in the baseline store, and when it differs re-emit the entity as a full frame carrying fresh `initialData` (instead of an empty/dropped delta). The existing wire format and generated SDK already decode `initialData` on any full entry, so no client change is needed.

**Tech Stack:** Go, ark ECS, the `pkg/system` replication system, `pkg/replication` baseline store.

**Spec:** [docs/superpowers/specs/2026-06-09-initial-field-on-change-replication-design.md](../specs/2026-06-09-initial-field-on-change-replication-design.md)

---

## Key facts (verified)

- Only `autoReplicator` implements `EntityReplicator` in production. Test mocks `testReplicator` ([pkg/system/replication_test.go:29](../../../pkg/system/replication_test.go#L29)) and `countingReplicator` ([:644](../../../pkg/system/replication_test.go#L644)) also implement the interface and must gain the new methods or the package won't compile.
- `ComponentBinding` has 7 implementers: `entryPositionBinding`, `viewerRelativePosBinding`, `qVelocityBinding`, `qAngleBinding`, `qSizeBinding` (all `hasInitial() == false`), `bindingGroup` (forwards to children), `reflectBinding` (the only one with real initial fields).
- Combined `Hash` already includes initial fields ([auto_replicator.go:678-680](../../../pkg/system/auto_replicator.go#L678-L680)) — leave that as-is; it keeps changed entities out of the unchanged-skip. The new initial-only hash is used solely to decide whether to attach `initialData`.
- The replication send decision is [pkg/system/replication.go:671-789](../../../pkg/system/replication.go#L671-L789). `ringDepth` is in scope from line 548. `GetOrCreateBaseline` is currently called at line 716 and must move earlier (before the dormancy skip at line 673).
- **Dormancy is the trap:** static entities (stations/POIs — exactly what gets renamed) accumulate `UnchangedTicks` and are skipped at line 673 *before* any hashing. The initial-change check must run before that skip and bypass it.

---

## Task 1: Add initial-fields-only hashing to the replicator

**Files:**
- Modify: `pkg/system/auto_replicator.go` (interface `ComponentBinding`, 7 implementers, `autoReplicator`)
- Modify: `pkg/system/replication.go` (interface `EntityReplicator`)
- Modify: `pkg/system/replication_test.go` (two mocks)
- Test: `pkg/system/auto_replicator_test.go`

- [ ] **Step 1: Write the failing test**

Add to `pkg/system/auto_replicator_test.go` (reuse whatever entity/world helper `TestAutoReplicatorInitialData` at line 201 already uses — mirror its setup for a kind with a `net:"initial"` string field named `Name`):

```go
func TestAutoReplicatorInitialHash_ChangesWithField(t *testing.T) {
	// Build the same auto-replicator + entity used by TestAutoReplicatorInitialData.
	rep, world, entity, entry, viewer := setupInitialDataReplicator(t) // existing helper or inline like TestAutoReplicatorInitialData

	if !rep.HasInitial() {
		t.Fatal("replicator with a net:\"initial\" field must report HasInitial() == true")
	}

	var h1 Hasher
	h1.Reset()
	rep.InitialHash(&h1, viewer, entry)
	before := h1.Sum()

	// Mutate the initial field.
	setEntityName(t, world, entity, "renamed") // mirror however the helper sets the Name component

	var h2 Hasher
	h2.Reset()
	rep.InitialHash(&h2, viewer, entry)
	after := h2.Sum()

	if before == after {
		t.Fatalf("InitialHash must change when an initial field changes: before=%d after=%d", before, after)
	}
}
```

If `TestAutoReplicatorInitialData` builds its replicator inline rather than via a helper, copy that inline construction into this test instead of inventing `setupInitialDataReplicator`/`setEntityName`. Match the existing test's exact component type and field-set call.

- [ ] **Step 2: Run the test to verify it fails to compile**

Run: `go test ./pkg/system/ -run TestAutoReplicatorInitialHash_ChangesWithField`
Expected: compile error — `rep.HasInitial` / `rep.InitialHash` undefined.

- [ ] **Step 3: Add `initialHash` to the `ComponentBinding` interface**

In `pkg/system/auto_replicator.go`, in the `ComponentBinding` interface (currently lines 23-30), add after `hasInitial()`:

```go
	// initialHash writes the entity's initial-only fields into the hasher.
	// No-op for bindings without initial fields. Used to detect when initial
	// data changed so it can be re-sent to already-visible viewers.
	initialHash(entity ecs.Entity, h *Hasher, viewer *ViewerInfo, entry spatial.Entry)
```

- [ ] **Step 4: Implement `initialHash` on all 7 bindings**

For each built-in no-op binding, add a method next to its existing `hasInitial()`:

```go
func (entryPositionBinding) initialHash(ecs.Entity, *Hasher, *ViewerInfo, spatial.Entry) {}
```

```go
func (b *viewerRelativePosBinding) initialHash(ecs.Entity, *Hasher, *ViewerInfo, spatial.Entry) {}
```

```go
func (b *qVelocityBinding) initialHash(ecs.Entity, *Hasher, *ViewerInfo, spatial.Entry) {}
```

```go
func (b *qAngleBinding) initialHash(ecs.Entity, *Hasher, *ViewerInfo, spatial.Entry) {}
```

```go
func (b *qSizeBinding) initialHash(ecs.Entity, *Hasher, *ViewerInfo, spatial.Entry) {}
```

(Match each binding's existing receiver style — value vs pointer — as written for its `hasInitial()`.)

For `bindingGroup`, add next to its `hasInitial()` (line 439):

```go
func (g *bindingGroup) initialHash(entity ecs.Entity, h *Hasher, viewer *ViewerInfo, entry spatial.Entry) {
	for _, b := range g.bindings {
		b.initialHash(entity, h, viewer, entry)
	}
}
```

For `reflectBinding`, add next to its `hasInitial()` (line 712). Lenient on missing components (the combined `Hash`/`Snapshot` paths already enforce required presence and will panic downstream if truly missing):

```go
func (rb *reflectBinding) initialHash(entity ecs.Entity, h *Hasher, _ *ViewerInfo, _ spatial.Entry) {
	if len(rb.initials) == 0 {
		return
	}
	if !rb.reader.has(entity) {
		if rb.optional {
			for _, tf := range rb.initials {
				tf.hashFn(zeroForEncoding(tf.meta.encoding), h)
			}
		}
		return
	}
	v := rb.reader.readValue(entity)
	for _, tf := range rb.initials {
		tf.hashFn(v.Field(tf.index).Interface(), h)
	}
}
```

- [ ] **Step 5: Add `HasInitial` + `InitialHash` to `autoReplicator`**

In `pkg/system/auto_replicator.go`, next to `InitialData` (line 149):

```go
func (a *autoReplicator) HasInitial() bool { return a.anyInitial }

func (a *autoReplicator) InitialHash(h *Hasher, viewer *ViewerInfo, entry spatial.Entry) {
	for _, b := range a.bindings {
		b.initialHash(entry.Entity, h, viewer, entry)
	}
}
```

- [ ] **Step 6: Add the two methods to the `EntityReplicator` interface**

In `pkg/system/replication.go`, in the `EntityReplicator` interface (line 88), add after the `InitialData` method (line 107):

```go
	// HasInitial reports whether this replicator has any initial-only fields.
	HasInitial() bool

	// InitialHash writes only the initial-only fields into the hasher, so the
	// system can detect when initial data changed and re-send it to already
	// visible viewers. Only called when HasInitial() is true.
	InitialHash(h *Hasher, viewer *ViewerInfo, entry spatial.Entry)
```

- [ ] **Step 7: Update the two test mocks**

In `pkg/system/replication_test.go`, add to `testReplicator` (near line 29) and `countingReplicator` (near line 644):

```go
func (r *testReplicator) HasInitial() bool                                        { return false }
func (r *testReplicator) InitialHash(h *Hasher, viewer *ViewerInfo, entry spatial.Entry) {}
```

```go
func (r *countingReplicator) HasInitial() bool                                        { return false }
func (r *countingReplicator) InitialHash(h *Hasher, viewer *ViewerInfo, entry spatial.Entry) {}
```

- [ ] **Step 8: Run the test to verify it passes**

Run: `go test ./pkg/system/ -run TestAutoReplicatorInitialHash_ChangesWithField -v`
Expected: PASS.

- [ ] **Step 9: Vet + full package tests still green**

Run: `go vet ./pkg/system/... && go test ./pkg/system/`
Expected: no vet errors; all existing tests pass.

- [ ] **Step 10: Commit**

```bash
git add pkg/system/auto_replicator.go pkg/system/replication.go pkg/system/replication_test.go pkg/system/auto_replicator_test.go
git commit -m "feat(replication): add initial-fields-only hash to EntityReplicator

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Track per-viewer last-sent initial hash in the baseline

**Files:**
- Modify: `pkg/replication/baseline.go:29` (`EntityBaseline` struct)

- [ ] **Step 1: Add fields to `EntityBaseline`**

In `pkg/replication/baseline.go`, add to the `EntityBaseline` struct (after the `Ring*` fields):

```go
	// InitialHash is the hash of the initial-only fields last sent to this
	// viewer for this entity. HasInitialHash is false until the first send.
	// Used to detect when initial data changed and must be re-sent.
	InitialHash    uint64
	HasInitialHash bool
```

- [ ] **Step 2: Verify it compiles**

Run: `go vet ./pkg/replication/...`
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add pkg/replication/baseline.go
git commit -m "feat(replication): track last-sent initial hash per viewer baseline

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Wire on-change re-send into the replication loop

**Files:**
- Modify: `pkg/system/replication.go` (ReplicationSystem struct + the per-entity block at lines 671-789)

- [ ] **Step 1: Add a dedicated hasher field to `ReplicationSystem`**

Find the struct field declaring `hasher Hasher` (used at lines 679-681) and add alongside it:

```go
	initHasher Hasher // initial-fields-only hash, kept separate from `hasher`
```

- [ ] **Step 2: Move baseline lookup earlier and compute `initialChanged`**

In `pkg/system/replication.go`, replace the block starting at line 671 (`// Dormancy: skip...`) down to line 691 (`conn.store.SetLastHash(netID, hash)`) with the following. This (a) fetches the baseline before the dormancy skip, (b) computes the initial-only hash, (c) makes dormancy and the unchanged-skip honor `initialChanged`:

```go
			ps := conn.store.Priority(netID)
			bl := conn.store.GetOrCreateBaseline(netID, ringDepth)

			// Detect a change in initial-only fields (name, etc.) so it can be
			// re-sent to already-visible viewers. Cheap: only the initial
			// fields are hashed, and only when the kind has any.
			hasInit := rep.HasInitial()
			var curInitHash uint64
			if hasInit {
				s.initHasher.Reset()
				rep.InitialHash(&s.initHasher, viewer, entry)
				curInitHash = s.initHasher.Sum()
			}
			initialChanged := hasInit && (!bl.HasInitialHash || bl.InitialHash != curInitHash)

			// Dormancy: skip all replication work for entities unchanged for N
			// ticks — unless an initial field changed (static entities like
			// stations are dormant exactly when they get renamed).
			if !isNew && !initialChanged && s.cfg.DormancyThreshold > 0 && ps.UnchangedTicks >= s.cfg.DormancyThreshold {
				currentVisible[netID] = true
				continue
			}

			// Fast hash pre-check.
			s.hasher.Reset()
			rep.Hash(&s.hasher, viewer, entry)
			hash := s.hasher.Sum()

			if !isNew && !isKeyframe && !initialChanged && conn.store.HasLastHash(netID) {
				if conn.store.LastHash(netID) == hash {
					ps.UnchangedTicks++
					currentVisible[netID] = true
					continue // unchanged — skip snapshot
				}
			}
			ps.UnchangedTicks = 0
			conn.store.SetLastHash(netID, hash)
```

- [ ] **Step 3: Make the divisor gate honor `initialChanged`**

At line 694, change the condition so a non-divisor tick still sends when initial data changed:

```go
			// Update divisor gate: skip snapshot on non-divisor ticks.
			if !isNew && !initialChanged && tier.UpdateDivisor > 1 && tick%tier.UpdateDivisor != 0 {
```

(Leave the body of that `if` unchanged.)

- [ ] **Step 4: Remove the now-duplicate baseline lookup and add the change branch**

The old `bl := conn.store.GetOrCreateBaseline(netID, ringDepth)` at line 716 is now redundant (we fetched `bl` in Step 2) — delete that single line. Then replace the send decision so an initial change forces a full-with-initial frame. The branch was:

```go
			if isNew || bl.Acked == nil {
				// Full snapshot with initial data.
				...
			} else if isKeyframe {
```

Change the conditions to:

```go
			if isNew || bl.Acked == nil || initialChanged {
				// Full snapshot with initial data. Reached on first visibility,
				// missing baseline, OR when an initial-only field changed.
				snap := make([]byte, len(curr))
				copy(snap, curr)

				var initData []byte
				initData = rep.InitialData(viewer, entry)

				s.fullBuf = append(s.fullBuf, FullPayload{
					NetID:        netID,
					Epoch:        epoch,
					Type:         entityType,
					ProducedAtMs: s.producedAtMs(entry.Entity),
					Snapshot:     snap,
					InitialData:  initData,
				})

				if hasInit {
					bl.InitialHash = curInitHash
					bl.HasInitialHash = true
				}

				// Store baseline.
				if s.cfg.AckMode == replication.AckReliable {
					bl.Acked = snap
				} else {
					bl.Acked = snap
					bl.PushSent(frameSeq, snap)
				}
			} else if isKeyframe {
```

(The `else if isKeyframe` and `else if enc != nil` branches stay exactly as they are.)

- [ ] **Step 5: Vet**

Run: `go vet ./pkg/system/...`
Expected: no errors. (If `bl declared and not used` appears, the line-716 deletion in Step 4 was missed.)

- [ ] **Step 6: Run the full replication test suite**

Run: `go test ./pkg/system/`
Expected: PASS (existing behavior preserved — immutable initial fields never set `initialChanged`).

- [ ] **Step 7: Commit**

```bash
git add pkg/system/replication.go
git commit -m "feat(replication): re-send initial fields to visible viewers on change

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Integration test — change propagates, no waste

**Files:**
- Test: `pkg/system/replication_test.go`

- [ ] **Step 1: Write the failing test**

Add a test that drives `ReplicationSystem.Update` across two ticks with a real `autoReplicator` for a kind carrying a `net:"initial"` `Name` field. Model it on the existing replication tests in this file (reuse their world/system/viewer setup helpers — do not invent new harness). Assert:

```go
func TestReplication_InitialFieldChange_ReSends(t *testing.T) {
	// Build a ReplicationSystem + a viewer + one entity of a kind whose
	// auto-replicator has a net:"initial" Name field, using the same harness
	// the other tests in this file use.
	h := newReplicationHarness(t) // <- use whatever the existing tests use
	ent := h.spawnNamed(t, "alpha")

	// Tick 1: viewer enters, gets a full frame whose initialData decodes to "alpha".
	f1 := h.tickFrameFor(t, viewerConnID)
	if got := h.decodeName(t, f1, ent); got != "alpha" {
		t.Fatalf("tick1 name = %q, want alpha", got)
	}

	// Mutate the initial field on the live component.
	h.setName(t, ent, "beta")

	// Tick 2: must produce a FULL entry carrying initialData = "beta"
	// (not a delta, not the stale value).
	f2 := h.tickFrameFor(t, viewerConnID)
	if !h.hasFullEntryWithInitial(t, f2, ent) {
		t.Fatalf("tick2 must contain a full entry with initialData for the renamed entity")
	}
	if got := h.decodeName(t, f2, ent); got != "beta" {
		t.Fatalf("tick2 name = %q, want beta", got)
	}

	// Tick 3 with no change: must NOT re-send a full+initial frame (delta or skip only).
	f3 := h.tickFrameFor(t, viewerConnID)
	if h.hasFullEntryWithInitial(t, f3, ent) {
		t.Fatalf("tick3 must not re-send initial data when nothing changed")
	}
}
```

Replace the `h.*` helper calls with the concrete setup/assert idiom used by the neighbouring tests (inspect `FullPayload`/`s.fullBuf` or the encoded frame the existing tests already assert on). The three assertions that matter: (1) change shows up as a full entry with initialData, (2) the decoded value is the new one, (3) an unchanged following tick does not re-emit initial data.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/system/ -run TestReplication_InitialFieldChange_ReSends -v`
Expected: FAIL before Task 3 is in (or, if run after, PASS) — confirm it genuinely exercises the path by temporarily reverting the Step-4 condition to `isNew || bl.Acked == nil` and seeing it fail.

- [ ] **Step 3: Run to verify it passes**

Run: `go test ./pkg/system/ -run TestReplication_InitialFieldChange_ReSends -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/system/replication_test.go
git commit -m "test(replication): cover initial-field on-change re-send

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Build verification

- [ ] **Step 1: Vet the whole tree**

Run: `go vet ./...`
Expected: no errors.

- [ ] **Step 2: Build via just (never `go build ./...`)**

Run: `just build`
Expected: builds to `bin/server` clean; SDK regen (part of `just build`) produces no diff (the wire format is unchanged, so the generated SDK should be byte-identical).

- [ ] **Step 3: Confirm no SDK drift**

Run: `git status --porcelain`
Expected: empty (or only the files you intend). If the SDK changed, investigate — this feature must not alter the wire schema.

---

## Self-review notes

- **Spec coverage:** semantics unification (Task 1 `HasInitial`/`InitialHash` + Task 3 wiring), no client/wire change (Task 5 SDK-drift check), per-viewer initial hash (Task 2), send-on-change decision (Task 3), tests for propagate / no-waste / immutable-silent (Task 4 + existing-suite green in Task 3). Dormancy + divisor traps (not in spec, found during planning) are handled in Task 3 Steps 2-3.
- **Type consistency:** `HasInitial()`/`InitialHash(h *Hasher, viewer *ViewerInfo, entry spatial.Entry)` used identically in the interface (Task 1 Step 6), `autoReplicator` (Step 5), mocks (Step 7), and the loop (Task 3 Step 2). `InitialHash`/`HasInitialHash` baseline fields used identically in Task 2 and Task 3 Step 4.
- **Known limitation (carried from spec):** in unreliable ack mode a dropped change-frame leaves the field stale until the next change or AoI re-enter — same fragility the enter-frame already has. Acceptable for v1.
