# Operations Channel Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Migrate the `0x01` operations channel from protobuf `OpRequest`/`OpResponse` envelopes to typed reflection-codec frames. Move Login from a typed event on `0x00` to a `RouteGatewayLocal` operation on `0x01`. Migrate marketplace + bank ops to typed `RegisterOp[Req, Res]` registrations. End state: zero protobuf bytes on any client-facing wire frame.

**Architecture:** Wire format on `0x01` becomes `[0x01][typeID:u32 LE][request_id:u64 LE][bodyLen:u32 LE][body:N bytes]` — same shape both directions. Each op registers a `Request → Response` typed pair; typeID identifies which. Disambiguation between legacy proto (`OpRequest`) and typed-op frames uses the same first-byte test that Plan 1 used for events: `0x08` (proto field-1 varint tag) → legacy, else → typed. `RouteKind` enum (`RouteGatewayLocal | RoutePlayerCell`) declared per-op; mirrors `cmdsys.Command.RouteKind`. Generic `OperationError{code, message}` framework type carries handler errors and unknown-typeID rejections back to the client; client framework intercepts and rejects the matching pending Promise by `request_id`.

**Tech Stack:** Go 1.21+ (generics), existing `pkg/universe/reflect_marshal.go` reflection codec (with the `bytes` fast-path codec from Plan 1), `mmokit.TypeIDOf()` FNV-1a 32-bit hash, WebSocket via `coder/websocket`, TypeScript codegen via `cmd/sdkgen` to `web-pixi/sdk/operations.ts` and `examples/4node-basic/web/sdk/operations.ts`.

**Spec:** [docs/superpowers/specs/2026-05-06-events-operations-channel-redesign.md](../specs/2026-05-06-events-operations-channel-redesign.md). This plan implements operations channel typed-bodies migration + login → operation + marketplace/bank ops migration. Plan 1 (events channel + chat decomm) landed at commit `6c7b4f0` on main.

**In scope:**
- Typed-op codec: `EncodeTypedOpFrame`, `DecodeTypedOpFrame`
- `mmokit.RegisterOp[Req, Res any](r, kind, handler)` registration verb
- `RouteKind` enum
- `OperationError` framework error type
- Server-side typed-op dispatcher + gateway routing (gateway-local vs cell-routed)
- Client-side framework: typed-op send + Promise correlator
- 5 marketplace ops migrated (Browse, CreateOrder, CancelOrder, MyOrders, InstantTrade)
- Bank op migrated (BankRequest → BankResponse)
- Login op migrated (LoginRequest → LoginResponse, RouteGatewayLocal)
- Bot client (`internal/bot/`) updated to send typed `LoginRequest`
- Codegen: `operations.ts` emitter
- Cleanup: legacy `pkg/ops` proto-constrained `Register` path retired; `enginepb.OpRequest`/`OpResponse` deleted; migrated marketplace/bank protos deleted

**Out of scope (deferred to future plans):**
- `CE_PING` inbound migration to typed (TODO at `cmd/server/main.go:101` from Plan 1)
- Bot client recv-loop rewire to typed events (TODO from Plan 1)
- Surviving `enginepb.SE_PLAYER_SPAWNED` / `SE_CELL_CHANGE` / `SE_SERVER_CONFIG` framework events on the legacy 0x00 ServerEvent envelope
- Chat service implementation (separate spec)
- Cached-events / late-joiner replay (Photon-style)
- Server-initiated operations (push-style RPC)

---

## File Structure

**New files:**

- `pkg/universe/typed_op_frame.go` — wire-format encoders for the 0x01 typed-op frame
- `pkg/universe/op_dispatch.go` — server-side typed-op dispatcher
- `pkg/mmokit/handle_op.go` — `RegisterOp[Req, Res]` registration verb + `RouteKind` enum + `OperationError` framework type
- `pkg/mmokit/op_messages.go` — typed Login Request/Response Go structs + Bank Request/Response (the structs the migrated ops register)
- `cmd/sdkgen/operations.go` — emits `operations.ts` from the protocol's op registry
- `web-pixi/sdk/operations.ts` — generated TS module (typed Promise wrappers per op)
- `examples/4node-basic/web/sdk/operations.ts` — same, for the basic example

**Modified files:**

- `pkg/universe/gateway.go` — add typed-op routing to inbound 0x01 frames
- `pkg/ops/router.go` — extend with typed-op handler registry; keep proto path during transition
- `pkg/mmokit/mmokit.go` — re-export `RegisterOp[Req, Res]` (typed shape) through facade; keep old proto-shape for transition
- `pkg/mmokit/protocol.go` — add `Operations []OperationSchema` section to schema dump
- `internal/marketplace/handler.go` — port 5 marketplace ops from proto to typed
- `internal/game/system_economy.go` — port `BankRequest` from input to op handler
- `internal/game/input_handlers.go` — remove `BankRequest` HandleClient registration
- `internal/game/input_messages.go` — remove `BankRequest` typed input struct
- `internal/bot/bot.go` — replace `enginepb.LoginMsg` send with typed `LoginRequest` op call
- `proto/gamepb/game.proto` — delete `Market*Request`, `Market*Response`, `MarketPriceLevel`, `MarketOrderEntry`, `BankRequestMsg`, `OperationCode` enum
- `proto/enginepb/engine.proto` — delete `OpRequest`, `OpResponse`, `LoginMsg`
- `web-pixi/sdk/transport.ts` — add typed-op frame encode + receive disambiguation
- `web-pixi/sdk/client.ts` — wire `operations.ts` Promise correlator
- `web-pixi/src/ui/{bank,market}.ts` — call sites switch to `client.market*` / `client.bank*` typed wrappers
- Mirror in `examples/4node-basic/web/sdk/` and `examples/4node-basic/web/src/`
- `cmd/sdkgen/{generate,main,schema}.go` — wire the operations generator into the build

---

## Phase 0 — Setup

### Task 0.1: Create branch from main

**Files:** none (git only)

- [ ] **Step 1: Verify clean tree on main**

```bash
git checkout main && git status
```

Expected: `On branch main`, `nothing to commit, working tree clean`. Plan 1's merge commit `6c7b4f0` should be the most recent commit on main.

- [ ] **Step 2: Create branch**

```bash
git checkout -b feat/mmokit-operations-channel
```

- [ ] **Step 3: Verify build is clean**

```bash
go vet ./... && (cd examples/4node-basic/web && bunx tsc --noEmit) && (cd web-pixi && bunx tsc --noEmit)
```

Expected: no output / no errors.

---

## Phase 1 — Typed-Op Foundation

### Task 1.0: Rename existing `mmokit.RegisterOp` to `RegisterProtoOp`

The existing `mmokit.RegisterOp` is the proto-constrained shape with signature `RegisterOp(router, code, name, handler)`. The new typed generic shape will be `RegisterOp[Req, Res any](kind, handler)`. Go does not allow two functions to share a name (no overloading), so the legacy one renames out of the way. Phase 5 deletes it.

**Files:**
- Modify: `pkg/mmokit/mmokit.go` (or wherever `RegisterOp` is currently re-exported)
- Modify: every caller — `internal/marketplace/handler.go` (5 call sites)

- [ ] **Step 1: Find current callers**

```bash
grep -rn "mmokit\.RegisterOp(" --include="*.go" .
```

Expected: 5 hits in `internal/marketplace/handler.go`.

- [ ] **Step 2: Rename the function**

In `pkg/mmokit/mmokit.go`, rename `RegisterOp` → `RegisterProtoOp`. Update the docstring to note "deprecated; will be deleted in Plan 2 Phase 5; use RegisterOp[Req, Res] for new code".

- [ ] **Step 3: Update callers**

In `internal/marketplace/handler.go`, replace each `mmokit.RegisterOp(...)` with `mmokit.RegisterProtoOp(...)`.

- [ ] **Step 4: Verify**

```bash
go vet ./... && go test ./internal/marketplace/ -v
```

Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/mmokit.go internal/marketplace/handler.go
git commit -m "chore(mmokit): rename RegisterOp → RegisterProtoOp

Frees the clean name 'RegisterOp' for the new generic typed-op
registration verb (Phase 1 Task 1.3). RegisterProtoOp is deprecated
and deleted in Phase 5 once all marketplace + bank ops migrate."
```

---

### Task 1.1: Add the typed-op wire-format encoder

**Files:**
- Create: `pkg/universe/typed_op_frame.go`
- Test: `pkg/universe/typed_op_frame_test.go`

- [ ] **Step 1: Write the failing test**

`pkg/universe/typed_op_frame_test.go`:

```go
package universe

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestEncodeTypedOpFrame(t *testing.T) {
	body := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
	frame := EncodeTypedOpFrame(0xDEADBEEF, 0x0123456789ABCDEF, body)

	if frame[0] != 0x01 {
		t.Fatalf("channel byte: got %#x, want 0x01", frame[0])
	}
	if binary.LittleEndian.Uint32(frame[1:5]) != 0xDEADBEEF {
		t.Fatalf("typeID")
	}
	if binary.LittleEndian.Uint64(frame[5:13]) != 0x0123456789ABCDEF {
		t.Fatalf("request_id")
	}
	if binary.LittleEndian.Uint32(frame[13:17]) != uint32(len(body)) {
		t.Fatalf("body_len")
	}
	if !bytes.Equal(frame[17:], body) {
		t.Fatalf("body bytes mismatch")
	}
	if len(frame) != 1+4+8+4+len(body) {
		t.Fatalf("total len")
	}
}

func TestDecodeTypedOpFrame(t *testing.T) {
	body := []byte{0xAA, 0xBB, 0xCC}
	frame := EncodeTypedOpFrame(0xCAFEBABE, 99, body)

	// Strip the channel byte before decoding (read pump strips it).
	typeID, requestID, gotBody, err := DecodeTypedOpFrame(frame[1:])
	if err != nil {
		t.Fatalf("DecodeTypedOpFrame: %v", err)
	}
	if typeID != 0xCAFEBABE {
		t.Fatalf("typeID: got %#x", typeID)
	}
	if requestID != 99 {
		t.Fatalf("request_id: got %d", requestID)
	}
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("body mismatch")
	}
}

func TestDecodeTypedOpFrame_Truncated(t *testing.T) {
	if _, _, _, err := DecodeTypedOpFrame([]byte{0x01, 0x02, 0x03}); err == nil {
		t.Fatalf("expected error on truncated payload")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/universe/ -run "TestEncodeTypedOpFrame|TestDecodeTypedOpFrame" -v`
Expected: FAIL — `EncodeTypedOpFrame undefined`, `DecodeTypedOpFrame undefined`.

- [ ] **Step 3: Write minimal implementation**

`pkg/universe/typed_op_frame.go`:

```go
package universe

import (
	"encoding/binary"
	"errors"

	pkgnet "github.com/zenion/mmokit/pkg/net"
)

// EncodeTypedOpFrame produces a single-op 0x01 frame:
//
//	[0x01][typeID:u32 LE][request_id:u64 LE][body_len:u32 LE][body]
//
// Same shape in both directions. The receiver determines request-vs-response
// from the typeID — each operation registers a Request → Response type pair,
// so a known typeID is unambiguously one or the other.
func EncodeTypedOpFrame(typeID uint32, requestID uint64, body []byte) []byte {
	frame := make([]byte, 1+4+8+4+len(body))
	frame[0] = pkgnet.ChannelOperation
	binary.LittleEndian.PutUint32(frame[1:5], typeID)
	binary.LittleEndian.PutUint64(frame[5:13], requestID)
	binary.LittleEndian.PutUint32(frame[13:17], uint32(len(body)))
	copy(frame[17:], body)
	return frame
}

// DecodeTypedOpFrame parses a 0x01 typed-op payload (channel byte already
// stripped by the read pump). Returns the typeID, request_id, and body slice
// (a view into the payload — caller must copy if needed past payload
// lifetime). Errors if the payload is structurally invalid.
func DecodeTypedOpFrame(payload []byte) (typeID uint32, requestID uint64, body []byte, err error) {
	const headerLen = 4 + 8 + 4
	if len(payload) < headerLen {
		return 0, 0, nil, errors.New("typed-op frame: truncated header")
	}
	typeID = binary.LittleEndian.Uint32(payload[0:4])
	requestID = binary.LittleEndian.Uint64(payload[4:12])
	bodyLen := binary.LittleEndian.Uint32(payload[12:16])
	if int(bodyLen) > len(payload)-headerLen {
		return 0, 0, nil, errors.New("typed-op frame: declared body_len exceeds payload")
	}
	body = payload[headerLen : headerLen+int(bodyLen)]
	return typeID, requestID, body, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/universe/ -run "TestEncodeTypedOpFrame|TestDecodeTypedOpFrame" -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/typed_op_frame.go pkg/universe/typed_op_frame_test.go
git commit -m "feat(universe): add typed-op 0x01 frame encoder/decoder

Wire format: [0x01][typeID:u32 LE][request_id:u64 LE][body_len:u32 LE]
[body]. Same shape both directions. Used by Plan 2's typed-op
registration (RegisterOp[Req, Res]) and the gateway-side typed-op
dispatcher."
```

---

### Task 1.2: Add `RouteKind` enum + `OperationError` framework type

**Files:**
- Create: `pkg/mmokit/handle_op.go`
- Test: `pkg/mmokit/handle_op_test.go`

- [ ] **Step 1: Write the failing test**

`pkg/mmokit/handle_op_test.go`:

```go
package mmokit_test

import (
	"reflect"
	"testing"

	"github.com/zenion/mmokit/pkg/mmokit"
)

func TestRouteKind_StringNames(t *testing.T) {
	if mmokit.RouteGatewayLocal.String() != "gateway-local" {
		t.Fatalf("RouteGatewayLocal: got %q", mmokit.RouteGatewayLocal.String())
	}
	if mmokit.RoutePlayerCell.String() != "player-cell" {
		t.Fatalf("RoutePlayerCell: got %q", mmokit.RoutePlayerCell.String())
	}
}

func TestOperationError_TypeIDStable(t *testing.T) {
	id := mmokit.TypeIDOf(reflect.TypeFor[mmokit.OperationError]())
	if id == 0 {
		t.Fatalf("OperationError typeID is zero")
	}
	// The typeID is implicitly registered via RegisterOpInit (called from package init).
	gotType, ok := mmokit.LookupServerEventType(id)
	if !ok {
		t.Fatalf("OperationError not registered as a server event")
	}
	if gotType != reflect.TypeFor[mmokit.OperationError]() {
		t.Fatalf("OperationError type mismatch")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/mmokit/ -run "TestRouteKind|TestOperationError" -v`
Expected: FAIL — `RouteKind`, `OperationError` undefined.

- [ ] **Step 3: Write minimal implementation**

`pkg/mmokit/handle_op.go`:

```go
package mmokit

import (
	"reflect"
	"sync"
)

// RouteKind identifies where an operation handler runs. Mirrors the
// cmdsys.Command.RouteKind taxonomy — uniform routing across the codebase.
type RouteKind uint8

const (
	// RouteGatewayLocal — handler runs on the gateway, no cell forwarding.
	// Used by Login (no player cell yet at handshake time) and any future
	// op that doesn't need ECS access.
	RouteGatewayLocal RouteKind = iota

	// RoutePlayerCell — handler runs on the player's authoritative cell via
	// engine.RunOnLoop. Used by marketplace, bank, future GM commands.
	RoutePlayerCell
)

func (k RouteKind) String() string {
	switch k {
	case RouteGatewayLocal:
		return "gateway-local"
	case RoutePlayerCell:
		return "player-cell"
	default:
		return "unknown"
	}
}

// OperationError is the framework's generic op-response type for unknown
// typeIDs, handler errors, and deserialization failures. The client framework
// intercepts this typeID and rejects the matching pending promise by
// request_id with the carried code + message.
//
// Game-specific errors (e.g., "insufficient funds") should travel as fields
// on the op's own response type, not as OperationError. OperationError is for
// framework-level failures only.
type OperationError struct {
	Code    uint32 // framework-defined: 1 = unknown typeID, 2 = handler returned err, 3 = decode failure
	Message string
}

const (
	OpErrorUnknownTypeID    uint32 = 1
	OpErrorHandlerFailed    uint32 = 2
	OpErrorDecodeFailed     uint32 = 3
)

// registerFrameworkOps is called from package init to register the
// framework's own typed messages (currently just OperationError) so they
// show up in the schema dump and have a stable typeID at runtime.
//
// OperationError registers as a server-event so the client SDK emits a
// decoder for it; the typed-op framework on the client intercepts the
// typeID before reaching the typedEvents dispatcher.
var registerFrameworkOpsOnce sync.Once

func registerFrameworkOps() {
	registerFrameworkOpsOnce.Do(func() {
		RegisterEvent[OperationError]()
	})
}

// init wires framework-op registration. Idempotent — safe under multiple
// Process instances in tests via the sync.Once guard.
func init() {
	_ = reflect.TypeFor[OperationError]() // keep reflect import used
	registerFrameworkOps()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/mmokit/ -run "TestRouteKind|TestOperationError" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/handle_op.go pkg/mmokit/handle_op_test.go
git commit -m "feat(mmokit): add RouteKind enum + OperationError framework type

RouteKind { RouteGatewayLocal | RoutePlayerCell } declares where each
typed-op handler runs. Mirrors cmdsys.Command.RouteKind — uniform
routing taxonomy across the codebase.

OperationError is the framework's generic op-response for unknown
typeIDs / handler errors / decode failures. Client framework
intercepts the typeID and rejects the matching pending promise by
request_id."
```

---

### Task 1.3: Add `RegisterOp[Req, Res any]` typed-op registration

**Files:**
- Modify: `pkg/mmokit/handle_op.go` (append)
- Test: `pkg/mmokit/handle_op_test.go` (append)

- [ ] **Step 1: Read existing `pkg/ops/router.go`**

Skim the file; the goal is to add a parallel typed-handler registry on `mmokit.OpRouter` (which is a re-export of `*ops.Router`) without disturbing the existing proto-typed path.

- [ ] **Step 2: Write the failing test**

Append to `pkg/mmokit/handle_op_test.go`:

```go
type opTestReq struct {
	Item uint32
}

type opTestRes struct {
	Found bool
	Name  string
}

func TestRegisterOp_LookupByTypeID(t *testing.T) {
	mmokit.ResetTypedOpRegistryForTest()
	mmokit.RegisterOp[opTestReq, opTestRes](mmokit.RoutePlayerCell, func(ctx *mmokit.OpContext, req *opTestReq) (*opTestRes, error) {
		return &opTestRes{Found: true, Name: "ok"}, nil
	})

	id := mmokit.TypeIDOf(reflect.TypeFor[opTestReq]())
	got, ok := mmokit.LookupTypedOp(id)
	if !ok {
		t.Fatalf("LookupTypedOp: not found")
	}
	if got.Kind != mmokit.RoutePlayerCell {
		t.Fatalf("Kind: got %v", got.Kind)
	}
	// ResponseTypeID is computed from opTestRes
	wantResID := mmokit.TypeIDOf(reflect.TypeFor[opTestRes]())
	if got.ResponseTypeID != wantResID {
		t.Fatalf("ResponseTypeID: got %#x, want %#x", got.ResponseTypeID, wantResID)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./pkg/mmokit/ -run TestRegisterOp_LookupByTypeID -v`
Expected: FAIL — `RegisterOp`, `LookupTypedOp`, `ResetTypedOpRegistryForTest` undefined.

- [ ] **Step 4: Append registration verb to `pkg/mmokit/handle_op.go`**

Append to `pkg/mmokit/handle_op.go`:

```go
// TypedOpEntry holds the dispatch metadata for one registered typed-op.
type TypedOpEntry struct {
	Kind           RouteKind
	RequestType    reflect.Type
	ResponseType   reflect.Type
	ResponseTypeID uint32
	// Handler is stored as an opaque func; the typed-op dispatcher uses
	// reflection to invoke it with the decoded *Req and to marshal the
	// returned *Res. Storing as `any` avoids an interface-method dance.
	Handler any
}

var (
	typedOpMu  sync.RWMutex
	typedOps   = map[uint32]*TypedOpEntry{} // keyed by Request typeID
	typedOpSet = map[reflect.Type]struct{}{}
)

// RegisterOp registers a typed operation handler. typeID is computed from
// the Request type (FNV-1a hash of fully-qualified Go type name); the
// matching response is identified by the Response type's typeID.
//
// The handler runs at the location declared by `kind`:
//   - RouteGatewayLocal: on the gateway, no cell. Used for Login etc.
//   - RoutePlayerCell:   on the player's authoritative cell via RunOnLoop.
//
// Panics on duplicate Request type registration or typeID collision.
func RegisterOp[Req any, Res any](kind RouteKind, handler func(*OpContext, *Req) (*Res, error)) {
	reqType := reflect.TypeFor[Req]()
	resType := reflect.TypeFor[Res]()
	reqID := TypeIDOf(reqType)
	resID := TypeIDOf(resType)

	typedOpMu.Lock()
	defer typedOpMu.Unlock()
	if _, exists := typedOpSet[reqType]; exists {
		panic(fmt.Sprintf("RegisterOp: request type %s already registered", reqType.String()))
	}
	if existing, ok := typedOps[reqID]; ok && existing.RequestType != reqType {
		panic(fmt.Sprintf("RegisterOp: typeID collision between %s and %s (id=%#x)",
			existing.RequestType.String(), reqType.String(), reqID))
	}
	typedOps[reqID] = &TypedOpEntry{
		Kind:           kind,
		RequestType:    reqType,
		ResponseType:   resType,
		ResponseTypeID: resID,
		Handler:        handler,
	}
	typedOpSet[reqType] = struct{}{}
}

// LookupTypedOp returns the entry registered for the given Request typeID,
// or (nil, false) if none.
func LookupTypedOp(reqTypeID uint32) (*TypedOpEntry, bool) {
	typedOpMu.RLock()
	defer typedOpMu.RUnlock()
	e, ok := typedOps[reqTypeID]
	return e, ok
}

// RegisteredTypedOps returns the registered entries in deterministic order
// (alphabetical by Request type name). Used by sdkgen and protocol-schema
// export.
func RegisteredTypedOps() []*TypedOpEntry {
	typedOpMu.RLock()
	defer typedOpMu.RUnlock()
	out := make([]*TypedOpEntry, 0, len(typedOps))
	for _, e := range typedOps {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RequestType.String() < out[j].RequestType.String()
	})
	return out
}

// ResetTypedOpRegistryForTest is exported for tests only.
func ResetTypedOpRegistryForTest() {
	typedOpMu.Lock()
	defer typedOpMu.Unlock()
	typedOps = map[uint32]*TypedOpEntry{}
	typedOpSet = map[reflect.Type]struct{}{}
}
```

Add the imports `"fmt"` and `"sort"` if not already present.

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./pkg/mmokit/ -run TestRegisterOp_LookupByTypeID -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/mmokit/handle_op.go pkg/mmokit/handle_op_test.go
git commit -m "feat(mmokit): add RegisterOp[Req, Res] typed-op registration

Generic typed-op registration verb. typeID is computed from the
Request type; the matching response typeID is stored alongside.
Each entry declares a RouteKind for dispatch routing.

Coexists with the legacy ops.Register proto-constrained path during
the migration. Phase 5 retires the legacy path."
```

---

### Task 1.4: Add server-side typed-op dispatcher

**Files:**
- Create: `pkg/universe/op_dispatch.go`
- Test: `pkg/universe/op_dispatch_test.go`

- [ ] **Step 1: Write the failing test**

`pkg/universe/op_dispatch_test.go`:

```go
package universe_test

import (
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/zenion/mmokit/pkg/mmokit"
	pkguniverse "github.com/zenion/mmokit/pkg/universe"
)

type dispOpReq struct{ X uint32 }
type dispOpRes struct{ Y uint32 }

func TestDispatchTypedOp_GatewayLocal_HappyPath(t *testing.T) {
	mmokit.ResetTypedOpRegistryForTest()
	t.Cleanup(mmokit.ResetTypedOpRegistryForTest)

	mmokit.RegisterOp[dispOpReq, dispOpRes](mmokit.RouteGatewayLocal,
		func(ctx *mmokit.OpContext, req *dispOpReq) (*dispOpRes, error) {
			return &dispOpRes{Y: req.X * 2}, nil
		})

	body := pkguniverse.ReflectMarshal(&dispOpReq{X: 21})
	reqTypeID := mmokit.TypeIDOf(reflect.TypeFor[dispOpReq]())
	resTypeID := mmokit.TypeIDOf(reflect.TypeFor[dispOpRes]())

	// Build the inbound payload (channel byte stripped — read pump strips it).
	payload := make([]byte, 4+8+4+len(body))
	binary.LittleEndian.PutUint32(payload[0:4], reqTypeID)
	binary.LittleEndian.PutUint64(payload[4:12], 0xABC) // request_id
	binary.LittleEndian.PutUint32(payload[12:16], uint32(len(body)))
	copy(payload[16:], body)

	ctx := &mmokit.OpContext{ConnID: 1, Username: "alice"}
	respFrame := pkguniverse.DispatchTypedOpInbound(payload, ctx)
	if respFrame == nil {
		t.Fatalf("DispatchTypedOpInbound returned nil for happy path")
	}

	// respFrame layout: [0x01][typeID:u32 LE][request_id:u64 LE][body_len][body]
	if respFrame[0] != 0x01 {
		t.Fatalf("response channel byte: %#x", respFrame[0])
	}
	if binary.LittleEndian.Uint32(respFrame[1:5]) != resTypeID {
		t.Fatalf("response typeID")
	}
	if binary.LittleEndian.Uint64(respFrame[5:13]) != 0xABC {
		t.Fatalf("response request_id (must echo request)")
	}
	bodyLen := binary.LittleEndian.Uint32(respFrame[13:17])
	resBody := respFrame[17 : 17+bodyLen]

	res := &dispOpRes{}
	pkguniverse.ReflectUnmarshal(resBody, res)
	if res.Y != 42 {
		t.Fatalf("res.Y: got %d, want 42", res.Y)
	}
}

func TestDispatchTypedOp_UnknownTypeID_ReturnsOperationError(t *testing.T) {
	mmokit.ResetTypedOpRegistryForTest()
	t.Cleanup(mmokit.ResetTypedOpRegistryForTest)

	body := []byte{0x00, 0x01}
	payload := make([]byte, 4+8+4+len(body))
	binary.LittleEndian.PutUint32(payload[0:4], 0xDEADBEEF) // unregistered
	binary.LittleEndian.PutUint64(payload[4:12], 99)
	binary.LittleEndian.PutUint32(payload[12:16], uint32(len(body)))
	copy(payload[16:], body)

	ctx := &mmokit.OpContext{}
	respFrame := pkguniverse.DispatchTypedOpInbound(payload, ctx)
	if respFrame == nil {
		t.Fatalf("expected OperationError frame, got nil")
	}

	// Decode response: typeID must be OperationError's typeID.
	gotResTypeID := binary.LittleEndian.Uint32(respFrame[1:5])
	wantResTypeID := mmokit.TypeIDOf(reflect.TypeFor[mmokit.OperationError]())
	if gotResTypeID != wantResTypeID {
		t.Fatalf("response typeID: got %#x, want OperationError %#x", gotResTypeID, wantResTypeID)
	}
	if binary.LittleEndian.Uint64(respFrame[5:13]) != 99 {
		t.Fatalf("response request_id")
	}
	bodyLen := binary.LittleEndian.Uint32(respFrame[13:17])
	resBody := respFrame[17 : 17+bodyLen]
	opErr := &mmokit.OperationError{}
	pkguniverse.ReflectUnmarshal(resBody, opErr)
	if opErr.Code != mmokit.OpErrorUnknownTypeID {
		t.Fatalf("OperationError.Code: got %d, want OpErrorUnknownTypeID", opErr.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/universe/ -run TestDispatchTypedOp -v`
Expected: FAIL — `DispatchTypedOpInbound undefined`.

- [ ] **Step 3: Write the dispatcher**

`pkg/universe/op_dispatch.go`:

```go
package universe

import (
	"fmt"
	"reflect"

	"github.com/zenion/mmokit/pkg/mmokit"
)

// DispatchTypedOpInbound consumes a 0x01 typed-op payload (channel byte
// already stripped), decodes the request body, looks up the registered
// handler by typeID, invokes it, marshals the response, and returns the
// outbound 0x01 typed-op frame.
//
// Always returns a frame on success or framework-level failure (unknown
// typeID, decode error, handler error). The framework-level failures
// produce an OperationError-typed response — never nil — so the caller's
// invariant is "send the returned frame on the connection" without a nil
// check.
//
// Returns nil ONLY for structurally invalid frames (truncated header /
// body) — those are dropped silently because the connection is malformed
// and there's no request_id to correlate a response to.
//
// For RouteGatewayLocal ops, the handler runs synchronously here.
// For RoutePlayerCell ops, the dispatcher schedules the handler via
// engine.RunOnLoop on the player's authoritative cell — see the
// gateway integration in pkg/universe/gateway.go.
//
// Plan 2 Phase 1: this entry point handles RouteGatewayLocal only.
// Phase 2 wires the cell-routing path for RoutePlayerCell ops.
func DispatchTypedOpInbound(payload []byte, ctx *mmokit.OpContext) []byte {
	typeID, requestID, body, err := DecodeTypedOpFrame(payload)
	if err != nil {
		// Truncated frame — no request_id to correlate. Drop silently.
		return nil
	}

	entry, ok := mmokit.LookupTypedOp(typeID)
	if !ok {
		return encodeOpError(requestID, mmokit.OpErrorUnknownTypeID,
			fmt.Sprintf("unknown typed-op typeID %#x", typeID))
	}

	if entry.Kind != mmokit.RouteGatewayLocal {
		// Phase 1 dispatcher only handles gateway-local. Cell-routed ops
		// are dispatched via the gateway.go integration in Phase 2.
		return encodeOpError(requestID, mmokit.OpErrorHandlerFailed,
			fmt.Sprintf("op %s requires cell routing; not supported by gateway-local dispatcher", entry.RequestType.String()))
	}

	// Decode the request body into a fresh *Req.
	reqPtr := reflect.New(entry.RequestType)
	ReflectUnmarshal(body, reqPtr.Interface())

	// Invoke the handler via reflect: handler is `func(*OpContext, *Req) (*Res, error)`.
	handlerVal := reflect.ValueOf(entry.Handler)
	results := handlerVal.Call([]reflect.Value{reflect.ValueOf(ctx), reqPtr})

	// Returns: (*Res, error). Either could be nil per Go zero-value semantics.
	resPtr := results[0]
	errVal := results[1]
	if !errVal.IsNil() {
		return encodeOpError(requestID, mmokit.OpErrorHandlerFailed,
			errVal.Interface().(error).Error())
	}
	if resPtr.IsNil() {
		// Handler returned (nil, nil) — encode an empty response.
		return EncodeTypedOpFrame(entry.ResponseTypeID, requestID, nil)
	}
	resBody := ReflectMarshal(resPtr.Interface())
	return EncodeTypedOpFrame(entry.ResponseTypeID, requestID, resBody)
}

// encodeOpError builds an OperationError response frame for a request_id.
func encodeOpError(requestID uint64, code uint32, message string) []byte {
	resTypeID := mmokit.TypeIDOf(reflect.TypeFor[mmokit.OperationError]())
	body := ReflectMarshal(&mmokit.OperationError{Code: code, Message: message})
	return EncodeTypedOpFrame(resTypeID, requestID, body)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/universe/ -run TestDispatchTypedOp -v`
Expected: PASS (2 tests).

- [ ] **Step 5: Run wider sanity check**

```bash
go vet ./...
```

Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add pkg/universe/op_dispatch.go pkg/universe/op_dispatch_test.go
git commit -m "feat(universe): add typed-op inbound dispatcher

DispatchTypedOpInbound consumes a 0x01 payload, decodes the typed-op
header, looks up the handler by typeID, invokes it via reflection, and
returns the encoded response frame. Framework-level failures (unknown
typeID, handler error, decode failure) produce an OperationError
response — never nil — so the caller's invariant is 'send the returned
frame'.

Phase 1 handles RouteGatewayLocal only. Phase 2 wires the cell-routing
path for RoutePlayerCell ops via engine.RunOnLoop on the player's cell."
```

---

### Task 1.5: Wire typed-op disambiguation into the gateway

**Files:**
- Modify: `pkg/universe/gateway.go` (the inbound 0x01 handler)
- Test: regression — existing marketplace ops still work via the legacy proto path

- [ ] **Step 1: Find the inbound 0x01 handler in the gateway**

```bash
grep -n "ChannelOperation\|DrainOpInput\|opInput" pkg/universe/gateway.go
```

The gateway's `forwardChannel` (or per-session pump) drains 0x01 frames and forwards them to the host via `MeshFrame`. The host-side processing of 0x01 inbound goes through `pkg/ops/router.go::Router.run` (or similar) which decodes the proto `OpRequest`.

For Phase 1 wiring, the disambiguation belongs at the **host-side** point where 0x01 frames are dispatched. Find that point: search `pkg/universe/` for where `OpRequest` is unmarshalled.

- [ ] **Step 2: Add disambiguation peek**

The disambiguation site is wherever `pkg/ops/router.go::Router.run` receives a 0x01 frame body. Pseudocode:

```go
// At the point where the existing code calls RequestParser to decode the
// proto OpRequest, peek the first byte:
if len(payload) > 0 && payload[0] == 0x08 {
    // Legacy path: decode OpRequest proto, dispatch by op_code (existing flow).
    ...
} else {
    // Typed-op path: decode typeID, dispatch by typeID.
    ctx := &mmokit.OpContext{
        ConnID:   sess.ConnID,
        Username: sess.Username,
        ClientIP: sess.ClientIP,
    }
    if respFrame := DispatchTypedOpInbound(payload, ctx); respFrame != nil {
        sess.Conn.SendReliable(respFrame)
    }
}
```

The exact location: locate `pkg/ops/router.go::Router.run` (the worker loop that processes drained ops queue). Insert the disambiguation just before the `RequestParser` call.

- [ ] **Step 3: Add a regression test**

`pkg/ops/router_test.go` already has tests for the legacy proto path. Verify they still pass after the disambiguation:

```bash
go test ./pkg/ops/ -v
```

Expected: existing tests PASS (the legacy path is unchanged).

- [ ] **Step 4: Add a typed-op smoke test**

Add to `pkg/ops/router_test.go`:

```go
func TestRouter_TypedOpPath_GatewayLocal(t *testing.T) {
	// Register a typed gateway-local op that doubles its input.
	mmokit.ResetTypedOpRegistryForTest()
	t.Cleanup(mmokit.ResetTypedOpRegistryForTest)

	type smokeReq struct{ X uint32 }
	type smokeRes struct{ Y uint32 }
	mmokit.RegisterOp[smokeReq, smokeRes](mmokit.RouteGatewayLocal,
		func(ctx *mmokit.OpContext, req *smokeReq) (*smokeRes, error) {
			return &smokeRes{Y: req.X * 2}, nil
		})

	// Build a typed-op frame and feed it through Router.processFrame
	// (or whatever the disambiguation entry point is named after step 2).
	// Assert the response frame matches the typed-op response shape.
	// Adapt the test to the actual function signature exposed.
}
```

- [ ] **Step 5: Verify**

```bash
go vet ./... && go test ./pkg/ops/ ./pkg/universe/ -v 2>&1 | grep -E "FAIL|^ok" | tail -10
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/ops/router.go pkg/ops/router_test.go pkg/universe/gateway.go
git commit -m "feat(ops): disambiguate typed-op vs legacy proto-op on 0x01

The 0x01 inbound dispatch peeks the first byte: 0x08 (protobuf field-1
varint tag for OpRequest.code) → legacy path; otherwise → typed-op
dispatcher. Same disambiguation pattern Plan 1 used for 0x00.

Both paths coexist during the migration. Phase 5 retires the legacy
path."
```

---

### Task 1.6: Phase 1 verification

- [ ] **Step 1: Full test suite**

```bash
go test ./... 2>&1 | grep -E "FAIL|^ok" | tail -25
```

Expected: 21 packages PASS.

- [ ] **Step 2: Both TS typechecks**

```bash
(cd web-pixi && bunx tsc --noEmit) && (cd examples/4node-basic/web && bunx tsc --noEmit)
```

Expected: both clean.

- [ ] **Step 3: Phase 1 commit summary**

```bash
git log --oneline main..HEAD
```

Expected: ~5 commits for Phase 1 tasks.

---

## Phase 2 — Codegen for `operations.ts`

### Task 2.1: Add `Operations` schema section

**Files:**
- Modify: `pkg/mmokit/protocol.go`
- Modify: `cmd/sdkgen/schema.go`

- [ ] **Step 1: Add typed-op schema struct**

In `pkg/mmokit/protocol.go`:

```go
// TypedOperationSchema describes one typed Request/Response operation pair
// for codegen. Parallel to the proto-typed OperationSchema, but identifies
// types by typeID + Go type name + field structure (reflection-codec) rather
// than proto name + op_code.
type TypedOperationSchema struct {
	Kind            string                  `json:"kind"`              // "gateway-local" or "player-cell"
	RequestTypeID   uint32                  `json:"request_type_id"`
	RequestTypeName string                  `json:"request_type_name"`
	RequestFields   []BroadcastFieldSchema  `json:"request_fields"`
	ResponseTypeID  uint32                  `json:"response_type_id"`
	ResponseTypeName string                 `json:"response_type_name"`
	ResponseFields  []BroadcastFieldSchema  `json:"response_fields"`
}

// (Add to ProtocolSchema:)
//   TypedOperations []TypedOperationSchema `json:"typed_operations,omitempty"`
```

Populate in `Protocol.Schema()` (next to where `BroadcastTypes` and `ServerEventTypes` are populated):

```go
for _, e := range RegisteredTypedOps() {
	reqType, _ := BroadcastTypeOf(e.RequestType)
	resType, _ := BroadcastTypeOf(e.ResponseType)
	s.TypedOperations = append(s.TypedOperations, TypedOperationSchema{
		Kind:             e.Kind.String(),
		RequestTypeID:    TypeIDOf(e.RequestType),
		RequestTypeName:  e.RequestType.String(),
		RequestFields:    reqType.Fields,
		ResponseTypeID:   e.ResponseTypeID,
		ResponseTypeName: e.ResponseType.String(),
		ResponseFields:   resType.Fields,
	})
}
```

- [ ] **Step 2: Mirror struct in `cmd/sdkgen/schema.go`**

Add a parallel struct definition + slice field on `ProtocolSchema`. Reuse `BroadcastFieldSchema` (already used for events).

- [ ] **Step 3: Verify schema dump round-trips**

```bash
go vet ./...
go test ./pkg/mmokit/ -run TestProtocol -v
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/mmokit/protocol.go cmd/sdkgen/schema.go
git commit -m "feat(mmokit,sdkgen): add typed-operations schema section

ProtocolSchema gains TypedOperations []TypedOperationSchema (omitempty)
populated from RegisteredTypedOps(). Sdkgen's mirror struct gets the
same shape so the JSON schema dump round-trips through the codegen
pipeline.

Empty until Phase 3 migrates marketplace + bank ops; Phase 5 retires
the legacy Operations (proto-typed) section."
```

---

### Task 2.2: Emit `operations.ts` from the typed-op registry

**Files:**
- Create: `cmd/sdkgen/operations.go`
- Modify: `cmd/sdkgen/main.go` (gate emission on non-empty typed-op registry)
- Modify: `cmd/sdkgen/generate.go` (wire `client.<opName>` Promise wrapper into the generated client.ts)

- [ ] **Step 1: Create `operations.go`**

`cmd/sdkgen/operations.go`:

```go
package main

import (
	"fmt"
	"strings"
)

// genOperations emits operations.ts containing one Request class + one
// Response class per registered typed-op, plus framework wiring helpers
// (the Promise correlator). The wire layout is the typed-op codec:
//
//	[0x01][typeID:u32 LE][request_id:u64 LE][body_len:u32 LE][body]
//
// Body is reflect-codec marshalled (same shape as broadcasts.ts uses).
//
// For each registered op, the generated client.ts gains a method:
//
//	async <opName>(req: <ReqClass>): Promise<<ResClass>>
//
// which allocates a request_id, encodes the request, posts to channel
// 0x01, and awaits a response keyed by request_id. Framework-level errors
// (OperationError typeID) cause the promise to reject.
func (g *Generator) genOperations() string {
	if len(g.schema.TypedOperations) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("// GENERATED by sdkgen — do not edit.\n\n")
	b.WriteString("// Typed operation request + response classes. Wire format:\n")
	b.WriteString("//   [0x01][typeID:u32 LE][request_id:u64 LE][body_len:u32 LE][body]\n")
	b.WriteString("// Bodies use the reflect-codec layout from pkg/universe/reflect_marshal.go.\n\n")

	// Per-op Request + Response classes. Reuses writeBroadcastClass for the
	// reflect-codec encode/decode body shape — the wire layout of a typed-op
	// body is identical to a typed-event body.
	for _, op := range g.schema.TypedOperations {
		reqBT := BroadcastTypeSchema{
			Name:   op.RequestTypeName,
			TypeID: op.RequestTypeID,
			Fields: op.RequestFields,
		}
		writeBroadcastClass(&b, reqBT)
		// Request also needs an encode() instance method (broadcasts only have decode).
		writeRequestEncode(&b, reqBT)

		resBT := BroadcastTypeSchema{
			Name:   op.ResponseTypeName,
			TypeID: op.ResponseTypeID,
			Fields: op.ResponseFields,
		}
		writeBroadcastClass(&b, resBT)
	}

	return b.String()
}

// writeRequestEncode emits an instance encode() method for an op Request
// class. Returns Uint8Array of the reflect-codec body bytes.
//
// Reuses the existing writeInputEncode helper from cmd/sdkgen/inputs.go,
// which already emits the reflect-codec encode for typed-input classes.
// Typed-op Request bodies use the identical wire layout, so this is a
// pure call-through with no new emission logic.
func writeRequestEncode(b *strings.Builder, bt BroadcastTypeSchema) {
	// inputs.go's writeInputEncode takes the same field schema and emits
	// the matching encode() instance method — extract it from inputs.go
	// (rename to writeReflectCodecEncode and export package-private if
	// it's currently inlined there) so both inputs.ts and operations.ts
	// share one source of truth for encode emission.
	writeReflectCodecEncode(b, bt)
}
```

**Implementation note on `writeReflectCodecEncode`:** `cmd/sdkgen/inputs.go` already contains the per-field encode logic (a switch over `f.Encoding` writing `dv.setUint32(off, val, true); off += 4` for u32, `setBigUint64` for u64, `setFloat32` for f32, length-prefixed UTF-8 bytes for string, length-prefixed bytes for `bytes`, recursive struct/slice encode for nested fields). Before implementing Task 2.2, do a small refactoring pass: extract that switch from `inputs.go`'s `writeInputEncode` into a shared package-level function `writeReflectCodecEncode(b *strings.Builder, bt BroadcastTypeSchema)`. Both `inputs.go::writeInputEncode` (which wraps with input-specific framing) and `operations.go::writeRequestEncode` call it. **Single source of truth for the encode emission — Plan 1's bytes fast-path lives in one place too, no risk of drift.**

- [ ] **Step 2: Wire into the generator**

In `cmd/sdkgen/main.go`, add operations.ts emission alongside the existing broadcasts.ts / inputs.ts:

```go
if ops := g.genOperations(); ops != "" {
    if err := writeFile(filepath.Join(*outDir, "operations.ts"), ops); err != nil {
        log.Fatalf("write operations.ts: %v", err)
    }
}
```

- [ ] **Step 3: Add the framework Promise correlator + per-op `client.<opName>` methods**

In `cmd/sdkgen/generate.go`, when `len(schema.TypedOperations) > 0`:

1. Emit a private `pendingOps: Map<bigint, { resolve, reject }>` field on the generated `<Game>Client` class plus a `nextRequestID: bigint` counter.
2. In the constructor, register a 0x01 onOperation handler that disambiguates: if the first byte is `0x08` use the legacy path; otherwise decode as typed-op (typeID + request_id + body), look up the pending entry by request_id, and resolve/reject.
3. For each typed-op, emit:

```typescript
async marketBrowse(req: MarketBrowseRequest): Promise<MarketOrderBookResponse> {
  return this.callOp<MarketBrowseRequest, MarketOrderBookResponse>(req, MarketOrderBookResponse);
}
```

where `callOp<Req, Res>` is a private method that:
- allocates the next request_id
- registers a pending entry
- encodes `[0x01][typeID:u32 LE][request_id:u64 LE][bodyLen:u32 LE][body]`
- sends via the transport
- returns the Promise

**Verify the existing client-side op-router code** (the legacy proto path's request/response correlation lives in `web-pixi/sdk/client.ts` or similar). The new typed-op correlator can either live alongside or replace it; for Phase 2 it lives alongside — Phase 5 retires the legacy.

**Concrete `callOp` implementation** (emit into the generated `<Game>Client` class):

```typescript
private nextOpRequestID: bigint = 1n;
private pendingOps: Map<bigint, { resolve: (msg: any) => void; reject: (err: Error) => void; resCls: { decode(buf: Uint8Array): any } }> = new Map();

private callOp<Req extends { encode(): Uint8Array; constructor: { typeID: number } }, Res>(
  req: Req,
  resCls: { decode(buf: Uint8Array): Res }
): Promise<Res> {
  const reqID = this.nextOpRequestID++;
  const reqTypeID = (req.constructor as { typeID: number }).typeID;
  const body = req.encode();

  // Frame: [0x01][typeID:u32 LE][request_id:u64 LE][bodyLen:u32 LE][body]
  const frame = new Uint8Array(1 + 4 + 8 + 4 + body.length);
  const dv = new DataView(frame.buffer);
  frame[0] = 0x01; // CH_OPERATION
  dv.setUint32(1, reqTypeID, true);
  dv.setBigUint64(5, reqID, true);
  dv.setUint32(13, body.length, true);
  frame.set(body, 17);

  return new Promise<Res>((resolve, reject) => {
    this.pendingOps.set(reqID, { resolve, reject, resCls });
    this.transport.sendRaw(frame);
  });
}
```

**Concrete inbound 0x01 dispatch** (in the constructor's `transport.onOperation` registration):

```typescript
this.transport.onOperation((payload) => {
  // Disambiguate: 0x08 = legacy proto OpResponse field-1 tag, else typed.
  if (payload.length > 0 && payload[0] === 0x08) {
    // Legacy proto-op path — keep existing op-router decode here.
    this.handleLegacyOpResponse(payload);
    return;
  }
  // Typed-op response: [typeID:u32 LE][request_id:u64 LE][bodyLen:u32 LE][body]
  if (payload.length < 16) return;
  const dv = new DataView(payload.buffer, payload.byteOffset, payload.byteLength);
  const resTypeID = dv.getUint32(0, true);
  const reqID = dv.getBigUint64(4, true);
  const bodyLen = dv.getUint32(12, true);
  const body = payload.subarray(16, 16 + bodyLen);

  const pending = this.pendingOps.get(reqID);
  if (!pending) {
    console.warn(`typed-op response for unknown request_id ${reqID}`);
    return;
  }
  this.pendingOps.delete(reqID);

  // OperationError typeID intercept: reject the promise.
  if (resTypeID === OperationError.typeID) {
    const err = OperationError.decode(body);
    pending.reject(new Error(`OperationError code=${err.code}: ${err.message}`));
    return;
  }

  pending.resolve(pending.resCls.decode(body));
});
```

The `OperationError` class is available because the `RegisterEvent[OperationError]()` call in `pkg/mmokit/handle_op.go::registerFrameworkOps` makes it appear in `schema.ServerEventTypes` → codegen emits the class into `broadcasts.ts`. `operations.ts` imports `OperationError` from `broadcasts.ts`.

- [ ] **Step 4: Verify codegen produces zero diff before any op is migrated**

```bash
just client-sdk examples/4node-basic
just space-sdk
git diff --stat web-pixi/sdk/ examples/4node-basic/web/sdk/
```

Expected: zero diff. No game has called `mmokit.RegisterOp[Req, Res]()` yet, so the typed-ops section is empty and the generator emits nothing.

- [ ] **Step 5: Verify TS typechecks**

```bash
(cd web-pixi && bunx tsc --noEmit)
(cd examples/4node-basic/web && bunx tsc --noEmit)
```

Expected: both clean.

- [ ] **Step 6: Commit**

```bash
git add cmd/sdkgen/operations.go cmd/sdkgen/generate.go cmd/sdkgen/main.go
git commit -m "feat(sdkgen): emit operations.ts from typed-op registry

Generator gains genOperations() that emits per-op Request + Response
classes (reusing the broadcasts.ts class shape) plus the client.<opName>
Promise wrapper. Zero output today since Phase 3 hasn't migrated any
ops yet; Phase 3 ops drop into the codegen pipeline cleanly.

The Promise correlator on the generated client uses request_id as the
key. OperationError responses reject the matching pending promise."
```

---

## Phase 3 — Marketplace + Bank Op Migrations

This phase is the bulk of the work. Six ops migrate from the proto-typed `pkg/ops.Register[Req, Res]` path to the typed `mmokit.RegisterOp[Req, Res]` path. The pattern is identical for each.

### The migration pattern (apply 6 times)

For each op:

1. **Define typed Go structs** for Request and Response in `pkg/mmokit/op_messages.go` (engine-level) or game-side equivalent. Mirror the proto field set:

   ```go
   // MarketBrowseRequest replaces gamepb.MarketBrowseRequest. Same field set,
   // typed Go struct (no proto getters), encoded via the reflect codec.
   type MarketBrowseRequest struct {
       ItemID uint32
   }

   type MarketOrderBookResponse struct {
       ItemID     uint32
       SellLevels []MarketPriceLevel
       BuyLevels  []MarketPriceLevel
   }

   type MarketPriceLevel struct {
       Price      float64
       Quantity   float32
       OrderCount uint32
   }
   ```

   Slice support already exists from Plan 1 (commit `9a24dcd`). Nested structs work too.

2. **Update the handler in `internal/marketplace/handler.go`** to use typed structs:

   ```go
   // Before (proto):
   mmokit.RegisterOp(
       router, uint32(gamepb.OperationCode_OP_MARKET_BROWSE), "marketBrowse",
       func(ctx *mmokit.OpContext, req *gamepb.MarketBrowseRequest) (*gamepb.MarketOrderBookResponse, error) { ... })

   // After (typed):
   mmokit.RegisterOp[MarketBrowseRequest, MarketOrderBookResponse](
       mmokit.RoutePlayerCell,
       func(ctx *mmokit.OpContext, req *MarketBrowseRequest) (*MarketBrowseResponse, error) { ... })
   ```

   The handler body needs only superficial changes: field-name case (proto camelCase → Go CamelCase) and dropping the `gamepb.` prefix from struct constructors.

3. **Wire cell routing** for `RoutePlayerCell` ops. The Phase 1 dispatcher only handles `RouteGatewayLocal`. For cell-routed ops, the gateway needs to forward the inbound 0x01 frame to the player's authoritative cell, run the handler via `engine.RunOnLoop`, and ship the response back. **This work belongs in Task 3.0 below** (foundation for the marketplace ops).

4. **Update web-pixi consumer** (`web-pixi/src/ui/market.ts`, `web-pixi/src/ui/bank.ts`) to call the new typed wrappers:

   ```typescript
   // Before:
   const response = await client.opRouter.send(OperationCode.OP_MARKET_BROWSE, request);
   // After:
   const response = await client.marketBrowse(request);
   ```

5. **Regenerate SDKs** + smoke-test the typecheck.

6. **Commit** per op (or per closely-related pair) for clean history.

### Task 3.0: Wire `RoutePlayerCell` cell-routing into the dispatcher

This is a foundation task before the marketplace ops migration. The Phase 1 dispatcher in `pkg/universe/op_dispatch.go` only handles `RouteGatewayLocal`. Marketplace + bank ops need cell routing.

**Files:**
- Modify: `pkg/universe/op_dispatch.go`
- Modify: `pkg/universe/gateway.go` (or wherever the inbound 0x01 frames meet `Stage`/`Engine`)
- Test: cell-routing roundtrip

- [ ] **Step 1: Investigate the existing cell-routing pattern**

The legacy proto-op `pkg/ops/router.go` already routes ops to the player's cell — find how. It likely uses `engine.RunOnLoop` after looking up the player's session. Mirror that mechanism for typed ops.

- [ ] **Step 2: Extend `DispatchTypedOpInbound` to handle `RoutePlayerCell`**

The Phase 1 dispatcher returns `OpErrorHandlerFailed` for `RoutePlayerCell` entries. Replace with the cell-routing path:

```go
if entry.Kind == mmokit.RoutePlayerCell {
    // Find the player's cell + engine; schedule via RunOnLoop.
    // The session lookup mechanism mirrors pkg/ops/router.go's existing path.
    // The handler runs on the cell goroutine; the response is built and sent
    // back asynchronously. The framework correlator on the client side waits
    // for the response by request_id.
    //
    // Critical: this function may need to return a response synchronously OR
    // schedule the send asynchronously. Likely the right shape is: return
    // nil here, and the cell-routed path sends its own response when it
    // completes.
    scheduleCellRoutedOp(entry, requestID, body, ctx)
    return nil // response will be sent asynchronously
}
```

`scheduleCellRoutedOp` is a new helper that:
1. Looks up the player's cell (via `Coordinator.activeUsers` or similar — see existing op-router code)
2. Schedules the handler via `engine.RunOnLoop`
3. Inside the closure: decode request, invoke handler, marshal response, send via the player's connection

- [ ] **Step 3: Add a regression test**

Mirror the existing `pkg/ops/router_test.go` cell-routing test for the typed path.

- [ ] **Step 4: Verify**

```bash
go test ./pkg/universe/ ./pkg/ops/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/op_dispatch.go pkg/universe/gateway.go
git commit -m "feat(universe): cell-routing for RoutePlayerCell typed-ops

Extends DispatchTypedOpInbound: RoutePlayerCell entries schedule the
handler via engine.RunOnLoop on the player's authoritative cell.
Response is sent asynchronously when the handler completes; the
client framework correlates by request_id.

Mirrors the legacy proto-op cell-routing path in pkg/ops/router.go."
```

### Task 3.1: Migrate `MarketBrowseRequest` → typed

**Files:**
- Modify: `pkg/mmokit/op_messages.go` (create on first migration)
- Modify: `internal/marketplace/handler.go`
- Modify: `web-pixi/src/ui/market.ts`

Apply the migration pattern. Single commit when complete; verify both `(cd web-pixi && bunx tsc --noEmit)` and `go test ./internal/marketplace/ ./pkg/mmokit/` pass.

Commit message:

```
feat(marketplace): migrate MarketBrowseRequest → typed op

Replaces protobuf OpRequest/OpResponse path with typed reflection-codec
on 0x01. Handler signature unchanged; field set unchanged.

gamepb.MarketBrowseRequest + OP_MARKET_BROWSE enum entry stay until
Phase 5 cleanup.
```

### Task 3.2-3.5: Apply pattern to remaining marketplace ops

Same pattern as 3.1 for each:

- 3.2: `MarketCreateOrderRequest` → `MarketOrderResultResponse` (handles both buy and sell)
- 3.3: `MarketCancelOrderRequest` → `MarketOrderResultResponse`
- 3.4: `MarketMyOrdersRequest` → `MarketMyOrdersResponse` (note: response contains `[]MarketOrderEntry`)
- 3.5: `MarketInstantTradeRequest` → `MarketOrderResultResponse`

Each commit follows the pattern in 3.1.

After all five marketplace ops land, the regenerated SDKs should have `client.marketBrowse`, `client.marketCreateOrder`, `client.marketCancelOrder`, `client.marketMyOrders`, `client.marketInstantTrade` methods returning typed Promises.

### Task 3.6: Migrate `BankRequest` → typed op (with consumer updates)

`BankRequest` is currently a typed input on 0x00 (registered via `mmokit.HandleClient[BankRequest]`). The Phase 6 work in Plan 1 left it on the events channel because at that point ops weren't yet typed. Plan 2 migrates it to a `RoutePlayerCell` op.

**Files:**
- Modify: `pkg/mmokit/op_messages.go` — define `BankRequest`/`BankResponse` typed structs (or move existing `BankRequest` definition from `internal/game/input_messages.go`)
- Delete: BankRequest from `internal/game/input_messages.go`
- Delete: BankRequest HandleClient registration from `internal/game/input_handlers.go`
- Modify: `internal/game/system_economy.go:processBankRequests` — replace the per-tick queue drain with a typed-op handler invoked synchronously via `engine.RunOnLoop` (`RoutePlayerCell` semantics).
- Modify: `internal/game/system_economy.go:SendBankContents` — keep as-is; `BankContents` stays as a typed event for out-of-band pushes (admin/GM, future automated payouts).
- Modify: `web-pixi/src/ui/bank.ts` — call `client.bankRequest(req)` instead of `client.send(BankRequest, ...)`.

The `BankResponse` payload reuses the existing `mmokit.BankContents` typed event struct as its body — same Go type, both as op response and as event push. Document this in the doc comment.

- [ ] **Step 1: Define typed structs**

In `pkg/mmokit/op_messages.go`:

```go
type BankRequest struct {
	Kind     BankRequestKind // deposit/withdraw/query
	ItemID   uint32
	Quantity float32
}

type BankRequestKind uint8

const (
	BankRequestQuery BankRequestKind = iota
	BankRequestDeposit
	BankRequestWithdraw
)

// BankResponse carries the post-mutation bank state. Reuses the existing
// BankContents event-push payload as its body — same Go type, both as op
// response (req/res via this typed-op) and as event push (out-of-band state
// changes via mmokit.SendEvent).
type BankResponse struct {
	Contents BankContents // same struct used by SE_BANK_CONTENTS event push
	Error    string       // populated on validation/permission failure; success → empty
}
```

- [ ] **Step 2: Migrate handler**

The existing `processBankRequests` drains a queue per tick. The typed-op path runs the handler synchronously (inside `RunOnLoop`) and returns the response. Refactor so the handler is a pure function callable from the typed-op dispatch:

```go
func handleBankRequest(gw *GameWorld, ctx *mmokit.OpContext, req *mmokit.BankRequest) (*mmokit.BankResponse, error) {
    // existing per-kind logic from processBankRequests, but synchronous:
    pdata := gw.PlayerDB.Get(ctx.Username)
    // dispatch on req.Kind: deposit / withdraw / query
    // return &mmokit.BankResponse{Contents: ..., Error: ""}, nil
}
```

Register the typed op:

```go
mmokit.RegisterOp[mmokit.BankRequest, mmokit.BankResponse](mmokit.RoutePlayerCell,
    func(ctx *mmokit.OpContext, req *mmokit.BankRequest) (*mmokit.BankResponse, error) {
        return handleBankRequest(gw, ctx, req)
    })
```

The per-tick queue + drain in `processBankRequests` is deleted (the typed-op path replaces it).

- [ ] **Step 3: Delete BankRequest from input registry**

```bash
grep -n "BankRequest\b" internal/game/input_messages.go internal/game/input_handlers.go
```

Delete the struct (in input_messages.go) and the `HandleClient[BankRequest]` registration (in input_handlers.go).

- [ ] **Step 4: Update web-pixi consumer**

`web-pixi/src/ui/bank.ts` (or wherever bank UI lives):

```typescript
// Before:
client.send(new BankRequest(BankRequestKind.Deposit, itemID, qty));
// After:
const resp = await client.bankRequest(new BankRequest(BankRequestKind.Deposit, itemID, qty));
if (resp.error) { showError(resp.error); }
else { renderBank(resp.contents); }
```

- [ ] **Step 5: Verify**

```bash
go vet ./... && go test ./internal/game/ ./pkg/mmokit/
(cd web-pixi && bunx tsc --noEmit)
```

Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add pkg/mmokit/op_messages.go internal/game/system_economy.go internal/game/input_messages.go internal/game/input_handlers.go web-pixi/src/ui/bank.ts
git commit -m "feat(bank): migrate BankRequest from input to typed op

BankRequest moves from typed input on 0x00 (HandleClient) to a typed
RoutePlayerCell op on 0x01. Handler is now synchronous (returns
*BankResponse + error directly) — no more per-tick queue drain.

BankResponse reuses the BankContents Go struct as its payload — same
type, both as op response (req/res correlation) and as event push
(out-of-band state changes via mmokit.SendEvent). The web-pixi consumer
gets per-action confirmation via the Promise.
"
```

### Task 3.7: Phase 3 verification

- [ ] **Step 1: Full test suite**

```bash
go test ./... 2>&1 | grep -E "FAIL|^ok" | tail -25
```

- [ ] **Step 2: TS typechecks**

```bash
(cd web-pixi && bunx tsc --noEmit) && (cd examples/4node-basic/web && bunx tsc --noEmit)
```

- [ ] **Step 3: Browser smoke test**

Run `just dev`, open the marketplace, place a sell order, cancel it, browse, view my-orders, instant-buy something. Open the bank, deposit, withdraw, query.

- [ ] **Step 4: Commit summary**

```bash
git log --oneline main..HEAD | head -20
```

Expected: ~12-15 commits across Phases 1-3.

---

## Phase 4 — Login → Operation

### Task 4.1: Define `LoginRequest` / `LoginResponse` typed ops

**Files:**
- Modify: `pkg/mmokit/op_messages.go`

```go
// LoginRequest replaces enginepb.LoginMsg. Sent on the operations channel
// (0x01) as a RouteGatewayLocal op — handled before the player has any
// cell assignment.
//
// The cookie is the primary credential. Username is a fallback for the
// no-cookie flow used by some clients (e.g., the bot); the gateway ignores
// it when a valid auth cookie is present. Once cookie-auth is universal,
// the field can be removed.
type LoginRequest struct {
	Username string
}

// LoginResponse carries the result of a login attempt. On success,
// Accepted=true and the user_id/username/spawn_target fields are populated.
// On rejection, Accepted=false and Error carries the reason.
type LoginResponse struct {
	Accepted bool
	UserID   string
	Username string
	SpawnX   float32
	SpawnY   float32
	Error    string
}
```

### Task 4.2: Wire the gateway-local handler

**Files:**
- Modify: `pkg/universe/gateway.go` or `pkg/universe/coordinator.go` — the existing `Config.LoginHandler` callback site

The existing `Config.LoginHandler` signature accepts a `payload []byte` and returns `(username, sessionData, error)`. The typed-op path decodes the `LoginRequest` first and passes the typed struct to the handler.

Wrap the existing handler in a typed-op registration:

```go
// In coordinator setup:
mmokit.RegisterOp[mmokit.LoginRequest, mmokit.LoginResponse](mmokit.RouteGatewayLocal,
    func(ctx *mmokit.OpContext, req *mmokit.LoginRequest) (*mmokit.LoginResponse, error) {
        // Reuse the existing LoginHandler shape internally — it knows about
        // cookies, PlayerRouter, SessionAnnounce. Just adapt I/O.
        username, _, err := cfg.LoginHandler(...)
        if err != nil {
            return &mmokit.LoginResponse{Accepted: false, Error: err.Error()}, nil
        }
        spawn := cfg.DefaultSpawn  // or via PlayerRouter
        return &mmokit.LoginResponse{
            Accepted: true,
            UserID:   /* from auth */,
            Username: username,
            SpawnX:   spawn.X,
            SpawnY:   spawn.Y,
        }, nil
    })
```

The existing legacy `gamepb.LoginMsg` path on channel 0x00 (which has no production handler — confirmed in Plan 1's deferred-Login note) is removed.

### Task 4.3: Update bot client

**Files:**
- Modify: `internal/bot/bot.go` — replace the `enginepb.LoginMsg` send with a typed `LoginRequest` op call

The bot's connect flow at `internal/bot/bot.go:115`:

```go
// Before:
b.sendEvent(uint32(enginepb.ClientEventCode_CE_LOGIN), &enginepb.LoginMsg{Username: b.name}, true)

// After:
resp, err := b.callOp(&mmokit.LoginRequest{Username: b.name})
if err != nil {
    return fmt.Errorf("login: %w", err)
}
if !resp.(*mmokit.LoginResponse).Accepted {
    return fmt.Errorf("login rejected: %s", resp.(*mmokit.LoginResponse).Error)
}
```

The bot needs a `callOp` helper that mirrors what the web-pixi SDK does: encode typed-op frame, await response by request_id. Add this helper inline.

### Task 4.4: Retire the legacy `LoginMsg`

**Files:**
- `proto/enginepb/engine.proto` — delete `message LoginMsg`
- `cmd/server/main.go` — remove `RegisterClientEvent[enginepb.LoginMsg]` call (if it's still there)

Per codebase memory: renumber from 1, no field reservations.

Run `just proto` and verify clean.

### Task 4.5: Phase 4 verification

```bash
go vet ./... && go test ./... 2>&1 | grep -E "FAIL|^ok" | tail -10
(cd web-pixi && bunx tsc --noEmit) && (cd examples/4node-basic/web && bunx tsc --noEmit)
```

Browser smoke: `just dev`, register a new user (auth flow), connect, verify spawn confirmation.

---

## Phase 5 — Cleanup

### Task 5.1: Retire `pkg/ops.Register` proto-constrained shape

After all 6 ops + login migrate, the legacy `pkg/ops.Register[Req, Res, Code, ReqP, ResP]` has zero callers. Delete it.

```bash
grep -rn "ops\.Register\[" --include="*.go" .
```

Expected: zero hits after Phase 3+4.

Delete the function. Update any docstrings that reference it.

### Task 5.2: Retire `enginepb.OpRequest` + `enginepb.OpResponse`

Delete from `proto/enginepb/engine.proto`. Run `just proto`. Fix any remaining references (likely just imports in the legacy router code).

### Task 5.3: Retire `gamepb` marketplace protos + `OperationCode` enum

```bash
grep -n "OP_MARKET\|OperationCode" --include="*.proto" --include="*.go"
```

Delete from `proto/gamepb/game.proto`:
- `MarketBrowseRequest`, `MarketCreateOrderRequest`, `MarketCancelOrderRequest`, `MarketMyOrdersRequest`, `MarketInstantTradeRequest`
- `MarketOrderBookResponse`, `MarketOrderResultResponse`, `MarketMyOrdersResponse`
- `MarketPriceLevel`, `MarketOrderEntry`
- `OperationCode` enum (renumber others if needed)

Run `just proto`. Fix consumers.

### Task 5.4: Retire `BankRequestMsg` from gamepb

Delete the proto. Run `just proto`. Should be no remaining consumers.

### Task 5.5: Retire `Operations` schema section + legacy proto-op-router code

After all migrations, the `pkg/ops.Router` may still have proto-typed handler infrastructure that's now unused. Audit:

```bash
grep -rn "ProtoMessage\[" --include="*.go" pkg/ops/
```

If the proto-message generic constraint is unused, delete it. The Router itself stays — it's the typed-op dispatcher's host now.

The schema dump's `Operations` field (proto-typed) becomes empty after all migrations; rename `TypedOperations` → `Operations` in a final cleanup commit so the wire-format-natural name belongs to the surviving section.

### Task 5.6: Final acceptance

- [ ] **Step 1: Full test suite**

```bash
go vet ./... && go test ./... 2>&1 | grep -E "FAIL|^ok" | tail -25
```

- [ ] **Step 2: Both TS typechecks**

```bash
(cd web-pixi && bunx tsc --noEmit) && (cd examples/4node-basic/web && bunx tsc --noEmit)
```

- [ ] **Step 3: `just build` end-to-end**

```bash
just build
```

Expected: produces clean binary.

- [ ] **Step 4: Browser smoke (`just dev`)**

Run through the full feature matrix:
- Connect (cookie-auth flow → typed `LoginRequest`)
- Spawn confirmation
- Click-to-move
- Mining + ability cast (still events, validates dual 0x00 / 0x01 paths in parallel)
- Dock + undock
- Marketplace: browse, place sell, place buy, cancel, my-orders, instant-trade
- Bank: deposit, withdraw, query (per-action Promise resolution)
- Equip + loot + transfer
- Login rejection on duplicate username (typed `LoginResponse.Error`)
- Cross-cell movement

- [ ] **Step 5: Distributed-mode smoke (`just distributed`)**

Connect via gateway. Verify:
- Login through the gateway (RouteGatewayLocal)
- Marketplace operation (RoutePlayerCell — runs on the player's host, response routes back through gateway)
- Cross-host handoff works

- [ ] **Step 6: Update spec status**

In `docs/superpowers/specs/2026-05-06-events-operations-channel-redesign.md`, mark Plan 2 landed.

```bash
git add docs/superpowers/specs/2026-05-06-events-operations-channel-redesign.md
git commit -m "docs(spec): mark Plan 2 (operations channel + login op) landed"
```

- [ ] **Step 7: Merge to main**

```bash
git checkout main
git merge --no-ff feat/mmokit-operations-channel -m "Merge branch 'feat/mmokit-operations-channel'

Plan 2 of the events/operations channel redesign. Operations channel
0x01 is now typed reflection-codec end-to-end (no more protobuf
OpRequest/OpResponse envelope); marketplace + bank ops migrated to
typed RegisterOp[Req, Res]; Login moved from a typed event on 0x00 to
a RouteGatewayLocal operation on 0x01.

End state: zero protobuf bytes on any client-facing wire frame. The
only proto package left in gen/go is meshpb (server-internal mesh
data plane, never reaches clients).

Spec: docs/superpowers/specs/2026-05-06-events-operations-channel-redesign.md
Plan: docs/superpowers/plans/2026-05-07-mmokit-operations-channel-redesign.md"
```

---

## Acceptance Criteria

- `just build` produces a clean binary; `go vet ./...` clean.
- Both TS typechecks clean.
- `just dev` smoke flow passes the full feature matrix above.
- `just distributed` smoke passes login + cross-host marketplace op.
- **Zero protobuf bytes** on any client-facing wire frame. Channel 0x00 is typed reflection-codec; channel 0x01 is typed reflection-codec.
- The only remaining `gen/go/*pb/` package used by clients is `enginepb` (for the framework events still on the legacy 0x00 ServerEvent envelope: `SE_PLAYER_SPAWNED`, `SE_CELL_CHANGE`, `SE_SERVER_CONFIG`). `meshpb` is server-internal. `gamepb` is reduced to non-wire-format types only (e.g., `EntityType` enum if still used).
- `mmokit.RegisterOp[Req, Res]` is the only op-registration verb; `pkg/ops.Register` (proto-constrained) deleted.
- `enginepb.OpRequest`, `enginepb.OpResponse`, `enginepb.LoginMsg`, `gamepb.OperationCode` enum, all `gamepb.Market*` proto messages, `gamepb.BankRequestMsg` deleted.
- Spec [2026-05-06-events-operations-channel-redesign.md](../specs/2026-05-06-events-operations-channel-redesign.md) marked **Plan 2 landed**.
