# AoI Auto-Broadcast Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Realize spec §4.5 — auto-broadcast typed messages to AoI viewers without per-verb plumbing. Default broadcast; `ServerOnly()` opt-out. Game events stop using `gamepb.AbilityCastResultMsg`; the Go struct IS the wire schema, encoded via the existing reflection codec, decoded on the TS client by sdkgen-generated classes.

**Architecture:** Three pieces ship together. (1) `pkg/universe/reflect_marshal.go` extends to encode `mmokit.Entity` as uint32 NetID via a small codec registry, so the reflection path can carry typed messages with Entity fields. (2) `pkg/mmokit/messaging_all.go::HandleAll[T]` extends to register T in a broadcast registry unless T implements `ServerOnly`. The framework owns post-handler broadcast: anchor extraction (msg's Entity-typed fields + the Send target), AoI filter per viewer, per-tick bundling into `WorldUpdateMsg.events`. (3) `cmd/sdkgen` reads the broadcast registry via `--dump-schema`, emits a TS class per broadcast-eligible message + a typed `client.on(MessageClass, handler)` API. The legacy `gamepb.AbilityCastResultMsg` proto + `WorldUpdateMsg.ability_events` field + `system_network.go::pendingAbilityEvents` plumbing all delete.

**Tech Stack:** Go 1.24, `pkg/mmokit` facade, `pkg/universe/reflect_marshal.go`, `cmd/sdkgen`, `web-pixi/sdk/`, protobuf for the envelope only.

**Spec:** `docs/superpowers/specs/2026-05-05-aoi-auto-broadcast-design.md`

**Predecessor plans (all on `feat/mmokit-entity-message-api`):** A+B (foundation), C (Damage + Mining), D (StatusEffect + legacy surface), E (Death/Currency + ECS sweep).

**Branch:** `feat/mmokit-entity-message-api` (continue on this branch — single ongoing dev branch per the solo-developer convention).

---

## Project memory to apply throughout

- `feedback_no_unnecessary_type_args` — drop generic params Go can infer.
- `feedback_no_backward_compat` — change consistently, no shims, no aliases.
- `feedback_mmokit_facade_only` — game code uses `mmokit.*`, never `pkg/` subpaths.
- `feedback_logging` — log significant state changes.
- `feedback_proto_field_cleanup` — never reserve old proto fields; renumber from 1 on changes (BUT trailing-field deletions don't require renumber).
- `feedback_wire_format_schema_runtime_match` — never conditionally drive `EngineBindingsConfig` from runtime state; schema dump must match server bytes.
- `feedback_position_quantization` — don't quantize positions.
- IDE diagnostics may be stale — trust `go vet` + `go test`.

---

## File structure

**New files (`pkg/universe/`):**

- `reflect_codec_registry.go` — registry of `reflect.Type` → custom encoder/decoder, used by `reflect_marshal.go` to delegate non-trivial field types (notably `mmokit.Entity`).

**New files (`pkg/mmokit/`):**

- `broadcast.go` — `BroadcastEligible(reflect.Type) bool`, `RegisteredBroadcastTypes(world *Process) []reflect.Type` (for sdkgen), `extractAnchors(msgPtr any, target Entity) []uint32`, anchor-position resolution, and the `serverOnly` reflective check.
- `broadcast_test.go` — unit tests for ServerOnly detection, anchor extraction, dedup.

**New files (`pkg/universe/`):**

- `broadcast_queue.go` — per-stage typed-event queue (write at handler-time, drain at end-of-tick).

**New files (`internal/game/`):**

- `auto_broadcast_test.go` — integration tests covering same-cell broadcast + cross-cell broadcast (source pre-handler + dest post-handler) + ServerOnly opt-out for KillCredit.

**Modified files (`pkg/universe/`):**

- `reflect_marshal.go` — add codec-registry lookup in marshal/unmarshal path; carry stage context through unmarshal.

**Modified files (`pkg/mmokit/`):**

- `entity.go` — `init()` registers `Entity`'s codec (encode → uint32 NetID, decode → `EntityByNetID(stage, netID)`).
- `messaging.go` / `messaging_all.go` — `Handle` / `HandleAll` extend to register broadcast unless ServerOnly. Adding a `BroadcastRegistry()` accessor on `Process` for sdkgen.

**Modified files (`pkg/universe/`):**

- `stage.go` — `RouteTypedMessage` fires post-handler auto-broadcast for same-cell sends; for cross-cell sends, source-cell pre-handler enqueue happens before the bridge dispatch.
- `bridge_*.go` (the cell-bridge impls) — dest-cell `HandleEngineAction` for `ActionTypedMessage` fires post-handler auto-broadcast on the dest stage.

**Modified files (`internal/game/`):**

- `verb_damage.go` — delete two `mmokit.Enqueue(gw.Queue, &gamepb.AbilityCastResultMsg{...})` calls.
- `verb_status.go` — delete two `mmokit.Enqueue(gw.Queue, &gamepb.AbilityCastResultMsg{...})` calls.
- `verb_mining.go` — `MineExtract` becomes broadcast-eligible automatically (no source code change needed if mmokit reflective broadcast is implicit). Add fields if the existing payload doesn't already carry what the client needs.
- `verb_death.go` — `Killed` becomes broadcast-eligible. Verify struct has fields the client needs to render a death effect; add if not.
- `system_ability.go` — locate the `mmokit.Enqueue(gw.Queue, &gamepb.AbilityCastResultMsg{...})` site (around line 338); migrate to a typed message OR delete if redundant with the verb broadcasts.
- `system_network.go` — delete `pendingAbilityEvents` field + `Peek` + `Drain`; delete the per-viewer `if visible[CasterId] || visible[TargetId]` filter; integrate framework's per-tick events bundle into the `WorldUpdateMsg` build path.

**Modified files (`proto/gamepb/`):**

- `game.proto` — add `TypedEvent` message; replace `WorldUpdateMsg.ability_events` (field 7) with `WorldUpdateMsg.events []TypedEvent` (also field 7); delete `AbilityCastResultMsg`.

**Modified files (`cmd/sdkgen/`):**

- `schema.go` — add `BroadcastTypes []BroadcastTypeSchema` to the JSON schema dump.
- `protoes.go` / `generate.go` — emit a TS class per broadcast type with field declarations + binary deserializer + typeID constant.
- `main.go` / supporting files — emit the typed `client.on(...)` dispatcher binding.

**Modified files (`web-pixi/sdk/`):**

- regenerated; new TS classes appear under `sdk/` for each broadcast type. Existing `client.ts` may need a small handcrafted addition to wire the dispatcher.

**Modified files (`web-pixi/src/`):**

- existing renderers that consume `AbilityCastResultMsg` migrate to typed handlers (`client.on(Damage, ...)`, etc.).

---

## Phase 1: Reflect codec extension

### Task 1.1: Codec registry primitive

**Files:**

- Create: `pkg/universe/reflect_codec_registry.go`
- Modify: `pkg/universe/reflect_marshal.go`

The reflect codec today inspects struct fields and either marshals primitives directly or recurses into structs. To handle `mmokit.Entity` (which lives in `pkg/mmokit` and contains a `*Stage` pointer field that the codec would reject), we need a registry of types with custom encode/decode functions. The codec consults the registry before falling back to the default path.

- [ ] **Step 1: Write the registry primitive**

```go
// pkg/universe/reflect_codec_registry.go
package universe

import (
    "reflect"
    "sync"
)

// ReflectCodec is a custom encoder/decoder for a single reflect.Type. Plug in
// when the default reflection path can't (or shouldn't) handle a type — the
// canonical example is mmokit.Entity, which contains a *Stage pointer the
// reflective codec would reject, but which we want to wire-encode as its
// uint32 NetID.
//
// Encode writes the field value as bytes; Size returns the byte width; Decode
// reads bytes back into the field. Stage context threads through Decode so
// codecs that need stage state (NetID resolution) can use it.
type ReflectCodec struct {
    Size   func() int                                // bytes used on the wire (fixed-size only for now)
    Encode func(buf []byte, v reflect.Value)         // write at offset 0
    Decode func(stage *Stage, data []byte, v reflect.Value) // read at offset 0
}

var (
    codecsMu sync.RWMutex
    codecs   = map[reflect.Type]*ReflectCodec{}
)

// RegisterReflectCodec installs a custom codec for type T. Call from init().
// Subsequent ReflectMarshal/ReflectUnmarshal calls on structs containing T
// fields delegate to the codec instead of recursing into T's fields.
func RegisterReflectCodec(t reflect.Type, codec *ReflectCodec) {
    codecsMu.Lock()
    defer codecsMu.Unlock()
    codecs[t] = codec
}

// LookupReflectCodec returns the codec for t, or nil if none is registered.
func LookupReflectCodec(t reflect.Type) *ReflectCodec {
    codecsMu.RLock()
    defer codecsMu.RUnlock()
    return codecs[t]
}
```

- [ ] **Step 2: Wire the registry into the marshal path**

Edit `pkg/universe/reflect_marshal.go`. In `validateStruct`, `marshalStruct`, `unmarshalStruct`, `structSize`, `valueSize`, before recursing into a struct field or rejecting an unsupported kind, check the registry:

```go
// Sketch — actual edit lands at every relevant case in reflect_marshal.go:

// In validateType:
if c := LookupReflectCodec(t); c != nil {
    return // codec handles it; no further validation needed
}

// In structSize:
if c := LookupReflectCodec(t); c != nil {
    return c.Size()
}
// ... existing default path

// In marshalStruct (per-field):
if c := LookupReflectCodec(f.Type); c != nil {
    c.Encode(buf[off:], v.Field(i))
    off += c.Size()
    continue
}
// ... existing default path

// In unmarshalStruct (per-field):
if c := LookupReflectCodec(f.Type); c != nil {
    c.Decode(stage, data[off:], v.Field(i))  // stage threaded through — see Step 3
    off += c.Size()
    continue
}
```

The `stage *Stage` parameter on Decode is new — see Step 3 for thread-through.

- [ ] **Step 3: Thread `*Stage` through unmarshal**

Today's `ReflectUnmarshal(data, ptr)` doesn't take a stage. Plan F's codec-on-decode (specifically the `mmokit.Entity` codec) needs stage context to call `EntityByNetID(stage, netID)`.

Add a stage-aware variant:

```go
// ReflectUnmarshalOnStage decodes data into the struct ptr, passing stage
// to any registered field codecs that need it. Use this when the struct
// contains types whose decode is stage-dependent (e.g. mmokit.Entity).
//
// Backward-compatible wrapper: ReflectUnmarshal calls this with stage=nil,
// which is sufficient for codecs that don't need stage context.
func ReflectUnmarshalOnStage(stage *Stage, data []byte, ptr any) {
    v := reflect.ValueOf(ptr)
    if v.Kind() == reflect.Ptr {
        v = v.Elem()
    }
    unmarshalStructOnStage(stage, data, 0, v)
}

func ReflectUnmarshal(data []byte, ptr any) {
    ReflectUnmarshalOnStage(nil, data, ptr)
}
```

Update internal helpers (`unmarshalStruct` → `unmarshalStructOnStage`) to thread the stage. Existing call sites in transfer codec etc. don't change; they call the no-stage `ReflectUnmarshal` which passes nil.

- [ ] **Step 4: Verify build**

```bash
go vet ./...
go test ./pkg/universe/...
```

Existing tests should pass — the registry is empty so the codec behaves identically.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/reflect_codec_registry.go pkg/universe/reflect_marshal.go
git commit -m "feat(universe): reflect codec registry for custom field encodings

Plug-in mechanism: RegisterReflectCodec(t, codec) installs a custom
encoder/decoder for a specific reflect.Type. The codec lookup happens
in validate/size/marshal/unmarshal before the default reflection path.
Stage context threads through unmarshal so codecs that need it (e.g.
mmokit.Entity) can resolve via the stage's NetID index.

No registered codecs yet; behavior unchanged. Plan F Phase 1.2 wires
the mmokit.Entity codec."
```

### Task 1.2: Register the mmokit.Entity codec

**Files:**

- Modify: `pkg/mmokit/entity.go`

- [ ] **Step 1: Add an init() that registers the Entity codec**

Append to `pkg/mmokit/entity.go`:

```go
import (
    "encoding/binary"
    "reflect"
    // ... existing imports
)

// Wire format: mmokit.Entity encodes as 4-byte little-endian NetID. Decode
// reconstructs via EntityByNetID using the stage threaded through ReflectUnmarshalOnStage.
//
// This is what makes the typed-message codec (used for cross-cell routing
// AND auto-broadcast wire bodies) carry Entity fields. Without it, the
// reflective codec at pkg/universe/reflect_marshal.go would reject Entity
// (it contains a *Stage pointer field which the default validator rejects).
func init() {
    pkguniverse.RegisterReflectCodec(reflect.TypeOf(Entity{}), &pkguniverse.ReflectCodec{
        Size: func() int { return 4 },
        Encode: func(buf []byte, v reflect.Value) {
            e := v.Interface().(Entity)
            binary.LittleEndian.PutUint32(buf, e.NetID())
        },
        Decode: func(stage *pkguniverse.Stage, data []byte, v reflect.Value) {
            netID := binary.LittleEndian.Uint32(data)
            v.Set(reflect.ValueOf(EntityByNetID(stage, netID)))
        },
    })
}
```

- [ ] **Step 2: Round-trip test**

Add to `pkg/mmokit/entity_test.go` (or a new file):

```go
func TestEntity_ReflectRoundtrip(t *testing.T) {
    type DamageMsg struct {
        Amount float32
        Source mmokit.Entity
        Slot   uint8
    }

    stageA, _, _ := newTwoCellLoopback(t) // existing test scaffolding
    stageA.SetGameWorld(testWorld{})

    // Spawn an entity with a known NetID on stage A.
    netID := uint32(42)
    spawnTestEntityOn(t, stageA, netID)
    src := mmokit.EntityByNetID(stageA, netID)

    in := DamageMsg{Amount: 25.0, Source: src, Slot: 3}
    body := pkguniverse.ReflectMarshal(&in)

    var out DamageMsg
    pkguniverse.ReflectUnmarshalOnStage(stageA, body, &out)

    if out.Amount != 25.0 || out.Slot != 3 {
        t.Fatalf("primitives: got %+v", out)
    }
    if out.Source.NetID() != 42 {
        t.Fatalf("Source.NetID: got %d, want 42", out.Source.NetID())
    }
    if !out.Source.Alive() {
        t.Fatal("Source should resolve to alive entity on stageA")
    }
}
```

- [ ] **Step 3: Verify + commit**

```bash
go test ./pkg/mmokit/ -run TestEntity_ReflectRoundtrip -v
go test ./pkg/... ./internal/...
git add pkg/mmokit/entity.go pkg/mmokit/entity_test.go
git commit -m "feat(mmokit): register Entity reflect codec (encodes as uint32 NetID)

mmokit.Entity now round-trips through the universe ReflectMarshal /
ReflectUnmarshalOnStage path: 4 bytes on the wire (the NetID), decode
reconstructs via EntityByNetID using the threaded stage.

This makes typed messages with Entity fields wire-encodable for both
cross-cell routing (already used) and AoI broadcast bodies (Plan F)."
```

---

## Phase 2: Broadcast registry + framework dispatcher

### Task 2.1: Broadcast registry primitive on Process

**Files:**

- Create: `pkg/universe/broadcast_queue.go`
- Modify: `pkg/universe/coordinator.go` (Process gains a per-stage queue accessor)

The broadcast queue accumulates typed events per stage per tick. Anchor extraction + AoI filter happens at end-of-tick drain time. Stage owns the queue; framework drains during the existing per-tick replication phase.

- [ ] **Step 1: Define the queue**

```go
// pkg/universe/broadcast_queue.go
package universe

import "sync"

// BroadcastEvent is one queued auto-broadcast event awaiting end-of-tick
// dispatch. Body is reflection-codec-encoded (use EncodeTypedMessage on
// the source side). Anchors are deduped Entity NetIDs on this stage whose
// positions drive the AoI filter.
type BroadcastEvent struct {
    TypeID  uint32
    Body    []byte
    Anchors []uint32
}

// BroadcastQueue is the per-stage per-tick queue. Drained by the framework
// at end-of-tick, AoI-filtered per viewer, packed into the WorldUpdateMsg
// envelope's events list.
type BroadcastQueue struct {
    mu     sync.Mutex
    events []BroadcastEvent
}

func (q *BroadcastQueue) Push(e BroadcastEvent) {
    q.mu.Lock()
    q.events = append(q.events, e)
    q.mu.Unlock()
}

func (q *BroadcastQueue) Drain() []BroadcastEvent {
    q.mu.Lock()
    out := q.events
    q.events = nil
    q.mu.Unlock()
    return out
}
```

- [ ] **Step 2: Wire the queue onto Stage**

Add a `*BroadcastQueue` field to `Stage`. Initialize in `NewStage`. Expose via `Stage.BroadcastQueue() *BroadcastQueue`.

- [ ] **Step 3: Verify + commit**

```bash
go vet ./pkg/universe/...
go test ./pkg/universe/...
git add pkg/universe/broadcast_queue.go pkg/universe/stage.go pkg/universe/coordinator.go
git commit -m "feat(universe): per-stage broadcast queue (drained at end-of-tick)"
```

### Task 2.2: ServerOnly detection + broadcast registration

**Files:**

- Create: `pkg/mmokit/broadcast.go`
- Create: `pkg/mmokit/broadcast_test.go`
- Modify: `pkg/mmokit/messaging.go`, `pkg/mmokit/messaging_all.go`

- [ ] **Step 1: ServerOnly + typeID + registration**

```go
// pkg/mmokit/broadcast.go
package mmokit

import (
    "hash/fnv"
    "reflect"
    "sync"

    pkguniverse "github.com/zenion/mmokit/pkg/universe"
)

// ServerOnly is the marker interface that opts a typed message OUT of
// AoI auto-broadcast. Implement via:
//
//   func (T) serverOnly() {}
//
// Used by KillCredit (currency rewards are server-internal accounting,
// no client visibility needed).
type ServerOnly interface{ serverOnly() }

var serverOnlyType = reflect.TypeOf((*ServerOnly)(nil)).Elem()

// IsServerOnly reflects T to determine if it implements the ServerOnly
// marker. Checked at registration time in HandleAll.
func IsServerOnly(t reflect.Type) bool {
    return t.Implements(serverOnlyType) ||
        reflect.PointerTo(t).Implements(serverOnlyType)
}

// TypeIDOf returns the stable wire identifier for a broadcast-eligible Go type.
// Computed as fnv32(reflect.Type.String()) — e.g. "game.Damage" → some uint32.
//
// Stable as long as the package path and type name don't change. Renaming
// is a deliberate wire-break in lockstep with SDK regeneration.
func TypeIDOf(t reflect.Type) uint32 {
    h := fnv.New32a()
    _, _ = h.Write([]byte(t.String()))
    return h.Sum32()
}

// broadcastRegistry holds the global registry of broadcast-eligible types.
// Sdkgen reads this via Process.BroadcastTypes() (see Task 2.5).
var (
    brMu  sync.RWMutex
    brSet = map[reflect.Type]struct{}{} // T (NOT *T)
)

// RegisterBroadcastType marks T as broadcast-eligible. Called from
// Handle/HandleAll when T does not implement ServerOnly. Idempotent.
func RegisterBroadcastType(t reflect.Type) {
    brMu.Lock()
    brSet[t] = struct{}{}
    brMu.Unlock()
}

// BroadcastTypes returns the registered broadcast-eligible types. Used by
// sdkgen to emit TS class declarations.
func BroadcastTypes() []reflect.Type {
    brMu.RLock()
    defer brMu.RUnlock()
    out := make([]reflect.Type, 0, len(brSet))
    for t := range brSet {
        out = append(out, t)
    }
    // sort for determinism (sdkgen output stability)
    sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
    return out
}
```

(Add `"sort"` to the imports.)

- [ ] **Step 2: Hook into Handle/HandleAll**

In `pkg/mmokit/messaging.go::Handle`, after the existing dispatcher registration, add:

```go
func Handle[M any](stage *pkguniverse.Stage, fn func(target Entity, msg *M)) {
    d := stage.Dispatcher()
    d.SetEntityCtor(entityCtorAdapter)
    var zero M
    msgType := reflect.TypeOf(zero)
    d.Register(typeKeyOf(msgType), msgType, reflect.ValueOf(fn))
    if !IsServerOnly(msgType) {
        RegisterBroadcastType(msgType)
    }
}
```

`HandleAll` already delegates to `Handle` per stage, so `RegisterBroadcastType` runs at first Handle call. The registry is global (one entry per type, regardless of how many stages share the registration).

- [ ] **Step 3: Tests**

```go
// pkg/mmokit/broadcast_test.go
package mmokit_test

import (
    "reflect"
    "testing"
    "github.com/zenion/mmokit/pkg/mmokit"
)

type testBroadcastableMsg struct {
    Caster mmokit.Entity
    Slot   uint8
}

type testServerOnlyMsg struct {
    Currency uint32
}
func (testServerOnlyMsg) serverOnly() {}

func TestIsServerOnly(t *testing.T) {
    if mmokit.IsServerOnly(reflect.TypeOf(testBroadcastableMsg{})) {
        t.Fatal("testBroadcastableMsg should NOT be ServerOnly")
    }
    if !mmokit.IsServerOnly(reflect.TypeOf(testServerOnlyMsg{})) {
        t.Fatal("testServerOnlyMsg SHOULD be ServerOnly")
    }
}

func TestTypeIDOf_Stable(t *testing.T) {
    a := mmokit.TypeIDOf(reflect.TypeOf(testBroadcastableMsg{}))
    b := mmokit.TypeIDOf(reflect.TypeOf(testBroadcastableMsg{}))
    if a != b {
        t.Fatalf("TypeIDOf is not stable: %d vs %d", a, b)
    }
    if a == 0 {
        t.Fatal("TypeIDOf returned 0 (suspicious)")
    }
}
```

- [ ] **Step 4: Verify + commit**

```bash
go vet ./...
go test ./pkg/mmokit/ -run "TestIsServerOnly\|TestTypeIDOf"
git add pkg/mmokit/broadcast.go pkg/mmokit/broadcast_test.go pkg/mmokit/messaging.go
git commit -m "feat(mmokit): broadcast registry + ServerOnly detection + typeID

Handle/HandleAll[T] auto-register T in the broadcast registry unless T
implements ServerOnly. typeID is fnv32(reflect.Type.String()) — stable
across rebuilds, regenerated when Go types are renamed."
```

### Task 2.3: Anchor extraction

**Files:**

- Modify: `pkg/mmokit/broadcast.go`
- Modify: `pkg/mmokit/broadcast_test.go`

- [ ] **Step 1: Implement anchor extraction**

Append to `broadcast.go`:

```go
// ExtractAnchors reflects on msgPtr (pointer to a broadcast-eligible struct)
// and returns deduped NetIDs of all Entity-typed fields plus the receiver.
//
// Recurses into sub-struct fields. Skips zero-value Entities (NetID == 0).
// Returns deduped slice in stable order (extracted-first wins).
func ExtractAnchors(msgPtr any, target Entity) []uint32 {
    seen := map[uint32]struct{}{}
    var out []uint32

    add := func(nid uint32) {
        if nid == 0 {
            return
        }
        if _, dup := seen[nid]; dup {
            return
        }
        seen[nid] = struct{}{}
        out = append(out, nid)
    }

    add(target.NetID())

    v := reflect.ValueOf(msgPtr)
    if v.Kind() == reflect.Ptr {
        v = v.Elem()
    }
    walkAnchors(v, add)
    return out
}

var entityType = reflect.TypeOf(Entity{})

func walkAnchors(v reflect.Value, add func(uint32)) {
    if v.Kind() != reflect.Struct {
        return
    }
    for i := 0; i < v.NumField(); i++ {
        f := v.Field(i)
        if f.Type() == entityType {
            add(f.Interface().(Entity).NetID())
            continue
        }
        if f.Kind() == reflect.Struct {
            walkAnchors(f, add)
        }
    }
}
```

- [ ] **Step 2: Tests**

```go
// pkg/mmokit/broadcast_test.go (extend)

func TestExtractAnchors_Dedup(t *testing.T) {
    type Msg struct {
        Caster mmokit.Entity
        Source mmokit.Entity
        Slot   uint8
    }
    // Stage scaffolding: we just need EntityByNetID; positions don't matter here.
    stage, _, _ := newTwoCellLoopback(t)
    spawnTestEntityOn(t, stage, 100)
    spawnTestEntityOn(t, stage, 200)

    e100 := mmokit.EntityByNetID(stage, 100)
    e200 := mmokit.EntityByNetID(stage, 200)

    msg := Msg{Caster: e100, Source: e100, Slot: 1}
    anchors := mmokit.ExtractAnchors(&msg, e200)

    // Expect: [200 (target), 100 (Caster)]; Source dedups to 100.
    if len(anchors) != 2 || anchors[0] != 200 || anchors[1] != 100 {
        t.Fatalf("anchors: got %v, want [200 100]", anchors)
    }
}

func TestExtractAnchors_SkipZero(t *testing.T) {
    type Msg struct {
        Killer mmokit.Entity // intentionally zero (unattributed)
    }
    stage, _, _ := newTwoCellLoopback(t)
    spawnTestEntityOn(t, stage, 50)
    target := mmokit.EntityByNetID(stage, 50)

    anchors := mmokit.ExtractAnchors(&Msg{}, target)
    if len(anchors) != 1 || anchors[0] != 50 {
        t.Fatalf("anchors: got %v, want [50] (zero Killer dropped)", anchors)
    }
}
```

- [ ] **Step 3: Verify + commit**

```bash
go test ./pkg/mmokit/ -run "TestExtractAnchors" -v
git add pkg/mmokit/broadcast.go pkg/mmokit/broadcast_test.go
git commit -m "feat(mmokit): anchor extraction (Entity fields + receiver, deduped)"
```

### Task 2.4: Integrate broadcast queue into Send/Handle

**Files:**

- Modify: `pkg/universe/stage.go::RouteTypedMessage`
- Modify: `pkg/universe/stage.go::HandleEngineAction`

The two integration points:

- **Same-cell:** `RouteTypedMessage` runs the handler synchronously. Post-handler, push to broadcast queue.
- **Cross-cell:** `RouteTypedMessage` ships the message via the bridge. **Source-cell pre-handler push.** On the dest cell, `HandleEngineAction` runs the handler post-deliver. Push to dest's broadcast queue post-handler.

- [ ] **Step 1: Same-cell post-handler push**

Edit `RouteTypedMessage` (around line 1653):

```go
func (s *Stage) RouteTypedMessage(targetNetID uint32, msgPtr any) bool {
    h, presence, ok := s.LookupNetID(targetNetID)
    if !ok {
        return false
    }
    if presence == PresenceLive {
        s.Dispatcher().Invoke(targetNetID, msgPtr)
        s.maybeBroadcast(targetNetID, msgPtr) // NEW
        return true
    }
    // Replica — route to source cell.
    if !s.replicaMap.HasAll(h) {
        return false
    }
    rep := s.replicaMap.Get(h)
    typeName := reflect.TypeOf(msgPtr).Elem().String()
    payload := EncodeTypedMessage(typeName, msgPtr)
    if s.bridge == nil {
        return false
    }
    if _, isNoop := s.bridge.(NoopBridge); isNoop {
        // ... existing log
        return false
    }
    s.bridge.SendAction(MeshCellID(rep.SourceCellID), &CrossCellAction{
        Type:         ActionTypedMessage,
        TargetNetID:  rep.SourceNetID,
        SourceCellID: string(s.cellID),
        Payload:      payload,
    })
    s.maybeBroadcast(targetNetID, msgPtr) // NEW — source-cell pre-handler push
    return true
}
```

`maybeBroadcast` is a new method on Stage (Step 3 below).

- [ ] **Step 2: Dest-cell post-handler push**

Edit `HandleEngineAction`:

```go
func (s *Stage) HandleEngineAction(action *CrossCellAction) bool {
    if action == nil || action.Type != ActionTypedMessage {
        return false
    }
    typeName, payload := SplitTypedMessage(action.Payload)
    msgType := s.Dispatcher().MessageType(typeName)
    if msgType == nil {
        return true
    }
    msgPtr := reflect.New(msgType)
    DecodeTypedMessageOnStage(s, payload, msgPtr.Interface()) // stage-aware decode
    s.Dispatcher().Invoke(action.TargetNetID, msgPtr.Interface())
    s.maybeBroadcast(action.TargetNetID, msgPtr.Interface()) // NEW — post-handler
    return true
}
```

`DecodeTypedMessageOnStage` is the stage-threaded variant — wraps `ReflectUnmarshalOnStage` from Task 1.1. Add it to `pkg/universe/typed_message_codec.go`:

```go
// DecodeTypedMessageOnStage unmarshals payload bytes into ptr (pointer to
// struct), threading stage to any field codecs that need it (notably
// mmokit.Entity).
func DecodeTypedMessageOnStage(stage *Stage, payload []byte, ptr any) {
    ReflectUnmarshalOnStage(stage, payload, ptr)
}
```

(Existing `DecodeTypedMessage` continues to work via the no-stage path, but every game callsite migrates to `DecodeTypedMessageOnStage` since broadcast bodies need stage context.)

- [ ] **Step 3: Implement `Stage.maybeBroadcast`**

```go
// maybeBroadcast pushes msg to this stage's broadcast queue if:
//   1. The msg type is in the broadcast registry (mmokit.HandleAll registered
//      it as not-ServerOnly).
//   2. msg has at least one anchor whose position resolves on this stage.
//
// Called twice for cross-cell sends (source pre-handler + dest post-handler);
// once for same-cell sends.
func (s *Stage) maybeBroadcast(targetNetID uint32, msgPtr any) {
    msgType := reflect.TypeOf(msgPtr).Elem()
    if !mmokitBroadcastEligible(msgType) {
        return
    }

    // Encode the body via the existing typed-message codec.
    typeName := msgType.String()
    body := EncodeTypedMessage(typeName, msgPtr)
    // Split off the typeName prefix — we carry typeID in the envelope, not
    // the body. Or keep prefix; sdkgen-side dispatcher reads typeID first.
    //
    // Actually: simpler to skip the type-name prefix in broadcast bodies
    // since the envelope already carries typeID. The reflective body is
    // just the struct bytes.
    bodyOnly := ReflectMarshal(msgPtr)
    typeID := mmokitTypeIDOf(msgType)

    anchors := mmokitExtractAnchors(msgPtr, EntityByNetIDOnStage(s, targetNetID))
    // Filter anchors to those resolvable on this stage.
    var localAnchors []uint32
    for _, nid := range anchors {
        if _, _, ok := s.LookupNetID(nid); ok {
            localAnchors = append(localAnchors, nid)
        }
    }
    if len(localAnchors) == 0 {
        return // no anchors on this stage; nothing to broadcast here
    }

    s.broadcastQueue.Push(BroadcastEvent{
        TypeID:  typeID,
        Body:    bodyOnly,
        Anchors: localAnchors,
    })
    _ = body // remove; was the typename-prefixed variant
}
```

The `mmokitBroadcastEligible`, `mmokitTypeIDOf`, `mmokitExtractAnchors`, `EntityByNetIDOnStage` helpers are stage-side hooks into the mmokit registry. To avoid an import cycle (`pkg/universe` → `pkg/mmokit`), we use the same indirection pattern as the typed-message dispatcher:

- `pkg/universe/stage.go` defines a registry-of-callbacks struct: `var broadcastHooks struct { Eligible func(reflect.Type) bool; TypeID func(reflect.Type) uint32; Anchors func(any, Entity-like) []uint32; ... }`.
- `pkg/mmokit/init.go` (or similar) populates the hooks at init.

Pattern: same as `entityCtorAdapter` already wired in `pkg/mmokit/messaging.go`. Adapt.

- [ ] **Step 4: Tests**

(Defer to Task 2.5 / Task 2.6 below — full integration test exercises this.)

- [ ] **Step 5: Verify + commit**

```bash
go vet ./...
go test ./pkg/...
git add pkg/universe/stage.go pkg/universe/typed_message_codec.go pkg/mmokit/init.go pkg/mmokit/broadcast.go
git commit -m "feat(universe,mmokit): hook auto-broadcast into RouteTypedMessage + HandleEngineAction

Same-cell Send: post-handler push to local broadcast queue.
Cross-cell Send: source-cell pre-handler push (before bridge dispatch);
dest-cell post-handler push (after handler runs on dest).

Anchors filtered to those resolvable on the local stage; messages with
no local anchors don't push (avoids zero-recipient broadcasts)."
```

### Task 2.5: Drain broadcast queue into WorldUpdateMsg

**Files:**

- Modify: `internal/game/system_network.go`
- Modify: `pkg/mmokit/network_helpers.go` (or wherever the per-tick replication frame is built)

The framework drains the per-stage broadcast queue at end-of-tick and packs into the `WorldUpdateMsg.events` field (added in Phase 3). For each viewer, AoI-filter the queue's events by anchor positions vs the viewer's AoI radius.

- [ ] **Step 1: Wire the drain into NetworkSystem.afterSend**

Today `afterSend` runs per-viewer, processes `pendingAbilityEvents`. Change it to consume the per-stage broadcast queue:

```go
func (s *NetworkSystem) beforeTick(tick uint32) {
    // ... existing
    s.pendingChat = mmokit.Peek[*enginepb.ChatMsg](gw.Queue)
    // DELETE: s.pendingAbilityEvents = mmokit.Peek[*gamepb.AbilityCastResultMsg](gw.Queue)
    s.pendingBroadcasts = gw.Stage.BroadcastQueue().Drain() // NEW
}

func (s *NetworkSystem) afterSend(viewer *mmokit.ViewerInfo, visible map[uint32]bool) {
    gw := s.World()
    // ... existing chat send
    // DELETE: ability-event filter loop
    var events []*gamepb.TypedEvent
    for _, evt := range s.pendingBroadcasts {
        // viewer sees the event if any anchor's NetID is in their visible set.
        for _, nid := range evt.Anchors {
            if visible[nid] {
                events = append(events, &gamepb.TypedEvent{
                    TypeId: evt.TypeID,
                    Body:   evt.Body,
                })
                break
            }
        }
    }
    if len(events) > 0 {
        frame := gw.ServerEvents().Build(uint32(enginepb.ServerEventCode_SE_WORLD_UPDATE), &gamepb.WorldUpdateMsg{
            Tick:   gw.eng.Tick,
            Events: events,
        })
        gw.eng.ConnMgr.Send(viewer.ConnID, frame)
    }
    // ... existing own-state send
}

func (s *NetworkSystem) afterTick(tick uint32) {
    // ... existing chat drain to docked players
    mmokit.Drain[*enginepb.ChatMsg](gw.Queue)
    // DELETE: mmokit.Drain[*gamepb.AbilityCastResultMsg](gw.Queue)
    // (BroadcastQueue is already drained in beforeTick; nothing left to do)
}
```

`pendingAbilityEvents` field on `NetworkSystem` deletes; replace with `pendingBroadcasts []pkguniverse.BroadcastEvent`.

NOTE: this task can't fully land until Phase 3 adds `gamepb.TypedEvent` and `WorldUpdateMsg.events`. Bundle the two-phase work into a single commit, OR commit Phase 3 first and this task second. Recommend Phase 3 first for atomicity.

- [ ] **Step 2: Verify + commit (after Phase 3 lands)**

```bash
go vet ./...
go test ./pkg/... ./internal/...
git add internal/game/system_network.go
git commit -m "refactor(game): NetworkSystem drains the framework broadcast queue

Replaces the hand-rolled pendingAbilityEvents path. Per-viewer AoI
filter compares each event's anchor NetIDs against the viewer's
visible set. Events with at least one visible anchor go into the
viewer's WorldUpdateMsg.events bundle."
```

### Task 2.6: Same-cell broadcast integration test

**Files:**

- Create: `internal/game/auto_broadcast_test.go`

- [ ] **Step 1: Test setup**

```go
package game

import (
    "testing"

    gamecomp "github.com/zenion/mmokit/internal/component"
    "github.com/zenion/mmokit/pkg/mmokit"
    pkguniverse "github.com/zenion/mmokit/pkg/universe"
)

// TestAutoBroadcast_SameCell_PostHandler verifies that target.Send(&Damage{...})
// pushes the post-handler msg into the stage's broadcast queue.
func TestAutoBroadcast_SameCell_PostHandler(t *testing.T) {
    gw, _ := newTestGameWorld()
    gw.Stage.SetGameWorld(gw)
    mmokit.Handle(gw.Stage, damageHandler)

    target := newTestShip(t, gw, 101, 100, 0)
    caster := newTestShip(t, gw, 202, 100, 0)
    targetE := mmokit.EntityByNetID(gw.Stage, target)
    casterE := mmokit.EntityByNetID(gw.Stage, caster)

    targetE.Send(&Damage{Amount: 25, Source: casterE, Slot: 0, AbilityType: 1})

    events := gw.Stage.BroadcastQueue().Drain()
    if len(events) != 1 {
        t.Fatalf("broadcast queue: got %d events, want 1", len(events))
    }
    e := events[0]
    if e.TypeID != mmokit.TypeIDOf(reflect.TypeOf(Damage{})) {
        t.Errorf("typeID mismatch")
    }
    if len(e.Anchors) != 2 {
        t.Errorf("anchors: got %d, want 2 (target+caster)", len(e.Anchors))
    }

    // Verify the body decodes back to the post-handler msg (Dealt populated).
    var decoded Damage
    pkguniverse.ReflectUnmarshalOnStage(gw.Stage, e.Body, &decoded)
    if decoded.Dealt <= 0 {
        t.Errorf("Dealt: got %v, want >0 (post-handler value)", decoded.Dealt)
    }
}

// TestAutoBroadcast_ServerOnly_DoesNotBroadcast verifies KillCredit (which has
// the serverOnly() marker) doesn't push to the broadcast queue.
func TestAutoBroadcast_ServerOnly_DoesNotBroadcast(t *testing.T) {
    gw, _ := newTestGameWorld()
    gw.Stage.SetGameWorld(gw)
    mmokit.Handle(gw.Stage, killCreditHandler)

    killer := newTestPlayerShip(t, gw, 303, "alice")
    killerE := mmokit.EntityByNetID(gw.Stage, killer)

    killerE.Send(&KillCredit{Currency: 1, Amount: 50})

    events := gw.Stage.BroadcastQueue().Drain()
    if len(events) != 0 {
        t.Fatalf("ServerOnly should suppress broadcast: got %d events", len(events))
    }
}
```

- [ ] **Step 2: Cross-cell test**

```go
// TestAutoBroadcast_CrossCell_BothSidesEnqueue verifies that Send to a
// replica produces a source-cell pre-handler push AND a dest-cell post-handler
// push.
func TestAutoBroadcast_CrossCell_BothSidesEnqueue(t *testing.T) {
    cellA, cellB, drain := /* two-cell setup */
    cellA.SetGameWorld(testWorld{})
    cellB.SetGameWorld(testWorld{})
    mmokit.Handle(cellB, damageHandler) // handler runs on dest cell

    // Spawn target authoritative on B, replica on A.
    targetNetID := uint32(101)
    spawnTestEntityOn(t, cellB, targetNetID)
    pushBorderReplicaTo(t, cellB, cellA, targetNetID)
    casterNetID := uint32(202)
    spawnTestEntityOn(t, cellA, casterNetID)

    targetOnA := mmokit.EntityByNetID(cellA, targetNetID) // replica
    casterOnA := mmokit.EntityByNetID(cellA, casterNetID)

    targetOnA.Send(&Damage{Amount: 25, Source: casterOnA, Slot: 0, AbilityType: 1})

    // Source cell A: pre-handler broadcast pushed.
    eventsA := cellA.BroadcastQueue().Drain()
    if len(eventsA) != 1 {
        t.Fatalf("source cell broadcast: got %d, want 1", len(eventsA))
    }

    // Drive the bridge so the cross-cell action delivers to B.
    drain(50 * time.Millisecond)

    // Dest cell B: handler runs, post-handler broadcast pushed.
    eventsB := cellB.BroadcastQueue().Drain()
    if len(eventsB) != 1 {
        t.Fatalf("dest cell broadcast: got %d, want 1", len(eventsB))
    }
}
```

- [ ] **Step 3: Verify + commit**

```bash
go test ./internal/game/ -run TestAutoBroadcast -v
git add internal/game/auto_broadcast_test.go
git commit -m "test(game): auto-broadcast — same-cell, ServerOnly, cross-cell"
```

---

## Phase 3: Wire format envelope change

### Task 3.1: Add TypedEvent + WorldUpdateMsg.events; delete ability_events

**Files:**

- Modify: `proto/gamepb/game.proto`
- Regenerate: `gen/go/gamepb/`, `gen/es/gamepb/`

- [ ] **Step 1: Edit the proto**

In `proto/gamepb/game.proto`:

```protobuf
// AbilityCastResultMsg deletion: remove the entire `message AbilityCastResultMsg { ... }`
// block (around line 308). NO RESERVED WORDS — per project memory feedback_proto_field_cleanup,
// don't reserve old proto fields; just delete.

// WorldUpdateMsg edit:
message WorldUpdateMsg {
    uint32 tick = 1;
    uint32 ack_input_seq = 2;
    repeated EntityState entities = 3;
    repeated uint32 removed_ids = 4;
    repeated enginepb.ChatMsg chat_messages = 5;
    repeated uint32 killed_ids = 6;
    repeated TypedEvent events = 7;  // REPLACED ability_events (was field 7)
}

// New message:
message TypedEvent {
    uint32 type_id = 1;
    bytes  body    = 2;
}
```

`ability_events` was field 7, the trailing field. Replacing it with `events` at the same field number is a wire-break (incompatible with old clients) but keeps numbering tight.

- [ ] **Step 2: Regenerate proto**

```bash
just proto
```

Verify `gen/go/gamepb/game.pb.go` has `Events []*TypedEvent` on `WorldUpdateMsg` and no `AbilityCastResultMsg`.

- [ ] **Step 3: Verify build (will break — uses sites for AbilityCastResultMsg + AbilityEvents still exist)**

```bash
go vet ./...
```

Expected errors: callers of `gamepb.AbilityCastResultMsg` and `gamepb.WorldUpdateMsg.AbilityEvents` (in `system_network.go` and verb_*.go). Tasks 5/6 fix these. The proto change must commit AFTER Phase 5/6 land — OR — bundle 3+5+6 in a single commit. Recommend bundling.

Actually: **commit Phase 3 LAST in this group of related changes**, after Phase 5+6 sites are migrated. Path:

1. Plan F Phases 1+2 (codec, registry, framework) — independent commits.
2. Plan F Phases 5+6 (verb-handler + NetworkSystem cleanup) — but these can't fully land without Phase 3's `TypedEvent`.
3. Plan F Phase 3 (proto change + regen) — wire-format break.
4. Plan F Phase 4 (sdkgen + TS client) — depends on Phase 3.

The cleanest sequence: **bundle Phase 3 + Phase 5 + Phase 6 in a single squash-commit** so the wire format and the consumers move together. Phase 4 (sdkgen / TS client) lands as a separate next commit since it only needs the proto + the registry, not the consumer cleanup. Or: commit each as its own change with a known-broken-build window between them.

Pragmatic call: **single big commit for Phase 3+5+6.** Marked clearly. Easier to bisect, single wire-format-break point.

(Implementer: choose split based on what builds cleanly. The plan accepts either.)

---

## Phase 4: sdkgen extension + TS client

### Task 4.1: Schema dump includes broadcast types

**Files:**

- Modify: `pkg/mmokit/protocol.go::AssembleFromProcess`
- Modify: `cmd/sdkgen/schema.go`

- [ ] **Step 1: Add broadcast types to the JSON schema dump**

In `pkg/mmokit/protocol.go::AssembleFromProcess`, after harvesting the runtime registries, also harvest broadcast types:

```go
// Inside AssembleFromProcess (or a new method called from it):
for _, t := range BroadcastTypes() {
    p.BroadcastTypes = append(p.BroadcastTypes, BroadcastTypeSchema{
        Name:   t.String(),
        TypeID: TypeIDOf(t),
        Fields: schemaFieldsOf(t),
    })
}
```

`schemaFieldsOf(t)` is a new helper that reflects on the struct and returns a serializable schema (field name, type, wire offset). Use the existing component-schema generator as a model — `cmd/sdkgen/schema.go::FieldSchema` has the shape.

- [ ] **Step 2: Add the schema type**

In `cmd/sdkgen/schema.go`:

```go
// BroadcastTypeSchema describes a typed message that the framework
// auto-broadcasts to AoI viewers. Mirrors the Go struct so sdkgen can emit
// a TS class with the same shape.
type BroadcastTypeSchema struct {
    Name   string         `json:"name"`    // reflect.Type.String()
    TypeID uint32         `json:"type_id"` // fnv32(Name)
    Fields []FieldSchema  `json:"fields"`
}
```

Add `BroadcastTypes []BroadcastTypeSchema` to the existing `ProtocolSchema` struct.

- [ ] **Step 3: Verify dump output**

Run `--dump-schema` against the example:

```bash
go run ./examples/4node-basic --dump-schema | jq '.broadcast_types'
```

Should list at least Damage, Status, MineExtract, Killed entries with TypeID + field shapes.

- [ ] **Step 4: Commit**

```bash
git add pkg/mmokit/protocol.go cmd/sdkgen/schema.go
git commit -m "feat(mmokit,sdkgen): broadcast registry exposed via --dump-schema"
```

### Task 4.2: Emit TS class per broadcast type

**Files:**

- Modify: `cmd/sdkgen/generate.go` (or wherever the TS class emit lives)

- [ ] **Step 1: Mirror the entity-class emit logic for broadcast types**

The existing entity-class generator produces TS classes with field declarations + a static `decode(buf, offset)` method. For broadcast types, emit similar:

```ts
// Generated for Damage:
export class Damage {
    static readonly typeID = 0xa1b2c3d4;

    amount: number = 0;
    source: number = 0;        // Entity field encoded as uint32 NetID
    slot: number = 0;
    abilityType: number = 0;
    dealt: number = 0;
    killed: boolean = false;

    static decode(buf: Uint8Array): Damage {
        const dv = new DataView(buf.buffer, buf.byteOffset, buf.byteLength);
        let off = 0;
        const m = new Damage();
        m.amount = dv.getFloat32(off, true); off += 4;
        m.source = dv.getUint32(off, true); off += 4;  // Entity → uint32
        m.slot = dv.getUint8(off); off += 1;
        m.abilityType = dv.getUint8(off); off += 1;
        m.dealt = dv.getFloat32(off, true); off += 4;
        m.killed = dv.getUint8(off) !== 0; off += 1;
        return m;
    }
}
```

The decoder mirrors the server-side `ReflectMarshal` field order. Field types + sizes must match exactly:

| Go type | TS type | Wire size |
|---|---|---|
| `float32` | `number` | 4 (LE float) |
| `uint8` / `int8` | `number` | 1 |
| `uint16` / `int16` | `number` | 2 (LE) |
| `uint32` / `int32` | `number` | 4 (LE) |
| `uint64` / `int64` | `number` | 8 (LE) — note: JS bigint or precision-clamp |
| `bool` | `boolean` | 1 |
| `string` | `string` | length-prefixed (existing pattern) |
| `mmokit.Entity` | `number` (NetID) | 4 (LE uint32) |

- [ ] **Step 2: Generate one class per broadcast type**

Iterate `schema.BroadcastTypes`. Emit each into a new file `sdk/broadcasts.ts` (or per-class files; match existing entity-class style).

- [ ] **Step 3: Emit the typed dispatcher binding**

In the generated `client.ts` (or a sibling `dispatcher.ts`):

```ts
type BroadcastConstructor<T> = { typeID: number; decode(buf: Uint8Array): T };

export class TypedDispatcher {
    private handlers = new Map<number, (msg: any) => void>();

    on<T>(cls: BroadcastConstructor<T>, fn: (msg: T) => void) {
        this.handlers.set(cls.typeID, (raw) => fn(cls.decode(raw)));
    }

    dispatch(typeID: number, body: Uint8Array) {
        const fn = this.handlers.get(typeID);
        if (fn) fn(body);
    }
}
```

The main client class instantiates `TypedDispatcher` and routes `WorldUpdateMsg.events` through it:

```ts
// In the generated client's WorldUpdateMsg handler:
for (const evt of msg.events) {
    this.typedDispatcher.dispatch(evt.typeID, evt.body);
}
```

- [ ] **Step 4: Regenerate web-pixi SDK**

```bash
just client-sdk examples/4node-basic
just space-sdk
```

Verify generated TS classes in `web-pixi/sdk/` (or wherever the example's SDK lives).

- [ ] **Step 5: Commit**

```bash
git add cmd/sdkgen/ web-pixi/sdk/
git commit -m "feat(sdkgen,client): TS classes + typed dispatcher for broadcast events"
```

### Task 4.3: Migrate web-pixi handlers from AbilityCastResultMsg to typed dispatch

**Files:**

- Modify: `web-pixi/src/` (wherever the existing AbilityCastResultMsg consumer lives)

- [ ] **Step 1: Find the existing handler**

```bash
grep -rn "AbilityCastResultMsg\|abilityEvents\|ability_events" web-pixi/src/
```

- [ ] **Step 2: Migrate to typed handlers**

Replace the existing `AbilityCastResultMsg` switch with typed `client.on(...)`:

```ts
import { Damage, MineExtract, Status, Killed } from '../sdk/broadcasts';

client.on(Damage, (msg) => {
    renderDamageNumber(msg.source, msg.dealt, msg.slot);
    fireWeaponAnimation(msg.source, msg.abilityType, msg.slot);
});
client.on(MineExtract, (msg) => {
    showMiningSpark(msg.caster, msg.beam);
});
client.on(Status, (msg) => {
    fireStatusAnimation(msg.source, msg.effectType, msg.slot, msg.abilityType);
});
client.on(Killed, (msg) => {
    spawnExplosion(msg.killer);
});
```

Field names match the generated TS (which mirror the Go struct field names lowercased).

- [ ] **Step 3: Smoke-test in browser**

```bash
just dev
```

Open browser, fire abilities, mine asteroids. Verify:
- Damage numbers render
- Cast animations fire
- Mining beam visual works
- DoT (IonBurn) cast animation fires
- Death explosions render

- [ ] **Step 4: Commit**

```bash
git add web-pixi/src/
git commit -m "refactor(web-pixi): migrate ability event consumers to typed dispatch"
```

---

## Phase 5+6: Verb handler + NetworkSystem cleanup (bundled with Phase 3)

### Task 5.1: Delete manual Enqueue calls in verb_*.go

**Files:**

- Modify: `internal/game/verb_damage.go` (×2 sites)
- Modify: `internal/game/verb_status.go` (×2 sites)
- Modify: `internal/game/system_ability.go` (×1 site, around line 338)

- [ ] **Step 1: Delete in verb_damage.go**

```bash
grep -n "mmokit.Enqueue(gw.Queue, &gamepb.AbilityCastResultMsg" internal/game/verb_damage.go
```

Two sites: one inside `damageHandler` (post-handler enqueue), one inside `gw.Damage(...)` helper (cross-cell pre-broadcast). With auto-broadcast both come for free from the framework.

Delete both blocks. After edit, `damageHandler` is purely the Health/Shield math + result-field mutation; `gw.Damage(...)` is just `target.Send(&Damage{...})`.

Imports: `gamepb` may become unused — clean up.

- [ ] **Step 2: Delete in verb_status.go**

Same pattern. Two sites: handler post-enqueue + helper pre-enqueue.

- [ ] **Step 3: Delete (or migrate) in system_ability.go**

```bash
grep -n "mmokit.Enqueue(gw.Queue, &gamepb.AbilityCastResultMsg" internal/game/system_ability.go
```

Read the surrounding code. If it's an animation cue for an ability that already has a typed-message verb (Damage / Status / Mining), it's redundant — delete. If it's for an ability with no typed-message verb yet (e.g. a fire-and-forget visual), migrate to a small typed message.

Pragmatic call: most likely deletable. Verify by smoke-testing the example after Phase 5+6 commit.

- [ ] **Step 4: Verify build**

```bash
go vet ./internal/game/...
```

Expect errors at this point: `system_network.go` still references `gamepb.AbilityCastResultMsg` until Task 6.1 fixes it. Bundle Tasks 5.1 + 6.1 + Phase 3 in one commit.

### Task 6.1: NetworkSystem cleanup

**Files:**

- Modify: `internal/game/system_network.go`

- [ ] **Step 1: Delete pendingAbilityEvents field + Peek/Drain**

```go
// DELETE:
pendingAbilityEvents []*gamepb.AbilityCastResultMsg

// DELETE in beforeTick:
s.pendingAbilityEvents = mmokit.Peek[*gamepb.AbilityCastResultMsg](gw.Queue)

// DELETE in afterTick:
mmokit.Drain[*gamepb.AbilityCastResultMsg](gw.Queue)
```

Replace with the broadcast-queue drain (per Task 2.5):

```go
// ADD to beforeTick:
s.pendingBroadcasts = gw.Stage.BroadcastQueue().Drain()
```

- [ ] **Step 2: Replace per-viewer ability-event filter with broadcast-queue filter**

In `afterSend`:

```go
// DELETE:
var abilityEvents []*gamepb.AbilityCastResultMsg
for _, evt := range s.pendingAbilityEvents {
    if visible[evt.CasterId] || visible[evt.TargetId] {
        abilityEvents = append(abilityEvents, evt)
    }
}
if len(abilityEvents) > 0 {
    frame := gw.ServerEvents().Build(uint32(enginepb.ServerEventCode_SE_WORLD_UPDATE), &gamepb.WorldUpdateMsg{
        Tick:          gw.eng.Tick,
        AbilityEvents: abilityEvents,
    })
    gw.eng.ConnMgr.Send(viewer.ConnID, frame)
}

// ADD:
var events []*gamepb.TypedEvent
for _, evt := range s.pendingBroadcasts {
    for _, nid := range evt.Anchors {
        if visible[nid] {
            events = append(events, &gamepb.TypedEvent{TypeId: evt.TypeID, Body: evt.Body})
            break
        }
    }
}
if len(events) > 0 {
    frame := gw.ServerEvents().Build(uint32(enginepb.ServerEventCode_SE_WORLD_UPDATE), &gamepb.WorldUpdateMsg{
        Tick:   gw.eng.Tick,
        Events: events,
    })
    gw.eng.ConnMgr.Send(viewer.ConnID, frame)
}
```

- [ ] **Step 3: Verify (after Phase 3 lands)**

```bash
go vet ./...
go test ./pkg/... ./internal/...
```

- [ ] **Step 4: Commit (bundle Phase 3 + Task 5.1 + Task 6.1)**

```bash
git add proto/gamepb/game.proto gen/go/gamepb/ gen/es/gamepb/ internal/game/verb_damage.go internal/game/verb_status.go internal/game/system_ability.go internal/game/system_network.go
git commit -m "refactor(game,proto): retire AbilityCastResultMsg + pendingAbilityEvents

Wire format: WorldUpdateMsg.ability_events (field 7) replaced by
WorldUpdateMsg.events (TypedEvent, field 7). AbilityCastResultMsg
proto deleted.

Game side: verb_damage / verb_status / system_ability stop manually
enqueueing; the framework auto-broadcast handles it.

NetworkSystem: pendingAbilityEvents replaced by per-stage broadcast
queue drain; per-viewer filter compares anchor NetIDs against the
viewer's visible set.

Wire-break: any pre-Plan-F client fails to decode WorldUpdateMsg.
SDK regenerates in lockstep (Plan F Phase 4)."
```

---

## Phase 7: Smoke-test the full path

### Task 7.1: Build + run example end-to-end

- [ ] **Step 1: Verify everything compiles**

```bash
go vet ./...
go test ./pkg/... ./internal/...
```

- [ ] **Step 2: Smoke-build the example**

```bash
mkdir -p /tmp/mmo-build
go build -o /tmp/mmo-build/4node-basic ./examples/4node-basic/
rm -rf /tmp/mmo-build
```

- [ ] **Step 3: Run + smoke-test in browser**

```bash
just dev
```

Connect a browser. Spawn bots (`bot spawn 30 cell_0_0`). Drive the player ship. Fire abilities. Verify:

- Damage numbers render correctly
- Pulse laser animation fires
- Piercing round / plasma torpedo animations fire
- Ion burn DoT cast animation + persistent effect render
- Mining beam + extract pulse animations fire
- NPC death explosions render
- Currency awards reach the killer's UI

- [ ] **Step 4: Trigger a cell split, smoke-test cross-cell broadcast**

```bash
cell split 0_0
```

Drive a player to the cell boundary, fire abilities at a target on the other cell. Verify:
- Caster sees the cast animation immediately (source-cell pre-handler broadcast)
- Damage number renders correctly when post-handler broadcast arrives
- No double-render artifacts

- [ ] **Step 5: Commit anything tweaked during smoke**

If smoke surfaces a bug, fix + commit. Otherwise skip.

---

## Phase 8: Closeout

### Task 8.1: Spec update + final report

- [ ] **Step 1: Update spec §10**

In `docs/superpowers/specs/2026-05-03-entity-message-passing-design.md`, find the closing prose and add the Plan F entry:

```diff
-Each step is independent and revertible. Steps 1-5 are landed (Plans A+B, C, D, E). Remaining: TargetLock + Dock-request migrations (Plan F), AoI auto-broadcast for typed messages (Plan G), input handling migration (step 6, Plan H).
+Each step is independent and revertible. Steps 1-5 are landed (Plans A+B, C, D, E), and the AoI auto-broadcast piece of §4.5 is landed (Plan F, 2026-05-05). Remaining: input handling migration (step 6, Plan G).
```

(Plan F renumbers because TargetLock + Dock turned out not to need a dedicated migration — the prior "Plan F" framing in the §10 prose was based on an incorrect spec assumption that got resolved during Plan F's brainstorming.)

- [ ] **Step 2: Update spec §4.5 status**

In `docs/superpowers/specs/2026-05-03-entity-message-passing-design.md` §4.5, add a "Status: implemented 2026-05-05, Plan F" line near the top.

- [ ] **Step 3: Commit spec update**

```bash
git add docs/superpowers/specs/2026-05-03-entity-message-passing-design.md
git commit -m "docs(spec): mark Plan F (AoI auto-broadcast) landed"
```

- [ ] **Step 4: Final report**

Summarize:
- Phase 1: reflect codec extension + mmokit.Entity codec.
- Phase 2: broadcast registry + framework dispatcher + anchor extraction.
- Phase 3: WorldUpdateMsg.events wire format.
- Phase 4: sdkgen extension + TS client typed dispatcher.
- Phase 5+6: verb handler + NetworkSystem cleanup; AbilityCastResultMsg deleted.
- Phase 7: smoke test end-to-end.
- Plan-G note: Input handling migration is the remaining big architectural plan.

---

## Out of scope / not in this plan

- **Replace `WorldUpdateMsg` envelope with binary frame.** Separate plan; orthogonal to Plan F.
- **Remove protobufs from server↔client engine envelopes broadly.** Separate plan; closes the protobuf-vestigiality question raised during brainstorming.
- **Input handling migration** (`OnInput*` → typed `Handle` with from-client-trust marker). Plan G.
- **Handler-less broadcast** (pure visual events with no state mutation). YAGNI.
- **Unity client SDK update** (`gen/csharp/`). Defer until that client is back in active use.

---

## Quick orientation for a fresh agent

If you're picking this up cold:

- **Branch:** `feat/mmokit-entity-message-api`. Continue on this branch (single ongoing dev branch per the solo-developer convention).
- **Latest commit before Plan F:** `9f6ddb1` (Plan F spec).
- **What's done before Plan F:** mmokit foundation, Damage/Mining/Status/Killed/KillCredit verbs (typed messages), death observer, ECS-access mechanical sweep complete. `internal/game/` is fully on the mmokit facade.
- **What you'll need to read first:**
  - `docs/superpowers/specs/2026-05-05-aoi-auto-broadcast-design.md` (Plan F design)
  - `pkg/universe/reflect_marshal.go` (the codec being extended)
  - `pkg/mmokit/messaging.go` + `messaging_all.go` (where Handle/HandleAll live)
  - `pkg/universe/typed_message_codec.go` (existing typed-message wire format for cross-cell)
  - `internal/game/system_network.go::beforeTick/afterSend/afterTick` (the path being replaced)
  - `internal/game/verb_damage.go` (one of four verbs whose manual enqueue deletes)
  - `cmd/sdkgen/main.go` + `protoes.go` + `generate.go` (the schema → TS pipeline)

The plan is concrete; mirror existing patterns (the typed-message dispatcher, the entity-component schema emitter) and don't over-think it. If something is genuinely ambiguous after exploration, make the reasonable choice and report DONE_WITH_CONCERNS.
