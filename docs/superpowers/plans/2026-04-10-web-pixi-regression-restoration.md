# Web-Pixi Regression Restoration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore five features dropped during the Phase 2 wire-protocol simplification — status effect VFX on all entities, loot crate inventory preview + popup contents, being-locked ring for non-local players, other players' mining laser VFX, and ability-bar mining beam highlight.

**Architecture:** Add one new engine capability — a var-tail `ComponentBinding` with typed sdkgen decoder support — then restore each regression using either the new var-tail binding (variable-length data) or existing scalar reflection (fixed-shape data). No "visual" state on the server; the server replicates game state and the client renders VFX from it.

**Tech Stack:** Go (`pkg/system`, `cmd/sdkgen`, `internal/game`), TypeScript (`web-pixi/sdk/`, `web-pixi/src/`), protobuf, PixiJS, Ark ECS, existing `DeltaEncoder` / `SnapshotWriter` var-tail infrastructure in `pkg/quantize/`.

**Spec:** [docs/superpowers/specs/2026-04-10-web-pixi-regression-restoration-design.md](../specs/2026-04-10-web-pixi-regression-restoration-design.md)

**Branch:** `feature/web-pixi-sdk-modernization` — stack directly on existing commits, no worktree.

---

## File Structure

### New files

- `pkg/system/var_tail_binding.go` — `VarTailComponent[T]` constructor, `varTailBinding[T]` struct implementing `ComponentBinding`, optional `VarTailProvider` interface, and the accessor type.
- `pkg/system/var_tail_binding_test.go` — unit tests for the binding (layout, snapshot bytes, schema output).

### Modified files (server)

- `pkg/system/auto_replicator.go` — `autoReplicator.Schema()` detects `VarTailProvider` bindings and fills `EntitySchema.VarTail`.
- `pkg/mmokit/mmokit.go` — new wrapper `KindComponentWithBinding[T]` that registers a component for cross-node transfer but uses a caller-supplied `ComponentBinding` for client replication. Also updates `BuildReplicators` to hoist var-tail bindings to the end of each entity's binding list.
- `cmd/sdkgen/schema.go` — (already has `VarTailSchema`, no changes needed if matching shape).
- `cmd/sdkgen/generate.go` — `genEntities` emits typed item interfaces + tail field; `genDeltaDecoder` emits tail-parsing code.
- `internal/component/components.go` — new `LockedBy` and `ActiveMining` structs with `net:"..."` tags.
- `internal/game/components.go` — new `LockedBy` and `ActiveMining` fields on `Components` struct + `NewComponents` initializer.
- `internal/game/entity_kinds.go` — register new components on `ship`, `npc`, `asteroid`; replace `StatusEffects` and loot crate `Inventory` registrations with `KindComponentWithBinding` using the new var-tail bindings.
- `internal/game/var_tail_bindings.go` — **new file** containing `NewStatusEffectsBinding(m)` and `NewInventoryBinding(m)` factory functions that return `ComponentBinding` values for the two var-tail use cases.
- `internal/game/system_network.go` — `NetworkSystem.beforeTick` populates `LockedBy` component on victim entities from the existing reverse lock map.
- `internal/game/system_mining.go` — `MiningSystem.Update` writes `ActiveMining` each tick (beam flags + target netID).
- `internal/game/system_ability.go` — beam toggle path writes `ActiveMining` immediately on state change.

### Modified files (client)

- `web-pixi/sdk/` — **regenerated** via `just space-sdk` after server changes. Do not hand-edit.
- `web-pixi/src/network.ts` — no structural changes needed; `updateEntityFromServer` already copies the full SDK entity into `ClientEntity.current`, so new fields are picked up automatically.
- `web-pixi/src/effects/being-locked-ring.ts` — render a ring on any entity whose replicated `lockerNetID != 0`, not only the local player's own-state.
- `web-pixi/src/effects/mining-laser.ts` — render beams for any ship whose replicated `beam0Active || beam1Active` is true, drawing to `miningTargetNetId`.
- `web-pixi/src/effects/ability-effects.ts` — restore `drawStatusEffects` to iterate any entity's replicated status effect tail and dispatch to the existing `drawIonBurn` / `drawFortified` / etc. helpers.
- `web-pixi/src/effects/target-highlight.ts` — loot crate preview uses the decoded `items` array to build an item list string.
- `web-pixi/src/ui/loot-popup.ts` — replace "Contents unknown" placeholder with per-item buttons read from the decoded `items` array.
- `web-pixi/src/ui/ability-bar.ts` — `isMiningBeamActive(state, slot)` reads the local player's replicated beam flags from their own entity snapshot.

### Task → file mapping

| Task | Primary files |
|---|---|
| 1 — Var-tail binding + schema collection | `pkg/system/var_tail_binding.go`, `pkg/system/var_tail_binding_test.go`, `pkg/system/auto_replicator.go`, `pkg/mmokit/mmokit.go` |
| 2 — sdkgen typed decoder | `cmd/sdkgen/generate.go` |
| 3 — LockedBy regression 3 | `internal/component/components.go`, `internal/game/components.go`, `internal/game/entity_kinds.go`, `internal/game/system_network.go`, `web-pixi/src/effects/being-locked-ring.ts` |
| 4 — ActiveMining regressions 4 + 5 | `internal/component/components.go`, `internal/game/components.go`, `internal/game/entity_kinds.go`, `internal/game/system_mining.go`, `internal/game/system_ability.go`, `web-pixi/src/effects/mining-laser.ts`, `web-pixi/src/ui/ability-bar.ts` |
| 5 — StatusEffects var-tail regression 1 | `internal/game/var_tail_bindings.go`, `internal/game/entity_kinds.go`, `web-pixi/src/effects/ability-effects.ts` |
| 6 — Inventory var-tail regression 2 | `internal/game/var_tail_bindings.go`, `internal/game/entity_kinds.go`, `web-pixi/src/effects/target-highlight.ts`, `web-pixi/src/ui/loot-popup.ts` |
| 7 — Verification pass | all |

---

## Conventions for every task

- **Commits:** at the end of every task, create ONE focused commit. Commit message format: `feat(<area>): <summary>\n\nCo-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>`. Do not amend earlier commits.
- **Build verification between tasks:** `go vet ./...` must pass; no `go build ./...` (it drops binaries in package dirs — use `just build` if a binary is needed).
- **Logging:** all new server-side writes to replicated state get a `gw.eng.Log.Log(category, ...)` line. Categories to reuse: `CatCombat`, `CatCombatLocking`, `CatEconomyMining`.
- **No backward-compatibility shims:** delete old stubs entirely; update callers directly.
- **Bun, not npm:** all JS/TS commands use `bun`.
- **SDK regeneration:** after any server-side change that affects entity schemas or operations, run `just space-sdk` to regenerate `web-pixi/sdk/`. Commit the regenerated files with the task that caused the regeneration.

---

## Task 1: Var-Tail `ComponentBinding` + Schema Collection

**Goal:** Add a generic var-tail `ComponentBinding` that produces a variable-length wire tail and advertises a `VarTailSchema` for client codegen. Extend `autoReplicator` to collect var-tail schemas and add an `mmokit.KindComponentWithBinding` wrapper.

**Files:**
- Create: `pkg/system/var_tail_binding.go`
- Create: `pkg/system/var_tail_binding_test.go`
- Modify: `pkg/system/auto_replicator.go`
- Modify: `pkg/mmokit/mmokit.go`

---

- [ ] **Step 1.1: Write the failing test for `VarTailComponent`**

Create `pkg/system/var_tail_binding_test.go`:

```go
package system

import (
	"bytes"
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// fakeItem is a test-only item in a var-tail component.
type fakeItem struct {
	Type uint8
	Val  uint8
}

// fakeVarTailComp is a test-only component with a fixed-capacity variable-length list.
type fakeVarTailComp struct {
	Items [4]fakeItem
	N     uint8
}

func TestVarTailComponent_SnapshotLayout(t *testing.T) {
	w := ecs.NewWorld(1024)
	m := ecs.NewMap1[fakeVarTailComp](&w)

	binding := VarTailComponent(m, VarTailAccessor[fakeVarTailComp]{
		Name:     "items",
		ItemSize: 2,
		ItemFields: []BindingSchemaField{
			{Name: "type", Encoding: "u8", Size: 1},
			{Name: "val", Encoding: "u8", Size: 1},
		},
		Count:      func(c *fakeVarTailComp) int { return int(c.N) },
		WriteItems: func(c *fakeVarTailComp, sw *quantize.SnapshotWriter) {},
		HashItems:  func(c *fakeVarTailComp, h *Hasher) {},
	})

	layout := binding.snapshotFields()
	if len(layout) != 1 || layout[0] != -1 {
		t.Fatalf("expected layout [-1], got %v", layout)
	}
}

func TestVarTailComponent_SnapshotBytes(t *testing.T) {
	w := ecs.NewWorld(1024)
	m := ecs.NewMap1[fakeVarTailComp](&w)
	e := m.NewEntity(&fakeVarTailComp{
		Items: [4]fakeItem{{Type: 1, Val: 10}, {Type: 2, Val: 20}, {}, {}},
		N:     2,
	})

	binding := VarTailComponent(m, VarTailAccessor[fakeVarTailComp]{
		Name:     "items",
		ItemSize: 2,
		ItemFields: []BindingSchemaField{
			{Name: "type", Encoding: "u8", Size: 1},
			{Name: "val", Encoding: "u8", Size: 1},
		},
		Count: func(c *fakeVarTailComp) int { return int(c.N) },
		WriteItems: func(c *fakeVarTailComp, sw *quantize.SnapshotWriter) {
			for i := uint8(0); i < c.N; i++ {
				sw.Uint8(c.Items[i].Type)
				sw.Uint8(c.Items[i].Val)
			}
		},
		HashItems: func(c *fakeVarTailComp, h *Hasher) {
			for i := uint8(0); i < c.N; i++ {
				h.Uint8(c.Items[i].Type)
				h.Uint8(c.Items[i].Val)
			}
		},
	})

	buf := make([]byte, 64)
	sw := quantize.NewSnapshotWriter(buf)
	binding.snapshot(e, sw, nil, spatial.Entry{})

	// Expected: uint16 BE byte length (4) + [1,10,2,20]
	want := []byte{0, 4, 1, 10, 2, 20}
	if got := sw.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("snapshot bytes: want %v got %v", want, got)
	}
}

func TestVarTailComponent_EmptyTail(t *testing.T) {
	w := ecs.NewWorld(1024)
	m := ecs.NewMap1[fakeVarTailComp](&w)
	e := m.NewEntity(&fakeVarTailComp{N: 0})

	binding := VarTailComponent(m, VarTailAccessor[fakeVarTailComp]{
		Name:     "items",
		ItemSize: 2,
		ItemFields: []BindingSchemaField{
			{Name: "type", Encoding: "u8", Size: 1},
			{Name: "val", Encoding: "u8", Size: 1},
		},
		Count:      func(c *fakeVarTailComp) int { return int(c.N) },
		WriteItems: func(c *fakeVarTailComp, sw *quantize.SnapshotWriter) {},
		HashItems:  func(c *fakeVarTailComp, h *Hasher) {},
	})

	buf := make([]byte, 64)
	sw := quantize.NewSnapshotWriter(buf)
	binding.snapshot(e, sw, nil, spatial.Entry{})

	// Empty tail still writes the uint16 length prefix (0).
	want := []byte{0, 0}
	if got := sw.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("empty snapshot: want %v got %v", want, got)
	}
}

func TestVarTailComponent_Schema(t *testing.T) {
	w := ecs.NewWorld(1024)
	m := ecs.NewMap1[fakeVarTailComp](&w)

	binding := VarTailComponent(m, VarTailAccessor[fakeVarTailComp]{
		Name:     "items",
		ItemSize: 2,
		ItemFields: []BindingSchemaField{
			{Name: "type", Encoding: "u8", Size: 1},
			{Name: "val", Encoding: "u8", Size: 1},
		},
		Count:      func(c *fakeVarTailComp) int { return int(c.N) },
		WriteItems: func(c *fakeVarTailComp, sw *quantize.SnapshotWriter) {},
		HashItems:  func(c *fakeVarTailComp, h *Hasher) {},
	})

	// The binding itself exposes zero per-tick fields in its BindingSchema
	// (scalar layout only). The var-tail is surfaced separately via VarTailProvider.
	bs := binding.schema()
	if len(bs.Fields) != 0 {
		t.Fatalf("var-tail binding should have 0 scalar fields, got %d", len(bs.Fields))
	}

	provider, ok := binding.(VarTailProvider)
	if !ok {
		t.Fatalf("VarTailComponent must implement VarTailProvider")
	}
	vts := provider.VarTailSchema()
	if vts == nil || vts.Name != "items" || vts.ItemSize != 2 || len(vts.ItemFields) != 2 {
		t.Fatalf("unexpected VarTailSchema: %+v", vts)
	}
}
```

- [ ] **Step 1.2: Run the test to confirm it fails**

```bash
go test ./pkg/system/ -run TestVarTailComponent -v
```

Expected: compile failure — `VarTailComponent`, `VarTailAccessor`, `VarTailProvider` undefined.

- [ ] **Step 1.3: Implement `VarTailComponent`**

Create `pkg/system/var_tail_binding.go`:

```go
package system

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// VarTailProvider is an optional interface on ComponentBinding. autoReplicator
// detects implementers and collects their VarTailSchema into the EntitySchema
// for client codegen. Only one var-tail binding is allowed per entity.
type VarTailProvider interface {
	VarTailSchema() *VarTailSchema
}

// VarTailAccessor describes how a component exposes its variable-length tail
// to a VarTailComponent binding. The caller provides closures that read the
// count, serialize all items to a SnapshotWriter, and hash all items to a
// Hasher.
//
// WriteItems must write exactly Count * ItemSize bytes. The binding writes
// a uint16 BE byte-length prefix before invoking WriteItems.
//
// HashItems must hash every item in a deterministic order. Order must match
// WriteItems or delta encoding will false-positive.
type VarTailAccessor[T any] struct {
	Name       string               // field name on the generated entity type
	ItemSize   int                  // bytes per item
	ItemFields []BindingSchemaField // per-item sub-fields for sdkgen
	Count      func(comp *T) int
	WriteItems func(comp *T, w *quantize.SnapshotWriter)
	HashItems  func(comp *T, h *Hasher)
}

// VarTailComponent returns a ComponentBinding that emits a variable-length
// tail from an ECS component. The tail wire format is:
//
//	[uint16 BE byte length][count * itemSize bytes]
//
// The binding advertises a layout of []int{-1} so DeltaEncoder treats it as
// the single var-tail field. Because of that, a VarTailComponent binding MUST
// be the last binding in an AutoReplicator's binding list. BuildReplicators in
// pkg/mmokit auto-hoists var-tail bindings to the end so games don't need to
// worry about ordering manually.
func VarTailComponent[T any](ecsMap *ecs.Map1[T], acc VarTailAccessor[T]) ComponentBinding {
	if acc.Count == nil || acc.WriteItems == nil || acc.HashItems == nil {
		panic("VarTailComponent: Count/WriteItems/HashItems must all be set")
	}
	if acc.ItemSize <= 0 {
		panic("VarTailComponent: ItemSize must be positive")
	}
	return &varTailBinding[T]{ecsMap: ecsMap, acc: acc}
}

type varTailBinding[T any] struct {
	ecsMap *ecs.Map1[T]
	acc    VarTailAccessor[T]
}

func (b *varTailBinding[T]) snapshotFields() []int { return []int{-1} }

func (b *varTailBinding[T]) hash(entity ecs.Entity, h *Hasher, _ *ViewerInfo, _ spatial.Entry) {
	if !b.ecsMap.HasAll(entity) {
		h.Uint32(0) // hash zero count when component absent
		return
	}
	comp := b.ecsMap.Get(entity)
	h.Uint32(uint32(b.acc.Count(comp)))
	b.acc.HashItems(comp, h)
}

func (b *varTailBinding[T]) snapshot(entity ecs.Entity, w *quantize.SnapshotWriter, _ *ViewerInfo, _ spatial.Entry) {
	if !b.ecsMap.HasAll(entity) {
		w.Uint16(0)
		return
	}
	comp := b.ecsMap.Get(entity)
	count := b.acc.Count(comp)
	byteLen := uint16(count * b.acc.ItemSize)
	w.Uint16(byteLen)
	b.acc.WriteItems(comp, w)
}

func (b *varTailBinding[T]) hasInitial() bool { return false }

func (b *varTailBinding[T]) initialData(_ ecs.Entity, _ *ViewerInfo, _ spatial.Entry, buf []byte) []byte {
	return buf
}

func (b *varTailBinding[T]) schema() BindingSchema {
	// The BindingSchema describes scalar per-tick wire fields. A var-tail
	// binding has none; its variable tail is surfaced via VarTailProvider so
	// autoReplicator can attach it to EntitySchema.VarTail.
	return BindingSchema{Type: "var_tail"}
}

func (b *varTailBinding[T]) VarTailSchema() *VarTailSchema {
	return &VarTailSchema{
		Name:       b.acc.Name,
		ItemSize:   b.acc.ItemSize,
		ItemFields: b.acc.ItemFields,
	}
}
```

- [ ] **Step 1.4: Extend `autoReplicator.Schema()` to detect `VarTailProvider`**

Edit `pkg/system/auto_replicator.go`, replace the body of `(a *autoReplicator) Schema()`:

```go
// Schema implements SchemaProvider for client SDK codegen.
func (a *autoReplicator) Schema() EntitySchema {
	var bindings []BindingSchema
	var varTail *VarTailSchema
	for _, b := range a.bindings {
		bindings = append(bindings, b.schema())
		if vtp, ok := b.(VarTailProvider); ok {
			if varTail != nil {
				panic("autoReplicator: only one var-tail binding allowed per entity")
			}
			varTail = vtp.VarTailSchema()
		}
	}
	resolveFieldNameCollisions(bindings)
	initialData := ""
	if a.anyInitial {
		initialData = "length_prefixed_string_u8"
	}
	return EntitySchema{
		Kind:        a.entityType,
		Bindings:    bindings,
		Layout:      a.layout,
		VarTail:     varTail,
		InitialData: initialData,
	}
}
```

- [ ] **Step 1.5: Validate var-tail binding is last in `AutoReplicator` constructor**

Edit `pkg/system/auto_replicator.go`, replace the body of the `AutoReplicator` function:

```go
// AutoReplicator builds an EntityReplicator from composable ComponentBinding values.
// The entityType is the wire constant sent to clients. If any binding is a
// VarTailProvider it must be the final binding (enforced at construction).
func AutoReplicator(entityType uint8, bindings ...ComponentBinding) EntityReplicator {
	var layout []int
	var anyInitial bool
	for i, b := range bindings {
		layout = append(layout, b.snapshotFields()...)
		if b.hasInitial() {
			anyInitial = true
		}
		if _, ok := b.(VarTailProvider); ok && i != len(bindings)-1 {
			panic("AutoReplicator: var-tail binding must be the last binding")
		}
	}
	return &autoReplicator{
		entityType: entityType,
		bindings:   bindings,
		layout:     layout,
		anyInitial: anyInitial,
	}
}
```

- [ ] **Step 1.6: Run the tests to verify they pass**

```bash
go test ./pkg/system/ -run TestVarTailComponent -v
go test ./pkg/system/ -v
```

Expected: all tests pass.

- [ ] **Step 1.7: Add `KindComponentWithBinding` wrapper in mmokit**

Edit `pkg/mmokit/mmokit.go`. Right after the existing `KindComponent` function (around line 982), add:

```go
// KindComponentWithBinding registers a component type for cross-node transfer
// (identical to KindComponent) but uses a caller-supplied ComponentBinding for
// client replication instead of the default reflection-based binding. Use for
// components that need var-tail encoding or other non-reflection serialization.
// The binding's component type must match T.
func KindComponentWithBinding[T any](def *universe.EntityKindDef, m *ecs.Map1[T], binding system.ComponentBinding, opts ...universe.ComponentOption[T]) {
	universe.KindComponent(def, m, opts...)
	def.NetworkBindings = append(def.NetworkBindings, binding)
}
```

- [ ] **Step 1.8: Hoist var-tail bindings to end of binding list in `BuildReplicators`**

Still in `pkg/mmokit/mmokit.go`, replace the body of `BuildReplicators` (around line 996) with:

```go
// BuildReplicators constructs a ReplicatorRegistry from EntityKindDefs.
// Used for schema export and auto-discovery by NewNetworkSystem. The w and coord
// parameters are needed to create EngineBindings; coord may be nil for schema export.
//
// Var-tail bindings (those implementing system.VarTailProvider) are automatically
// moved to the end of each entity's binding list so games don't need to worry
// about registration order. At most one var-tail binding is allowed per entity;
// AutoReplicator will panic if there are more.
func BuildReplicators(w *ecs.World, coord *universe.Coordinator, defs ...universe.EntityKindDef) *system.ReplicatorRegistry {
	replicators := system.NewReplicatorRegistry()
	for _, def := range defs {
		var bindings []system.ComponentBinding
		if def.EngineBindings != nil {
			if ebCfg, ok := def.EngineBindings.(*EngineBindingsConfig); ok {
				if coord != nil {
					ebCfg.IncludeMeshState = coord.DebugTopology()
				}
				bindings = append(bindings, EngineBindings(w, coord, *ebCfg))
			}
		} else {
			var cfg EngineBindingsConfig
			if coord != nil {
				cfg.IncludeMeshState = coord.DebugTopology()
			}
			bindings = append(bindings, EngineBindings(w, coord, cfg))
		}

		// Partition game bindings: var-tail bindings go to the end.
		var regular, varTails []system.ComponentBinding
		for _, nb := range def.NetworkBindings {
			cb, ok := nb.(system.ComponentBinding)
			if !ok {
				continue
			}
			if _, isVarTail := cb.(system.VarTailProvider); isVarTail {
				varTails = append(varTails, cb)
			} else {
				regular = append(regular, cb)
			}
		}
		bindings = append(bindings, regular...)
		bindings = append(bindings, varTails...)

		replicators.Register(system.AutoReplicator(def.Kind, bindings...))
	}
	return replicators
}
```

- [ ] **Step 1.9: Verify the whole package still compiles and tests pass**

```bash
go vet ./pkg/... ./cmd/... ./internal/...
go test ./pkg/system/... -v
```

Expected: vet clean, all existing tests still pass alongside the new ones.

- [ ] **Step 1.10: Commit Task 1**

```bash
git add pkg/system/var_tail_binding.go pkg/system/var_tail_binding_test.go pkg/system/auto_replicator.go pkg/mmokit/mmokit.go
git commit -m "$(cat <<'EOF'
feat(pkg/system): add VarTailComponent binding + KindComponentWithBinding

VarTailComponent produces a variable-length snapshot tail using the existing
DeltaEncoder var-tail wire format. Advertises a VarTailSchema for client
codegen via the new VarTailProvider interface. BuildReplicators auto-hoists
var-tail bindings to the end of each entity's binding list.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: sdkgen Typed Var-Tail Decoder

**Goal:** Teach `cmd/sdkgen/generate.go` to emit typed per-item interfaces for entities that declare a `VarTail`, decode the tail bytes into a typed array, and expose the array as a field on the generated entity type.

**Files:**
- Modify: `cmd/sdkgen/generate.go`

---

- [ ] **Step 2.1: Add a helper to generate a tail item type name**

Edit `cmd/sdkgen/generate.go`. Add near `entityName` (around line 151):

```go
// tailItemTypeName returns the generated TypeScript interface name for a
// var-tail's per-item record. Example: Ship → ShipStatusEffectItem.
func (g *Generator) tailItemTypeName(ent EntitySchema) string {
	if ent.VarTail == nil {
		return ""
	}
	return g.entityName(ent) + titleCase(ent.VarTail.Name) + "Item"
}
```

- [ ] **Step 2.2: Emit tail item interfaces and entity field in `genEntities`**

Edit `cmd/sdkgen/generate.go`, inside `genEntities`. Extend the entity loop so that right before the closing `b.WriteString("}\n\n")` of the entity interface, it adds the tail field, and after the entity interface it emits the item type.

Replace the entity loop body (currently lines ~99-128) with:

```go
	for _, ent := range g.schema.Entities {
		name := g.entityName(ent)
		entityNames = append(entityNames, name)

		fmt.Fprintf(&b, "/** Entity kind %d. */\n", ent.Kind)
		fmt.Fprintf(&b, "export interface %s {\n", name)
		b.WriteString("  netID: number;\n")
		fmt.Fprintf(&b, "  entityType: %d;\n", ent.Kind)

		for _, binding := range ent.Bindings {
			for _, field := range binding.Fields {
				if field.Initial {
					continue
				}
				tsType := encodingToTSType(field.Encoding)
				fmt.Fprintf(&b, "  %s: %s;\n", field.Name, tsType)
			}
		}
		// Initial fields.
		for _, binding := range ent.Bindings {
			for _, field := range binding.Fields {
				if !field.Initial {
					continue
				}
				tsType := encodingToTSType(field.Encoding)
				fmt.Fprintf(&b, "  %s: %s;\n", field.Name, tsType)
			}
		}

		// Var-tail field (if any).
		if ent.VarTail != nil {
			itemType := g.tailItemTypeName(ent)
			fmt.Fprintf(&b, "  %s: %s[];\n", ent.VarTail.Name, itemType)
		}

		b.WriteString("}\n\n")

		// Emit the var-tail item type interface after the entity interface.
		if ent.VarTail != nil {
			itemType := g.tailItemTypeName(ent)
			fmt.Fprintf(&b, "/** Item record for %s.%s var-tail. */\n", name, ent.VarTail.Name)
			fmt.Fprintf(&b, "export interface %s {\n", itemType)
			for _, f := range ent.VarTail.ItemFields {
				tsType := encodingToTSType(f.Encoding)
				fmt.Fprintf(&b, "  %s: %s;\n", f.Name, tsType)
			}
			b.WriteString("}\n\n")
		}
	}
```

- [ ] **Step 2.3: Emit tail-parsing code in `decodeXxxSnapshot`**

Edit `cmd/sdkgen/generate.go`, inside `genDeltaDecoder` around the `decode%sSnapshot` generator (currently lines ~205-237). Add a helper function and call it.

Add this helper above `writeFieldDecoder` (around line 330):

```go
// writeVarTailDecoder emits TypeScript that parses the snapshot's var-tail
// into a typed item array on the decoded entity. Called once per decode
// function after all scalar fields have been consumed.
func writeVarTailDecoder(b *strings.Builder, ent EntitySchema, itemType string) {
	vt := ent.VarTail
	fmt.Fprintf(b, "  const %sByteLen = readUint16(snap, o); o += 2;\n", vt.Name)
	fmt.Fprintf(b, "  const %sEnd = o + %sByteLen;\n", vt.Name, vt.Name)
	fmt.Fprintf(b, "  const %s: %s[] = [];\n", vt.Name, itemType)
	fmt.Fprintf(b, "  while (o < %sEnd) {\n", vt.Name)
	for _, f := range vt.ItemFields {
		writeFieldDecoderIndented(b, f, "    ")
	}
	fmt.Fprintf(b, "    %s.push({ %s });\n", vt.Name, joinItemFieldNames(vt.ItemFields))
	b.WriteString("  }\n")
}

// writeFieldDecoderIndented is writeFieldDecoder with configurable indent, so
// it can be nested inside the while loop for var-tail items.
func writeFieldDecoderIndented(b *strings.Builder, field BindingSchemaField, indent string) {
	switch field.Encoding {
	case "f32":
		fmt.Fprintf(b, "%sconst %s = readFloat32(snap, o); o += 4;\n", indent, field.Name)
	case "qvel":
		fmt.Fprintf(b, "%sconst %s = unVel(readInt16(snap, o), %g); o += 2;\n", indent, field.Name, field.Scale)
	case "qangle":
		fmt.Fprintf(b, "%sconst %s = unAngle(readUint16(snap, o)); o += 2;\n", indent, field.Name)
	case "qnorm":
		fmt.Fprintf(b, "%sconst %s = unNorm(snap[o]); o += 1;\n", indent, field.Name)
	case "u8":
		fmt.Fprintf(b, "%sconst %s = snap[o]; o += 1;\n", indent, field.Name)
	case "u16":
		fmt.Fprintf(b, "%sconst %s = readUint16(snap, o); o += 2;\n", indent, field.Name)
	case "u32":
		fmt.Fprintf(b, "%sconst %s = readUint32(snap, o); o += 4;\n", indent, field.Name)
	case "i16":
		fmt.Fprintf(b, "%sconst %s = readInt16(snap, o); o += 2;\n", indent, field.Name)
	case "bool":
		fmt.Fprintf(b, "%sconst %s = !!snap[o]; o += 1;\n", indent, field.Name)
	}
}

func joinItemFieldNames(fields []BindingSchemaField) string {
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.Name
	}
	return strings.Join(names, ", ")
}
```

Then update `genDeltaDecoder` per-entity block. Inside `genDeltaDecoder`, replace the decode-snapshot function block (currently lines ~205-237) with:

```go
		// Decode snapshot function.
		fmt.Fprintf(&b, "function decode%sSnapshot(snap: Uint8Array, initial: Uint8Array | null, existing?: %s): %s {\n", name, name, name)
		b.WriteString("  let o = 0;\n")

		// Snapshot fields (non-initial).
		for _, binding := range ent.Bindings {
			for _, field := range binding.Fields {
				if field.Initial {
					continue
				}
				writeFieldDecoder(&b, field)
			}
		}

		// Var-tail parsing.
		if ent.VarTail != nil {
			writeVarTailDecoder(&b, ent, g.tailItemTypeName(ent))
		}

		// Initial fields.
		for _, binding := range ent.Bindings {
			for _, field := range binding.Fields {
				if !field.Initial {
					continue
				}
				writeInitialFieldDecoder(&b, field, ent.InitialData)
			}
		}

		// Return object.
		fmt.Fprintf(&b, "  return { netID: 0, entityType: %d", ent.Kind)
		for _, binding := range ent.Bindings {
			for _, field := range binding.Fields {
				fmt.Fprintf(&b, ", %s", field.Name)
			}
		}
		if ent.VarTail != nil {
			fmt.Fprintf(&b, ", %s", ent.VarTail.Name)
		}
		b.WriteString(" };\n")
		b.WriteString("}\n\n")
```

- [ ] **Step 2.4: Update the import list in the generated decoder (if needed)**

`writeFieldDecoder` already uses `readUint16`, `readInt16`, etc., which are already imported. No change needed.

- [ ] **Step 2.5: Verify sdkgen still compiles**

```bash
go vet ./cmd/sdkgen/...
go build -o /tmp/sdkgen-check ./cmd/sdkgen
rm /tmp/sdkgen-check
```

Expected: clean compile.

- [ ] **Step 2.6: Regenerate the space SDK and verify nothing broke**

The current entity set still has no var-tail bindings. The regeneration should produce identical output to what's checked in (the new code paths are conditional on `ent.VarTail != nil`).

```bash
just space-sdk
git status web-pixi/sdk
git diff web-pixi/sdk
```

Expected: `git status` shows no changes to `web-pixi/sdk/`, since no entity currently declares a `VarTail`. The new code paths are conditional and inert on the existing schema.

- [ ] **Step 2.7: Verify the client still builds**

```bash
cd web-pixi && bun run build && cd ..
```

Expected: clean build.

- [ ] **Step 2.8: Commit Task 2**

```bash
git add cmd/sdkgen/generate.go
# If the SDK regen produced incidental changes, include them too:
git add web-pixi/sdk 2>/dev/null || true
git commit -m "$(cat <<'EOF'
feat(sdkgen): emit typed var-tail item decoders

genEntities emits a TypeScript interface per var-tail item shape and adds the
decoded array field to the entity interface. genDeltaDecoder parses the
length-prefixed tail bytes into typed items after reading scalar fields.
Entities without VarTail are unchanged.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Regression 3 — `LockedBy` Component

**Goal:** Restore the being-locked ring on all non-local entities. Add a `LockedBy` game-state component with scalar `net:"u32"` + `net:"qnorm"` tags, populate it each tick from the existing reverse lock map, and render rings from the replicated state on the client.

**Files:**
- Modify: `internal/component/components.go`
- Modify: `internal/game/components.go`
- Modify: `internal/game/entity_kinds.go`
- Modify: `internal/game/system_network.go`
- Modify: `web-pixi/src/effects/being-locked-ring.ts`
- Regenerate: `web-pixi/sdk/`

---

- [ ] **Step 3.1: Add `LockedBy` struct to `internal/component/components.go`**

Add after the `TargetLock` struct (around line 47):

```go
// LockedBy is a replicated "who is locking me" marker. The NetworkSystem
// populates it each tick from the reverse lock map so clients can render a
// warning ring on any entity currently being target-locked. Zero LockerNetID
// means nobody is currently locking this entity.
//
// Field names are prefixed with "Locker" to avoid colliding with the
// hardcoded netID field on every generated entity interface (cmd/sdkgen
// writes netID: number before processing bindings, and the collision
// resolver in auto_replicator.go doesn't know about hardcoded fields).
type LockedBy struct {
	LockerNetID    uint32  `net:"u32"`
	LockerProgress float32 `net:"qnorm"`
}
```

- [ ] **Step 3.2: Add `LockedBy` map to `Components` struct**

Edit `internal/game/components.go`. In `Components` struct (around line 30), add after `StatusEffects`:

```go
	StatusEffects    *ecs.Map1[gamecomp.StatusEffects]
	LockedBy         *ecs.Map1[gamecomp.LockedBy]
```

In `NewComponents` function (around line 66), add:

```go
		StatusEffects:    ecs.NewMap1[gamecomp.StatusEffects](world),
		LockedBy:         ecs.NewMap1[gamecomp.LockedBy](world),
```

- [ ] **Step 3.3: Register `LockedBy` on ship / NPC / asteroid kinds**

Edit `internal/game/entity_kinds.go`. In `buildShipDef`, insert the registration after `KindComponent(&def, c.MoveTarget)` (around line 57), immediately before the "Local-only components" comment block:

```go
	mmokit.KindComponent(&def, c.LockedBy)
```

In `buildAsteroidDef`, insert after `KindComponent(&def, c.Minable)` (around line 70):

```go
	mmokit.KindComponent(&def, c.LockedBy)
```

In `buildNpcDef`, insert after `KindComponent(&def, c.StatusEffects)` (around line 92):

```go
	mmokit.KindComponent(&def, c.LockedBy)
```

Do NOT add to station or loot crate — stations aren't lockable, and loot crates aren't targeted via `TargetLock` in game logic.

- [ ] **Step 3.4: Populate `LockedBy` in `NetworkSystem.beforeTick`**

Edit `internal/game/system_network.go`. Add a new query field to the `NetworkSystem` struct. Change the struct definition (around line 14) to:

```go
type NetworkSystem struct {
	mmokit.SystemBase
	gw      *GameWorld
	replSys *mmokit.ReplicationSystem
	ctx     *gameNetContext

	locks mmokit.Query[struct {
		Lock  *gamecomp.TargetLock
		NetID *mmokit.NetworkID
	}]

	lockVictims mmokit.Query[struct {
		LB *gamecomp.LockedBy
	}]

	// Per-tick shared data hoisted outside the per-viewer loop
	pendingChat          []*enginepb.ChatMsg
	pendingAbilityEvents []*gamepb.AbilityCastResultMsg
}
```

In `Init` (around line 30), add query initialization after `s.locks.Init(s, mmokit.IncludeAll())`:

```go
	s.locks.Init(s, mmokit.IncludeAll())
	s.lockVictims.Init(s, mmokit.IncludeAll())
```

In `beforeTick` (around line 67), replace the body with:

```go
// beforeTick builds the reverse lock map, syncs the LockedBy component on all
// lockable entities, and hoists per-tick lookups.
func (s *NetworkSystem) beforeTick(tick uint32) {
	gw := s.gw

	// Build reverse lock map: for each entity being locked, track the most-progressed locker.
	clear(s.ctx.lockedBy)
	for _, b := range s.locks.All() {
		if b.Lock.TargetNetID == 0 || b.Lock.Progress <= 0 {
			continue
		}
		if !gw.eng.ECS.Alive(b.Lock.TargetEntity) {
			continue
		}
		if existing, ok := s.ctx.lockedBy[b.Lock.TargetEntity]; !ok || b.Lock.Progress > existing.progress {
			s.ctx.lockedBy[b.Lock.TargetEntity] = lockerInfo{netID: b.NetID.ID, progress: b.Lock.Progress}
		}
	}

	// Sync LockedBy component on every lockable entity. Zero first, then
	// populate from the reverse map. Entities with LockedBy that aren't in
	// the map get zeroed out — the client reads LockerNetID==0 as "not locked".
	for e, b := range s.lockVictims.All() {
		if info, ok := s.ctx.lockedBy[e]; ok {
			b.LB.LockerNetID = info.netID
			b.LB.LockerProgress = info.progress
		} else {
			b.LB.LockerNetID = 0
			b.LB.LockerProgress = 0
		}
	}

	// Hoist per-tick lookups outside the viewer loop.
	s.pendingChat = mmokit.Peek[*enginepb.ChatMsg](gw.Queue)
	s.pendingAbilityEvents = mmokit.Peek[*gamepb.AbilityCastResultMsg](gw.Queue)
}
```

- [ ] **Step 3.5: Verify the server compiles and existing tests still pass**

```bash
go vet ./...
go test ./internal/game/... -v
```

Expected: clean vet, tests pass.

- [ ] **Step 3.6: Regenerate the SDK**

```bash
just space-sdk
```

After regen, `web-pixi/sdk/entities.ts` should gain `lockerNetID: number;` and `lockerProgress: number;` fields on `ShipEntity`, `AsteroidEntity`, and `NPCEntity` (the Go fields `LockerNetID` and `LockerProgress` become `lockerNetID` / `lockerProgress` via `lcFirst`).

```bash
grep -n "lockerNetID\|lockerProgress" web-pixi/sdk/entities.ts
```

Expected: shows the new fields on ship/asteroid/npc entities. If casing differs (the `lcFirst` helper preserves all-caps `ID` suffixes), use whatever the generator emitted in the client code below.

- [ ] **Step 3.7: Update `being-locked-ring.ts` to render rings from replicated state**

Edit `web-pixi/src/effects/being-locked-ring.ts`. Replace the entire file with:

```ts
import { Container, Graphics, Text } from "pixi.js";
import type { ShipEntity, AsteroidEntity, NPCEntity } from "../../sdk/index.js";
import { px } from "../view";
import type { GameState } from "../state";
import type { ClientEntity } from "../types";
import { getShip } from "../entity-accessors";

const COLOR_WARNING = 0xff4444;
const COLOR_LOCKING = 0xff6600;
const COLOR_LOCKED = 0xff0000;

// Entity shapes that may carry LockedBy scalar fields via replication.
type LockableEntity = ShipEntity | AsteroidEntity | NPCEntity;

interface RingEntry {
	container: Container;
	ring: Graphics;
	label: Text;
}

/**
 * BeingLockedRing renders a warning ring around any entity currently being
 * target-locked. The lock source is the replicated `lockerNetID` /
 * `lockerProgress` fields on each lockable entity (not PlayerOwnStateMsg).
 * Zero lockerNetID means nobody is locking this entity.
 */
export class BeingLockedRing {
	private parent: Container;
	private entries = new Map<number, RingEntry>();

	constructor(parent: Container) {
		this.parent = parent;
	}

	update(state: GameState, now: number): void {
		const alive = new Set<number>();

		for (const [netID, ent] of state.entities) {
			const lb = extractLockedBy(ent);
			if (!lb || lb.lockerNetID === 0) continue;

			alive.add(netID);
			let entry = this.entries.get(netID);
			if (!entry) {
				entry = createRingEntry(this.parent);
				this.entries.set(netID, entry);
			}
			drawRing(entry, ent, lb, state, now);
		}

		// Clean up rings for entities that are no longer locked.
		for (const [netID, entry] of this.entries) {
			if (!alive.has(netID)) {
				this.parent.removeChild(entry.container);
				entry.container.destroy({ children: true });
				this.entries.delete(netID);
			}
		}
	}
}

function extractLockedBy(ent: ClientEntity): { lockerNetID: number; lockerProgress: number } | null {
	// Field names match those emitted by the generator from LockerNetID / LockerProgress.
	// If the SDK generator changed casing (e.g. `lockerNetId`), update both fields here.
	const e = ent.current as Partial<LockableEntity> & { lockerNetID?: number; lockerProgress?: number };
	if (typeof e.lockerNetID !== "number") return null;
	return { lockerNetID: e.lockerNetID, lockerProgress: e.lockerProgress ?? 0 };
}

function createRingEntry(parent: Container): RingEntry {
	const container = new Container();
	parent.addChild(container);

	const ring = new Graphics();
	container.addChild(ring);

	const label = new Text({
		text: "",
		style: { fontFamily: "monospace", fontSize: 11, fill: COLOR_WARNING },
	});
	label.anchor.set(0.5, 1);
	label.scale.set(px(1), px(1));
	container.addChild(label);

	return { container, ring, label };
}

function drawRing(
	entry: RingEntry,
	ent: ClientEntity,
	lb: { lockerNetID: number; lockerProgress: number },
	state: GameState,
	now: number,
): void {
	entry.container.position.set(ent.renderX, ent.renderY);

	const asShip = ent.current as Partial<ShipEntity>;
	const w = asShip.width ?? 1;
	const h = asShip.height ?? 1;
	const baseRadius = Math.max(w, h, 1) * 0.5 + px(18);
	const progress = lb.lockerProgress;
	const locked = progress >= 1.0;

	// Resolve locker name for display (best effort — locker may be outside AoI).
	const locker = state.entities.get(lb.lockerNetID);
	const lockerName = (locker ? getShip(locker)?.name : undefined) || "???";

	entry.ring.clear();

	if (locked) {
		const pulse = 0.6 + 0.4 * Math.sin(now * 0.006);
		entry.ring
			.circle(0, 0, baseRadius)
			.stroke({ color: COLOR_LOCKED, width: px(3), alpha: pulse });
		entry.label.text = `LOCKED BY ${lockerName.toUpperCase()}`;
		entry.label.style.fill = COLOR_LOCKED;
	} else {
		drawDashedCircle(entry.ring, baseRadius, 0x333333, px(1.5), 0.4, now);

		const color = progress > 0.5 ? COLOR_WARNING : COLOR_LOCKING;
		const startAngle = -Math.PI / 2;
		const endAngle = startAngle + progress * Math.PI * 2;
		const pulse = 0.6 + 0.4 * Math.sin(now * 0.008);
		const sx = Math.cos(startAngle) * baseRadius;
		const sy = Math.sin(startAngle) * baseRadius;
		entry.ring
			.moveTo(sx, sy)
			.arc(0, 0, baseRadius, startAngle, endAngle)
			.stroke({ color, width: px(3), alpha: pulse });

		entry.label.text = `LOCKING: ${lockerName.toUpperCase()} ${Math.floor(progress * 100)}%`;
		entry.label.style.fill = color;
	}

	const hw = w * 0.5;
	const hh = h * 0.5;
	const halfDiag = Math.sqrt(hw * hw + hh * hh);
	entry.label.position.set(0, -(halfDiag + px(52)));
}

function drawDashedCircle(
	ring: Graphics,
	radius: number,
	color: number,
	width: number,
	alpha: number,
	now: number,
): void {
	const dashCount = 24;
	const gapFraction = 0.4;
	const segmentAngle = (Math.PI * 2) / dashCount;
	const dashAngle = segmentAngle * (1 - gapFraction);
	const rotation = now * 0.001;

	for (let i = 0; i < dashCount; i++) {
		const startAngle = rotation + i * segmentAngle;
		const endAngle = startAngle + dashAngle;
		const x0 = Math.cos(startAngle) * radius;
		const y0 = Math.sin(startAngle) * radius;
		ring.moveTo(x0, y0);

		const steps = 3;
		for (let s = 1; s <= steps; s++) {
			const a = startAngle + (endAngle - startAngle) * (s / steps);
			ring.lineTo(Math.cos(a) * radius, Math.sin(a) * radius);
		}
		ring.stroke({ color, width, alpha });
	}
}
```

Note: `state.beingLockedById` / `state.beingLockedProgress` remain in `PlayerOwnStateMsg` and may still be used elsewhere (HUD alarm, audio cue) — do not remove them from `GameState`. The ring renderer just no longer reads them.

- [ ] **Step 3.8: Typecheck the client**

```bash
cd web-pixi && bun run build && cd ..
```

Expected: no TypeScript errors.

- [ ] **Step 3.9: Commit Task 3**

```bash
git add internal/component/components.go internal/game/components.go internal/game/entity_kinds.go internal/game/system_network.go web-pixi/sdk/ web-pixi/src/effects/being-locked-ring.ts
git commit -m "$(cat <<'EOF'
feat(game): restore being-locked ring for non-local entities

Adds a replicated LockedBy component populated each tick by NetworkSystem
from the existing reverse lock map. Client renders the warning ring on any
entity whose lockerNetID is non-zero, restoring the pre-Phase-2 behavior
for PvP lock indicators on other players.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Regressions 4 + 5 — `ActiveMining` Component

**Goal:** Restore other players' mining laser VFX and the ability-bar mining beam highlight. Add an `ActiveMining` game-state component with scalar fields, populate it whenever a beam state changes (ability-system toggle or mining-system shutoff), and render beams from the replicated state.

**Files:**
- Modify: `internal/component/components.go`
- Modify: `internal/game/components.go`
- Modify: `internal/game/entity_kinds.go`
- Modify: `internal/game/system_mining.go`
- Modify: `internal/game/system_ability.go`
- Modify: `web-pixi/src/effects/mining-laser.ts`
- Modify: `web-pixi/src/ui/ability-bar.ts`
- Regenerate: `web-pixi/sdk/`

---

- [ ] **Step 4.1: Add `ActiveMining` struct to `internal/component/components.go`**

Add after the `MiningLaser` struct (around line 76):

```go
// ActiveMining is a lean replicated game-state component describing whether
// each of a ship's mining beams is currently active and what asteroid it is
// targeting. MiningSystem / AbilitySystem write this on state change.
// MiningLaser remains LocalOnly because it carries an ecs.Entity target ref
// and per-beam timers/cooldowns the client doesn't need.
//
// The target field is named MiningTargetNetID (not TargetNetID) to keep it
// unambiguous on the generated client entity interface.
type ActiveMining struct {
	Beam0Active       bool   `net:"bool"`
	Beam1Active       bool   `net:"bool"`
	MiningTargetNetID uint32 `net:"u32"`
}
```

- [ ] **Step 4.2: Add `ActiveMining` map to `Components` struct**

Edit `internal/game/components.go`. In `Components` struct, add after `MiningLaser`:

```go
	MiningLaser      *ecs.Map1[gamecomp.MiningLaser]
	ActiveMining     *ecs.Map1[gamecomp.ActiveMining]
```

In `NewComponents`, add:

```go
		MiningLaser:      ecs.NewMap1[gamecomp.MiningLaser](world),
		ActiveMining:     ecs.NewMap1[gamecomp.ActiveMining](world),
```

- [ ] **Step 4.3: Register `ActiveMining` on ship kind**

Edit `internal/game/entity_kinds.go`. In `buildShipDef`, add right before the `KindComponentLocalOnly(&def, c.MiningLaser)` line (around line 60):

```go
	mmokit.KindComponent(&def, c.ActiveMining)
	// Local-only components (added after transfer, not serialized)
	mmokit.KindComponentLocalOnly(&def, c.PlayerInput)
	mmokit.KindComponentLocalOnly(&def, c.MiningLaser)
```

NPCs do not currently use `MiningLaser` in the game code, so `ActiveMining` is ship-only for now. If NPC mining is added later, add the same line to `buildNpcDef`.

- [ ] **Step 4.4: Populate `ActiveMining` in the mining system**

Edit `internal/game/system_mining.go`. The current Update loop handles beam shutoff (`beam.Active = false`) but doesn't write `ActiveMining`. At the end of each entity's per-beam processing, sync the component.

Extend the query in the struct (around line 18):

```go
type MiningSystem struct {
	mmokit.SystemBase
	gw       *GameWorld
	entities mmokit.Query[struct {
		Input  *gamecomp.PlayerInput
		Laser  *gamecomp.MiningLaser
		Pos    *mmokit.Position
		Inv    *gamecomp.Inventory
		Active *gamecomp.ActiveMining
	}]
}
```

In `Update`, after the `for i := range laser.Beams` loop completes for an entity, sync the `Active` component. Find the line `// Spawn loot crates for jettisoned cargo (after query iteration)` (currently around line 149) and, just before the end of the `for e, b := range s.entities.All()` loop body (the closing brace that immediately follows the `laser.Beams` loop), add:

```go
		// Sync replicated active-mining state after beam updates.
		active := b.Active
		newBeam0 := laser.Beams[0].Active
		newBeam1 := laser.Beams[1].Active
		var newTarget uint32
		if (newBeam0 || newBeam1) && gw.eng.ECS.Alive(laser.Target) && gw.C.NetworkID.HasAll(laser.Target) {
			newTarget = gw.C.NetworkID.Get(laser.Target).ID
		}
		if active.Beam0Active != newBeam0 || active.Beam1Active != newBeam1 || active.MiningTargetNetID != newTarget {
			gw.eng.Log.Log(CatEconomyMining, "active-mining sync: player=%d beams=[%v,%v] target=%d",
				gw.C.NetworkID.Get(e).ID, newBeam0, newBeam1, newTarget)
		}
		active.Beam0Active = newBeam0
		active.Beam1Active = newBeam1
		active.MiningTargetNetID = newTarget
```

The query bundle change (adding `Active`) also needs the mining system to unpack `active` — adjust the destructuring at the top of the loop:

Replace:

```go
		input, laser, pos, inv := b.Input, b.Laser, b.Pos, b.Inv
```

with:

```go
		input, laser, pos, inv := b.Input, b.Laser, b.Pos, b.Inv
		_ = b.Active // touched below after beam updates
```

(Or just inline `b.Input`, `b.Laser`, etc. — pick whichever is minimal.)

- [ ] **Step 4.5: Populate `ActiveMining` in the ability system on beam toggle**

Edit `internal/game/system_ability.go` around line 270. The toggle branch is:

```go
		if laser.Beams[beamIdx].Active {
			// Toggle off
			laser.Beams[beamIdx].Active = false
			gw.eng.Log.Log(CatEconomyMining, "mining beam off: %d beam=%d", action.casterNetID, beamIdx)
		} else {
			// Toggle on — require lock and validate target is minable
			if !lock.Locked || !gw.eng.ECS.Alive(lock.TargetEntity) || !gw.C.Minable.HasAll(lock.TargetEntity) {
				fired = false
				break
			}
			laser.Beams[beamIdx].Active = true
			laser.Target = lock.TargetEntity
			gw.eng.Log.Log(CatEconomyMining, "mining beam on: %d beam=%d target=%d",
				action.casterNetID, beamIdx, lock.TargetNetID)
		}
```

Append this after the `gw.eng.Log.Log(CatEconomyMining, "mining beam on: %d beam=%d target=%d", ...)` line (still inside the `else` branch) AND after the `gw.eng.Log.Log(CatEconomyMining, "mining beam off: %d beam=%d", ...)` line (inside the `if` branch). The cleanest structure: hoist the sync to a single block after the if/else:

Replace the whole mining-beam-toggle branch (the `if laser.Beams[beamIdx].Active { ... } else { ... }` block) with:

```go
		if laser.Beams[beamIdx].Active {
			// Toggle off
			laser.Beams[beamIdx].Active = false
			gw.eng.Log.Log(CatEconomyMining, "mining beam off: %d beam=%d", action.casterNetID, beamIdx)
		} else {
			// Toggle on — require lock and validate target is minable
			if !lock.Locked || !gw.eng.ECS.Alive(lock.TargetEntity) || !gw.C.Minable.HasAll(lock.TargetEntity) {
				fired = false
				break
			}
			laser.Beams[beamIdx].Active = true
			laser.Target = lock.TargetEntity
			gw.eng.Log.Log(CatEconomyMining, "mining beam on: %d beam=%d target=%d",
				action.casterNetID, beamIdx, lock.TargetNetID)
		}
		// Sync replicated ActiveMining immediately so clients see the toggle
		// on the same tick, without waiting for the next MiningSystem pass.
		if gw.C.ActiveMining.HasAll(entity) {
			am := gw.C.ActiveMining.Get(entity)
			am.Beam0Active = laser.Beams[0].Active
			am.Beam1Active = laser.Beams[1].Active
			if (am.Beam0Active || am.Beam1Active) && gw.eng.ECS.Alive(laser.Target) && gw.C.NetworkID.HasAll(laser.Target) {
				am.MiningTargetNetID = gw.C.NetworkID.Get(laser.Target).ID
			} else {
				am.MiningTargetNetID = 0
			}
		}
```

- [ ] **Step 4.6: Verify the server compiles and vets clean**

```bash
go vet ./...
go test ./internal/game/... -v
```

Expected: clean vet, all tests pass.

- [ ] **Step 4.7: Regenerate the SDK**

```bash
just space-sdk
grep -n "beam0Active\|beam1Active\|miningTargetNetID" web-pixi/sdk/entities.ts
```

Expected: `ShipEntity` gains `beam0Active: boolean;`, `beam1Active: boolean;`, and `miningTargetNetID: number;`. Exact casing is derived from the Go field names via `lcFirst` — confirm in the regenerated file. `TargetLock` is not replicated (no net tags on its fields), so there is no collision with the `MiningTargetNetID` name.

- [ ] **Step 4.8: Update `mining-laser.ts` to render all ships' beams**

Edit `web-pixi/src/effects/mining-laser.ts`. Replace the entire file with:

```ts
import { Container, Graphics } from "pixi.js";
import { px } from "../view";
import type { GameState } from "../state";
import { audio } from "../audio/audio-manager";
import { SoundId } from "../audio/sounds";
import { getShip } from "../entity-accessors";
import type { ClientEntity } from "../types";

/**
 * MiningLaserRenderer draws mining beams between ships and their targets.
 * Reads the replicated ActiveMining component (beam0Active, beam1Active,
 * targetNetId) from every visible ship entity — not just the local player.
 */
export class MiningLaserRenderer {
	private gfx: Graphics;
	private wasMiningLocally = false;

	constructor(parent: Container) {
		this.gfx = new Graphics();
		parent.addChild(this.gfx);
	}

	update(state: GameState, now: number): void {
		this.gfx.clear();

		let localMining = false;
		const pulse = 0.5 + 0.5 * Math.sin(now * 0.01);

		for (const [netID, ent] of state.entities) {
			const ship = getShip(ent);
			if (!ship) continue;
			const beams = extractMiningState(ent);
			if (!beams || (!beams.beam0Active && !beams.beam1Active)) continue;

			const target = state.entities.get(beams.miningTargetNetID);
			if (!target) continue;

			this.gfx
				.moveTo(ent.renderX, ent.renderY)
				.lineTo(target.renderX, target.renderY)
				.stroke({ color: 0x00ff80, width: px(2 + pulse), alpha: 0.4 + pulse * 0.4 });

			if (netID === state.myEntityId) {
				localMining = true;
			}
		}

		if (localMining && !this.wasMiningLocally) {
			audio.loop(SoundId.MiningLaser);
		} else if (!localMining && this.wasMiningLocally) {
			audio.stopLoop(SoundId.MiningLaser);
		}
		this.wasMiningLocally = localMining;
	}
}

interface MiningState {
	beam0Active: boolean;
	beam1Active: boolean;
	miningTargetNetID: number;
}

function extractMiningState(ent: ClientEntity): MiningState | null {
	// Field names match the SDK generator output for ActiveMining. Confirm
	// casing after regen with `grep miningTargetNetID web-pixi/sdk/entities.ts`.
	const e = ent.current as { beam0Active?: boolean; beam1Active?: boolean; miningTargetNetID?: number };
	if (typeof e.beam0Active !== "boolean") return null;
	return {
		beam0Active: !!e.beam0Active,
		beam1Active: !!e.beam1Active,
		miningTargetNetID: e.miningTargetNetID ?? 0,
	};
}
```

- [ ] **Step 4.9: Update `ability-bar.ts` `isMiningBeamActive`**

Edit `web-pixi/src/ui/ability-bar.ts`. Around line 191, replace the stub:

```ts
function isMiningBeamActive(_state: GameState, _slot: number): boolean {
	return false;
}
```

with:

```ts
function isMiningBeamActive(state: GameState, slot: number): boolean {
	const myEnt = state.myEntityId ? state.entities.get(state.myEntityId) : undefined;
	if (!myEnt) return false;
	const e = myEnt.current as { beam0Active?: boolean; beam1Active?: boolean };
	// Slot 1 = W key (weapon1 secondary) → beam0, slot 3 = R key (weapon2 secondary) → beam1.
	if (slot === 1) return !!e.beam0Active;
	if (slot === 3) return !!e.beam1Active;
	return false;
}
```

- [ ] **Step 4.10: Typecheck the client**

```bash
cd web-pixi && bun run build && cd ..
```

Expected: no TypeScript errors.

- [ ] **Step 4.11: Commit Task 4**

```bash
git add internal/component/components.go internal/game/components.go internal/game/entity_kinds.go internal/game/system_mining.go internal/game/system_ability.go web-pixi/sdk/ web-pixi/src/effects/mining-laser.ts web-pixi/src/ui/ability-bar.ts
git commit -m "$(cat <<'EOF'
feat(game): restore mining laser VFX + ability bar highlight

Adds a replicated ActiveMining component (beam flags + target netID)
written by MiningSystem and AbilitySystem whenever beam state changes.
Client renders beams for any ship with active beams — not just the local
player — and the ability-bar mining highlight reads local beam state from
the same replicated component.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Regression 1 — `StatusEffects` Var-Tail Binding

**Goal:** Restore status effect VFX on all entities. Add a custom var-tail binding for the existing `StatusEffects` component that serializes `[uint16 byteLen][count * (u8 type + qnorm duration)]` per tick. Replace the existing `KindComponent(StatusEffects)` registration with `KindComponentWithBinding` using the new binding. Restore `drawStatusEffects` on the client.

**Files:**
- Create: `internal/game/var_tail_bindings.go`
- Modify: `internal/game/entity_kinds.go`
- Modify: `web-pixi/src/effects/ability-effects.ts`
- Regenerate: `web-pixi/sdk/`

---

- [ ] **Step 5.1: Create `internal/game/var_tail_bindings.go` with the status effects binding**

Create the file:

```go
package game

import (
	"sort"

	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/system"
)

// StatusEffectDurationScale is the [0, StatusEffectDurationScale] range used
// to quantize effect duration into a qnorm byte (0-255). 25.5s with 0.1s
// resolution is a reasonable default: ion burn lasts a few seconds, afterburner
// up to ~10s, fortified up to ~15s. Effects with duration > 25.5s saturate.
const StatusEffectDurationScale = 25.5

// NewStatusEffectsBinding returns a ComponentBinding that emits every active
// status effect as a 2-byte record: [u8 type][qnorm duration/StatusEffectDurationScale].
// Inactive slots (i >= Count) are not emitted.
func NewStatusEffectsBinding(m *ecs.Map1[gamecomp.StatusEffects]) system.ComponentBinding {
	return system.VarTailComponent(m, system.VarTailAccessor[gamecomp.StatusEffects]{
		Name:     "statusEffects",
		ItemSize: 2,
		ItemFields: []system.BindingSchemaField{
			{Name: "type", Encoding: "u8", Size: 1},
			{Name: "duration", Encoding: "qnorm", Size: 1, Scale: StatusEffectDurationScale},
		},
		Count: func(se *gamecomp.StatusEffects) int { return int(se.Count) },
		WriteItems: func(se *gamecomp.StatusEffects, w *quantize.SnapshotWriter) {
			for i := uint8(0); i < se.Count; i++ {
				eff := se.Effects[i]
				w.Uint8(uint8(eff.Type))
				// QNorm clamps to [0,1]; scale Duration into that range.
				w.QNorm(eff.Duration / StatusEffectDurationScale)
			}
		},
		HashItems: func(se *gamecomp.StatusEffects, h *system.Hasher) {
			for i := uint8(0); i < se.Count; i++ {
				eff := se.Effects[i]
				h.Uint8(uint8(eff.Type))
				h.Float32(eff.Duration)
			}
		},
	})
}

// NewInventoryBinding returns a ComponentBinding that emits every inventory
// item as an 8-byte record: [u32 itemID][u32 quantity]. Map iteration order
// is sorted by item ID for deterministic hashing.
func NewInventoryBinding(m *ecs.Map1[gamecomp.Inventory]) system.ComponentBinding {
	return system.VarTailComponent(m, system.VarTailAccessor[gamecomp.Inventory]{
		Name:     "items",
		ItemSize: 8,
		ItemFields: []system.BindingSchemaField{
			{Name: "itemId", Encoding: "u32", Size: 4},
			{Name: "quantity", Encoding: "u32", Size: 4},
		},
		Count: func(inv *gamecomp.Inventory) int {
			count := 0
			for _, qty := range inv.Items {
				if qty > 0 {
					count++
				}
			}
			return count
		},
		WriteItems: func(inv *gamecomp.Inventory, w *quantize.SnapshotWriter) {
			keys := sortedInventoryKeys(inv)
			for _, id := range keys {
				qty := inv.Items[id]
				if qty <= 0 {
					continue
				}
				w.Uint32(id)
				w.Uint32(uint32(qty))
			}
		},
		HashItems: func(inv *gamecomp.Inventory, h *system.Hasher) {
			keys := sortedInventoryKeys(inv)
			for _, id := range keys {
				qty := inv.Items[id]
				if qty <= 0 {
					continue
				}
				h.Uint32(id)
				h.Int32(int32(qty))
			}
		},
	})
}

// sortedInventoryKeys returns the inventory's item IDs sorted ascending.
// Used for deterministic iteration when writing/hashing.
func sortedInventoryKeys(inv *gamecomp.Inventory) []uint32 {
	if len(inv.Items) == 0 {
		return nil
	}
	keys := make([]uint32, 0, len(inv.Items))
	for id := range inv.Items {
		keys = append(keys, id)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}
```

- [ ] **Step 5.2: Replace `StatusEffects` registrations in `entity_kinds.go`**

Edit `internal/game/entity_kinds.go`. In `buildShipDef`, replace the existing `mmokit.KindComponent(&def, c.StatusEffects, ...)` block (around line 50) with:

```go
	mmokit.KindComponentWithBinding(&def, c.StatusEffects, NewStatusEffectsBinding(c.StatusEffects),
		mmokit.WithPreMarshal(func(se *gamecomp.StatusEffects) {
			for i := uint8(0); i < se.Count; i++ {
				se.Effects[i].Source = ecs.Entity{}
			}
		}),
	)
```

In `buildNpcDef`, replace `mmokit.KindComponent(&def, c.StatusEffects)` (around line 92) with:

```go
	mmokit.KindComponentWithBinding(&def, c.StatusEffects, NewStatusEffectsBinding(c.StatusEffects))
```

- [ ] **Step 5.3: Verify the server compiles**

```bash
go vet ./...
go test ./internal/game/... -v
```

Expected: clean vet, tests pass.

- [ ] **Step 5.4: Regenerate the SDK**

```bash
just space-sdk
grep -n "statusEffects\|StatusEffectItem" web-pixi/sdk/entities.ts
```

Expected: `ShipEntity` and `NPCEntity` now have `statusEffects: ShipStatusEffectsItem[]` (or similar — note the exact generated type name, e.g. `ShipStatusEffectItem` or `ShipStatuseffectsItem`). Check the actual names and use them in the client code.

- [ ] **Step 5.5: Restore `drawStatusEffects` in `ability-effects.ts`**

Edit `web-pixi/src/effects/ability-effects.ts`. Find `drawStatusEffects` around line 648. Replace the no-op body with logic that iterates every visible entity's `statusEffects` tail and dispatches to the existing drawing helpers.

The existing effect type enum values (from server `internal/component/components.go`):

- `StatusNone = 0`
- `StatusIonBurn = 1`
- `StatusFortified = 2`
- `StatusAfterburner = 3`
- `StatusShieldRegen = 4`

Replace `drawStatusEffects`:

```ts
// Effect type constants mirror internal/component/components.go StatusType.
const STATUS_NONE = 0;
const STATUS_ION_BURN = 1;
const STATUS_FORTIFIED = 2;
const STATUS_AFTERBURNER = 3;
const STATUS_SHIELD_REGEN = 4;

private drawStatusEffects(state: GameState, now: number): void {
	for (const ent of state.entities.values()) {
		// Generated entity interfaces carry statusEffects only on entity kinds
		// that declared the var-tail (ship, npc). Other entities (asteroid,
		// loot crate, station) will not have the field.
		const e = ent.current as { statusEffects?: Array<{ type: number; duration: number }> };
		const effects = e.statusEffects;
		if (!effects || effects.length === 0) continue;

		for (const eff of effects) {
			switch (eff.type) {
				case STATUS_ION_BURN:
					this.drawIonBurnFor(ent, now);
					break;
				case STATUS_FORTIFIED:
					this.drawFortifiedFor(ent, now);
					break;
				case STATUS_AFTERBURNER:
					this.drawAfterburnerFor(ent, now);
					break;
				case STATUS_SHIELD_REGEN:
					this.drawShieldRegenFor(ent, now);
					break;
				case STATUS_NONE:
				default:
					break;
			}
		}
	}
}
```

**This call references helpers `drawIonBurnFor`, `drawFortifiedFor`, `drawAfterburnerFor`, `drawShieldRegenFor`.** The existing file already has the drawing code for these effects around lines 652-720+ but structured as class methods operating on `state` / self-emission context. Adapt each existing helper to take an `(ent: ClientEntity, now: number)` pair by moving the drawing code into a helper that reads the ship's `renderX`/`renderY` instead of the local player's position.

**If the existing helpers already draw by entity (not "self"), rename them to `drawXxxFor` signatures that take `(ent, now)`.** If they draw only for the local player, extract the per-entity drawing into a helper and keep the local-player-only call path for other effects that may rely on it.

Inspect the file to determine exact method names and signatures:

```bash
grep -n "drawIonBurn\|drawFortified\|drawAfterburner\|drawShieldRegen" web-pixi/src/effects/ability-effects.ts
```

If the existing drawing code lives inline inside `drawStatusEffects`, extract each branch into its own method with the `(ent: ClientEntity, now: number)` signature before calling it from the new loop above.

- [ ] **Step 5.6: Typecheck the client**

```bash
cd web-pixi && bun run build && cd ..
```

Expected: no TypeScript errors. If there are missing helper method errors, extract them from the existing `drawStatusEffects` body or from the individual effect drawing code elsewhere in the file.

- [ ] **Step 5.7: Commit Task 5**

```bash
git add internal/game/var_tail_bindings.go internal/game/entity_kinds.go web-pixi/sdk/ web-pixi/src/effects/ability-effects.ts
git commit -m "$(cat <<'EOF'
feat(game): restore status effect VFX on all entities via var-tail

Adds a VarTailComponent binding for StatusEffects that serializes each
active effect as [u8 type][qnorm duration]. Replaces the existing
KindComponent registration on ship and npc with KindComponentWithBinding.
Client iterates every visible entity's statusEffects tail and dispatches
to the existing VFX drawing helpers.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Regression 2 — Loot Crate `Inventory` Var-Tail Binding

**Goal:** Restore loot crate inventory preview on target highlight and per-item buttons in the loot popup. Replace the loot crate's `Inventory` registration with `KindComponentWithBinding` using the already-written `NewInventoryBinding` from Task 5. Update the client to read the decoded `items` array.

**Files:**
- Modify: `internal/game/entity_kinds.go`
- Modify: `web-pixi/src/effects/target-highlight.ts`
- Modify: `web-pixi/src/ui/loot-popup.ts`
- Regenerate: `web-pixi/sdk/`

---

- [ ] **Step 6.1: Replace loot crate `Inventory` registration**

Edit `internal/game/entity_kinds.go`. In `buildLootCrateDef` (around line 96), replace:

```go
	mmokit.KindComponent(&def, c.Inventory,
		mmokit.WithMarshal(MarshalInventory, UnmarshalInventoryInto),
	)
```

with:

```go
	mmokit.KindComponentWithBinding(&def, c.Inventory, NewInventoryBinding(c.Inventory),
		mmokit.WithMarshal(MarshalInventory, UnmarshalInventoryInto),
	)
```

**Important**: leave the ship's `Inventory` registration in `buildShipDef` alone. It stays on `KindComponent` with its existing reflection binding (which produces zero wire fields because `Inventory` has no `net:"..."` tags — effectively a no-op for ship replication, but the `KindComponent` still wires up cross-node transfer via `WithMarshal`). The ship's inventory reaches its own player through `PlayerOwnStateMsg`; other players don't need to see ship inventories.

- [ ] **Step 6.2: Verify the server compiles**

```bash
go vet ./...
go test ./internal/game/... -v
```

Expected: clean vet, tests pass.

- [ ] **Step 6.3: Regenerate the SDK**

```bash
just space-sdk
grep -n "items\|LootCrateItem" web-pixi/sdk/entities.ts
```

Expected: `LootCrateEntity` gains `items: LootCrateItemsItem[]` (or similar — confirm generator's exact item type name).

- [ ] **Step 6.4: Restore loot crate preview in `target-highlight.ts`**

Edit `web-pixi/src/effects/target-highlight.ts`. Around line 59-68 (the loot crate section), replace the placeholder label logic with code that reads the `items` array.

Find the lines that set the loot crate ring + label + `sublabel.visible = false`, and change them to build a sublabel from the item list. Example replacement (adjust to match the actual surrounding structure):

```ts
// Loot crate target highlight
} else if (crate) {
	this.ring.clear();
	this.ring
		.circle(0, 0, baseRadius)
		.stroke({ color: 0xffcc00, width: px(2), alpha: 0.8 });
	this.label.text = "LOOT CRATE";
	this.label.style.fill = 0xffcc00;

	// Item preview from replicated inventory.
	const items = (crate.current as { items?: Array<{ itemId: number; quantity: number }> }).items ?? [];
	if (items.length > 0) {
		const lines = items.slice(0, 5).map((it) => {
			const def = state.itemDefs.get(it.itemId);
			const name = def?.name ?? `item${it.itemId}`;
			return `${name} x${it.quantity}`;
		});
		if (items.length > 5) lines.push(`+${items.length - 5} more`);
		this.sublabel.text = lines.join("\n");
		this.sublabel.visible = true;
	} else {
		this.sublabel.visible = false;
	}
}
```

**Adapt to actual variable names in the file.** The variable for the loot crate entity is likely `crate` or `lootEnt`; the field access pattern matches Task 3/4. Inspect the current file to confirm:

```bash
grep -n "LOOT CRATE\|sublabel" web-pixi/src/effects/target-highlight.ts
```

- [ ] **Step 6.5: Restore per-item buttons in `loot-popup.ts`**

Edit `web-pixi/src/ui/loot-popup.ts`. Around line 200-206, replace the "Contents unknown" placeholder. Find the crate lookup via `state.lootCrateId` and rebuild the per-item UI from the decoded `items` array.

Locate where `itemsContainer` is populated (around line 116-119 where the old code was disabled):

```bash
grep -n "itemsContainer\|Contents unknown\|lootCrateId" web-pixi/src/ui/loot-popup.ts
```

Replace the placeholder with:

```ts
// Read the replicated inventory from the targeted crate entity.
const crateEnt = state.lootCrateId ? state.entities.get(state.lootCrateId) : undefined;
const items = crateEnt
	? (crateEnt.current as { items?: Array<{ itemId: number; quantity: number }> }).items ?? []
	: [];

if (items.length === 0) {
	const emptyEl = document.createElement("div");
	emptyEl.className = "loot-popup-empty";
	emptyEl.textContent = "Empty";
	itemsContainer!.appendChild(emptyEl);
} else {
	for (const item of items) {
		const def = state.itemDefs.get(item.itemId);
		const itemName = def?.name ?? `item${item.itemId}`;
		const btn = document.createElement("button");
		btn.className = "loot-popup-item";
		btn.textContent = `${itemName} x${item.quantity}`;
		btn.addEventListener("click", () => {
			state.client?.sendLootItem({
				crateId: state.lootCrateId,
				itemId: item.itemId,
				quantity: item.quantity,
			});
		});
		itemsContainer!.appendChild(btn);
	}
}
```

**Verify `sendLootItem` exists on the SDK client.** If the current SDK doesn't expose it, either:

1. Find the existing loot single-item request method (`grep "sendLoot" web-pixi/sdk/client.ts`) and use its real name.
2. Or, if only `sendLootAll` exists, leave the per-item buttons functional but wire them to the same method with extra client-side filtering (not ideal).
3. Or, if neither works, fall back to displaying item names only (non-interactive) — keep the LOOT ALL button as the single loot action. In that case, make the items spans instead of buttons, but still show the contents.

The minimum requirement for this task is that the loot popup shows the actual item list, even if per-item buttons aren't wired. Full per-item interactivity is a bonus.

- [ ] **Step 6.6: Typecheck the client**

```bash
cd web-pixi && bun run build && cd ..
```

Expected: no TypeScript errors.

- [ ] **Step 6.7: Commit Task 6**

```bash
git add internal/game/entity_kinds.go web-pixi/sdk/ web-pixi/src/effects/target-highlight.ts web-pixi/src/ui/loot-popup.ts
git commit -m "$(cat <<'EOF'
feat(game): restore loot crate inventory preview + popup via var-tail

Replaces loot crate Inventory registration with KindComponentWithBinding
using the generic NewInventoryBinding (u32 itemId + u32 qty per item).
Client target highlight shows the item list inline, and the loot popup
rebuilds per-item buttons from the decoded array.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Final Verification Pass

**Goal:** End-to-end validation that all five regressions are restored without breaking existing functionality.

**Files:** None modified (verification only, unless issues surface).

---

- [ ] **Step 7.1: Clean build + vet + test**

```bash
go vet ./...
go test ./... -count=1
just build
```

Expected: all clean. If `go test` surfaces integration failures in `internal/game/` related to entity spawning or replication, investigate — the most likely cause is a spawn test that didn't expect the new `LockedBy` / `ActiveMining` components to be auto-added.

- [ ] **Step 7.2: Client build**

```bash
cd web-pixi && bun run build && cd ..
```

Expected: clean build, no TypeScript errors.

- [ ] **Step 7.3: Start a dev server for manual smoke testing**

```bash
just dev
```

Open two browser windows at `http://localhost:8080` and log in with two different usernames. Verify each regression:

- [ ] **Step 7.4: Manual test — status effect VFX**

1. Player A fires an ability that applies ion burn to Player B (or use `/setstatus` admin console command if available).
2. Player A and Player B both see the purple ion-burn overlay on Player B's ship.
3. Apply fortified or afterburner to Player A.
4. Player B sees the corresponding VFX on Player A.

Expected: all status VFX visible on both local and remote entities.

- [ ] **Step 7.5: Manual test — being-locked ring on other players**

1. Player A target-locks Player B.
2. Player B sees the red/orange ring around their own ship (this already worked via PlayerOwnStateMsg).
3. A third observer (bot or additional window) sees the same ring around Player B's ship.

Expected: ring visible on non-local players being locked.

- [ ] **Step 7.6: Manual test — other players' mining beams**

1. Player A locks an asteroid and toggles on a mining beam.
2. Player B, who has Player A in their AoI, sees the green beam from Player A's ship to the asteroid.
3. Player A toggles the beam off; Player B's view stops rendering the beam.

Expected: mining beams visible for non-local ships.

- [ ] **Step 7.7: Manual test — ability bar mining highlight**

1. Player A activates a mining beam on slot 1 (W key).
2. Player A's ability bar slot 1 shows the highlight state (not the red "out of range" overlay).
3. Player A deactivates the beam. The highlight clears.

Expected: ability bar reflects the local player's mining state without a server round-trip delay.

- [ ] **Step 7.8: Manual test — loot crate inventory preview + popup**

1. Player A kills an NPC or jettisons cargo to spawn a loot crate.
2. Player A targets the loot crate. The target-highlight tooltip shows the item list (e.g. "iron x10", "copper x5").
3. Player A flies to the crate and opens the loot popup.
4. The popup shows per-item buttons (or item names) — not "Contents unknown".
5. LOOT ALL button still works.

Expected: contents visible in both the preview and the popup.

- [ ] **Step 7.9: Cross-cutting test — existing functionality intact**

1. Two-player combat still works (damage, shields, death).
2. Cross-node transfer still works (fly across a cell boundary and confirm entities re-render).
3. PlayerOwnStateMsg still delivers own cargo, equipment, ability cooldowns, and beingLocked state.
4. Cell topology overlay (if enabled) still renders.
5. Marketplace still works.
6. Docking still works.

Expected: no regressions in existing features.

- [ ] **Step 7.10: Stop the dev server and assess**

If any manual test failed, open a follow-up task to fix the specific regression and re-verify. Document any known issues in a final commit message. If all pass:

- [ ] **Step 7.11: Sanity-check the commit history**

```bash
git log --oneline feature/web-pixi-sdk-modernization ^main
```

Expected: 6 original migration commits + 6 new commits (one per task 1-6). No merge commits.

- [ ] **Step 7.12: No additional commit for Task 7**

Verification-only task. No new commits unless a follow-up fix was needed.

---

## Out of scope (do not add to this plan)

- Array-valued struct field reflection in `auto_replicator.go` (rejected during brainstorming in favor of var-tail + scalar split).
- `OP_INSPECT_CRATE` operation-based loot crate inspection (rejected; var-tail is cheaper and avoids the latency beat on target hover).
- Replicating full `MiningLaser` state (timers, cooldowns, heat) — client doesn't need it.
- Replicating `StatusEffect.Value` or `.Source` — client doesn't need effect magnitudes or source references.
- Adding `ActiveMining` to NPCs (NPCs don't currently mine; add later if/when NPC mining is implemented).
- Status effect VFX on asteroids or loot crates (no game logic applies status effects to them).

## Success criteria

- All five regressions verified fixed in a live two-player session (Task 7 steps 7.4 through 7.8).
- `go vet ./...`, `go test ./...`, `just build`, and `bun run build` all clean.
- New unit tests in `pkg/system/var_tail_binding_test.go` pass.
- No changes to wire format for entities not touched by this work (asteroid, station).
- Commit history is clean: 6 task commits stacked on `feature/web-pixi-sdk-modernization`.
