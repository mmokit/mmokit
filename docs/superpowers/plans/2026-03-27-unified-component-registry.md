# Unified Component Registry with Reflection-Based Marshaling

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Games register ECS components once with the framework and get both cross-node replication and entity transfers automatically — no custom marshal/unmarshal code needed for simple structs. The framework inspects struct fields via reflection and generates serialization at registration time.

**Architecture:** A generic `RegisterComponent[T]` function captures the typed `*ecs.Map1[T]` mapper and builds `Scan`/`Apply`/`Add` closures using reflection-based marshaling. For complex types (maps, ring buffers), games provide custom marshal functions via options. `WorldBase.SerializeEntity` and `SpawnFromTransferCore` iterate the registry automatically, eliminating game overrides. `ecs.Entity` fields are skipped automatically (never valid across nodes).

**Tech Stack:** Go, `reflect` package, Ark ECS, mmokit framework

---

## File Structure

### Framework (create + modify)
- **Create:** `pkg/universe/reflect_marshal.go` — Reflection-based binary marshal/unmarshal for component structs
- **Create:** `pkg/universe/reflect_marshal_test.go` — Tests for reflection marshaler
- **Create:** `pkg/universe/component_registry.go` — Generic `RegisterComponent[T]` function + options
- **Modify:** `pkg/universe/world_base.go` — `SerializeEntity` uses registry, `SpawnFromTransferCore` applies registry components, add hooks
- **Modify:** `pkg/universe/replication.go` — Minor: keep `ComponentReplicator` as internal implementation; public API is `RegisterComponent`

### Space game (modify + delete)
- **Modify:** `internal/universe/replicators.go` — Replace manual replicator functions with `RegisterComponent` calls
- **Modify:** `internal/universe/adapter.go` — Remove `SerializeEntity`/`SpawnFromTransfer` overrides
- **Modify:** `internal/universe/factory.go` — Wire hooks
- **Delete:** `internal/game/transfer_components.go` — All marshal/unmarshal helpers (replaced by reflection)
- **Modify:** `internal/game/transfer.go` — Delete `AppendTransferComponents`, `ApplyTransferComponents`; keep `FinishTransferSpawn` (hook) and `WireTransferPlayer`

### Slither (modify)
- **Modify:** `examples/slither/world.go` — Replace inline `ComponentReplicator` blocks with `RegisterComponent` calls; delete `SerializeEntity`/`SpawnFromTransfer` overrides

---

### Task 1: Reflection-based binary marshaler

**Files:**
- Create: `pkg/universe/reflect_marshal.go`
- Create: `pkg/universe/reflect_marshal_test.go`

- [ ] **Step 1: Write tests for the reflection marshaler**

```go
func TestReflectMarshal_SimpleStruct(t *testing.T) {
    type Health struct {
        Current float32
        Max     float32
    }
    h := Health{Current: 75.5, Max: 100}
    data := ReflectMarshal(&h)

    var out Health
    ReflectUnmarshal(data, &out)
    if out != h {
        t.Fatalf("got %+v, want %+v", out, h)
    }
}

func TestReflectMarshal_NestedStruct(t *testing.T) { ... }
func TestReflectMarshal_SkipsEntityFields(t *testing.T) { ... }
func TestReflectMarshal_FixedArray(t *testing.T) { ... }
func TestReflectMarshal_Bool(t *testing.T) { ... }
func TestReflectMarshal_String(t *testing.T) { ... }
func TestValidateType_RejectsMap(t *testing.T) { ... }
func TestValidateType_RejectsSlice(t *testing.T) { ... }
```

- [ ] **Step 2: Implement reflection marshaler**

`pkg/universe/reflect_marshal.go`:

```go
package universe

import (
    "encoding/binary"
    "fmt"
    "math"
    "reflect"

    "github.com/mlange-42/ark/ecs"
)

var entityType = reflect.TypeOf(ecs.Entity{})

// ValidateComponentType checks that a struct type can be automatically marshaled.
// Panics with a descriptive error if unsupported field types are found.
// Call at registration time (startup), not at runtime.
func ValidateComponentType(t reflect.Type) {
    if t.Kind() == reflect.Ptr {
        t = t.Elem()
    }
    if t.Kind() != reflect.Struct {
        panic(fmt.Sprintf("component type must be struct, got %s", t.Kind()))
    }
    for i := 0; i < t.NumField(); i++ {
        f := t.Field(i)
        if !f.IsExported() {
            continue
        }
        if f.Type == entityType {
            continue // skipped during marshal
        }
        validateFieldType(f.Type, f.Name)
    }
}

func validateFieldType(t reflect.Type, path string) {
    switch t.Kind() {
    case reflect.Float32, reflect.Float64,
        reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
        reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
        reflect.Bool:
        // OK
    case reflect.String:
        // OK — length-prefixed (uint16)
    case reflect.Array:
        validateFieldType(t.Elem(), path+"[]")
    case reflect.Struct:
        for i := 0; i < t.NumField(); i++ {
            f := t.Field(i)
            if !f.IsExported() {
                continue
            }
            if f.Type == entityType {
                continue
            }
            validateFieldType(f.Type, path+"."+f.Name)
        }
    default:
        panic(fmt.Sprintf("unsupported field type %s at %s — use WithMarshal for custom serialization", t.Kind(), path))
    }
}

// ReflectMarshal serializes a component struct to binary using reflection.
// Exported fields are serialized in declaration order. ecs.Entity fields are skipped.
func ReflectMarshal(ptr any) []byte {
    v := reflect.ValueOf(ptr)
    if v.Kind() == reflect.Ptr {
        v = v.Elem()
    }
    // Pre-calculate size
    size := reflectSize(v)
    buf := make([]byte, size)
    off := reflectWrite(buf, 0, v)
    return buf[:off]
}

// ReflectUnmarshal deserializes binary data into a component struct.
func ReflectUnmarshal(data []byte, ptr any) {
    v := reflect.ValueOf(ptr).Elem()
    reflectRead(data, 0, v)
}
```

Implement `reflectSize`, `reflectWrite`, `reflectRead` as recursive helpers that walk struct fields. For each supported kind:
- `float32`: 4 bytes via `math.Float32bits`/`Float32frombits`
- `float64`: 8 bytes via `math.Float64bits`/`Float64frombits`
- `uint8`/`bool`: 1 byte (bool: 0/1)
- `uint16`/`int16`: 2 bytes
- `uint32`/`int32`: 4 bytes
- `uint64`/`int64`: 8 bytes
- `string`: `[2]uint16 length + [N]bytes`
- `ecs.Entity`: skip (0 bytes written, zero value on read)
- `[N]T`: N × sizeof(T)
- Nested struct: recursive

- [ ] **Step 3: Run tests**

Run: `go test ./pkg/universe/ -v -run TestReflect`
Expected: all pass

- [ ] **Step 4: Commit**

```
feat(universe): add reflection-based binary marshaler for ECS components

Automatically serializes/deserializes component structs by inspecting
field types. Supports float32/64, uint/int 8-64, bool, string, fixed
arrays, and nested structs. Skips ecs.Entity fields (not valid across
nodes). Validates types at registration time with descriptive errors.
```

---

### Task 2: Generic RegisterComponent function

**Files:**
- Create: `pkg/universe/component_registry.go`

- [ ] **Step 1: Implement RegisterComponent**

```go
package universe

import (
    "reflect"
    "github.com/mlange-42/ark/ecs"
)

// ComponentOption configures a component registration.
type ComponentOption[T any] func(*componentConfig[T])

type componentConfig[T any] struct {
    marshal   func(*T) []byte
    unmarshal func([]byte, *T)
    preMarshal func(*T)
}

// WithMarshal overrides reflection-based marshaling with custom functions.
// Use for complex types (maps, slices, ring buffers).
func WithMarshal[T any](marshal func(*T) []byte, unmarshal func([]byte, *T)) ComponentOption[T] {
    return func(c *componentConfig[T]) {
        c.marshal = marshal
        c.unmarshal = unmarshal
    }
}

// WithPreMarshal runs a function on the component before marshaling.
// Use to clear entity references or other preprocessing.
func WithPreMarshal[T any](fn func(*T)) ComponentOption[T] {
    return func(c *componentConfig[T]) {
        c.preMarshal = fn
    }
}

// RegisterComponent registers a game component for automatic replication and
// transfer. The framework reflects on T to generate binary serialization.
// For complex types, use WithMarshal to provide custom functions.
//
// Example:
//
//     mmokit.RegisterComponent(reg, 1, gw.C.Health)           // auto-marshal
//     mmokit.RegisterComponent(reg, 2, gw.SnakeBodyMap,       // custom marshal
//         mmokit.WithMarshal(marshalSnakeBody, unmarshalSnakeBody))
//
func RegisterComponent[T any](reg *ReplicationRegistry, id ComponentID, m *ecs.Map1[T], opts ...ComponentOption[T]) {
    cfg := &componentConfig[T]{}
    for _, opt := range opts {
        opt(cfg)
    }

    // If no custom marshal, validate struct type and use reflection
    if cfg.marshal == nil {
        var zero T
        ValidateComponentType(reflect.TypeOf(zero))
    }

    scan := func(entity ecs.Entity) []byte {
        if !m.HasAll(entity) {
            return nil
        }
        comp := m.Get(entity)
        if cfg.preMarshal != nil {
            // Copy to avoid mutating the original
            copy := *comp
            cfg.preMarshal(&copy)
            comp = &copy
        }
        if cfg.marshal != nil {
            return cfg.marshal(comp)
        }
        return ReflectMarshal(comp)
    }

    apply := func(entity ecs.Entity, data []byte) {
        if !m.HasAll(entity) {
            return
        }
        comp := m.Get(entity)
        if cfg.unmarshal != nil {
            cfg.unmarshal(data, comp)
        } else {
            ReflectUnmarshal(data, comp)
        }
    }

    add := func(entity ecs.Entity, data []byte) {
        var comp T
        if cfg.unmarshal != nil {
            cfg.unmarshal(data, &comp)
        } else {
            ReflectUnmarshal(data, &comp)
        }
        m.Add(entity, &comp)
    }

    reg.Register(ComponentReplicator{
        ID:    id,
        Scan:  scan,
        Apply: apply,
        Add:   add,
    })
}
```

- [ ] **Step 2: Re-export from mmokit facade**

In `pkg/mmokit/mmokit.go`, add:

```go
var RegisterComponent = universe.RegisterComponent
type ComponentOption = universe.ComponentOption  // Note: can't alias generic types directly — re-export the functions
```

Actually, since `ComponentOption` is generic, it can't be aliased. Instead, games import `universe` directly for `RegisterComponent`, or we provide `WithMarshal`/`WithPreMarshal` as package-level functions in mmokit that forward to universe. The simplest approach: export `WithMarshal` and `WithPreMarshal` from mmokit, and have games call `mmokit.RegisterComponent(...)`.

Actually in Go, generic type aliases aren't supported. The cleanest approach: games call `universe.RegisterComponent` directly (already available via the `mmokit` facade pattern where `mmokit.RegisterComponent = universe.RegisterComponent` works since it's a generic function value).

Check if Go allows this. If not, games just import `pkg/universe` for registration — they already import it for `ComponentID` etc.

- [ ] **Step 3: Write test**

```go
func TestRegisterComponent_ReflectMarshal(t *testing.T) {
    type Shield struct {
        Current float32
        Max     float32
        Regen   float32
    }
    w := ecs.NewWorld(64)
    m := ecs.NewMap1[Shield](&w)
    reg := NewReplicationRegistry()

    RegisterComponent(reg, 1, m)

    entity := ecs.NewMap0(&w).NewEntity()
    m.Add(entity, &Shield{Current: 50, Max: 100, Regen: 2.5})

    rep := reg.Get(1)
    data := rep.Scan(entity)
    if data == nil {
        t.Fatal("Scan returned nil")
    }

    // Create new entity, Add component from data
    entity2 := ecs.NewMap0(&w).NewEntity()
    rep.Add(entity2, data)

    s := m.Get(entity2)
    if s.Current != 50 || s.Max != 100 || s.Regen != 2.5 {
        t.Fatalf("got %+v", s)
    }
}
```

- [ ] **Step 4: Commit**

```
feat(universe): add generic RegisterComponent with auto-marshal

Games call RegisterComponent(reg, id, mapper) to register ECS components
for replication and transfer. The framework auto-generates binary
serialization via reflection. WithMarshal and WithPreMarshal options
available for complex types.
```

---

### Task 3: WorldBase uses registry for transfers + hooks

**Files:**
- Modify: `pkg/universe/world_base.go`

- [ ] **Step 1: Add hook fields and setters**

```go
// In WorldBase struct:
onTransferReceived       func(entity ecs.Entity, frame *TransferFrame)
onPlayerTransferReceived func(entity ecs.Entity, frame *TransferFrame)
```

Add setters: `SetOnTransferReceived`, `SetOnPlayerTransferReceived`.

- [ ] **Step 2: SerializeEntity uses registry**

```go
func (b *WorldBase) SerializeEntity(entity ecs.Entity) ([]byte, error) {
    frame := b.SerializeEntityCore(entity)
    if b.replRegistry != nil {
        for _, rep := range b.replRegistry.All() {
            if data := rep.Scan(entity); data != nil {
                frame.Components = append(frame.Components, ComponentSlice{ID: rep.ID, Data: data})
            }
        }
    }
    return MarshalTransferFrame(frame)
}
```

- [ ] **Step 3: SpawnFromTransferCore applies registry + calls hooks**

After existing entity creation code, add:

```go
// Apply registered game components
if b.replRegistry != nil {
    for _, cs := range frame.Components {
        if rep := b.replRegistry.Get(cs.ID); rep != nil {
            if rep.Add != nil {
                rep.Add(entity, cs.Data)
            } else if rep.Apply != nil {
                rep.Apply(entity, cs.Data)
            }
        }
    }
}

// Game-specific post-processing
if b.onTransferReceived != nil {
    b.onTransferReceived(entity, frame)
}

// Player-specific post-processing
if frame.ConnID != 0 && b.onPlayerTransferReceived != nil {
    b.onPlayerTransferReceived(entity, frame)
}
```

- [ ] **Step 4: Verify compilation**

Run: `go vet ./...`

- [ ] **Step 5: Commit**

```
feat(universe): WorldBase auto-serializes/applies components via registry

SerializeEntity iterates registered components. SpawnFromTransferCore
applies them after entity creation. Games no longer need to override
these methods. OnTransferReceived and OnPlayerTransferReceived hooks
provide extension points for game-specific post-processing.
```

---

### Task 4: Migrate space game to RegisterComponent

**Files:**
- Modify: `internal/universe/replicators.go`
- Modify: `internal/universe/adapter.go`
- Modify: `internal/universe/factory.go`
- Modify: `internal/game/transfer.go`
- Delete: `internal/game/transfer_components.go`

- [ ] **Step 1: Rewrite replicators.go with RegisterComponent calls**

Replace the existing manual replicator functions with:

```go
func buildReplicationRegistry(gw *game.GameWorld) *pkguniverse.ReplicationRegistry {
    reg := pkguniverse.NewReplicationRegistry()

    // Auto-marshaled via reflection (simple structs)
    pkguniverse.RegisterComponent(reg, 1, gw.C.Velocity)
    pkguniverse.RegisterComponent(reg, 2, gw.C.Rotation)
    pkguniverse.RegisterComponent(reg, 3, gw.C.Health)
    pkguniverse.RegisterComponent(reg, 4, gw.C.Shield)
    pkguniverse.RegisterComponent(reg, 5, gw.C.ShipControl)
    pkguniverse.RegisterComponent(reg, 6, gw.C.Equipment)
    pkguniverse.RegisterComponent(reg, 7, gw.C.AbilitySet)
    pkguniverse.RegisterComponent(reg, 9, gw.C.MoveTarget)
    pkguniverse.RegisterComponent(reg, 10, gw.C.Minable)
    pkguniverse.RegisterComponent(reg, 11, gw.C.Lifetime)
    pkguniverse.RegisterComponent(reg, 13, gw.C.TargetLock)

    // Custom marshal: StatusEffects needs entity ref clearing
    pkguniverse.RegisterComponent(reg, 8, gw.C.StatusEffects,
        pkguniverse.WithPreMarshal(func(se *gamecomp.StatusEffects) {
            for i := uint8(0); i < se.Count; i++ {
                se.Effects[i].Source = ecs.Entity{}
            }
        }),
    )

    // Custom marshal: Inventory has a map field
    pkguniverse.RegisterComponent(reg, 12, gw.C.Inventory,
        pkguniverse.WithMarshal(game.MarshalInventory, game.UnmarshalInventory),
    )

    return reg
}
```

Delete all the old `velocityReplicator`, `rotationReplicator`, etc. functions and the `Repl*` constants.

- [ ] **Step 2: Wire hooks in factory.go**

```go
base.SetOnTransferReceived(func(entity ecs.Entity, frame *mmokit.TransferFrame) {
    gw.FinishTransferSpawn(entity, frame)
})

base.SetOnPlayerTransferReceived(func(entity ecs.Entity, frame *mmokit.TransferFrame) {
    gw.WireTransferPlayer(entity, ...)
    // ... sector change, map data messages ...
})
```

- [ ] **Step 3: Remove adapter overrides**

Delete `SerializeEntity` and `SpawnFromTransfer` from `adapter.go`. WorldBase handles everything via registry + hooks.

- [ ] **Step 4: Delete transfer_components.go**

All marshal/unmarshal helpers are replaced by reflection, except `MarshalInventory`/`UnmarshalInventory` (which move to transfer.go or a small helpers file since Inventory has a map).

Also delete `AppendTransferComponents` and `ApplyTransferComponents` from transfer.go.

- [ ] **Step 5: Verify and test**

Run: `go vet ./...`
Run: `go test ./internal/game/ ./internal/universe/ ./pkg/universe/`

- [ ] **Step 6: Commit**

```
refactor(game): use RegisterComponent for all component replication

Replaces ~200 lines of manual marshal/unmarshal helpers with
RegisterComponent calls. Only Inventory (map type) and StatusEffects
(entity ref clearing) need custom handling. All other components
auto-marshal via reflection.
```

---

### Task 5: Migrate slither to RegisterComponent

**Files:**
- Modify: `examples/slither/world.go`

- [ ] **Step 1: Replace inline ComponentReplicator blocks with RegisterComponent**

In `NewSlitherWorld`, replace the verbose inline `ComponentReplicator` registrations:

```go
reg := mmokit.NewReplicationRegistry()

// SnakeBody needs custom marshal (ring buffer + relative offsets)
mmokit.RegisterComponent(reg, 1, gw.SnakeBodyMap,
    mmokit.WithMarshal(
        func(b *SnakeBody) []byte { return marshalSnakeBodyForReplication(b, gw) },
        func(data []byte, b *SnakeBody) { unmarshalSnakeBodyFromReplication(data, b, gw) },
    ))

// Auto-marshaled
mmokit.RegisterComponent(reg, 2, gw.SnakeStateMap)
mmokit.RegisterComponent(reg, 3, gw.BotMap)
mmokit.RegisterComponent(reg, 4, gw.FoodMap)

gw.SetReplicationRegistry(reg)
```

Note: SnakeBody still needs custom marshal because of the ring buffer structure and relative-offset encoding. SnakeState, Bot, and Food are simple structs — reflection handles them.

The existing Collider registration (ID 0) should be removed — WorldBase handles Collider as a core component.

- [ ] **Step 2: Delete SerializeEntity override**

Remove the entire `func (gw *SlitherWorld) SerializeEntity(...)` method. WorldBase's default (registry-based) handles it.

The sector-delta adjustment for SnakeBody segments should be handled in the custom SnakeBody marshal function (which uses relative offsets, already sector-independent).

- [ ] **Step 3: Delete SpawnFromTransfer override or simplify to hook**

Set the player transfer hook instead of overriding:

```go
base.SetOnPlayerTransferReceived(func(entity ecs.Entity, frame *mmokit.TransferFrame) {
    if s := gw.Engine().Players.ByConnID(frame.ConnID); s != nil {
        s.Entity = entity
    }
    gw.SendSpawnedMsg(frame.ConnID, frame.NetworkID)
})
```

Delete the `SpawnFromTransfer` override.

- [ ] **Step 4: Delete now-unused marshal helpers**

Delete `marshalSnakeState`, `unmarshalSnakeStateInto`, `marshalBot`, `unmarshalBotInto`, `marshalFood`, `unmarshalFoodInto` — these are replaced by reflection. Keep `marshalSnakeBody*` / `unmarshalSnakeBody*` (custom).

- [ ] **Step 5: Verify**

Run: `go vet ./...`
Run: `cd examples/slither && go build .`

- [ ] **Step 6: Commit**

```
refactor(slither): use RegisterComponent — delete manual marshaling

SnakeState, Bot, and Food auto-marshal via reflection. Only SnakeBody
retains custom marshal (ring buffer + relative offsets). Deleted
SerializeEntity and SpawnFromTransfer overrides — framework handles
everything via registry and hooks.
```

---

### Task 6: Final cleanup and verification

- [ ] **Step 1: Remove any dead code**

Check for unused imports, unused functions, stale comments across all modified files.

- [ ] **Step 2: Full test suite**

Run: `go test ./...`
Expected: all pass

- [ ] **Step 3: Manual verification**

- Space game: `make dev`, cross sector boundary, verify ship transfers with full state
- Slither: `cd examples/slither && go run .`, cross boundary, verify smooth snake transfer
- Both: verify edge clamping, bot/NPC transfers, interactions with border entities

- [ ] **Step 4: Commit cleanup**

```
cleanup: remove dead code from component registry migration
```
