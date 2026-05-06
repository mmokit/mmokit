# Events Channel Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Migrate the `0x00` event channel from protobuf-wrapped `ServerEvent{code, data}` envelopes to typed reflection-codec frames, retire the `0x02` typed-client-input channel (subsumed into `0x00`), and decommission in-engine chat. Login stays on `0x00` as a typed event for now; Plan 2 moves it to a `0x01` operation.

**Architecture:** Channel `0x00` becomes a single bidirectional typed-message channel. Wire format: `[0x00] [typeID:u32 BE] [body_len:u32 BE] [body:N bytes]` repeated until end of WebSocket message. Same shape as today's `0x02` client-input frame, just consume-to-end so a frame can carry one typed message (single push, single input) or many (per-tick AoI-batched broadcasts). Server-side broadcasts batch all of a viewer's tick events into one `0x00` frame in `afterSend`. Per-message codec is the existing `pkg/universe/reflect_marshal.go` reflection codec; per-message identity is the existing `TypeIDOf()` FNV-1a 32-bit hash already used for `0x02`.

**Tech Stack:** Go 1.21+ (generics), `pkg/universe/reflect_marshal.go` reflection codec, `pkg/mmokit/broadcast.go:TypeIDOf()` FNV-1a hash, WebSocket via `coder/websocket` (TCP-NODELAY default), TypeScript codegen via `cmd/sdkgen` to `web-pixi/sdk/` and `examples/4node-basic/web/sdk/`.

**Spec:** [docs/superpowers/specs/2026-05-06-events-operations-channel-redesign.md](../specs/2026-05-06-events-operations-channel-redesign.md). This plan implements phases 1–5 + 9 + the events portion of phase 10.

**Out of scope (deferred to Plan 2):** `0x01` operations channel typed-bodies migration; Login → operation; marketplace+bank ops migration; final retirement of `pkg/ops` `Register`.

---

## File Structure

**New files:**

- `pkg/mmokit/handle_event.go` — server-event registration `RegisterEvent[T]`, parallel to `handle_client.go`'s `HandleClient[T]`. Owns the `seByType` registry.
- `pkg/universe/event_dispatch.go` — server-side `0x00` dispatcher: decodes inbound typed-event frames after `0x02` retires, routes to registered handlers.
- `pkg/universe/typed_event_frame.go` — wire-format encode helpers (`EncodeTypedEventFrame(typeID, body) []byte`, `EncodeBatchedTypedEventFrame([]BroadcastEvent) []byte`); shared by `Stage.SendEvent`, broadcast `afterSend`, and tests.
- `cmd/sdkgen/events.go` — emits per-server-event TS class with `decode()` and registers `client.on*` handlers in `client.ts` (parallel to `cmd/sdkgen/broadcasts.go`).
- `internal/game/event_messages.go` — typed Go structs for migrated server-events (`PlayerSpawned`, `PlayerDied`, `DockingState`, `Docked`, `CurrencyUpdate`, `BankContents`, `EquipResult`, `TransferResult`, `MapData`, `PlayerOwnState`, `CellTopology`, `LoginRequest`, `LoginRejected`, `WorldDelta`).

**Modified files:**

- `pkg/universe/stage.go` — add generic `SendEvent[T]` method; current proto-based `SendEvent` deprecated then deleted.
- `pkg/universe/reflect_codec_registry.go` — register passthrough `[]byte` codec for `WorldDelta.Body` field.
- `pkg/universe/gateway.go:forwardChannel` — once `0x02` retires, the channel-tagging branch for `ChannelClientInput` is removed; client-input frames flow over `ChannelEvent` like everything else.
- `pkg/universe/virtual_conn_manager.go` — `appendChannel` no longer routes to `clientInput` queue; the `clientInput` slice is removed in phase 7.
- `pkg/net/conn.go` — `ChannelClientInput` constant retired in phase 7; `clientInput [][]byte` slice and `DrainClientInput()` removed.
- `pkg/mmokit/protocol.go` — `Protocol.assembleFromProcess()` adds a server-event section to the schema dump.
- `pkg/mmokit/server_events.go` — `RegisterServerEvent[T]`, `ServerEvents.Build`, `ServerEvents.Send` deleted in phase 7 (replaced by `RegisterEvent[T]` + `Stage.SendEvent[T]`).
- `internal/game/system_network.go:159` — `afterSend` writes typed-event batched frames instead of building `WorldUpdateMsg{events: [...]}`.
- `internal/game/system_network.go:142-148,189-202` — chat plumbing deleted in phase 6.
- `internal/game/input_handlers.go:121-153` — Chat HandleClient registration deleted in phase 6.
- `internal/game/input_messages.go` — `Chat` struct deleted in phase 6.
- `proto/gamepb/game.proto` — `WorldUpdateMsg`, `TypedEvent`, all migrated server-event protos deleted in phase 7.
- `proto/enginepb/engine.proto` — `ChatMsg` deleted in phase 6; migrated `SE_*` enum entries deleted in phase 7.
- `web-pixi/sdk/transport.ts`, `web-pixi/sdk/client.ts`, `web-pixi/sdk/inputs.ts`, `web-pixi/sdk/broadcasts.ts`, `web-pixi/src/network.ts`, `web-pixi/src/input.ts` — channel-byte flip + per-event handler wiring + chat code removal.
- Mirrored in `examples/4node-basic/web/sdk/` and `examples/4node-basic/web/src/`.

---

## Phase 0 — Setup

### Task 0.1: Create branch from main

**Files:** none (git only)

- [ ] **Step 1: Verify clean tree on main**

```bash
git checkout main && git status
```

Expected: `On branch main`, `nothing to commit, working tree clean`.

- [ ] **Step 2: Create branch**

```bash
git checkout -b feat/mmokit-events-channel
```

- [ ] **Step 3: Verify build is clean**

```bash
go vet ./... && (cd examples/4node-basic/web && bunx tsc --noEmit) && (cd web-pixi && bunx tsc --noEmit)
```

Expected: no output / no errors. If any errors surface, fix them in a separate commit on `main` first, then re-branch.

---

## Phase 1 — Foundation: Bidirectional Typed-Message Codec

### Task 1.1: Add the typed-event frame encoder

**Files:**
- Create: `pkg/universe/typed_event_frame.go`
- Test: `pkg/universe/typed_event_frame_test.go`

- [ ] **Step 1: Write the failing test**

`pkg/universe/typed_event_frame_test.go`:

```go
package universe

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestEncodeTypedEventFrame_SingleEvent(t *testing.T) {
	body := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
	frame := EncodeTypedEventFrame(0xDEADBEEF, body)

	if frame[0] != 0x00 {
		t.Fatalf("channel byte: got %#x, want 0x00", frame[0])
	}
	gotTypeID := binary.BigEndian.Uint32(frame[1:5])
	if gotTypeID != 0xDEADBEEF {
		t.Fatalf("typeID: got %#x, want 0xDEADBEEF", gotTypeID)
	}
	gotLen := binary.BigEndian.Uint32(frame[5:9])
	if gotLen != uint32(len(body)) {
		t.Fatalf("body_len: got %d, want %d", gotLen, len(body))
	}
	if !bytes.Equal(frame[9:], body) {
		t.Fatalf("body bytes mismatch")
	}
	if len(frame) != 1+4+4+len(body) {
		t.Fatalf("total len: got %d, want %d", len(frame), 1+4+4+len(body))
	}
}

func TestEncodeBatchedTypedEventFrame_TwoEvents(t *testing.T) {
	events := []BroadcastEvent{
		{TypeID: 0xAAAA0001, Body: []byte{0x10, 0x20}},
		{TypeID: 0xBBBB0002, Body: []byte{0x30, 0x40, 0x50}},
	}
	frame := EncodeBatchedTypedEventFrame(events)

	if frame[0] != 0x00 {
		t.Fatalf("channel byte: got %#x, want 0x00", frame[0])
	}
	// Entry 1: typeID=AAAA0001 len=2 body=10,20
	if binary.BigEndian.Uint32(frame[1:5]) != 0xAAAA0001 {
		t.Fatal("entry 1 typeID")
	}
	if binary.BigEndian.Uint32(frame[5:9]) != 2 {
		t.Fatal("entry 1 body_len")
	}
	if !bytes.Equal(frame[9:11], []byte{0x10, 0x20}) {
		t.Fatal("entry 1 body")
	}
	// Entry 2: typeID=BBBB0002 len=3 body=30,40,50
	if binary.BigEndian.Uint32(frame[11:15]) != 0xBBBB0002 {
		t.Fatal("entry 2 typeID")
	}
	if binary.BigEndian.Uint32(frame[15:19]) != 3 {
		t.Fatal("entry 2 body_len")
	}
	if !bytes.Equal(frame[19:22], []byte{0x30, 0x40, 0x50}) {
		t.Fatal("entry 2 body")
	}
	if len(frame) != 22 {
		t.Fatalf("total len: got %d, want 22", len(frame))
	}
}

func TestEncodeBatchedTypedEventFrame_Empty(t *testing.T) {
	frame := EncodeBatchedTypedEventFrame(nil)
	if frame != nil {
		t.Fatalf("empty batch should return nil, got %v", frame)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./pkg/universe/ -run TestEncodeTypedEventFrame -v
```

Expected: FAIL — `EncodeTypedEventFrame undefined`, `EncodeBatchedTypedEventFrame undefined`.

- [ ] **Step 3: Write minimal implementation**

`pkg/universe/typed_event_frame.go`:

```go
package universe

import (
	pkgnet "github.com/zenion/mmoserver/pkg/net"
)

// EncodeTypedEventFrame produces a single-event 0x00 frame:
//   [0x00][typeID:u32 BE][body_len:u32 BE][body]
func EncodeTypedEventFrame(typeID uint32, body []byte) []byte {
	frame := make([]byte, 1+4+4+len(body))
	frame[0] = pkgnet.ChannelEvent
	beUint32(frame[1:5], typeID)
	beUint32(frame[5:9], uint32(len(body)))
	copy(frame[9:], body)
	return frame
}

// EncodeBatchedTypedEventFrame packs multiple events into a single 0x00 frame.
// Returns nil for an empty list — callers must skip writing empty frames.
func EncodeBatchedTypedEventFrame(events []BroadcastEvent) []byte {
	if len(events) == 0 {
		return nil
	}
	total := 1
	for _, e := range events {
		total += 4 + 4 + len(e.Body)
	}
	frame := make([]byte, total)
	frame[0] = pkgnet.ChannelEvent
	off := 1
	for _, e := range events {
		beUint32(frame[off:off+4], e.TypeID)
		off += 4
		beUint32(frame[off:off+4], uint32(len(e.Body)))
		off += 4
		copy(frame[off:off+len(e.Body)], e.Body)
		off += len(e.Body)
	}
	return frame
}

func beUint32(b []byte, v uint32) {
	b[0] = byte(v >> 24)
	b[1] = byte(v >> 16)
	b[2] = byte(v >> 8)
	b[3] = byte(v)
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./pkg/universe/ -run TestEncodeTypedEventFrame -v
go test ./pkg/universe/ -run TestEncodeBatchedTypedEventFrame -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/typed_event_frame.go pkg/universe/typed_event_frame_test.go
git commit -m "feat(universe): add typed-event 0x00 frame encoder

Single and batched encode helpers for the new event channel wire
format: [0x00][typeID:u32 BE][body_len:u32 BE][body]. Used by
Stage.SendEvent and afterSend broadcast write paths."
```

---

### Task 1.2: Add server-event registry parallel to client-input

**Files:**
- Create: `pkg/mmokit/handle_event.go`
- Test: `pkg/mmokit/handle_event_test.go`

- [ ] **Step 1: Write the failing test**

`pkg/mmokit/handle_event_test.go`:

```go
package mmokit_test

import (
	"reflect"
	"testing"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

type testServerEventA struct {
	X int32
	Y int32
}

type testServerEventB struct {
	Name string
}

func TestRegisterEvent_TypeIDLookup(t *testing.T) {
	mmokit.ResetServerEventRegistryForTest()
	mmokit.RegisterEvent[testServerEventA]()
	mmokit.RegisterEvent[testServerEventB]()

	idA := mmokit.TypeIDOf(reflect.TypeOf(testServerEventA{}))
	idB := mmokit.TypeIDOf(reflect.TypeOf(testServerEventB{}))

	gotA, okA := mmokit.LookupServerEventType(idA)
	if !okA || gotA != reflect.TypeOf(testServerEventA{}) {
		t.Fatalf("lookup A: got=%v ok=%v", gotA, okA)
	}
	gotB, okB := mmokit.LookupServerEventType(idB)
	if !okB || gotB != reflect.TypeOf(testServerEventB{}) {
		t.Fatalf("lookup B: got=%v ok=%v", gotB, okB)
	}
}

func TestRegisterEvent_RegisteredTypes(t *testing.T) {
	mmokit.ResetServerEventRegistryForTest()
	mmokit.RegisterEvent[testServerEventA]()
	mmokit.RegisterEvent[testServerEventB]()

	types := mmokit.RegisteredServerEventTypes()
	if len(types) != 2 {
		t.Fatalf("got %d types, want 2", len(types))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./pkg/mmokit/ -run TestRegisterEvent -v
```

Expected: FAIL — `RegisterEvent`, `LookupServerEventType`, `RegisteredServerEventTypes`, `ResetServerEventRegistryForTest` undefined.

- [ ] **Step 3: Write minimal implementation**

`pkg/mmokit/handle_event.go`:

```go
package mmokit

import (
	"fmt"
	"reflect"
	"sort"
	"sync"
)

var (
	seMu     sync.RWMutex
	seByType = map[uint32]reflect.Type{}
	seSet    = map[reflect.Type]bool{}
)

// RegisterEvent registers T as a server→client typed event. Subsequent calls
// to Stage.SendEvent[T] use the FNV-1a typeID derived from T to identify the
// frame on the wire; the SDK codegen iterates this registry to emit per-event
// TS decoder classes and per-event onXxx handlers.
//
// Panics on duplicate registration of the same Go type. Two distinct types
// hashing to the same typeID is also a panic — collision should never happen
// at codebase scale; if it does, rename one type.
func RegisterEvent[T any]() {
	t := reflect.TypeOf((*T)(nil)).Elem()
	id := TypeIDOf(t)

	seMu.Lock()
	defer seMu.Unlock()
	if seSet[t] {
		panic(fmt.Sprintf("RegisterEvent: type %s registered twice", t.String()))
	}
	if existing, ok := seByType[id]; ok && existing != t {
		panic(fmt.Sprintf("RegisterEvent: typeID collision between %s and %s (id=%#x)",
			existing.String(), t.String(), id))
	}
	seByType[id] = t
	seSet[t] = true
}

// LookupServerEventType returns the Go type registered for the given typeID,
// or (nil, false) if none.
func LookupServerEventType(id uint32) (reflect.Type, bool) {
	seMu.RLock()
	defer seMu.RUnlock()
	t, ok := seByType[id]
	return t, ok
}

// RegisteredServerEventTypes returns the registered types in deterministic
// (alphabetical) order. Used by sdkgen and protocol-schema export.
func RegisteredServerEventTypes() []reflect.Type {
	seMu.RLock()
	defer seMu.RUnlock()
	out := make([]reflect.Type, 0, len(seSet))
	for t := range seSet {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// ResetServerEventRegistryForTest is exported for tests only.
func ResetServerEventRegistryForTest() {
	seMu.Lock()
	defer seMu.Unlock()
	seByType = map[uint32]reflect.Type{}
	seSet = map[reflect.Type]bool{}
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./pkg/mmokit/ -run TestRegisterEvent -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/handle_event.go pkg/mmokit/handle_event_test.go
git commit -m "feat(mmokit): add server-event registry (RegisterEvent[T])

Parallel to HandleClient's client-input registry. Stores typeID → Go
type mapping for outbound typed events; codegen + protocol-schema
export iterate via RegisteredServerEventTypes()."
```

---

### Task 1.3: Add Stage.SendEvent[T] generic primitive

**Files:**
- Modify: `pkg/universe/stage.go`
- Modify: `pkg/mmokit/mmokit.go` (add typed wiring helper)
- Test: `pkg/universe/send_event_test.go`

- [ ] **Step 1: Read existing SendEvent at pkg/universe/stage.go:656**

```bash
sed -n '650,680p' pkg/universe/stage.go
```

Note the current proto-based signature `SendEvent(connID, code, msg interface{ Reset() })` and how it delegates via `worldBaseSendEvent`. The new generic method lives alongside it during the transition; the old method stays until phase 7.

- [ ] **Step 2: Write the failing test**

`pkg/universe/send_event_test.go`:

```go
package universe_test

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/universe"
)

type sendEventTestMsg struct {
	A int32
	B int32
}

func TestStageSendEventTyped_WritesEventChannelFrame(t *testing.T) {
	mmokit.ResetServerEventRegistryForTest()
	mmokit.RegisterEvent[sendEventTestMsg]()

	stage, fakeConn := newStageWithFakeConn(t)
	defer stage.Close()

	universe.SendEventTyped(stage, fakeConn.ConnID, &sendEventTestMsg{A: 7, B: 9})

	frame := fakeConn.LastSent()
	if len(frame) < 9 {
		t.Fatalf("frame too short: %d", len(frame))
	}
	if frame[0] != 0x00 {
		t.Fatalf("channel byte: got %#x, want 0x00", frame[0])
	}
	gotTypeID := binary.BigEndian.Uint32(frame[1:5])
	wantTypeID := mmokit.TypeIDOf(reflect.TypeOf(sendEventTestMsg{}))
	if gotTypeID != wantTypeID {
		t.Fatalf("typeID: got %#x, want %#x", gotTypeID, wantTypeID)
	}
	bodyLen := binary.BigEndian.Uint32(frame[5:9])
	body := frame[9 : 9+bodyLen]

	// Reflection-codec body for {A:7, B:9} = LE int32 7 then LE int32 9 = 8 bytes.
	want := []byte{7, 0, 0, 0, 9, 0, 0, 0}
	if !bytes.Equal(body, want) {
		t.Fatalf("body: got %v, want %v", body, want)
	}
}
```

(`newStageWithFakeConn` is a test helper. If one doesn't exist in `pkg/universe/`, write a minimal one in `pkg/universe/testutil_test.go`. Look for `newTestStage` or similar — `pkg/universe/coordinator_test.go` likely has a pattern to copy.)

- [ ] **Step 3: Run test to verify it fails**

```bash
go test ./pkg/universe/ -run TestStageSendEventTyped -v
```

Expected: FAIL — `SendEventTyped undefined` (or similar).

- [ ] **Step 4: Implement SendEventTyped as a free function**

Go does not allow generic methods on non-generic types, so `Stage.SendEvent[T]` is impossible. Use a free function `universe.SendEventTyped[T]` that takes the stage as its first argument. Future ergonomic wrapper in `mmokit` package will provide `mmokit.SendEvent[T](stage, connID, msg)`.

Append to `pkg/universe/stage.go` (after the existing `SendEvent` method, ~line 680):

```go
// SendEventTyped writes a single-event 0x00 frame for msg to connID.
// msg must be a pointer to a Go struct registered via mmokit.RegisterEvent[T].
// The reflection codec serializes the body; the typeID is derived from T.
//
// SendEventTyped is the typed replacement for the proto-based SendEvent;
// proto-based SendEvent will be deleted in the cleanup phase once all
// callers migrate.
func SendEventTyped[T any](stage *Stage, connID uint32, msg *T) {
	t := reflect.TypeOf(*msg)
	id := mmokit.TypeIDOf(t)
	if _, ok := mmokit.LookupServerEventType(id); !ok {
		panic(fmt.Sprintf("SendEventTyped: type %s not registered via mmokit.RegisterEvent[T]", t.String()))
	}
	body := ReflectMarshal(msg)
	frame := EncodeTypedEventFrame(id, body)
	stage.eng.ConnMgr.SendReliable(connID, frame)
}
```

Imports needed at top of `stage.go` (verify they're already imported, add if not):

```go
import (
    "fmt"
    "reflect"
    "github.com/zenion/mmoserver/pkg/mmokit"
)
```

- [ ] **Step 5: Run test to verify it passes**

```bash
go test ./pkg/universe/ -run TestStageSendEventTyped -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/universe/stage.go pkg/universe/send_event_test.go pkg/universe/testutil_test.go
git commit -m "feat(universe): add SendEventTyped[T] free function

Generic typed replacement for the proto-based Stage.SendEvent. Caller:
universe.SendEventTyped(stage, connID, &MyMsg{...}). Body is encoded
via the existing reflection codec; typeID is the registered FNV-1a
hash. Proto-based SendEvent stays during the migration."
```

---

### Task 1.4: Wire SendEventTyped through the mmokit facade

**Files:**
- Modify: `pkg/mmokit/messaging.go` (or wherever SendEvent re-export lives — find it)

- [ ] **Step 1: Find the existing mmokit.SendEvent re-export**

```bash
grep -rn "func SendEvent\|SendEvent =" pkg/mmokit/
```

If the proto-based `mmokit.SendEvent` re-exports `universe.Stage.SendEvent`, add a typed parallel in the same file.

- [ ] **Step 2: Add typed re-export**

In the file found (likely `pkg/mmokit/messaging.go` or `pkg/mmokit/init.go`), append:

```go
// SendEvent writes a single typed event to one connection. T must be
// registered via RegisterEvent[T]() at startup.
//
// Usage:
//   mmokit.SendEvent(stage, connID, &MyEvent{...})
func SendEvent[T any](stage *Stage, connID uint32, msg *T) {
	universe.SendEventTyped(stage, connID, msg)
}
```

If `Stage` in mmokit is a re-export of `*universe.Stage` (it is — check `pkg/mmokit/mmokit.go`), this signature compiles cleanly.

- [ ] **Step 3: Verify it compiles**

```bash
go vet ./...
```

Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add pkg/mmokit/messaging.go
git commit -m "feat(mmokit): re-export SendEvent[T] through facade

Game code calls mmokit.SendEvent(stage, connID, &Msg{...}) the same way
it called the proto-based predecessor."
```

---

### Task 1.5: Server-side typed-event dispatcher (preparation for phase 5)

**Files:**
- Create: `pkg/universe/event_dispatch.go`
- Test: `pkg/universe/event_dispatch_test.go`

This dispatcher decodes inbound `0x00` typed-event frames and routes them. During phases 1–4 the dispatcher is dormant (no inbound typed-event frames yet — inputs are still on `0x02`); phase 5 wires it in when `0x02` retires. Building it now means phase 5 is a one-line `forwardChannel` change.

- [ ] **Step 1: Write the failing test**

`pkg/universe/event_dispatch_test.go`:

```go
package universe_test

import (
	"bytes"
	"testing"

	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/universe"
)

type evDispatchTestInput struct {
	V int32
}

func TestDispatchInboundTypedEvent_RoutesToHandler(t *testing.T) {
	mmokit.ResetClientInputRegistryForTest()
	mmokit.ResetServerEventRegistryForTest()

	var seenV int32
	mmokit.RegisterClientInputType[evDispatchTestInput]()  // registers in client-input registry
	mmokit.SetClientInputHandler[evDispatchTestInput](func(playerNetID uint32, msg *evDispatchTestInput) {
		seenV = msg.V
	})

	body := universe.ReflectMarshal(&evDispatchTestInput{V: 42})
	id := mmokit.TypeIDOf(reflectTypeOf(evDispatchTestInput{}))
	frame := bytes.Buffer{}
	frameAppendU32BE(&frame, id)
	frameAppendU32BE(&frame, uint32(len(body)))
	frame.Write(body)

	stage := newTestStage(t)
	defer stage.Close()
	universe.DispatchInboundEventFrame(stage, /*playerNetID=*/100, frame.Bytes())

	if seenV != 42 {
		t.Fatalf("handler not called or wrong V: got %d, want 42", seenV)
	}
}
```

(Test uses `mmokit.ResetClientInputRegistryForTest`, `mmokit.RegisterClientInputType`, `mmokit.SetClientInputHandler`. These may need to be added as test-only exports in `pkg/mmokit/handle_client.go` — find the existing equivalents and surface them.)

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./pkg/universe/ -run TestDispatchInboundTypedEvent -v
```

Expected: FAIL — symbol undefined.

- [ ] **Step 3: Implement DispatchInboundEventFrame**

`pkg/universe/event_dispatch.go`:

```go
package universe

import (
	"encoding/binary"
	"reflect"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

// DispatchInboundEventFrame consumes a payload (frame body, channel byte
// already stripped) carrying one or more typed-event entries:
//
//   [typeID:u32 BE][body_len:u32 BE][body] repeated
//
// For each entry, the typeID is looked up in the client-input registry; if
// registered, the body is decoded and routed to the matching handler. If the
// typeID is unknown, the entry is logged and skipped.
//
// playerNetID is the NetID of the player entity associated with the connection
// (resolved upstream by the read loop).
func DispatchInboundEventFrame(stage *Stage, playerNetID uint32, payload []byte) {
	off := 0
	for off+8 <= len(payload) {
		typeID := binary.BigEndian.Uint32(payload[off : off+4])
		bodyLen := binary.BigEndian.Uint32(payload[off+4 : off+8])
		off += 8
		if int(bodyLen) > len(payload)-off {
			stage.Log().Log(CatClientInput, "DispatchInboundEventFrame: truncated body for typeID %#x", typeID)
			return
		}
		body := payload[off : off+int(bodyLen)]
		off += int(bodyLen)

		t, ok := mmokit.LookupClientInputType(typeID)
		if !ok {
			stage.Log().Log(CatClientInput, "DispatchInboundEventFrame: unknown typeID %#x", typeID)
			continue
		}
		msgPtr := reflect.New(t)
		ReflectUnmarshalOnStage(stage, body, msgPtr.Interface())
		stage.Dispatcher().Invoke(playerNetID, msgPtr.Interface())
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./pkg/universe/ -run TestDispatchInboundTypedEvent -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/event_dispatch.go pkg/universe/event_dispatch_test.go
git commit -m "feat(universe): add inbound typed-event frame dispatcher

Decodes a 0x00 frame body containing N typeID+body_len+body entries
and routes each to the registered client-input handler. Dormant
until phase 5 retires the 0x02 channel and routes inbound 0x02
frames through this path."
```

---

### Task 1.6: Phase 1 commit (review + tests pass)

- [ ] **Step 1: Run full test suite**

```bash
go test ./... 2>&1 | tail -40
```

Expected: all packages pass.

- [ ] **Step 2: Run go vet**

```bash
go vet ./...
```

Expected: clean.

- [ ] **Step 3: Push branch (no merge yet)**

```bash
git log --oneline main..HEAD
```

Expected: ~5 commits for Phase 1 tasks.

---

## Phase 2 — Broadcasts on 0x00 (batched per viewer per tick)

### Task 2.1: Replace afterSend WorldUpdateMsg path with batched typed-event frame

**Files:**
- Modify: `internal/game/system_network.go:159`
- Test: `internal/game/system_network_afterSend_test.go`

- [ ] **Step 1: Read existing afterSend**

```bash
sed -n '155,200p' internal/game/system_network.go
```

Confirm the body matches the layout in the spec (drained `s.pendingBroadcasts`, AoI filter loop, `gw.ServerEvents().Build/Send`).

- [ ] **Step 2: Write the failing test**

`internal/game/system_network_afterSend_test.go`:

```go
package game

import (
	"encoding/binary"
	"testing"

	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/universe"
)

func TestAfterSend_WritesBatchedTypedEventFrame(t *testing.T) {
	gw, viewer, fakeConn := newGameWorldWithViewer(t)
	defer gw.Close()

	// Push two broadcasts whose anchor netIDs are visible to the viewer.
	visibleNID := uint32(42)
	gw.Stage.BroadcastQueue().Push(universe.BroadcastEvent{
		TypeID:  0xCAFEBABE,
		Body:    []byte{1, 2, 3},
		Anchors: []uint32{visibleNID},
	})
	gw.Stage.BroadcastQueue().Push(universe.BroadcastEvent{
		TypeID:  0xDEADBEEF,
		Body:    []byte{4, 5, 6, 7},
		Anchors: []uint32{visibleNID},
	})

	gw.NetworkSystem().BeforeTick(0)  // drain queue into pendingBroadcasts
	visible := map[uint32]bool{visibleNID: true}
	gw.NetworkSystem().AfterSendForTest(viewer, visible)

	frame := fakeConn.LastSent()
	if frame == nil {
		t.Fatal("no frame written")
	}
	if frame[0] != 0x00 {
		t.Fatalf("channel: got %#x", frame[0])
	}
	// First entry: typeID=CAFEBABE len=3 body=1,2,3
	if binary.BigEndian.Uint32(frame[1:5]) != 0xCAFEBABE {
		t.Fatal("entry 1 typeID")
	}
	if binary.BigEndian.Uint32(frame[5:9]) != 3 {
		t.Fatal("entry 1 body_len")
	}
	// Second entry starts at offset 12
	if binary.BigEndian.Uint32(frame[12:16]) != 0xDEADBEEF {
		t.Fatal("entry 2 typeID")
	}
	if binary.BigEndian.Uint32(frame[16:20]) != 4 {
		t.Fatal("entry 2 body_len")
	}
	_ = mmokit.ChannelEvent
}
```

(Test helpers `newGameWorldWithViewer`, `gw.NetworkSystem().AfterSendForTest`, `fakeConn.LastSent` may need to be added. See `internal/game/cell_test.go` for the existing pattern.)

- [ ] **Step 3: Run test to verify it fails**

```bash
go test ./internal/game/ -run TestAfterSend_WritesBatchedTypedEventFrame -v
```

Expected: FAIL — old path writes `WorldUpdateMsg` bytes.

- [ ] **Step 4: Replace afterSend body**

In `internal/game/system_network.go`, replace the `afterSend` method body (lines ~159–182) with:

```go
// afterSend filters auto-broadcast typed events by AoI and writes one batched
// 0x00 frame per viewer per tick containing all events whose anchor NetIDs
// are visible to that viewer. Empty viewers get no frame.
func (s *NetworkSystem) afterSend(viewer *mmokit.ViewerInfo, visible map[uint32]bool) {
	if len(s.pendingBroadcasts) == 0 {
		return
	}
	gw := s.World()

	var passed []universe.BroadcastEvent
	for _, evt := range s.pendingBroadcasts {
		for _, nid := range evt.Anchors {
			if visible[nid] {
				passed = append(passed, evt)
				break
			}
		}
	}
	frame := universe.EncodeBatchedTypedEventFrame(passed)
	if frame == nil {
		return
	}
	gw.eng.ConnMgr.Send(viewer.ConnID, frame)
}
```

Drop the `gamepb` and `enginepb` imports from this file if `afterSend` was the only consumer (likely false — chat code below still uses them; clean up in phase 6).

- [ ] **Step 5: Run test to verify it passes**

```bash
go test ./internal/game/ -run TestAfterSend_WritesBatchedTypedEventFrame -v
```

Expected: PASS.

- [ ] **Step 6: Run existing AfterSend regression tests**

```bash
go test ./internal/game/ -run TestAfterSend -v
go test ./pkg/mmokit/ -run TestAutoBroadcast -v
go test ./pkg/universe/ -run TestBroadcast -v
```

Expected: PASS. If anything fails because tests inspect `WorldUpdateMsg.events` directly, those tests need to be updated to inspect the new wire bytes — do so now.

- [ ] **Step 7: Commit**

```bash
git add internal/game/system_network.go internal/game/system_network_afterSend_test.go
git commit -m "feat(game): batched 0x00 typed-event frame for AoI broadcasts

Replaces WorldUpdateMsg{events: [...]} envelope build/send in afterSend
with EncodeBatchedTypedEventFrame on the existing reflection-codec body
bytes. Per-tick per-viewer one-write semantics preserved."
```

---

### Task 2.2: Update web-pixi SDK transport to dispatch typed-event frames on 0x00

**Files:**
- Modify: `web-pixi/sdk/transport.ts`
- Modify: `web-pixi/sdk/client.ts`
- Modify: `web-pixi/sdk/broadcasts.ts`

The legacy `client.onWorldUpdate(msg => msg.events)` path decodes `WorldUpdateMsg.events` (TypedEvent[]) entries and dispatches each by typeID. After Phase 2.1, the wire stops carrying `WorldUpdateMsg` entirely for broadcasts (chat still flows in Phase 2 — chat decomm is phase 6); broadcasts come as a raw 0x00 typed-event frame. The dispatcher needs to be split: 0x00 frames whose first byte is the protobuf field-tag `0x08` are legacy `ServerEvent` envelopes; otherwise they're typed-event frames.

- [ ] **Step 1: Read existing transport**

```bash
sed -n '1,80p' web-pixi/sdk/transport.ts
sed -n '1,60p' web-pixi/sdk/client.ts
sed -n '1,50p' web-pixi/sdk/broadcasts.ts
```

- [ ] **Step 2: Add typed-event detection in transport.ts**

In `web-pixi/sdk/transport.ts`, where the channel-byte switch lives, find the `CH_EVENT` (0x00) case. The existing handler decodes a protobuf `ServerEvent` and routes by `code`. Add a peek at the first byte of the channel-stripped payload: if it's `0x08` (protobuf field-1 varint tag), it's the legacy envelope — keep current path. Otherwise, treat the whole payload as typed-event entries and dispatch each via `client.typedEvents.dispatch(typeID, body)`.

Pseudocode:

```typescript
case CH_EVENT: {
  const payload = data.slice(1);
  if (payload.length > 0 && payload[0] === 0x08) {
    // Legacy ServerEvent path — unchanged.
    handleServerEvent(payload);
  } else {
    // New typed-event frame — repeated [typeID:u32 BE][body_len:u32 BE][body]
    let off = 0;
    while (off + 8 <= payload.length) {
      const view = new DataView(payload.buffer, payload.byteOffset + off, 8);
      const typeID = view.getUint32(0, /*littleEndian=*/false);
      const bodyLen = view.getUint32(4, /*littleEndian=*/false);
      off += 8;
      const body = payload.subarray(off, off + bodyLen);
      off += bodyLen;
      client.typedEvents.dispatch(typeID, body);
    }
  }
  break;
}
```

(Adjust to the actual class structure of `transport.ts` and `client.ts`.)

- [ ] **Step 3: Verify the typed-events dispatcher already handles the new typeIDs**

The existing `client.typedEvents.on(Damage, ...)` registration in `web-pixi/src/network.ts:323` should continue to work; same typeIDs as before, just delivered via the new path instead of via WorldUpdateMsg.events parsing in `client.ts:115`. Confirm by grepping:

```bash
grep -n "typedEvents.dispatch\|typedEvents.on" web-pixi/sdk/ web-pixi/src/
```

If the typedEvents dispatcher takes (typeID, body Uint8Array), the existing handlers wire through. If it takes (TypedEvent proto), update its signature to take raw bytes.

- [ ] **Step 4: Manually smoke-test in browser**

Run `just dev`, connect to http://localhost:8080, fire abilities at an asteroid. Expected: damage numbers + cast animations + mining beam render. If broken, check browser devtools network tab: WebSocket frames for the player should now be 0x00 typed-event frames (look for binary frames whose first byte is `00` and second byte is non-`08`).

- [ ] **Step 5: Commit**

```bash
git add web-pixi/sdk/transport.ts web-pixi/sdk/client.ts web-pixi/sdk/broadcasts.ts
git commit -m "feat(sdk/web): decode batched 0x00 typed-event frames

Transport layer disambiguates legacy ServerEvent (first byte 0x08
protobuf field-tag) vs new typed-event frames (any other first byte).
Typed-event frames dispatch per-entry to the existing typedEvents
registry — no changes to per-handler API surface."
```

---

### Task 2.3: Mirror to examples/4node-basic SDK

**Files:**
- Modify: `examples/4node-basic/web/sdk/transport.ts`, `client.ts`, `broadcasts.ts`

- [ ] **Step 1: Apply identical changes to the 4node-basic SDK**

The 4node-basic SDK is a smaller/simpler subset of web-pixi/sdk. Apply the same disambiguation logic.

```bash
diff -u web-pixi/sdk/transport.ts examples/4node-basic/web/sdk/transport.ts
```

(The two SDKs are codegen output; some changes might happen automatically when sdkgen is re-run later. For now, hand-edit to keep them in sync.)

- [ ] **Step 2: Run typecheck for both**

```bash
(cd web-pixi && bunx tsc --noEmit)
(cd examples/4node-basic/web && bunx tsc --noEmit)
```

Expected: both clean.

- [ ] **Step 3: Commit**

```bash
git add examples/4node-basic/web/sdk/
git commit -m "chore(4node-basic/sdk): mirror typed-event 0x00 dispatch"
```

---

### Task 2.4: Retire `WorldUpdateMsg.events` field + `gamepb.TypedEvent`

**Files:**
- Modify: `proto/gamepb/game.proto` (remove `events` from WorldUpdateMsg, remove TypedEvent)
- Modify: regenerate `gen/`

After Phase 2.1, `system_network.go` no longer writes `WorldUpdateMsg.events`. The proto field is dead. Removing it now keeps the schema honest.

- [ ] **Step 1: Edit proto**

Delete the `repeated TypedEvent events = 7;` line from `WorldUpdateMsg`. Delete the entire `TypedEvent` message definition.

- [ ] **Step 2: Regenerate**

```bash
just proto
```

Expected: builds cleanly. `gen/go/gamepb/game.pb.go` and `gen/es/gamepb/game_pb.{js,d.ts}` regenerate without `events` and without `TypedEvent`.

- [ ] **Step 3: Confirm Go compile is still clean**

```bash
go vet ./...
```

If anything still references `gamepb.TypedEvent` or `WorldUpdateMsg.Events`, remove those references. Likely sites: `internal/bot/`, `cmd/sdkgen/broadcasts.go`. The sdkgen broadcasts module will need updating (see next task).

- [ ] **Step 4: Commit**

```bash
git add proto/gamepb/game.proto gen/go/gamepb/game.pb.go gen/es/gamepb/
git commit -m "chore(proto): remove WorldUpdateMsg.events + TypedEvent

Both fields were dead after broadcasts moved to typed-event 0x00
frames. WorldUpdateMsg still carries chat for now; chat is
decommissioned in phase 6."
```

---

### Task 2.5: Update sdkgen broadcasts.go to read events from server-event registry

The `cmd/sdkgen/broadcasts.go` generator emits TS classes for every `WorldUpdateMsg.events`-eligible type. Post-2.4, it needs to iterate the new typed-event registry instead.

- [ ] **Step 1: Read the existing generator**

```bash
cat cmd/sdkgen/broadcasts.go
```

Locate the iteration over the schema's `Events` (or similar) section.

- [ ] **Step 2: Add a parallel iteration over server-event types**

The schema dump from `--dump-schema` needs a `server_events` section listing all `RegisterEvent[T]` registrations. `pkg/mmokit/protocol.go:Protocol.assembleFromProcess()` is where this is built.

In `pkg/mmokit/protocol.go`, add a section to the schema:

```go
type ProtocolSchema struct {
    // ... existing fields ...
    ServerEvents []ServerEventSchema `json:"server_events"`
}

type ServerEventSchema struct {
    TypeID uint32 `json:"type_id"`
    GoTypeName string `json:"go_type_name"`
    Fields []FieldSchema `json:"fields"`
}
```

Populate in `assembleFromProcess`:

```go
for _, t := range RegisteredServerEventTypes() {
    s.ServerEvents = append(s.ServerEvents, ServerEventSchema{
        TypeID: TypeIDOf(t),
        GoTypeName: t.String(),
        Fields: extractFieldsFromType(t),  // existing helper used by client-input
    })
}
```

- [ ] **Step 3: Update broadcasts.go to read from the new section**

Modify the generator to iterate `schema.ServerEvents` (and continue iterating the AoI-broadcast types — those still register via `mmokit.Broadcast[T]` which has its own registry section in the schema). Each emits a TS class with `static typeID`, `static decode(buf)` returning a class instance.

- [ ] **Step 4: Run the codegen**

```bash
just client-sdk examples/4node-basic
just space-sdk
```

Expected: regenerated `broadcasts.ts` compiles. No regressions.

- [ ] **Step 5: Commit**

```bash
git add cmd/sdkgen/broadcasts.go pkg/mmokit/protocol.go web-pixi/sdk/broadcasts.ts examples/4node-basic/web/sdk/broadcasts.ts
git commit -m "feat(sdkgen): emit broadcasts.ts from server-event registry

Adds server_events section to --dump-schema; broadcasts.go iterates
the section and emits per-event class+decoder. Replaces old path
that discovered events via WorldUpdateMsg.events field."
```

---

### Task 2.6: Phase 2 verification

- [ ] **Step 1: Run full Go test suite**

```bash
go test ./... 2>&1 | tail -30
```

Expected: PASS.

- [ ] **Step 2: Run typecheck**

```bash
(cd web-pixi && bunx tsc --noEmit)
(cd examples/4node-basic/web && bunx tsc --noEmit)
```

Expected: clean.

- [ ] **Step 3: Browser smoke test**

`just dev`, connect, fire abilities at an asteroid: damage numbers visible, mining beam renders, status effects display, kill produces explosion. If broken, debug; do not advance to Phase 3 with broken broadcasts.

- [ ] **Step 4: No commit needed unless smoke surfaces a fix**

---

## Phase 3 — Server-side Engine Events

Each task in this phase migrates one `gamepb.*Msg` from the protobuf-envelope path to a typed Go struct + reflection codec. The pattern is identical for each; tasks 3.1–3.11 follow the same 7-step shape:

1. Define typed Go struct in `internal/game/event_messages.go` mirroring proto fields.
2. Register via `mmokit.RegisterEvent[T]()` at `cmd/server/main.go` startup (next to existing `RegisterServerEvent` calls — the new and old registries coexist during the migration).
3. Replace caller's `gw.ServerEvents().Send(code, &gamepb.XxxMsg{...})` with `mmokit.SendEvent(gw.Stage, connID, &Xxx{...})`.
4. Update client-side handler from `client.onXxx(msg => ...)` (unchanged in mechanism — codegen emits `onXxx` from `RegisterEvent[T]` registrations) to consume the typed Go struct shape.
5. Run unit + smoke tests.
6. Delete the proto message + the `SE_*` enum entry only at phase 7 cleanup (keep coexisting for rollback during phasing).
7. Commit.

The detailed shape below is shown for **Task 3.1** in full; tasks 3.2–3.11 are abbreviated to (a) new typed struct, (b) caller migration site(s), (c) any per-message quirks. Repeat the 7 steps for each.

---

### Task 3.1: PlayerSpawnedMsg → PlayerSpawned typed event

**Files:**
- Create (or append): `internal/game/event_messages.go`
- Modify: `cmd/server/main.go` — add `mmokit.RegisterEvent[game.PlayerSpawned]()` near existing `RegisterServerEvent` block.
- Modify: caller(s) — find via `grep -rn "PlayerSpawnedMsg\|GSE_PLAYER_SPAWNED\|SE_PLAYER_SPAWNED" --include="*.go"`.

- [ ] **Step 1: Read the existing proto**

```bash
grep -A 10 "message PlayerSpawnedMsg" proto/gamepb/game.proto
```

Note the field set (likely `entity_net_id`, `world_x`, `world_y`, `username`, etc).

- [ ] **Step 2: Define typed struct**

In `internal/game/event_messages.go`:

```go
package game

// PlayerSpawned — server pushes once when the player's entity is created in
// the destination cell after login or respawn. Replaces gamepb.PlayerSpawnedMsg
// during the events-channel migration.
type PlayerSpawned struct {
	EntityNetID uint32
	WorldX      float32
	WorldY      float32
	Username    string
}
```

Match the field set to the existing proto exactly (including any user_id fields).

- [ ] **Step 3: Register the typed event**

In `cmd/server/main.go`, near the existing protocol setup:

```go
mmokit.RegisterEvent[game.PlayerSpawned]()
```

- [ ] **Step 4: Find existing send sites**

```bash
grep -rn "PlayerSpawnedMsg{" internal/game/ pkg/
```

- [ ] **Step 5: Migrate the send sites**

Replace each of the form

```go
gw.ServerEvents().Send(gw.eng.ConnMgr, connID,
    uint32(gamepb.GameServerEventCode_GSE_PLAYER_SPAWNED),
    &gamepb.PlayerSpawnedMsg{EntityNetId: nid, WorldX: x, WorldY: y, Username: u})
```

with

```go
mmokit.SendEvent(gw.Stage, connID, &game.PlayerSpawned{EntityNetID: nid, WorldX: x, WorldY: y, Username: u})
```

(Drop the `gamepb` import from the file if it was the only reason the import existed; verify by `goimports -l <file>`.)

- [ ] **Step 6: Regenerate client SDK + verify typecheck**

```bash
just client-sdk examples/4node-basic
just space-sdk
(cd web-pixi && bunx tsc --noEmit)
(cd examples/4node-basic/web && bunx tsc --noEmit)
```

Expected: client SDK now exports `PlayerSpawned` class with `decode()`, and `client.onPlayerSpawned(handler)` is generated. Typecheck clean.

- [ ] **Step 7: Update web-pixi handler**

In `web-pixi/src/network.ts`, the existing `client.onPlayerSpawned(msg => ...)` registration consumes the protobuf-shape `PlayerSpawnedMsg`. After codegen, the message is the typed `PlayerSpawned` class. Field names should be identical — if not (camelCase vs snake_case), fix the caller. Run typecheck.

- [ ] **Step 8: Smoke test**

`just dev`, connect, verify spawn message in player UI. Same UX as before.

- [ ] **Step 9: Commit**

```bash
git add internal/game/event_messages.go cmd/server/main.go internal/game/*.go web-pixi/src/network.ts examples/4node-basic/web/src/ web-pixi/sdk/ examples/4node-basic/web/sdk/
git commit -m "feat(game): migrate PlayerSpawnedMsg → typed PlayerSpawned event

Replaces protobuf-envelope path with typed reflection-codec on 0x00.
gamepb.PlayerSpawnedMsg + GSE_PLAYER_SPAWNED enum entry stay until
phase 7 cleanup. Same field set, same UX."
```

---

### Tasks 3.2–3.11: Mechanical migrations

Apply the same 9-step shape to each of the following. Per-task notes only.

#### Task 3.2: PlayerDiedMsg → PlayerDied

Source: `gamepb.PlayerDiedMsg`. New struct `game.PlayerDied{...fields}`. Sites: search for `PlayerDiedMsg{`.

#### Task 3.3: DockingStateMsg → DockingState

Source: `gamepb.DockingStateMsg`. Carries the docking-progress UI state.

#### Task 3.4: DockedMsg → Docked

Source: `gamepb.DockedMsg`. Sent on dock-completion.

#### Task 3.5: CurrencyUpdateMsg → CurrencyUpdate

Source: `gamepb.CurrencyUpdateMsg`. Carries currency map (likely a repeated `Currency` sub-message). The reflection codec handles slice-of-struct natively; mirror the field set including the nested struct.

#### Task 3.6: BankContentsMsg → BankContents

Source: `gamepb.BankContentsMsg`. **Note:** in Plan 2, the same `BankContents` Go type will be reused as both an event push (for out-of-band changes) and an op response payload. Keep the type design clean (no event-specific fields).

#### Task 3.7: EquipResultMsg → EquipResult

Source: `gamepb.EquipResultMsg`.

#### Task 3.8: TransferResultMsg → TransferResult

Source: `gamepb.TransferResultMsg`. Sent after inventory transfer success/failure.

#### Task 3.9: MapDataMsg → MapData

Source: `gamepb.MapDataMsg`. One-shot push after spawn — likely contains station list, point-of-interest array.

#### Task 3.10: PlayerOwnStateMsg → PlayerOwnState

Source: `gamepb.PlayerOwnStateMsg`. Per-tick push of own-entity state. Highest-frequency event in this list — verify smoke test that gameplay UI (HUD: position, velocity, health, shield) updates as before.

#### Task 3.11: CellTopologyMsg → CellTopology

Source: `enginepb.CellTopologyMsg` (note: enginepb, not gamepb). Pushed on topology change. Verify the `debug` console command's overlay still works.

#### Task 3.12: LoginMsg → LoginRequest typed event

Source: `gamepb.LoginMsg` (client → server). Per plan opening, Login stays on 0x00 as a typed event in Plan 1; Plan 2 moves it to a 0x01 operation. The Plan 1 migration retires the legacy protobuf path so Phase 7 can fully delete `enginepb.ServerEvent`.

Special: `LoginRequest` arrives on the gateway *before* the player has a cell (no `playerNetID` for the dispatcher). The existing `Config.LoginHandler` callback in `pkg/universe/coordinator.go` is invoked from the gateway's read path, not via the cell-side dispatcher. The wire-format migration is local to the gateway:

- `pkg/universe/gateway.go` (find the inbound login handler) — the existing path decodes a protobuf `LoginMsg` from a `ServerEvent.Data`. Replace with: read the typed-event frame body, look up typeID, if it matches `TypeIDOf(LoginRequest)`, decode via `ReflectUnmarshalOnStage(stage, body, &req)`, pass `req` to the user-provided LoginHandler.
- The user-provided `Config.LoginHandler` signature stays the same (accepts an `any` payload). The 4node-basic main.go's LoginHandler (look at `examples/4node-basic/main.go`) does a type-assert to `*LoginMsg`; update to type-assert to `*LoginRequest`.

#### Task 3.13a: PingMsg → Ping typed event (client → server)

Source: `enginepb.PingMsg`. Today handled inline by the `EventInterceptor` in `pkg/net/conn.go:readPump` (see `pkg/mmokit/protocol.go:96` registration; the actual interceptor wiring lives in `pkg/universe/`). Migration: define typed `Ping` struct, register via `mmokit.RegisterClientInputType[Ping]()` (same registry as other typed inputs), update the gateway/coordinator's interceptor to peek for the typeID and respond inline. The bot client (`internal/bot/bot.go`) sends the protobuf today — update to send the typed frame.

#### Task 3.13b: PongMsg → Pong typed event (server → client)

Source: `enginepb.PongMsg`. Server-side Pong is sent inline as the response to Ping. Replace the protobuf send with `mmokit.SendEvent(stage, connID, &game.Pong{Timestamp: ...})`.

#### Task 3.14: LoginRejectedMsg → LoginRejected typed event

Source: `gamepb.LoginRejectedMsg` (server → client). Pushed on login rejection (duplicate username, etc).

Migration: server side, replace the `ServerEvents().Send(SE_LOGIN_REJECTED, &LoginRejectedMsg{...})` call (in `pkg/universe/coordinator.go` or the gateway's login path — find via `grep -rn "LoginRejectedMsg{"`) with `mmokit.SendEvent(stage, connID, &game.LoginRejected{...})`. Client side, the `client.onLoginRejected(rejected => ...)` registration in `web-pixi/src/network.ts:288` continues to work — the codegen emits the typed handler from the new `RegisterEvent[T]` registration.

---

### Task 3.15: Phase 3 verification

- [ ] **Step 1: Run full test suite**

```bash
go test ./...
```

- [ ] **Step 2: Browser smoke**

`just dev`, run through: connect, spawn, click-to-move, mining, ability cast, dock, undock, equip, loot, transfer, station map data, currency display, bank contents (input still on 0x02 from old chat queue path until Phase 5), debug-overlay topology.

- [ ] **Step 3: Distributed-mode smoke**

```bash
just distributed
```

Connect across the gateway, verify cross-host migration produces correct CellTopology / PlayerOwnState pushes. If broken, debug before advancing.

---

## Phase 4 — WorldDelta

The `SE_DELTA_WORLD_UPDATE` (code 13) frame today is a fully custom binary body wrapped in a protobuf `ServerEvent` envelope. The body bytes (20-byte header + per-entity FullEntry/DeltaEntry/Removed/Exited) stay byte-for-byte identical; only the wrapper changes.

### Task 4.1: Define WorldDelta typed event with passthrough body

**Files:**
- Modify: `internal/game/event_messages.go`
- Modify: `pkg/universe/reflect_codec_registry.go` (or wherever passthrough byte-slice codec lives)

- [ ] **Step 1: Define struct**

```go
// WorldDelta — per-tick entity-state delta. Body is the custom binary frame
// produced by pkg/quantize.Encode... — opaque to the reflection codec, copied
// through verbatim via the passthrough []byte codec.
type WorldDelta struct {
	Body []byte
}
```

- [ ] **Step 2: Verify the reflection codec handles []byte natively**

If `ReflectMarshal` already encodes `[]byte` as `[u32 len][bytes]` (check the existing unmarshal path), no custom codec is needed. The wire bytes for `WorldDelta` are then `[bodyLen u32][body N bytes]`, plus the 8-byte typeID+body_len header for the frame itself. That's an extra 4 bytes per delta vs. the today's wire — acceptable.

If `[]byte` doesn't encode natively, register a passthrough codec for it:

```go
RegisterReflectCodec(reflect.TypeOf([]byte{}), &ReflectCodec{
    Size: func() int { return -1 },  // variable
    Encode: func(buf []byte, v reflect.Value) {
        body := v.Bytes()
        beUint32(buf[0:4], uint32(len(body)))
        copy(buf[4:], body)
    },
    Decode: func(_ *Stage, data []byte, v reflect.Value) {
        bodyLen := beUint32From(data[0:4])
        v.SetBytes(append([]byte(nil), data[4:4+bodyLen]...))
    },
})
```

(Adjust to the actual `ReflectCodec` shape; this is illustrative.)

- [ ] **Step 3: Register**

```go
mmokit.RegisterEvent[game.WorldDelta]()
```

- [ ] **Step 4: Find and migrate the WorldDelta producer**

```bash
grep -rn "SE_DELTA_WORLD_UPDATE" pkg/system/replication.go
```

The replication system's frame producer wraps the encoded delta in a `ServerEvent{code: 13, data: encoded}` proto. Replace with:

```go
mmokit.SendEvent(stage, connID, &game.WorldDelta{Body: encoded})
```

- [ ] **Step 5: Update client decoder**

`web-pixi/src/network.ts:294` calls `client.onDeltaWorldUpdate(update => applyDeltaUpdate(state, update))`. After codegen, this becomes `client.onWorldDelta(msg => applyDeltaUpdate(state, decodeDeltaFrame(msg.body)))`. The `decodeDeltaFrame` helper exists in `web-pixi/sdk/_core/` (or similar) — repoint it to consume `WorldDelta.body` directly.

The 4-byte length prefix from the reflection codec on `[]byte` is consumed by the `WorldDelta.decode()` codegen; the body that emerges is the same 20-byte-header binary frame the client is already prepared to parse.

- [ ] **Step 6: Smoke test**

`just dev`, verify entity state replicates (other players visible, asteroids, lootcrates).

- [ ] **Step 7: Commit**

```bash
git add internal/game/event_messages.go pkg/universe/reflect_codec_registry.go pkg/system/replication.go web-pixi/src/network.ts cmd/server/main.go
git commit -m "feat(game): migrate SE_DELTA_WORLD_UPDATE → typed WorldDelta event

Strips the protobuf ServerEvent wrapper from the entity-state delta
frame. Body bytes (20-byte header + FullEntry/DeltaEntry/Removed/
Exited) pass through verbatim via the reflection codec's []byte
handling."
```

---

## Phase 5 — Inputs Channel-Byte Change

Flip 0x02 typed-client-input frames over to 0x00. The wire shape is identical, so the dispatcher built in Task 1.5 already handles them. Server-side change: `gateway.go:forwardChannel` no longer routes 0x02 separately. Client-side change: SDK transport flips the channel byte in `client.send(...)`.

### Task 5.1: Server-side channel-byte flip

**Files:**
- Modify: `pkg/universe/gateway.go:forwardChannel`
- Modify: `pkg/universe/virtual_conn_manager.go:appendChannel`
- Modify: `pkg/net/conn.go:readPump` (add 0x00 dispatch to the new path)

- [ ] **Step 1: Read existing forwardChannel**

```bash
sed -n '885,920p' pkg/universe/gateway.go
```

The function as it stands forwards 0x00, 0x01, 0x02 by tagging the channel byte on the MeshFrame for the host to demultiplex. After this phase, only 0x00 and 0x01 exist.

- [ ] **Step 2: Wire DispatchInboundEventFrame into the consumer path**

Inbound 0x00 traffic today comes through two paths:

1. **Gateway interceptor** (`pkg/net/conn.go:readPump` calls `EventInterceptor` before queueing) — handles inline events like Ping. After Phase 3.13a (Ping migrated), the interceptor needs to peek at the typed-event frame's first byte and disambiguate: legacy `ServerEvent` (first byte `0x08` protobuf tag) vs typed-event frame (any other first byte, dispatch via `DispatchInboundEventFrame`).
2. **Per-cell dispatch** — currently the gateway's `forwardChannel` ships drained 0x00 bytes via MeshData to the host's `VirtualConnManager` (already typed-input-aware after the recent Plan G follow-up commit `8f8557a`). The host's existing `Stage.DispatchClientInput()` (called per tick) drains the per-session client-input queue; after Phase 5, that same queue receives 0x00 typed frames and the dispatcher just needs to not require the channel byte. Modify `Stage.DispatchClientInput` (or the helper it calls) to invoke `DispatchInboundEventFrame(stage, playerNetID, payload)` for each drained payload.

Add the disambiguation at the gateway interceptor:

```go
for _, payload := range conn.DrainInput(connID) {
    if len(payload) > 0 && payload[0] == 0x08 {
        // Legacy ServerEvent — current path.
        handleLegacyServerEvent(payload)
    } else {
        // Typed-event frame on 0x00.
        playerNetID := resolvePlayerNetID(connID)
        universe.DispatchInboundEventFrame(stage, playerNetID, payload)
    }
}
```

(The disambiguation byte test mirrors the client-side check from Task 2.2.)

- [ ] **Step 3: Update VirtualConnManager**

In `pkg/universe/virtual_conn_manager.go:appendChannel`, the `case pkgnet.ChannelClientInput:` branch currently appends to `sess.clientInput`. After this phase, no frame should arrive on `ChannelClientInput`. Add a panic with a helpful message:

```go
case pkgnet.ChannelClientInput:
    panic("VirtualConnManager: ChannelClientInput received after phase 5 retirement; sender must use ChannelEvent")
```

This catches any source still emitting 0x02; the panic clears in phase 7 when the constant retires.

- [ ] **Step 4: Run server tests**

```bash
go test ./...
```

Expected: PASS. The server-side flip is invisible until clients flip too.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/gateway.go pkg/universe/virtual_conn_manager.go pkg/net/conn.go pkg/engine/loop.go
git commit -m "feat(universe): route inbound 0x00 typed events to engine dispatcher

Read pump disambiguates legacy ServerEvent (first byte 0x08) vs new
typed-event frames; typed frames route to DispatchInboundEventFrame.
ChannelClientInput receive path panics defensively — no frame should
arrive on 0x02 after the client flip."
```

---

### Task 5.2: Client-side channel-byte flip

**Files:**
- Modify: `web-pixi/sdk/transport.ts`
- Modify: `web-pixi/sdk/inputs.ts`
- Modify: `examples/4node-basic/web/sdk/transport.ts`
- Modify: `examples/4node-basic/web/sdk/inputs.ts`

- [ ] **Step 1: Find the client.send channel byte**

```bash
grep -n "0x02\|CH_CLIENT_INPUT" web-pixi/sdk/inputs.ts web-pixi/sdk/transport.ts
```

`web-pixi/sdk/transport.ts:5` defines `CH_CLIENT_INPUT = 0x02`. The encoded send path puts this byte first.

- [ ] **Step 2: Flip the byte**

In the typed-input encode path (in `inputs.ts` or wherever `client.send(MsgClass, args)` builds the wire bytes), change the channel byte from `0x02` to `0x00`. Leave the `[typeID:u32 BE][body_len:u32 BE][body]` layout unchanged.

- [ ] **Step 3: Mirror to 4node-basic**

Same change in `examples/4node-basic/web/sdk/`.

- [ ] **Step 4: Smoke test**

`just dev`, click-to-move, dock, undock, cast ability, mine, equip, loot. All must work.

`just distributed`, repeat. The forwardChannel mapping in gateway.go must route the 0x00 frames coming over MeshData correctly.

- [ ] **Step 5: Commit**

```bash
git add web-pixi/sdk/ examples/4node-basic/web/sdk/
git commit -m "feat(sdk/web): flip typed-input channel byte 0x02 → 0x00

Inputs now share the same channel byte as events. Wire shape is
identical (typeID + body_len + body); the channel was the only
distinction. Server dispatcher disambiguates legacy ServerEvent vs
typed frames by first-byte test."
```

---

## Phase 6 — Chat Decommission

Server-side chat plumbing is fully deleted. Client-side chat HUD DOM elements stay; the wiring (send + receive) is removed.

### Task 6.1: Delete server-side chat plumbing

**Files:**
- Delete: `internal/game/input_handlers.go:121-153` (Chat HandleClient registration)
- Delete: `internal/game/input_messages.go` Chat struct
- Delete: `internal/game/system_network.go:131,142-148,189-202` chat queue/send/drain
- Delete: `pkg/universe/bridge.go` `RelayChatToOtherCells` method
- Delete: `pkg/universe/cell_bridge_impl.go` corresponding implementation
- Delete: `internal/game/logcat.go` `CatPlayerChat` (if no other users)
- Modify: `proto/enginepb/engine.proto` — remove `ChatMsg`

- [ ] **Step 1: Remove Chat HandleClient handler**

In `internal/game/input_handlers.go`, delete the entire block from `// ─── Chat (input direction only — broadcast leg keeps enginepb.ChatMsg) ─` through the closing `})` of the `mmokit.HandleClient[Chat]` registration.

- [ ] **Step 2: Remove Chat input struct**

In `internal/game/input_messages.go`, delete the `type Chat struct { ... }` definition and the surrounding doc comment.

- [ ] **Step 3: Remove chat queue + send + drain**

In `internal/game/system_network.go`:
- Line 131: delete `s.pendingChat = mmokit.Peek[*enginepb.ChatMsg](gw.Queue)`.
- Lines 142–148: delete the `if len(s.pendingChat) > 0 { ... }` block in `beforeSend`.
- Lines 189–202: delete the `if len(s.pendingChat) > 0 { ... }` block + `mmokit.Drain[*enginepb.ChatMsg](gw.Queue)` in `afterTick`.
- Remove `s.pendingChat` field on the system struct.
- Remove unused `enginepb` import if it was only for ChatMsg.

- [ ] **Step 4: Remove RelayChatToOtherCells**

```bash
grep -rn "RelayChatToOtherCells" pkg/universe/ internal/
```

Delete the method declaration in `bridge.go`, the implementation in `cell_bridge_impl.go`, and any cross-cell relay handler.

- [ ] **Step 5: Remove ChatMsg proto**

Delete `message ChatMsg { ... }` from `proto/enginepb/engine.proto`. Run `just proto`.

- [ ] **Step 6: Remove CatPlayerChat if unused**

```bash
grep -rn "CatPlayerChat" .
```

If only the now-deleted handler used it, remove the registration.

- [ ] **Step 7: Verify Go compile**

```bash
go vet ./...
```

Fix any leftover dangling references.

- [ ] **Step 8: Commit**

```bash
git add -u internal/ pkg/universe/ proto/enginepb/ gen/go/enginepb/
git commit -m "feat(game): decommission in-engine chat

Chat moves to its own service (mirroring auth-service pattern). All
server-side chat plumbing deleted: Chat input handler, chat queue
drain, beforeSend/afterTick chat sends, RelayChatToOtherCells,
enginepb.ChatMsg, CatPlayerChat. Client UI shells (chat <div>, input
box) stay; future chat service wires up to existing DOM."
```

---

### Task 6.2: Client-side chat wiring removal

**Files:**
- Modify: `web-pixi/src/network.ts:298-316` (delete onWorldUpdate chat block)
- Modify: `web-pixi/src/input.ts` (delete chat input box submit handler)

- [ ] **Step 1: Remove chat receive handler in network.ts**

Delete the `client.onWorldUpdate(msg => { if (msg.chatMessages) { ... } })` block at network.ts:298-316.

If `client.onWorldUpdate` has no other consumers after this deletion, the registration line in `client.ts` (auto-generated, but still — `WorldUpdateMsg` is empty after Phase 2 except for the `tick` field that nobody reads) can be removed too. Check for any other references:

```bash
grep -rn "onWorldUpdate\|WorldUpdateMsg" web-pixi/ examples/4node-basic/web/
```

After Phase 2, only chat referenced WorldUpdateMsg; removal is total.

- [ ] **Step 2: Remove chat send call in input.ts**

```bash
grep -n "Chat\|sendChat\|chat-input" web-pixi/src/input.ts
```

Delete the chat input box submit listener / `client.send(Chat, {...})` call. Leave the `<input id="chat-input">` DOM element in HTML untouched.

- [ ] **Step 3: Verify typecheck**

```bash
(cd web-pixi && bunx tsc --noEmit)
```

Expected: clean. The `Chat` class no longer generated by sdkgen (its server-side type was deleted in 6.1, so the SDK regenerates without it on next build).

- [ ] **Step 4: Smoke test**

`just dev`, connect, verify everything still works (chat input box does nothing — that's expected). Spawn, ability cast, dock, market browse, etc. all must work.

- [ ] **Step 5: Commit**

```bash
git add web-pixi/src/network.ts web-pixi/src/input.ts examples/4node-basic/web/src/
git commit -m "feat(web): unwire client-side chat (UI shells stay)

Chat HUD <div> and input box stay in HTML for the future chat-service
wiring. Send + receive plumbing in network.ts and input.ts deleted."
```

---

## Phase 7 — Final Cleanup

### Task 7.1: Delete `WorldUpdateMsg` proto entirely

**Files:** `proto/gamepb/game.proto`, regen, references.

- [ ] **Step 1: Confirm zero remaining consumers**

```bash
grep -rn "WorldUpdateMsg" --include="*.go" --include="*.ts" .
```

Expected: only `cmd/sdkgen/broadcasts.go` legacy comment and a couple of generated TS files. Update sdkgen to no longer reference WorldUpdateMsg in its iteration logic; regenerate.

- [ ] **Step 2: Delete `message WorldUpdateMsg`**

In `proto/gamepb/game.proto`, delete the `WorldUpdateMsg` message definition entirely.

- [ ] **Step 3: Delete `SE_WORLD_UPDATE` enum entry**

In `proto/enginepb/engine.proto`, delete the `SE_WORLD_UPDATE = 0;` line. Renumber subsequent SE_* entries from 0 (per the project's `feedback_proto_field_cleanup` memory: never reserve, always renumber).

- [ ] **Step 4: Regen + compile**

```bash
just proto
go vet ./...
(cd web-pixi && bunx tsc --noEmit)
```

Fix any breakage.

- [ ] **Step 5: Commit**

```bash
git add proto/ gen/ cmd/sdkgen/
git commit -m "chore(proto): retire WorldUpdateMsg + SE_WORLD_UPDATE

Last carrier was chat (decommissioned in phase 6). Renumbered SE_*
enum entries from 0 per project policy."
```

---

### Task 7.2: Retire `ChannelClientInput` (0x02)

**Files:** `pkg/net/conn.go`, references.

- [ ] **Step 1: Remove the constant**

In `pkg/net/conn.go`, delete `ChannelClientInput byte = 0x02`. Delete the `clientInput [][]byte` field, `DrainClientInput()` method, the `case ChannelClientInput:` branch in `readPump`, and any `clientInput: make([][]byte, 0, 8)` initializer.

- [ ] **Step 2: Remove the mmokit re-export**

In `pkg/mmokit/mmokit.go`, delete `ChannelClientInput = net.ChannelClientInput` if it exists.

- [ ] **Step 3: Remove the gateway forward branch**

In `pkg/universe/gateway.go:forwardChannel`, delete the `g.connMgr.DrainClientInput(connID)` line. The remaining code forwards 0x00 events and 0x01 ops.

- [ ] **Step 4: Remove VCM branch**

In `pkg/universe/virtual_conn_manager.go:appendChannel`, delete the `case pkgnet.ChannelClientInput:` panic added in Task 5.1 and the `clientInput` slice on the session struct.

- [ ] **Step 5: Verify**

```bash
go vet ./...
go test ./...
```

Expected: clean. `grep -rn "ChannelClientInput\|0x02" pkg/ internal/` should return zero hits in code paths (only comments/docs may remain — clean those up too).

- [ ] **Step 6: Commit**

```bash
git add -u pkg/ internal/
git commit -m "chore(net): retire ChannelClientInput (0x02)

Channel byte and all session-level plumbing deleted. Two channels
remain: 0x00 (events) and 0x01 (operations — Plan 2 will redesign)."
```

---

### Task 7.3: Retire migrated SE_* enum entries + legacy ServerEvents.Build/Send

**Files:** `proto/enginepb/engine.proto`, `pkg/mmokit/server_events.go`, regen, references.

- [ ] **Step 1: Delete migrated SE_* entries**

For every server event migrated in phases 3–4, the corresponding `SE_*` enum entry in `enginepb.ServerEventCode` and `gamepb.GameServerEventCode` is dead. Delete them and renumber.

Migrated entries: `SE_PLAYER_SPAWNED`, `SE_PLAYER_DIED`, `SE_DOCKING_STATE`, `SE_DOCKED`, `SE_CURRENCY_UPDATE`, `SE_BANK_CONTENTS`, `SE_EQUIP_RESULT`, `SE_TRANSFER_RESULT`, `SE_MAP_DATA`, `SE_PLAYER_OWN_STATE`, `SE_CELL_TOPOLOGY`, `SE_DELTA_WORLD_UPDATE` (plus any `GSE_*` variants the game-side enum has).

- [ ] **Step 2: Delete `ServerEvents.Build` and `ServerEvents.Send`**

In `pkg/mmokit/server_events.go`, the `Build` and `Send` methods are no longer used (all callers migrated to `mmokit.SendEvent[T]`). Delete them. The `RegisterServerEvent[T]` registration may still be referenced by the protocol-schema export — replace with the parallel `RegisterEvent[T]` registry from Task 1.2.

- [ ] **Step 3: Delete `MakeEvent` if no longer used**

```bash
grep -rn "MakeEvent" pkg/ internal/ cmd/
```

If `MakeEvent` was only used by `ServerEvents.Build`, delete it. `enginepb.ServerEvent` proto type may now also be deletable — check.

- [ ] **Step 4: Regen + compile + test**

```bash
just proto
go vet ./...
go test ./...
(cd web-pixi && bunx tsc --noEmit)
(cd examples/4node-basic/web && bunx tsc --noEmit)
```

- [ ] **Step 5: Commit**

```bash
git add -u
git commit -m "chore(mmokit,proto): retire ServerEvents.Build/Send + migrated SE_*

All callers migrated to mmokit.SendEvent[T] in phase 3–4. Delete the
proto-based path entirely. enginepb.ServerEvent may also retire if no
remaining consumers."
```

---

### Task 7.4: Final smoke + acceptance

- [ ] **Step 1: Full test suite**

```bash
go test ./... 2>&1 | tail -40
```

Expected: PASS.

- [ ] **Step 2: Browser smoke (`just dev`)**

Run through the full feature matrix:
- Connect with new username (auto-register via auth flow)
- Spawn confirmation
- Click-to-move
- Mine an asteroid (mining beam VFX, extract events visible)
- Cast ability on an asteroid (damage numbers, cast animation)
- Take damage from an NPC (DoT visualization)
- Death + respawn explosion
- Dock at a station
- Browse marketplace (Plan 1 doesn't migrate this — should still work via 0x01 protobuf path)
- Place a sell order
- Cancel an order
- View bank contents (input still works through input-on-0x00 path)
- Equip an item
- Loot a crate
- Inventory transfer
- Jettison an item
- Login rejection on duplicate username (should still work — Login is still on 0x00 events)
- Cell-topology overlay (`debug` console command)
- Cross-cell movement (walk to a cell border)

- [ ] **Step 3: Distributed-mode smoke (`just distributed`)**

Start the 4-process setup. Connect via the gateway. Verify:
- Login works through the gateway
- Spawn on the correct host
- Cross-host handoff (walk across a cell boundary owned by a different host)
- Marketplace operation (place an order — currently routed to player's cell)
- Graceful shutdown of one host (SIGINT) drains its cells

- [ ] **Step 4: Distributed-mode integrity invariants**

The 4node-basic example runs with `InvariantPanic` + `StrictNetIDIndex`. If anything panics during smoke, fix the underlying issue before merging.

- [ ] **Step 5: Update spec status**

In `docs/superpowers/specs/2026-05-06-events-operations-channel-redesign.md`, mark phases 1–5 + 9 as **landed**. Status remains `design` until Plan 2 also lands.

```bash
git add docs/superpowers/specs/2026-05-06-events-operations-channel-redesign.md
git commit -m "docs(spec): mark Plan 1 (events channel + chat decomm) landed"
```

- [ ] **Step 6: Merge to main**

Solo-dev workflow per project memory. Use `--no-ff` to preserve the per-phase commit history:

```bash
git checkout main
git merge --no-ff feat/mmokit-events-channel -m "Merge branch 'feat/mmokit-events-channel'

Plan 1 of the events/operations channel redesign. Channel 0x00 now
carries typed reflection-codec frames; protobuf ServerEvent envelope
retired; channel 0x02 retired (typed-input merged into 0x00); in-
engine chat decommissioned (UI shells stay).

Plan 2 (operations channel) follows."
```

- [ ] **Step 7: Verify post-merge**

```bash
go vet ./...
go test ./...
git log --oneline -5
```

Expected: clean state on main.

---

## Acceptance Criteria

- `just build` produces a clean binary; `go vet ./...` clean.
- Web-pixi `tsc --noEmit` clean; `examples/4node-basic/web` `tsc --noEmit` clean.
- `just dev` smoke flow passes the full feature matrix in Task 7.4 Step 2.
- `just distributed` smoke flow passes the full matrix in Task 7.4 Step 3.
- Channel 0x00 carries only typed reflection-codec frames after Phase 7 (no protobuf `ServerEvent` envelope).
- Channel 0x02 (`ChannelClientInput`) retired; constant deleted from `pkg/net/conn.go`.
- In-engine chat fully removed: `enginepb.ChatMsg` deleted, `internal/game/input_handlers.go` Chat handler deleted, `internal/game/system_network.go` chat plumbing deleted, `RelayChatToOtherCells` deleted.
- `gamepb.WorldUpdateMsg`, `gamepb.TypedEvent`, all migrated `gamepb.*Msg` and `enginepb.SE_*` enum entries deleted from proto.
- Client-side chat HUD `<div>` and input box DOM elements remain in HTML (UI shells for the future chat service).
- Spec [2026-05-06-events-operations-channel-redesign.md](../specs/2026-05-06-events-operations-channel-redesign.md) phases 1–5 + 9 marked landed.
