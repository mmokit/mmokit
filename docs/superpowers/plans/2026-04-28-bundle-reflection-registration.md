# Bundle-Reflection Entity Kind Registration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate the per-component duplication between bundle struct fields and `Field[T]()` registration calls. Make `RegisterKind[T]` walk the bundle struct via reflection so the struct *is* the spec — no parallel field list. Keep `KindComponent[T]` as a typed convenience that delegates to the same type-erased core. Migrate `internal/game/` and the demo to the bundle pattern uniformly.

**Architecture:** The blocker today is that two registry layers (`pkg/universe.RegisterComponent` for the cross-cell transfer codec, `pkg/system.Component` for the AutoReplicator binding) are parametrized on `T` and capture a typed `*ecs.Map1[T]` in their closures. This refactor type-erases both: registries store `(ecs.ID, reflect.Type)` and access components via `World.Unsafe().Get(entity, id) → unsafe.Pointer`, round-tripped to typed pointers via `(*T)(ptr)` or `reflect.NewAt(t, ptr)`. The typed `KindComponent[T]` / `RegisterComponent[T]` APIs become thin generic wrappers over the type-erased core. `RegisterKind[T]` walks `reflect.TypeFor[T]()`'s exported `*ComponentType` fields, resolves each via `ecs.TypeID(w, t)` (auto-registers in ark), and calls the type-erased core directly — no compile-time `T` per field needed.

`ComponentOption[T]` (WithMarshal, WithPreMarshal) becomes type-erased internally: each generic constructor wraps its typed closure in a `func(unsafe.Pointer)` adapter once at call time. Var-tail bindings (`StatusEffectsBinding`, `InventoryBinding`) carry their own typed closures and are passed as overrides via `WithField[T](mmokit.WithBinding(...))` — these stay explicit per-component because they encode business logic that reflection cannot infer.

**Tech Stack:** Go 1.24+, ark v0.7.1 ECS (typed `Map1[T]` + type-erased `Unsafe.Get(entity, id) → unsafe.Pointer` + `ecs.TypeID(w, reflect.Type) → ID` with auto-registration), `reflect.NewAt`, `unsafe.Pointer`.

---

## File Map

**Modify:**
- `pkg/universe/component_registry.go` — type-erase `RegisterComponent`, internal `componentReplicator` closures move to `(ecs.ID, reflect.Type)`. ComponentOption becomes a type-erased internal struct with generic constructors.
- `pkg/universe/entity_kind.go` — `KindComponent[T]` and `KindComponentLocalOnly[T]` become thin generic wrappers around a new type-erased `kindComponentByID(def, w, id, type, opts...)`. Drop `AddKindComponentByID` (replaced by the type-erased path with a `localOnly` flag).
- `pkg/system/auto_replicator.go` — `Component[T]` becomes a thin generic wrapper. The `reflectBinding[T]` struct loses its `T` parameter and stores `(ecs.ID, reflect.Type)`; per-tick reads use `(*T-shaped-via-reflect)(unsafe.Pointer)`. `varTailBinding[T]` keeps its generic — var-tail providers are explicit closures and don't gain anything from erasure. (Note: `varTailBinding` is constructed by `VarTailComponent[T]` which is called by per-game code building custom bindings; keep it generic.)
- `pkg/mmokit/kindreg.go` — `RegisterKind[T]` walks the bundle struct via reflection, calls type-erased registry. Drop `Field[T]()`, `FieldWithBinding[T]()`, `FieldLocalOnly[T]()`. Add `WithField[T](opts ...ComponentOption[T])` for per-component overrides (binding, marshal, premarshal, local-only). Add struct tag support: `mmokit:"local"` for local-only, `mmokit:"-"` to exclude.
- `pkg/mmokit/mmokit.go` — re-export `KindComponent`, `KindComponentLocalOnly`, `KindComponentWithBinding`, `WithField`, `WithMarshal`, `WithPreMarshal`, `WithBinding`. Drop re-exports of `Field`, `FieldWithBinding`, `FieldLocalOnly`.
- `internal/game/entity_kinds.go` — replace each `buildXxxDef()` with bundle-driven `RegisterKind[XxxBundle](mmo, ...)`. Define bundle structs alongside (or in `internal/game/components.go` near the existing `Components` struct). `RegisterGlobalTransferComponents` keeps its current shape (it registers Velocity + Rotation directly to the registry, not via bundles).
- `examples/4node-basic/components.go` — bundle structs already defined; just confirm they don't need adjustment after the field-list change.
- `examples/4node-basic/main.go` — drop the 7 `mmokit.Field[T]()` calls in the two `RegisterKind` blocks.
- `examples/4node-basic/mesh_e2e_test.go` — same.

**Create:** none. Whole refactor lives in existing files.

**Test:**
- `pkg/universe/component_registry_test.go` (new file if absent, otherwise extend) — exercise type-erased path: register a component by reflect.Type, transfer it across cells via `serializeEntity`/`SpawnFromTransferCore`, verify round-trip.
- `pkg/system/auto_replicator_test.go` — confirm `reflectBinding` driven by `(ecs.ID, reflect.Type)` produces identical output to the typed path. (Snapshot bytes equality is the right assertion.)
- `pkg/mmokit/kindreg_test.go` — extend to verify `RegisterKind[T]` works with no `Field()` calls; verify `WithField[T](opts...)` overrides are applied; verify `mmokit:"local"` struct tag opts component out of transfer codec.
- Existing tests carry the regression load: `pkg/universe/s7_*_test.go` (split/merge/migrate exercise the transfer codec), `pkg/universe/handoff_test.go`, `examples/4node-basic/mesh_e2e_test.go` (full e2e through the demo).

---

## Phase 1: Type-erase the universe transfer registry

### Task 1: Add type-erased ComponentOption representation

**Files:**
- Modify: `pkg/universe/component_registry.go`

The current `ComponentOption[T]` is a typed struct that the typed `RegisterComponent[T]` consumes. We need an internal type-erased form so the new `RegisterComponentByID` can accept opts that were built generically.

- [ ] **Step 1: Add the type-erased internal option struct**

In `pkg/universe/component_registry.go`, alongside the existing `ComponentOption[T]`:

```go
// erasedComponentOpts is the type-erased internal representation of a
// per-component option set. Generic constructors (WithMarshal, WithPreMarshal)
// build one of these by wrapping their typed closures in unsafe.Pointer
// adapters once, at construction time. The registry reads only this form.
type erasedComponentOpts struct {
    // marshal serializes the component at the given pointer (pointer to T).
    // nil = use default reflection marshal.
    marshal func(p unsafe.Pointer) ([]byte, error)
    // unmarshalInto writes from bytes into the component at the given pointer.
    // nil = use default reflection unmarshal.
    unmarshalInto func(b []byte, p unsafe.Pointer) error
    // preMarshal mutates the component at the given pointer before serialization.
    // Called for every cross-cell handoff. nil = no-op.
    preMarshal func(p unsafe.Pointer)
}
```

Add `import "unsafe"` if absent.

- [ ] **Step 2: Reshape ComponentOption[T] to build erasedComponentOpts**

Replace the existing `ComponentOption[T]` definition. It becomes a thin builder function whose only output is an `erasedComponentOpts`-mutating closure. The **public** API stays generic (callers continue to write `WithMarshal[T](m, um)`), but the internal representation is type-erased:

```go
// ComponentOption configures per-component behavior at registration time.
// Constructed via WithMarshal, WithPreMarshal, etc. Internally type-erased
// so the universe registry layer never carries T.
type ComponentOption struct {
    apply func(*erasedComponentOpts)
}

// WithMarshal registers custom serialization for component type T.
// marshal returns the wire bytes for an existing component value.
// unmarshalInto writes into an existing component (for transfer-receive).
func WithMarshal[T any](
    marshal func(*T) ([]byte, error),
    unmarshalInto func([]byte, *T) error,
) ComponentOption {
    return ComponentOption{apply: func(o *erasedComponentOpts) {
        o.marshal = func(p unsafe.Pointer) ([]byte, error) { return marshal((*T)(p)) }
        o.unmarshalInto = func(b []byte, p unsafe.Pointer) error { return unmarshalInto(b, (*T)(p)) }
    }}
}

// WithPreMarshal registers a sanitization hook called on the component just
// before cross-cell serialization. Use to clear local-only fields like
// ecs.Entity references that wouldn't be valid on the destination cell.
func WithPreMarshal[T any](fn func(*T)) ComponentOption {
    return ComponentOption{apply: func(o *erasedComponentOpts) {
        o.preMarshal = func(p unsafe.Pointer) { fn((*T)(p)) }
    }}
}
```

- [ ] **Step 3: Run vet to confirm signature change compiles in isolation**

```bash
go vet ./pkg/universe/...
```

Expected: errors at every call site of `WithMarshal[T]` and `WithPreMarshal[T]` because callers used to receive `ComponentOption[T]` which was generic-typed. The new `ComponentOption` is non-generic. We'll fix call sites in Task 2/Phase 3.

- [ ] **Step 4: Commit (broken state OK — phase 1 lands as one logical change)**

Skip the commit here; the phase commits as a unit at the end of Task 4.

### Task 2: Add type-erased RegisterComponentByID + rewrite RegisterComponent[T] as a wrapper

**Files:**
- Modify: `pkg/universe/component_registry.go`

The existing `RegisterComponent[T](reg, m)` closure-captures `m *ecs.Map1[T]` and uses `m.Get(entity)` and `m.Add(entity, &val)`. The new core takes `(reg, w, id, t)` and uses `w.Unsafe().Get(entity, id)` + `w.Unsafe().Add(entity, id)`.

- [ ] **Step 1: Read the current RegisterComponent + componentReplicator implementation**

```bash
grep -n "RegisterComponent\|componentReplicator" pkg/universe/component_registry.go
```

Make sure you understand: which closures consume the typed `m`, and which are pure on `(entity, payload []byte)`.

- [ ] **Step 2: Add RegisterComponentByID alongside the existing RegisterComponent**

```go
// RegisterComponentByID is the type-erased counterpart to RegisterComponent.
// All transfer codec access goes through World.Unsafe() — no typed Map1.
// Used by mmokit.RegisterKind[T] which discovers components via reflection.
func RegisterComponentByID(
    reg *ReplicationRegistry,
    w *ecs.World,
    id ecs.ID,
    t reflect.Type,
    opts ...ComponentOption,
) {
    var o erasedComponentOpts
    for _, opt := range opts {
        opt.apply(&o)
    }
    u := w.Unsafe()
    rep := componentReplicator{
        id:        id,
        typ:       t,
        scan: func(entity ecs.Entity, w_ *ecs.World) ([]byte, bool) {
            if !u.Has(entity, id) {
                return nil, false
            }
            ptr := u.Get(entity, id)
            if o.preMarshal != nil {
                o.preMarshal(ptr)
            }
            if o.marshal != nil {
                b, err := o.marshal(ptr)
                if err != nil {
                    panic(fmt.Sprintf("custom marshal failed for %v: %v", t, err))
                }
                return b, true
            }
            // Default reflection marshal — same encoder the typed path used,
            // but reads via reflect.NewAt(t, ptr).Elem().
            v := reflect.NewAt(t, ptr).Elem()
            return defaultReflectMarshal(v), true
        },
        apply: func(entity ecs.Entity, w_ *ecs.World, payload []byte) error {
            if !u.Has(entity, id) {
                u.Add(entity, id) // ark zero-initializes
            }
            ptr := u.Get(entity, id)
            if o.unmarshalInto != nil {
                return o.unmarshalInto(payload, ptr)
            }
            v := reflect.NewAt(t, ptr).Elem()
            return defaultReflectUnmarshalInto(payload, v)
        },
    }
    reg.add(rep)
}

// RegisterComponent is the typed convenience wrapper for callers that already
// have a *ecs.Map1[T] in hand. Delegates to RegisterComponentByID.
func RegisterComponent[T any](
    reg *ReplicationRegistry,
    m *ecs.Map1[T],
    opts ...ComponentOption,
) {
    w := m.World() // verify ark exposes this; if not, store the World on the registry
    RegisterComponentByID(reg, w, m.ID(), reflect.TypeFor[T](), opts...)
}
```

If `*ecs.Map1[T]` doesn't expose `World()` and `ID()`, you'll need to dig: ark stores both internally. Check `./go/pkg/mod/github.com/mlange-42/ark@v0.7.1/ecs/maps_gen.go` for `Map1[T]` accessors, or use `ecs.ComponentID[T](w)` to recover the ID from a separately-passed world.

- [ ] **Step 3: Update componentReplicator struct shape**

The existing `componentReplicator` likely has fields like `marshal`, `unmarshal`, `name`. Replace its body:

```go
type componentReplicator struct {
    id    ecs.ID
    typ   reflect.Type
    scan  func(ecs.Entity, *ecs.World) ([]byte, bool)
    apply func(ecs.Entity, *ecs.World, []byte) error
}
```

Update `ReplicationRegistry.serializeEntity` and `applyEntity` (or whatever the consumer methods are called) to call `rep.scan(entity, w)` / `rep.apply(entity, w, payload)` instead of the old typed accessors.

- [ ] **Step 4: Extract or write defaultReflectMarshal / defaultReflectUnmarshalInto helpers**

The current code has reflection-based marshal/unmarshal somewhere — likely inline in the existing `RegisterComponent[T]` closures. Extract into package-level functions that work on a `reflect.Value` (or pointer + type). Keep wire format byte-identical to the previous output so existing tests pass without a wire format bump.

- [ ] **Step 5: Run universe package tests**

```bash
go test ./pkg/universe/... -count=1 -run TestComponentRegistry
```

Expected: existing component registry tests pass with the new internals. If any test fails, the new path isn't byte-identical to the old one — fix `defaultReflectMarshal` / `defaultReflectUnmarshalInto` until it is.

### Task 3: Type-erase KindComponent + KindComponentLocalOnly

**Files:**
- Modify: `pkg/universe/entity_kind.go`

- [ ] **Step 1: Add the type-erased internal core**

```go
// kindComponentByID is the type-erased core for entity kind component
// registration. mmokit.RegisterKind[T] calls this per-field. The typed
// KindComponent[T] / KindComponentLocalOnly[T] wrappers also delegate here.
//
// localOnly=true skips transfer codec registration but still ensures the
// component is added on transfer receive (for local-only state like input).
func kindComponentByID(
    def *EntityKindDef,
    w *ecs.World,
    id ecs.ID,
    t reflect.Type,
    localOnly bool,
    opts ...ComponentOption,
) {
    u := w.Unsafe()
    kc := kindComponent{
        ensureExists: func(entity ecs.Entity) {
            if !u.Has(entity, id) {
                u.Add(entity, id)
            }
        },
    }
    if !localOnly {
        kc.registerTransfer = func(reg *ReplicationRegistry) {
            RegisterComponentByID(reg, w, id, t, opts...)
        }
    }
    def.components = append(def.components, kc)
}
```

- [ ] **Step 2: Reshape KindComponent[T] and KindComponentLocalOnly[T] as wrappers**

```go
func KindComponent[T any](def *EntityKindDef, m *ecs.Map1[T], opts ...ComponentOption) {
    kindComponentByID(def, m.World(), m.ID(), reflect.TypeFor[T](), false, opts...)
}

func KindComponentLocalOnly[T any](def *EntityKindDef, m *ecs.Map1[T]) {
    kindComponentByID(def, m.World(), m.ID(), reflect.TypeFor[T](), true)
}
```

- [ ] **Step 3: Drop AddKindComponentByID**

It's been superseded by `kindComponentByID(localOnly=true)`. Find any callers (likely in `pkg/mmokit/kindreg.go`'s old reflection path) and migrate them in Task 5.

- [ ] **Step 4: Run universe tests**

```bash
go test ./pkg/universe/... -count=1
```

Expected: green. Any failures here are usually missing world plumbing — `m.World()` may not exist on ark Map1; if so, store the world on the EntityKindDef (`def.world *ecs.World`) when it's registered, and pass that to the wrapper.

### Task 4: Commit Phase 1

- [ ] **Step 1: Run full vet + universe tests**

```bash
go vet ./...
go test ./pkg/universe/... -count=1
```

Expected: vet clean, universe tests pass. Other packages may have stale imports of `ComponentOption[T]` — fix those if vet flags them.

- [ ] **Step 2: Commit**

```bash
git add pkg/universe/component_registry.go pkg/universe/entity_kind.go
git commit -m "$(cat <<'EOF'
refactor(universe): type-erase transfer registry and entity-kind components

The transfer codec and KindComponent wiring no longer carry T at the
internal layer — closures access components via World.Unsafe().Get(entity,
id) -> unsafe.Pointer, round-tripped via reflect.NewAt or (*T)(ptr).
ComponentOption is now a non-generic struct built by typed constructors
(WithMarshal[T], WithPreMarshal[T]) that wrap their closures in
unsafe.Pointer adapters once.

This unblocks bundle-driven RegisterKind[T] in pkg/mmokit, which can now
walk a bundle struct via reflection and invoke kindComponentByID per
field with no compile-time T per call.

KindComponent[T] / KindComponentLocalOnly[T] / RegisterComponent[T] remain
as thin generic wrappers — existing call sites unchanged.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 2: Type-erase the network replication binding

### Task 5: Add type-erased reflect binding in pkg/system

**Files:**
- Modify: `pkg/system/auto_replicator.go`

`reflectBinding[T]` currently captures `*ecs.Map1[T]`. Per-tick `m.Get(entity)` returns `*T`, then reflection iterates `net:"..."` tags. Replace with `(ecs.ID, reflect.Type, *ecs.World)` access.

- [ ] **Step 1: Define a non-generic reflectBinding struct**

```go
type reflectBinding struct {
    id     ecs.ID
    typ    reflect.Type
    fields []reflectField // pre-computed field metadata, unchanged
    // ... other state from the existing reflectBinding[T]
}
```

The pre-computed `[]reflectField` list (one per `net:"..."`-tagged field) is **already type-erased** — it stores reflect.StructField indices and codec functions. The only T-bound part of the existing binding is the `*ecs.Map1[T]`. Replace it.

- [ ] **Step 2: Rewrite hash() and snapshot() to use Unsafe.Get**

```go
func (b *reflectBinding) snapshot(entity ecs.Entity, w *quantize.SnapshotWriter, ...) {
    // was: ptr := b.m.Get(entity)
    ptr := b.world.Unsafe().Get(entity, b.id)
    v := reflect.NewAt(b.typ, ptr).Elem()
    for _, f := range b.fields {
        f.encode(v, w)
    }
}
```

`b.world` is captured at binding-construction time. If the binding outlives the world (it shouldn't — bindings are per-cell), this needs revisiting; otherwise it's fine.

- [ ] **Step 3: Rewrite Component[T] as a generic wrapper**

```go
func Component[T any](m *ecs.Map1[T]) ComponentBinding {
    return ComponentByID(m.World(), m.ID(), reflect.TypeFor[T]())
}

// ComponentByID is the type-erased entry point. Used by mmokit.RegisterKind[T]
// when walking the bundle struct via reflection.
func ComponentByID(w *ecs.World, id ecs.ID, t reflect.Type) ComponentBinding {
    return newReflectBinding(w, id, t)
}
```

- [ ] **Step 4: Run replication tests**

```bash
go test ./pkg/system/... -count=1
```

Expected: green. Snapshot bytes must be identical to the typed path — if any AutoReplicator or Replication tests fail, a reflect.Value vs. typed-pointer access path diverged. Fix by ensuring the reflect access reads from the same offsets.

- [ ] **Step 5: Commit**

```bash
git add pkg/system/auto_replicator.go
git commit -m "$(cat <<'EOF'
refactor(system): type-erase reflect-based AutoReplicator binding

reflectBinding no longer carries T. Per-tick component access goes via
World.Unsafe().Get(entity, id) -> unsafe.Pointer, then reflect.NewAt(t, ptr)
to drive the same per-field encoder list the typed path used. Snapshot
wire bytes are byte-identical (validated by replication tests).

varTailBinding[T] stays generic — var-tail providers carry typed Count /
WriteItems / HashItems closures and don't benefit from erasure.

Component[T] remains as a thin wrapper over ComponentByID for callers
that have a *ecs.Map1[T] in hand.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 3: Bundle-driven RegisterKind in pkg/mmokit

### Task 6: Rewrite RegisterKind[T] to walk the bundle via reflection

**Files:**
- Modify: `pkg/mmokit/kindreg.go`

- [ ] **Step 1: Define the unified FieldOption API (no double-wrapping)**

`FieldOption` is the unified per-component option type that flows through both bundle-driven and explicit-call code paths. Internally it carries the same `erasedComponentOpts`-mutating closure that `universe.ComponentOption` carries, plus an optional `binding` channel for var-tail bindings:

```go
// FieldOption configures a single component within an entity kind. Used
// inline as the ...opts variadic on KindComponent[T] / KindComponentLocalOnly,
// or wrapped by WithField[T](opts...) when registering a bundle.
//
// FieldOption is an alias for universe.ComponentOption so the same value
// flows through both the bundle path (RegisterKind[T] + WithField[T]) and
// the explicit path (KindComponent[T]) without conversion.
type FieldOption = universe.ComponentOption

// WithMarshal registers custom serialization for component type T.
// Re-exports universe.WithMarshal so game code only imports mmokit.
func WithMarshal[T any](
    marshal func(*T) ([]byte, error),
    unmarshalInto func([]byte, *T) error,
) FieldOption {
    return universe.WithMarshal(marshal, unmarshalInto)
}

// WithPreMarshal registers a sanitization hook called on the component just
// before cross-cell serialization. Re-exports universe.WithPreMarshal.
func WithPreMarshal[T any](fn func(*T)) FieldOption {
    return universe.WithPreMarshal(fn)
}

// WithBinding overrides the AutoReplicator binding for a bundle field with
// a caller-supplied ComponentBinding (typically a custom var-tail binding
// like NewStatusEffectsBinding or NewInventoryBinding). Only meaningful
// inside WithField[T]; the explicit KindComponentWithBinding[T] form takes
// the binding as an explicit argument.
func WithBinding(b system.ComponentBinding) FieldOption {
    return universe.ComponentOption{Apply: func(o *universe.ErasedOpts) {
        o.Binding = b
    }}
}

// LocalOnly marks a bundle field as local-only — added on transfer receive
// but never serialized for cross-cell transfer or replication. Equivalent
// to the `mmokit:"local"` struct tag, but useful when the local-only
// decision is computed at registration time.
func LocalOnly() FieldOption {
    return universe.ComponentOption{Apply: func(o *universe.ErasedOpts) {
        o.LocalOnly = true
    }}
}
```

This requires Phase 1's `erasedComponentOpts` (renamed `ErasedOpts`, exported) to grow two extra fields:

```go
type ErasedOpts struct {
    Marshal       func(p unsafe.Pointer) ([]byte, error)
    UnmarshalInto func(b []byte, p unsafe.Pointer) error
    PreMarshal    func(p unsafe.Pointer)
    Binding       system.ComponentBinding // var-tail binding override (bundle path only)
    LocalOnly     bool                    // skip transfer codec entirely (bundle path only)
}
```

The `Binding` and `LocalOnly` fields are ignored by the explicit `KindComponent[T]` path (which takes `localOnly` as a separate function arg via `KindComponentLocalOnly[T]`, and which has `KindComponentWithBinding[T]` for explicit bindings). They're consumed only by `RegisterKind[T]`'s bundle walker.

- [ ] **Step 1b: Define WithField[T] and the kind-scoped extra-binding option**

```go
// fieldOverride is the bundle-walker's per-component override record. Built
// by WithField[T](opts...) and indexed by component type during reflection
// so the walker knows which fields have non-default behavior.
type fieldOverride struct {
    typ  reflect.Type
    opts []FieldOption
}

// WithField returns a fieldOverride for component type T inside a bundle
// passed to RegisterKind[BundleT]. T must match the pointer-element type
// of one of the bundle's exported fields.
//
// Example:
//   mmokit.RegisterKind[ShipBundle](mmo, KindShip, "Ship", bindings,
//       mmokit.WithField[gamecomp.Inventory](
//           mmokit.WithMarshal(MarshalInventory, UnmarshalInventoryInto),
//       ),
//       mmokit.WithField[gamecomp.StatusEffects](
//           mmokit.WithBinding(NewStatusEffectsBinding(c.StatusEffects)),
//           mmokit.WithPreMarshal(clearSourceRefs),
//       ),
//   )
func WithField[T any](opts ...FieldOption) fieldOverride {
    return fieldOverride{typ: reflect.TypeFor[T](), opts: opts}
}

// KindOption configures kind-scoped (rather than per-component) behavior on
// RegisterKind[T]. Constructed via WithExtraBinding.
type KindOption struct {
    apply func(*kindBuildContext)
}

// WithExtraBinding attaches an additional network binding to the entity
// kind that isn't tied to any bundle field. Used for components like
// Rotation that are registered for cross-cell transfer globally (via
// RegisterGlobalTransferComponents) but still need a per-kind network
// binding for replication to clients.
//
// Example:
//   mmokit.RegisterKind[ShipBundle](mmo, KindShip, "Ship", bindings,
//       mmokit.WithExtraBinding(mmokit.QAngle(c.Rotation)),
//   )
func WithExtraBinding(b system.ComponentBinding) KindOption {
    return KindOption{apply: func(ctx *kindBuildContext) {
        ctx.extraBindings = append(ctx.extraBindings, b)
    }}
}

// kindBuildContext is the internal state shared between RegisterKind and
// its KindOption appliers.
type kindBuildContext struct {
    extraBindings []system.ComponentBinding
}
```

Note: `RegisterKind[T]`'s variadic must accept BOTH `fieldOverride` and `KindOption`. Use a sealed interface or a single sum type. Concretely:

```go
// RegisterKindArg is satisfied by fieldOverride (from WithField[T]) and
// KindOption (from WithExtraBinding, etc.). Sealed via the unexported
// isRegisterKindArg method.
type RegisterKindArg interface {
    isRegisterKindArg()
}

func (fieldOverride) isRegisterKindArg() {}
func (KindOption)    isRegisterKindArg() {}
```

Implementer note: this is the common Go pattern for variadic-of-mixed-types. Keep the interface unexported-method-sealed so callers can't add their own.

- [ ] **Step 2: Rewrite RegisterKind[T]**

```go
// RegisterKind registers an entity kind whose components are described by
// the bundle struct T. Each exported pointer-to-struct field becomes a
// registered KindComponent automatically — no per-field Field[T]() calls.
//
// Struct tags:
//   `mmokit:"local"`  — register as KindComponentLocalOnly (added on
//                       transfer receive but never serialized).
//   `mmokit:"-"`      — skip this field entirely.
//
// Per-field overrides (custom binding, marshal, premarshal) are passed via
// WithField[T](opts...). Kind-scoped extras (Rotation network binding,
// etc.) attach via WithExtraBinding(b). Both flow through the same variadic
// using the RegisterKindArg sum.
func RegisterKind[T any](
    p *universe.Process,
    kind uint8,
    name string,
    bindings EngineBindingsConfig,
    args ...RegisterKindArg,
) {
    bundleType := reflect.TypeFor[T]()
    if bundleType.Kind() != reflect.Struct {
        panic(fmt.Sprintf("mmokit.RegisterKind: T must be a struct, got %v", bundleType.Kind()))
    }

    // Partition args into per-field overrides and kind-scoped options.
    overrideByType := make(map[reflect.Type]fieldOverride)
    var ctx kindBuildContext
    for _, a := range args {
        switch v := a.(type) {
        case fieldOverride:
            if _, dup := overrideByType[v.typ]; dup {
                panic(fmt.Sprintf("mmokit.RegisterKind: duplicate WithField[%v]", v.typ))
            }
            overrideByType[v.typ] = v
        case KindOption:
            v.apply(&ctx)
        default:
            panic(fmt.Sprintf("mmokit.RegisterKind: unexpected arg type %T", a))
        }
    }

    // Walk bundle fields, collect plan up-front (for validation).
    type fieldPlan struct {
        name      string
        compType  reflect.Type
        localOnly bool
        opts      []FieldOption // resolved options including the binding override
    }
    var plan []fieldPlan
    for i := range bundleType.NumField() {
        f := bundleType.Field(i)
        if !f.IsExported() {
            continue
        }
        tag := f.Tag.Get("mmokit")
        if tag == "-" {
            continue
        }
        if f.Type.Kind() != reflect.Pointer {
            panic(fmt.Sprintf("mmokit.RegisterKind: bundle field %s.%s must be a pointer (got %v)", bundleType.Name(), f.Name, f.Type.Kind()))
        }
        ct := f.Type.Elem()
        if ct.Kind() != reflect.Struct {
            panic(fmt.Sprintf("mmokit.RegisterKind: bundle field %s.%s must point to a struct (got *%v)", bundleType.Name(), f.Name, ct.Kind()))
        }
        // Materialize the resolved opts to inspect LocalOnly + Binding.
        ov, hasOv := overrideByType[ct]
        var resolved universe.ErasedOpts
        if hasOv {
            for _, o := range ov.opts {
                o.Apply(&resolved)
            }
        }
        plan = append(plan, fieldPlan{
            name:      f.Name,
            compType:  ct,
            localOnly: tag == "local" || resolved.LocalOnly,
            opts:      ov.opts, // raw (unapplied); the universe layer re-applies them
        })
    }
    if len(plan) == 0 {
        panic(fmt.Sprintf("mmokit.RegisterKind: bundle %s has no registrable fields", bundleType.Name()))
    }

    // Verify every override matched a field.
    matched := make(map[reflect.Type]bool, len(plan))
    for _, p := range plan {
        matched[p.compType] = true
    }
    for t := range overrideByType {
        if !matched[t] {
            panic(fmt.Sprintf("mmokit.RegisterKind: WithField[%v] does not match any bundle field", t))
        }
    }

    realize := func(stage *universe.Stage) {
        w := stage.ECSWorld()
        def := universe.EntityKindDef{Kind: kind, Name: name, EngineBindings: &bindings}
        for _, p := range plan {
            id := ecs.TypeID(w, p.compType)
            if p.localOnly {
                universe.KindComponentByID(&def, w, id, p.compType, true) // localOnly=true
                continue
            }
            // Transfer codec registration (filters Binding/LocalOnly internally).
            universe.KindComponentByID(&def, w, id, p.compType, false, p.opts...)
            // Network binding: custom Binding from opts wins, else default reflect binding.
            var bound system.ComponentBinding
            for _, o := range p.opts {
                var probe universe.ErasedOpts
                o.Apply(&probe)
                if probe.Binding != nil {
                    bound = probe.Binding
                }
            }
            if bound == nil {
                bound = system.ComponentByID(w, id, p.compType)
            }
            def.NetworkBindings = append(def.NetworkBindings, bound)
        }
        // Append kind-scoped extra bindings (Rotation, etc.).
        def.NetworkBindings = append(def.NetworkBindings, ctx.extraBindings...)
        stage.RegisterEntityKind(def)
    }
    p.RegisterKindSpec(realize)
}
```

(Note: `universe.KindComponentByID` is the public name; the Phase 1 task called it `kindComponentByID` — export it when it crosses the package boundary.)

- [ ] **Step 3: Drop the old Field[T], FieldWithBinding[T], FieldLocalOnly[T] functions**

Delete them outright. No stub, no deprecation — we have "no backward compat" in the project's principles.

- [ ] **Step 4: Update mmokit re-exports in pkg/mmokit/mmokit.go**

Drop re-exports of `Field`, `FieldWithBinding`, `FieldLocalOnly`. Add re-exports for `WithField`, `LocalOnly`, `WithBinding`, `WithMarshal`, `WithPreMarshal`. Verify `KindComponent`, `KindComponentLocalOnly`, `KindComponentWithBinding`, `RegisterComponent` still re-export cleanly (their signatures changed slightly — `ComponentOption` is now non-generic, but the typed functions remain generic).

- [ ] **Step 5: Run mmokit + universe tests**

```bash
go vet ./...
go test ./pkg/mmokit/... ./pkg/universe/... -count=1
```

Expected: vet shows errors at the demo + internal/game callers (they still call the old `Field[T]()` and the bundle-less `RegisterKind`). That's expected — Phase 4 fixes them.

### Task 7: Add bundle reflection unit tests

**Files:**
- Modify: `pkg/mmokit/kindreg_test.go` (or create if absent)

- [ ] **Step 1: Add a test that registers a kind from a bundle with no overrides**

```go
func TestRegisterKind_BundleReflection(t *testing.T) {
    type FooBundle struct {
        Pos *Position
        Vel *Velocity
    }
    p := newTestProcess(t)
    RegisterKind[FooBundle](p, 42, "Foo", EngineBindingsConfig{})
    p.Build()
    // Verify both components are registered on every cell's stage.
    p.ForEachCell(func(stage *universe.Stage) {
        def, ok := stage.EntityKindDef(42)
        if !ok {
            t.Fatal("kind 42 not registered")
        }
        if def.Components() != 2 {
            t.Errorf("expected 2 components, got %d", def.Components())
        }
        if len(def.NetworkBindings) != 2 {
            t.Errorf("expected 2 network bindings, got %d", len(def.NetworkBindings))
        }
    })
}
```

- [ ] **Step 2: Add a test that exercises WithField[T] override**

```go
func TestRegisterKind_WithFieldOverride(t *testing.T) {
    type Bag struct {
        Items map[uint32]int32
    }
    type FooBundle struct {
        Bag *Bag
    }
    customBinding := newMockBinding(t)
    p := newTestProcess(t)
    RegisterKind[FooBundle](p, 43, "Foo", EngineBindingsConfig{},
        WithField[Bag](WithBinding(customBinding)),
    )
    p.Build()
    // ... verify the binding came through customBinding, not the default reflect path.
}
```

- [ ] **Step 3: Add a test that exercises mmokit:"local" struct tag**

```go
func TestRegisterKind_LocalOnlyTag(t *testing.T) {
    type FooBundle struct {
        Pos *Position
        Tmp *PlayerInput `mmokit:"local"`
    }
    p := newTestProcess(t)
    RegisterKind[FooBundle](p, 44, "Foo", EngineBindingsConfig{})
    p.Build()
    // Verify Pos is in NetworkBindings but PlayerInput is not.
    p.ForEachCell(func(stage *universe.Stage) {
        def, _ := stage.EntityKindDef(44)
        if len(def.NetworkBindings) != 1 {
            t.Errorf("expected 1 network binding (only Pos), got %d", len(def.NetworkBindings))
        }
        // Verify the transfer codec excludes PlayerInput too.
        // (Read Components()'s registerTransfer and confirm it's nil for the local field.)
    })
}
```

- [ ] **Step 4: Add an error-case test (mismatched WithField)**

```go
func TestRegisterKind_WithFieldUnmatched(t *testing.T) {
    type FooBundle struct {
        Pos *Position
    }
    type SomethingElse struct{}
    p := newTestProcess(t)
    defer func() {
        if r := recover(); r == nil {
            t.Fatal("expected panic from unmatched WithField")
        }
    }()
    RegisterKind[FooBundle](p, 45, "Foo", EngineBindingsConfig{},
        WithField[SomethingElse](LocalOnly()),
    )
}
```

- [ ] **Step 5: Run kindreg tests**

```bash
go test ./pkg/mmokit/... -count=1 -run TestRegisterKind
```

Expected: all four tests pass.

- [ ] **Step 6: Commit Phase 3**

```bash
git add pkg/mmokit/kindreg.go pkg/mmokit/kindreg_test.go pkg/mmokit/mmokit.go
git commit -m "$(cat <<'EOF'
feat(mmokit): bundle-reflection RegisterKind, drop per-field Field[T]() calls

RegisterKind[T] walks the bundle struct via reflection and registers
each exported *ComponentType field automatically. No more Field[T]()
duplication of the bundle's own field list.

Per-field overrides (custom AutoReplicator binding, custom marshal,
pre-marshal sanitizer, local-only flag) attach via the new variadic
WithField[T](opts...). Struct tag `mmokit:"local"` opts a field out of
the transfer codec without an explicit override.

Drops Field[T](), FieldWithBinding[T](), FieldLocalOnly[T]().

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 4: Migrate callers

### Task 8: Migrate examples/4node-basic to bundle-only

**Files:**
- Modify: `examples/4node-basic/main.go`
- Modify: `examples/4node-basic/mesh_e2e_test.go`

- [ ] **Step 1: Drop the Field[T]() calls from main.go**

Replace lines 50-62 of `examples/4node-basic/main.go`:

```go
mmokit.RegisterKind[PlayerComponents](mmo, KindPlayer, "Player", playerBindings)

mmokit.RegisterKind[BotComponents](mmo, KindBot, "Bot", playerBindings)
```

Both bundle structs already exist in `examples/4node-basic/components.go` — no struct edits needed.

- [ ] **Step 2: Drop the Field[T]() calls from mesh_e2e_test.go**

Same pattern — find the two RegisterKind blocks (around lines 215-226) and reduce each to a single line.

- [ ] **Step 3: Build the example**

```bash
cd examples/4node-basic && just build
```

Expected: clean build, no warnings.

- [ ] **Step 4: Run the e2e test**

```bash
go test ./examples/4node-basic/... -count=1 -timeout 120s
```

Expected: green. The test exercises full transfer codec + replication through the new path.

- [ ] **Step 5: Smoke-run the example**

```bash
cd examples/4node-basic && timeout 10s ./bin/server --headless || true
# (Ctrl-C interrupts a server that doesn't exit cleanly — `|| true` swallows it.)
```

Look at the log for any "panic" / "registration failed" / "kind not found" lines. None expected.

### Task 9: Migrate internal/game to bundle-only

**Files:**

- Modify: `internal/game/entity_ship.go`, `entity_asteroid.go`, `entity_station.go`, `entity_npc.go`, `entity_lootcrate.go` — define each kind's bundle struct alongside its spawn function.
- Modify: `internal/game/entity_kinds.go` — replace `buildXxxDef()` functions with `RegisterKind[XxxBundle](...)` blocks.

Bundles live next to spawn functions (per the "co-locate" decision in the Resolved Decisions section). If a kind's `entity_*.go` doesn't exist yet for whatever reason, fall back to `internal/game/components.go`.

This is the bulk of the migration. Take it kind by kind to keep blast radius small.

- [ ] **Step 1: Define ShipBundle and migrate buildShipDef**

Add the bundle struct at the top of `internal/game/entity_ship.go` (alongside `SpawnShip`):

```go
package game

import (
    gamecomp "github.com/mmokit/mmokit/internal/component"
    "github.com/mmokit/mmokit/pkg/mmokit"
)

type ShipBundle struct {
    PilotName     *gamecomp.PilotName
    Health        *gamecomp.Health
    Shield        *gamecomp.Shield
    ShipControl   *gamecomp.ShipControl
    Equipment    *gamecomp.Equipment
    Inventory     *gamecomp.Inventory
    TargetLock    *gamecomp.TargetLock
    AbilitySet    *gamecomp.AbilitySet
    StatusEffects *gamecomp.StatusEffects
    MoveTarget    *mmokit.MoveTarget
    LockedBy      *gamecomp.LockedBy
    ActiveMining  *gamecomp.ActiveMining
    PlayerInput   *gamecomp.PlayerInput   `mmokit:"local"`
    MiningLaser   *gamecomp.MiningLaser   `mmokit:"local"`
}

type AsteroidBundle struct {
    Minable *gamecomp.Minable
}

type StationBundle struct {
    Station *gamecomp.Station `mmokit:"local"`
}

type NPCBundle struct {
    Health        *gamecomp.Health
    Shield        *gamecomp.Shield
    StatusEffects *gamecomp.StatusEffects
}

type LootCrateBundle struct {
    Inventory *gamecomp.Inventory
    Lifetime  *mmokit.Lifetime
    LootCrate *gamecomp.LootCrate `mmokit:"local"`
}
```

In `internal/game/entity_kinds.go`, replace `buildShipDef(c)` and the loop in `initEntityKinds`:

```go
func (gw *GameWorld) initEntityKinds() {
    RegisterGlobalTransferComponents(gw.C, gw.ReplicationRegistry())

    shipBindings := mmokit.EngineBindingsConfig{VelQuantScale: 2000, SizeQuantScale: 500, IncludeMeshState: true}
    asteroidBindings := mmokit.EngineBindingsConfig{SizeQuantScale: 500, IncludeMeshState: true}
    stationBindings := mmokit.EngineBindingsConfig{SizeQuantScale: 500, IncludeMeshState: true}
    npcBindings := mmokit.EngineBindingsConfig{VelQuantScale: 2000, SizeQuantScale: 500, IncludeMeshState: true}
    lootBindings := mmokit.EngineBindingsConfig{IncludeMeshState: true}

    statusEffectsClearSource := mmokit.WithPreMarshal(func(se *gamecomp.StatusEffects) {
        for i := uint8(0); i < se.Count; i++ {
            se.Effects[i].Source = ecs.Entity{}
        }
    })

    mmokit.RegisterKind[ShipBundle](gw.Process, gamecomp.TypeShip, "Ship", shipBindings,
        mmokit.WithField[gamecomp.Inventory](
            mmokit.WithMarshal(MarshalInventory, UnmarshalInventoryInto),
        ),
        mmokit.WithField[gamecomp.StatusEffects](
            mmokit.WithBinding(NewStatusEffectsBinding(gw.C.StatusEffects)),
            statusEffectsClearSource,
        ),
        // Rotation is registered for transfer globally (see RegisterGlobalTransferComponents);
        // here we attach only the per-kind network binding.
        mmokit.WithExtraBinding(mmokit.QAngle(gw.C.Rotation)),
    )

    mmokit.RegisterKind[AsteroidBundle](gw.Process, gamecomp.TypeAsteroid, "Asteroid", asteroidBindings)
    mmokit.RegisterKind[StationBundle](gw.Process, gamecomp.TypeStation, "Station", stationBindings)

    mmokit.RegisterKind[NPCBundle](gw.Process, gamecomp.TypeNPC, "NPC", npcBindings,
        mmokit.WithField[gamecomp.StatusEffects](
            mmokit.WithBinding(NewStatusEffectsBinding(gw.C.StatusEffects)),
            statusEffectsClearSource,
        ),
    )

    mmokit.RegisterKind[LootCrateBundle](gw.Process, gamecomp.TypeLootCrate, "LootCrate", lootBindings,
        mmokit.WithField[gamecomp.Inventory](
            mmokit.WithBinding(NewInventoryBinding(gw.C.Inventory)),
            mmokit.WithMarshal(MarshalInventory, UnmarshalInventoryInto),
        ),
    )

    // EntityRegistry registrations (admin commands) — unchanged.
    gw.Registry.Register(mmokit.EntityDef{Name: "ship", ...})
    // ... etc, unchanged
}
```

**Open issue:** the existing code does `def.NetworkBindings = append(def.NetworkBindings, mmokit.QAngle(c.Rotation))` to attach a Rotation binding without re-registering it for transfer. The bundle pattern doesn't naturally express this. Options:

  1. Add an `appendNetworkBinding(p, kind, binding)` helper that mutates the realize closure's def.
  2. Add a `WithExtraBinding(b)` option to `RegisterKind` (not field-scoped — kind-scoped).
  3. Include `Rotation` in the bundle as a field with a network override binding, and add a tag like `mmokit:"transfer-skip"` (transfer is registered globally, not per-kind).

Pick 2 — kind-scoped extras keep all wiring inside `RegisterKind`. Add it to `RegisterKind` as a variadic of `mmokit.KindOption` or similar. Update Task 6 if this comes up before Phase 4.

- [ ] **Step 2: Run game tests**

```bash
go test ./internal/game/... -count=1
```

Expected: green. Failures here usually mean a bundle field is wrong-typed or out of order; check the panic message for the bundle type and field name.

- [ ] **Step 3: Run full test suite**

```bash
go test ./... -count=1 -timeout 300s
```

Expected: all green. Particular focus: `pkg/universe/s7_*_test.go` (transfer codec stress), `pkg/universe/handoff_test.go`.

- [ ] **Step 4: Smoke-test the main game**

```bash
just build
timeout 10s ./bin/server --headless || true
```

Look at the log for panic / registration / kind-not-found.

- [ ] **Step 5: Commit Phase 4**

```bash
git add internal/game/entity_kinds.go internal/game/entity_bundles.go examples/4node-basic/
git commit -m "$(cat <<'EOF'
feat(game,examples): migrate to bundle-driven entity kind registration

internal/game and examples/4node-basic both use mmokit.RegisterKind[T]
with bundle structs as the single source of truth for component lists.
Per-component overrides (custom var-tail bindings, custom marshal,
pre-marshal sanitization) attach via WithField[T](opts...).

Drops 39 individual KindComponent[T] / KindComponentLocalOnly[T] /
KindComponentWithBinding[T] calls in favor of bundle declarations.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 5: Cleanup + verification

### Task 10: Audit for dead code

- [ ] **Step 1: Grep for any remaining stale calls**

```bash
grep -rn "mmokit.Field\b\|mmokit.FieldWithBinding\|mmokit.FieldLocalOnly\|universe.AddKindComponentByID" --include="*.go"
```

Expected: zero hits. If anything turns up, migrate it now or remove it.

- [ ] **Step 2: Grep for unused helpers**

```bash
grep -rn "kindFieldRegistrar\|KindFieldSpec\|buildKindSpec" --include="*.go"
```

The old helper types from `kindreg.go` (`KindFieldSpec`, `kindFieldRegistrar`, `buildKindSpec`) should be unreferenced. Delete.

### Task 11: Final verification

- [ ] **Step 1: Vet + typecheck**

```bash
go vet ./...
```

Expected: clean.

- [ ] **Step 2: Full test pass**

```bash
go test ./... -count=1 -timeout 600s
```

Expected: green.

- [ ] **Step 3: Schema export sanity check**

```bash
./bin/server --dump-schema | jq '.entities[] | {kind, name, components: (.components | length)}'
```

Expected output: counts match the bundle structs (Ship has 14 fields → 14 components incl. local-only, etc.).

- [ ] **Step 4: Run a 30-second bot smoke**

```bash
cd examples/4node-basic && timeout 30s ./bin/server &
SERVER=$!
sleep 5
# In another shell — or use the already-running server's console via expect — spawn 100 bots and split a cell.
# This exercises full transfer + replication through the type-erased path.
kill $SERVER || true
```

Expected: no panics, no "kind not found" logs, no replication errors.

- [ ] **Step 5: Update CLAUDE.md**

The "Coordinator setup pattern" example block in CLAUDE.md (around line 200-ish) shows `RegisterKind` with `Field[T]()` calls. Update it to the new bundle-only form. Also update the `mmokit.RegisterKind[T]` paragraph in the Server Meshing section if it lists `Field[T]()` as an API.

- [ ] **Step 6: Commit cleanup**

```bash
git add CLAUDE.md pkg/mmokit/kindreg.go
git commit -m "$(cat <<'EOF'
docs+chore: drop stale Field[T] references after bundle-reflection refactor

Updates CLAUDE.md examples to the bundle-only RegisterKind form. Removes
KindFieldSpec, kindFieldRegistrar, and buildKindSpec — superseded by the
direct reflection walk in RegisterKind[T].

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Resolved Decisions

- **Rotation-as-extra binding** — Confirmed: add a kind-scoped `WithExtraBinding(b system.ComponentBinding)` option to `RegisterKind` in Task 6 (Phase 3). Used in Task 9 to attach Ship's Rotation network binding without re-registering it for transfer.

- **Bundle struct location** — Co-locate. Each entity's bundle struct lives alongside its spawn function (`entity_ship.go` defines `ShipBundle` near `SpawnShip`), or in `components.go` if the file already defines related types. No new `entity_bundles.go` file. The demo's `examples/4node-basic/components.go` already follows this pattern.

- **Option API surface** — Re-export `WithMarshal` and `WithPreMarshal` directly from `mmokit` as `FieldOption` (which doubles as a `universe.ComponentOption` internally). One unified vocabulary across both bundle-driven `RegisterKind[T]`+`WithField[T]` callers and explicit `KindComponent[T]` callers. No `WithMarshalOpt` wrapper needed.

  Concretely: `mmokit.WithMarshal[T]` and `mmokit.WithPreMarshal[T]` build a `FieldOption` whose `apply` populates both the inner `erasedComponentOpts` (for the transfer codec, when the option is consumed by `WithField[T]` or directly by `KindComponent[T]`) and is also accepted by anywhere that takes a `universe.ComponentOption`. Mechanically: `FieldOption` IS `universe.ComponentOption` (same struct, exported from `mmokit`), or `mmokit.FieldOption` wraps `universe.ComponentOption` with an additional `binding` channel for var-tail bindings. Implementer: pick the cleaner of the two during Task 6, prefer "FieldOption is just universe.ComponentOption with one extra `binding system.ComponentBinding` field" — keeps the type system simple.

## Open Questions

- **`MoveTarget` bundle position:** field order doesn't matter functionally. Convention to follow: transferred fields first, local-only fields last (with the `mmokit:"local"` tag). Apply uniformly across all bundles.

- **Schema export sanity check** — In Phase 5 Task 11 Step 3, diff `./bin/server.old --dump-schema` against the new build's output. Should be byte-identical or only differ in component ordering. Any other diff is a regression.
