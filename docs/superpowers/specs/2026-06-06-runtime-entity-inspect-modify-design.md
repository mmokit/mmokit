# Runtime Entity Inspect & Modify

**Date:** 2026-06-06
**Status:** Design — pending implementation plan

## Summary

Add the ability to inspect and mutate a live entity's components at runtime, by
network ID, from both the server console and the admin UI. Two new cmdsys verbs:

- **`entity.inspect <netid>`** — list every component on an entity and the value
  of each field.
- **`entity.modify <netid> <component> <field> <value>`** — set a single scalar
  field on a component to a new value.

Both are generic — they work for any registered entity kind with zero
per-component code, by reflecting over the bundle components already declared via
`mmokit.RegisterKind[T]`. The admin SPA gains an **Entities** page that lists live
entities and opens an inspect/edit drawer over the same verbs.

This is a developer/operator tuning tool: it writes raw component fields with no
gameplay validation or clamping. Changes propagate to clients automatically via
the normal per-tick replication diff.

## Motivation

Today, tweaking a single entity's state at runtime requires either a hardcoded
command (like `player.heal`'s manual `mmokit.Get[Health]`) or a server restart
with edited config. There is no generic way to look at *what* components an
entity has, see their field values, or poke one value to reproduce a bug or tune
behavior live. `config get/set` does this for global `GameConfig`; this feature
is the per-entity analog.

## Existing infrastructure (what we build on)

- **`entity.*` command group** — [pkg/universe/builtins_entity.go](../../../pkg/universe/builtins_entity.go)
  already registers `entity.spawn/despawn/list/tp/summary`. The new verbs join it.
- **`RouteEntityOwner`** — [pkg/universe/cmdsys_resolver.go](../../../pkg/universe/cmdsys_resolver.go)
  auto-extracts the `NetID` field from a command's Args struct and routes the
  command to the host owning that entity. Works transparently in single-process
  and distributed mode.
- **`Stage.LookupNetID(netID) (ecs.Entity, Presence, bool)`** — resolves a netID
  to an ECS entity on a stage. We require `PresenceLive` (reject replicas).
- **Kind registration reflection walk** — [pkg/mmokit/kindreg.go](../../../pkg/mmokit/kindreg.go)
  `RegisterKind[T]` already walks each bundle's `*Component` fields with `T` in
  generic scope. We extend that walk to also capture a runtime accessor per
  component.
- **`CmdOnLoop` / `RunOnLoop`** — run handler bodies on the game-loop goroutine
  for thread-safe ECS access.
- **Replication** — [pkg/system/](../../../pkg/system/) replication uses per-tick
  hash-diff detection, so a field mutated on the live entity is picked up and
  pushed to clients/border replicas with no special handling.

The one gap: there is no generic "enumerate all components on an entity and
read/write a field by string path." `mmokit.Get[T]` is generic and needs `T` at
compile time. This design fills that gap.

## Scope

**In scope (v1):**

- Inspect: every component declared in the entity's kind bundle (including
  `mmokit:"local"` components), flattened to scalar leaf fields by dotted path.
- Modify: any scalar leaf reachable by a dotted path through nested structs —
  e.g. `Health.Current`, `ShipControl.MaxSpeed`, `Foo.Bar.Baz`. Coercion
  `string → int/uint/float/bool/string`; enums set by their numeric value.
- Console: both verbs, table-rendered.
- Admin UI: an Entities list page + inspect/edit drawer over the same verbs.

**Out of scope (v1):**

- **Slice / map / custom-marshaled fields are read-only** — inspect renders them
  as a JSON summary string; modify rejects them. (A future `entity.patch <netid>
  <component> <json>` whole-component round-trip can cover these.)
- **Components not declared in the kind bundle** — dynamically added components
  outside the bundle won't appear. Bundles are intended to be comprehensive;
  this is noted as a known limitation, not handled.
- No gameplay validation, clamping, or derived-stat re-sync. (See Risks.)
- No live SSE entity stream in the admin UI — on-demand fetch/refresh only.

## Architecture

Three layers, bottom-up:

### 1. Component accessor metadata (engine)

Extend the kind-registration walk so each `EntityKindDef` can yield, bound to a
world, the set of components an entity of that kind carries:

```go
// pkg/universe/entity_kind.go (or alongside the existing kind metadata)

type ComponentAccessor struct {
    Name string          // component type name, e.g. "Health"
    Type reflect.Type    // the component struct type, e.g. gamecomp.Health
    Get  func(ecs.Entity) (any, bool) // returns *Health as any, false if absent
}

// EntityKindDef gains:
func (d *EntityKindDef) ComponentAccessors(w *ecs.World) []ComponentAccessor
```

`RegisterKind[T]` already iterates the bundle's fields with each component's
concrete type `C` in generic scope. For each, we additionally capture a builder
closure `func(w *ecs.World) ComponentAccessor` that constructs
`ecs.NewMap1[C](w)` and returns an accessor whose `Get` does `m.HasAll` + `m.Get`
and returns the `*C` as `any`. The builders are stored on the kind def and
realized per-stage (the world is per-stage) on demand. `mmokit:"local"` fields
get accessors too — the server-side tool inspects everything.

### 2. Generic reflect field-walker (engine, reusable)

A small standalone helper — the one primitive both verbs share. Candidate
location: `pkg/mmokit/fieldpath.go` (game-adjacent reflection already lives in
mmokit) or `pkg/engine/`.

```go
type FieldInfo struct {
    Path     string  // dotted path within the component, e.g. "Current" or "Sub.Max"
    Type     string  // human type, e.g. "int32", "float32", "bool", "string", "[]Item"
    Value    string  // rendered value (scalars: literal; non-scalar: JSON summary)
    Editable bool    // true for coercible scalar leaves; false for slice/map/custom
}

// ListFields reflects over *component (any), producing one FieldInfo per scalar
// leaf (recursing through nested structs) plus one read-only entry per
// slice/map/unsupported field rendered as JSON.
func ListFields(component any) []FieldInfo

// SetFieldByPath walks the dotted path to a settable scalar leaf, coerces
// strVal to the leaf's kind, sets it, and returns the rendered old/new values.
// Errors: unknown path, non-settable/non-scalar leaf, uncoercible value.
func SetFieldByPath(component any, path, strVal string) (old, new string, err error)
```

Coercion supports `bool`, all int widths, all uint widths, `float32/64`, and
`string`. Named scalar types (enums defined as `type X int32`) are set by their
underlying numeric value. Pointers/interfaces/nested structs are traversed;
slices/maps/arrays/custom-marshaled fields are surfaced read-only by `ListFields`
and rejected by `SetFieldByPath`.

### 3. The two cmdsys verbs (universe)

Registered in [pkg/universe/builtins_entity.go](../../../pkg/universe/builtins_entity.go)
next to the existing entity verbs. Both use `RouteEntityOwner` and run their
bodies on the loop.

```go
// entity.inspect
type entityInspectArgs struct {
    NetID uint32 `cmd:"help=entity network ID"`
}
type entityInspectRow struct {
    Component string
    Field     string // dotted path WITHIN the component (maps directly to modify)
    Type      string
    Value     string
    Editable  bool
}
type entityInspectResult struct {
    NetID      uint32
    Kind       string
    Components []entityInspectRow `cmd:"table"`
}
```

Handler: resolve via `stage.LookupNetID(netID)`, require `PresenceLive`; get the
entity's kind; for each `ComponentAccessor` of that kind, `Get` the component and
`ListFields` it, emitting `{Component: accessor.Name, Field: fi.Path, ...}` rows.

```go
// entity.modify
type entityModifyArgs struct {
    NetID     uint32 `cmd:"help=entity network ID"`
    Component string `cmd:"help=component name, e.g. Health"`
    Field     string `cmd:"help=dotted field path within the component, e.g. Current"`
    Value     string `cmd:"help=new value (coerced to the field's type)"`
}
type entityModifyResult struct {
    NetID     uint32
    Component string
    Field     string
    Old       string
    New       string
}
```

Handler: resolve entity (live), find the accessor whose `Name == Component`, `Get`
the component, `SetFieldByPath(component, Field, Value)`. Because `Get` returns a
pointer into ECS storage, the mutation is in place — no write-back needed. Errors
map to clear messages (unknown component, unknown field, read-only field,
bad value).

All required args are positional per the project's command-arg convention.

## Admin UI

A new **Entities** page in the **ENGINE** sidebar group. No new backend beyond
the two verbs above plus the existing `entity.list` / `entity.despawn`.

### `web-admin/src/routes/entities.svelte` (list)

- Fetches on demand via `POST /admin/api/commands/entity.list` (existing,
  `RouteAllHosts`). Entity counts can be large, so **no live SSE topic in v1** —
  a **Refresh** button + cell-filter + kind-filter + text search. This matches
  the on-demand convention used elsewhere for potentially large lists.
- Table columns: NetID, Kind, Cell, X, Y. Row click opens the drawer.
- Wire into [app.svelte](../../../web-admin/src/app.svelte) route switch and
  [Sidebar.svelte](../../../web-admin/src/components/Sidebar.svelte) `builtinItems`
  (ENGINE group, after Tunables).

### `web-admin/src/components/EntityDrawer.svelte` (inspect + edit)

- On open: `POST /admin/api/commands/entity.inspect {NetID}`; render rows grouped
  by `Component`.
- Editing follows the **WorldInspector dirty-buffer pattern**
  ([WorldInspector.svelte](../../../web-admin/src/components/WorldInspector.svelte)):
  a local `dirty` record keyed by `"<Component>/<Field>"`. Each editable row is an
  inline input whose widget is chosen from the row's `Type` (number / text /
  checkbox). Read-only rows render their JSON `Value`, disabled.
- **Apply** POSTs `entity.modify` **once per dirty field** (sequential), so each
  change is individually audited and 1:1 with the console verb; then re-inspects
  to refresh. **Discard** clears the buffer.
- **Despawn** button reuses the existing `entity.despawn` verb, gated behind
  `ConfirmDialog` (no browser-native dialogs).
- All command calls use the standard `apiPost("/admin/api/commands/<verb>", …)`
  helper and handle the `{ok, result, error}` envelope.

### Command palette

Add an `entity` entry type to
[CommandPalette.svelte](../../../web-admin/src/components/CommandPalette.svelte) so
⌘K search can navigate to `/entities` and open the drawer via the `pendingNav`
signal — consistent with how cells/players are handled.

## Data flow

```
console:  entity modify 42 Health Current 50
              │ cmdsys Dispatcher (RouteEntityOwner → host owning netID 42)
              ▼
          handler (on owning host, on game loop)
              │ stage.LookupNetID(42) → live entity
              │ accessor("Health").Get(entity) → *Health
              │ SetFieldByPath(*Health, "Current", "50")
              ▼
          *Health.Current = 50  (in-place in ECS storage)
              │ next tick: replication hash-diff detects change
              ▼
          clients & border replicas receive the new value

admin UI: POST /admin/api/commands/entity.modify {NetID,Component,Field,Value}
          → same dispatcher path → same handler → audit-logged automatically
```

## Error handling

- **Entity not found / not live** — both verbs return a clear error
  (`entity <netid> not found or not live`). Replicas are rejected — only the
  authoritative host mutates.
- **Unknown component** — `entity.modify` lists available component names in the
  error.
- **Unknown / non-scalar / read-only field** — explicit error; `entity.inspect`
  is the discovery path.
- **Uncoercible value** — error names the target type
  (`cannot parse "abc" as int32`).
- **RBAC** — both verbs declare capabilities (`entity.inspect`, `entity.modify`);
  the admin UI surfaces RBAC errors via the existing `ApiError` flow.
- Every invocation is audit-logged automatically (cmdsys convention).

## Testing

- **`fieldpath` unit tests** — table-driven over a fixture struct with nested
  structs, every scalar kind, an enum type, a pointer field, and a
  slice/map/custom field. Assert `ListFields` output (paths, types, editability)
  and `SetFieldByPath` for: each scalar coercion, nested path, unknown path,
  read-only-field rejection, and bad-value rejection.
- **Component accessor test** — register a small kind via `RegisterKind`, spawn,
  assert `ComponentAccessors(world)` returns one accessor per bundle component
  (including a `mmokit:"local"` one) and that each `Get` returns the live
  pointer.
- **Verb integration test** — in a single-process universe fixture, spawn an
  entity, `entity.inspect` it (assert rows for a known component), `entity.modify`
  a field, then re-inspect and assert the new value. Assert modify on a replica /
  unknown netID errors.
- **Distributed smoke** — manual: in `examples/4node-basic just distributed`,
  spawn bots, `entity.modify` one across a remote host, confirm via re-inspect
  (exercises `RouteEntityOwner` to a remote host).

## Risks & limitations

- **No validation / derived-stat re-sync.** Setting a raw field can desync
  derived state (cf. the derived-stat-caching pitfall: some component values are
  copied from config/equipment at spawn and re-synced via `ApplyEquipmentStats`).
  This is an operator tool — the contract is "you set exactly what you typed."
  Documented in the verb help text.
- **Bundle-only visibility.** Components added outside the kind bundle won't
  appear (v1 limitation).
- **Read-only complex fields.** Slices/maps/custom-marshaled fields (e.g.
  Inventory) are inspectable but not settable; a future `entity.patch` JSON
  round-trip is the planned complement.
- **Per-field UI apply.** The drawer POSTs one `entity.modify` per dirty field;
  there is no transactional multi-field set. Acceptable for a tuning tool and
  keeps audit entries granular.

## Future extensions (not in this spec)

- `entity.patch <netid> <component> <json>` — whole-component JSON round-trip to
  cover slices/maps/custom-marshaled fields.
- Live SSE entity topic (per-cell) for the admin list if on-demand fetch proves
  insufficient.
- Optional value-validation hooks per component kind.
