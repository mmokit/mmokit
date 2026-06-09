# C# SDK — Plan 8: Client.cs (stateless facade)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Generate `Client.cs` — the stateless high-level facade that ties `UdpTransport` + the op-channel framing + the `TypedDispatcher` + the `DeltaDecoder` into one usable client: `Connect`, typed op calls (incl. `AuthLogin`/`AuthRegister`), per-event `On<Name>` subscriptions, and per-input `Send<Name>` methods.

**Architecture:** A new emitter `csharpBackend.genClient` (in `cmd/sdkgen/backend_csharp_client.go`) produces a `<Game>Client` class wrapping a `UdpTransport`. A background pump drains `transport.Recv()` and routes each frame by its leading channel byte: `0x00` (events) → iterate `[typeID u32 LE][bodyLen u32 LE][body]` entries → `TypedDispatcher.Dispatch`; `0x01` (op responses) → `[typeID u32][reqID u64][bodyLen u32][body]` → complete the pending `TaskCompletionSource` keyed on `reqID`. Per **server-event** type → `On<Name>(Action<EventClass>)` that subscribes to the dispatcher and decodes the body. Per **operation** → `Task<Res> <Op>(Req)` that frames `[0x01][typeID][reqID][bodyLen][body]`, sends reliably, and awaits the decoded response. Per **client-input** type → `Send<Name>(msg, reliable=false)` framing `[0x00][typeID][bodyLen][body]`. The `DeltaDecoder` is exposed as a property; the consumer wires `OnWorldDelta(msg => Decoder.Decode(msg.body))` — the SDK stays **stateless** (no managed world model), exactly as web-pixi does (`examples/4node-basic/web/src/network.ts:80`).

**Tech Stack:** Go (`cmd/sdkgen`), C# (`netstandard2.1`, `System.Threading.Tasks`), `dotnet build` compile gate.

**Spec:** [docs/superpowers/specs/2026-06-06-csharp-sdk-unity-design.md](../specs/2026-06-06-csharp-sdk-unity-design.md) §D (Client.cs — the stateless facade) + the Architecture amendment (stateless, headless-capable; consumer owns state).

**Prerequisites:** Plans 1–7 merged (`UdpTransport`, `TypedDispatcher`/Events, Inputs, Operations, `DeltaDecoder`, `DeltaWorldUpdate`).

---

## Background facts (verified)

- **Delta delivery:** the per-tick world delta is a typed `WorldDelta` server event whose reflect-codec `body` (`bytes`) field carries the binary delta frame. The consumer decodes it: `client.OnWorldDelta(msg => decoder.Decode(msg.body))` (`examples/4node-basic/web/src/network.ts:74-81`). The SDK does NOT auto-decode (stateless).
- **Op frame** (both directions, channel stripped by the transport caller): `[typeID u32 LE][request_id u64 LE][body_len u32 LE][body]`; with the channel byte it's `[0x01]` + that. (`generate.go::callOp` / `writeTypedOpDispatch`.)
- **Event frame** (server→client, channel `0x00`): a sequence of `[typeID u32 LE][body_len u32 LE][body]` entries. (`generate.go::handleEvent`.)
- **Client-input frame** (client→server): `[0x00][typeID u32 LE][body_len u32 LE][body]`. (`generate.go::send` + transport `sendClientInput`.)
- `UdpTransport` (Plan 4): `static Connect(host, port, handshakeTimeoutMs=5000)`, `SendReliable(byte[])`, `SendUnreliable(byte[])`, `byte[]? Recv()` (blocking; null on close), `Close()`. Inbound frames from `Recv()` include the leading channel byte (the server's `[0x00]`/`[0x01]` game frame; UdpTransport strips only the UDP packet header).
- Generated deps: `Events.cs` classes have `const uint TypeID` + `static <Name> Decode(byte[])`; `Inputs.cs` classes have `const uint TypeID` + `byte[] Encode()`; `Operations.cs` Request classes have `const uint TypeID` + `Encode()`, Response classes have `Decode`; `TypedDispatcher.On(uint, Action<byte[]>)` / `Dispatch(uint, byte[])`; `<Game>DeltaDecoder` has `Decode(byte[])` + `Clear()`.
- Helper names from the TS: `csReflectClassName` (strip pkg prefix); op method name = strip `"Request"` suffix; server-event method name = `On` + class name. `OperationError` is auto-registered as a server event ONLY in games that register ops with the framework envelope — reference it conditionally (the 4node target + the sample schema don't have it; auth carries its own `ErrorCode`/`ErrorMessage` fields).
- `OutputFiles` already gates Events/Inputs/Operations; `Client.cs` is emitted whenever ANY of typed-events / inputs / ops / entities exist (so the facade ships even for a minimal game) — but to keep it simple, gate it on `len(Entities) > 0 || hasTypedEvents || hasTypedOps || hasInputs`.

---

## File Structure

- **Create:** `cmd/sdkgen/backend_csharp_client.go` — `genClient` + helpers.
- **Modify:** `cmd/sdkgen/backend_csharp.go` — wire `Client.cs` into `OutputFiles`.
- **Modify:** `cmd/sdkgen/backend_csharp_test.go` — emitter assertions.

---

### Task 1: genClient emitter + wiring + tests

**Files:**
- Create: `cmd/sdkgen/backend_csharp_client.go`
- Modify: `cmd/sdkgen/backend_csharp.go`, `cmd/sdkgen/backend_csharp_test.go`

- [ ] **Step 1: Create `cmd/sdkgen/backend_csharp_client.go`:**

```go
package main

import (
	"fmt"
	"strings"
)

// csOpMethodName converts a Request class name to a C# method name:
// "AuthLoginRequest" -> "AuthLogin"; falls back to the class name.
func csOpMethodName(reqClassName string) string {
	n := strings.TrimSuffix(reqClassName, "Request")
	if n == "" {
		return reqClassName
	}
	return n
}

// hasServerEvent reports whether a server-event class with the given bare
// name is registered (used to conditionally wire OperationError intercept).
func hasServerEvent(schema ProtocolSchema, bareName string) bool {
	for _, st := range schema.ServerEventTypes {
		if csReflectClassName(st.Name) == bareName {
			return true
		}
	}
	return false
}

// genClient emits Client.cs: the stateless <Game>Client facade over UdpTransport.
func (b csharpBackend) genClient(schema ProtocolSchema) string {
	gameName := titleCase(schema.Game)
	hasEvents := len(schema.BroadcastTypes) > 0 || len(schema.ServerEventTypes) > 0
	hasOps := len(schema.Operations) > 0
	hasInputs := len(schema.ClientInputTypes) > 0
	hasOpError := hasOps && hasServerEvent(schema, "OperationError")

	var sb strings.Builder
	sb.WriteString("// GENERATED by sdkgen — do not edit.\n\n")
	sb.WriteString("using System;\nusing System.Collections.Generic;\nusing System.Threading;\nusing System.Threading.Tasks;\nusing Mmokit.Sdk.Core;\n\n")
	fmt.Fprintf(&sb, "namespace %s\n{\n", b.namespace)

	fmt.Fprintf(&sb, "    /// Stateless high-level client. Owns transport + decode + dispatch;\n")
	fmt.Fprintf(&sb, "    /// the CONSUMER owns accumulated world state (Unity GameObjects / a bot's\n")
	fmt.Fprintf(&sb, "    /// store). Decode a WorldDelta via OnWorldDelta(m => Decoder.Decode(m.body)).\n")
	fmt.Fprintf(&sb, "    public sealed class %sClient\n    {\n", gameName)
	sb.WriteString("        UdpTransport? _transport;\n")
	sb.WriteString("        CancellationTokenSource? _pumpCts;\n")
	fmt.Fprintf(&sb, "        public %sDeltaDecoder Decoder { get; } = new();\n", gameName)
	if hasEvents {
		sb.WriteString("        public TypedDispatcher TypedEvents { get; } = new();\n")
	}
	if hasOps {
		sb.WriteString("        readonly Dictionary<ulong, TaskCompletionSource<byte[]>> _pendingOps = new();\n")
		sb.WriteString("        readonly object _opLock = new();\n")
		sb.WriteString("        long _nextReqId;\n")
	}
	sb.WriteString("\n")

	// Connect / Disconnect.
	sb.WriteString("        /// Connect over UDP and start the receive pump.\n")
	sb.WriteString("        public void Connect(string host, int port, int handshakeTimeoutMs = 5000)\n        {\n")
	sb.WriteString("            _transport = UdpTransport.Connect(host, port, handshakeTimeoutMs);\n")
	sb.WriteString("            _pumpCts = new CancellationTokenSource();\n")
	sb.WriteString("            var t = _transport;\n")
	sb.WriteString("            var ct = _pumpCts.Token;\n")
	sb.WriteString("            _ = Task.Run(() => Pump(t, ct));\n")
	sb.WriteString("        }\n\n")

	sb.WriteString("        public void Disconnect()\n        {\n")
	sb.WriteString("            _pumpCts?.Cancel();\n")
	if hasOps {
		sb.WriteString("            lock (_opLock) { foreach (var p in _pendingOps.Values) p.TrySetException(new Exception(\"client disconnected\")); _pendingOps.Clear(); }\n")
	}
	sb.WriteString("            _transport?.Close();\n")
	sb.WriteString("        }\n\n")

	sb.WriteString("        /// Clear delta baselines (call on reconnect).\n")
	sb.WriteString("        public void ClearBaselines() => Decoder.Clear();\n\n")

	// Pump + routing.
	sb.WriteString("        void Pump(UdpTransport t, CancellationToken ct)\n        {\n")
	sb.WriteString("            while (!ct.IsCancellationRequested)\n            {\n")
	sb.WriteString("                byte[]? frame = t.Recv();\n")
	sb.WriteString("                if (frame == null) return; // transport closed\n")
	sb.WriteString("                if (frame.Length == 0) continue;\n")
	sb.WriteString("                byte ch = frame[0];\n")
	sb.WriteString("                byte[] body = Sub(frame, 1, frame.Length - 1);\n")
	sb.WriteString("                if (ch == 0x00) HandleEvent(body);\n")
	if hasOps {
		sb.WriteString("                else if (ch == 0x01) HandleOperation(body);\n")
	}
	sb.WriteString("            }\n        }\n\n")

	// HandleEvent.
	sb.WriteString("        void HandleEvent(byte[] body)\n        {\n")
	if hasEvents {
		sb.WriteString("            int off = 0;\n")
		sb.WriteString("            while (off + 8 <= body.Length)\n            {\n")
		sb.WriteString("                uint typeID = ReadU32(body, off);\n")
		sb.WriteString("                int bodyLen = (int)ReadU32(body, off + 4);\n")
		sb.WriteString("                off += 8;\n")
		sb.WriteString("                if (off + bodyLen > body.Length) return; // truncated\n")
		sb.WriteString("                TypedEvents.Dispatch(typeID, Sub(body, off, bodyLen));\n")
		sb.WriteString("                off += bodyLen;\n")
		sb.WriteString("            }\n")
	}
	sb.WriteString("        }\n\n")

	// HandleOperation.
	if hasOps {
		sb.WriteString("        void HandleOperation(byte[] body)\n        {\n")
		sb.WriteString("            if (body.Length < 16) return;\n")
		sb.WriteString("            uint resTypeID = ReadU32(body, 0);\n")
		sb.WriteString("            ulong reqID = ReadU64(body, 4);\n")
		sb.WriteString("            int bodyLen = (int)ReadU32(body, 12);\n")
		sb.WriteString("            if (16 + bodyLen > body.Length) return;\n")
		sb.WriteString("            byte[] opBody = Sub(body, 16, bodyLen);\n")
		sb.WriteString("            TaskCompletionSource<byte[]>? p;\n")
		sb.WriteString("            lock (_opLock) { if (!_pendingOps.TryGetValue(reqID, out p)) return; _pendingOps.Remove(reqID); }\n")
		if hasOpError {
			sb.WriteString("            if (resTypeID == OperationError.TypeID) { var err = OperationError.Decode(opBody); p.TrySetException(new Exception($\"OperationError typeID=0x{resTypeID:x}\")); return; }\n")
		}
		sb.WriteString("            p.TrySetResult(opBody);\n")
		sb.WriteString("        }\n\n")

		// CallOp helper.
		sb.WriteString("        Task<byte[]> CallOp(uint reqTypeID, byte[] reqBody)\n        {\n")
		sb.WriteString("            ulong reqID = (ulong)Interlocked.Increment(ref _nextReqId);\n")
		sb.WriteString("            var tcs = new TaskCompletionSource<byte[]>();\n")
		sb.WriteString("            lock (_opLock) { _pendingOps[reqID] = tcs; }\n")
		sb.WriteString("            byte[] frame = new byte[1 + 4 + 8 + 4 + reqBody.Length];\n")
		sb.WriteString("            frame[0] = 0x01;\n")
		sb.WriteString("            WriteU32(frame, 1, reqTypeID);\n")
		sb.WriteString("            WriteU64(frame, 5, reqID);\n")
		sb.WriteString("            WriteU32(frame, 13, (uint)reqBody.Length);\n")
		sb.WriteString("            Array.Copy(reqBody, 0, frame, 17, reqBody.Length);\n")
		sb.WriteString("            _transport!.SendReliable(frame);\n")
		sb.WriteString("            return tcs.Task;\n")
		sb.WriteString("        }\n\n")

		// Per-op methods.
		emitted := map[string]struct{}{}
		for _, op := range schema.Operations {
			reqCls := csReflectClassName(op.RequestTypeName)
			resCls := csReflectClassName(op.ResponseTypeName)
			method := csOpMethodName(reqCls)
			if _, dup := emitted[method]; dup {
				continue
			}
			emitted[method] = struct{}{}
			fmt.Fprintf(&sb, "        /// Typed op %s → %s.\n", op.RequestTypeName, op.ResponseTypeName)
			fmt.Fprintf(&sb, "        public async Task<%s> %s(%s req)\n        {\n", resCls, method, reqCls)
			fmt.Fprintf(&sb, "            byte[] raw = await CallOp(%s.TypeID, req.Encode());\n", reqCls)
			fmt.Fprintf(&sb, "            return %s.Decode(raw);\n", resCls)
			sb.WriteString("        }\n\n")
		}
	}

	// Per-input send methods.
	if hasInputs {
		sb.WriteString("        void SendInputFrame(uint typeID, byte[] body, bool reliable)\n        {\n")
		sb.WriteString("            byte[] frame = new byte[1 + 4 + 4 + body.Length];\n")
		sb.WriteString("            frame[0] = 0x00;\n")
		sb.WriteString("            WriteU32(frame, 1, typeID);\n")
		sb.WriteString("            WriteU32(frame, 5, (uint)body.Length);\n")
		sb.WriteString("            Array.Copy(body, 0, frame, 9, body.Length);\n")
		sb.WriteString("            if (reliable) _transport!.SendReliable(frame); else _transport!.SendUnreliable(frame);\n")
		sb.WriteString("        }\n\n")
		for _, ct := range schema.ClientInputTypes {
			cls := csReflectClassName(ct.Name)
			fmt.Fprintf(&sb, "        /// Send a %s client-input (unreliable by default).\n", cls)
			fmt.Fprintf(&sb, "        public void Send%s(%s msg, bool reliable = false) => SendInputFrame(%s.TypeID, msg.Encode(), reliable);\n\n", cls, cls, cls)
		}
	}

	// Per-server-event On<Name> subscriptions.
	if hasEvents {
		for _, st := range schema.ServerEventTypes {
			cls := csReflectClassName(st.Name)
			fmt.Fprintf(&sb, "        /// Subscribe to the %s server event. Returns an unsubscribe.\n", st.Name)
			fmt.Fprintf(&sb, "        public Action On%s(Action<%s> handler) => TypedEvents.On(%s.TypeID, b => handler(%s.Decode(b)));\n\n", cls, cls, cls, cls)
		}
	}

	// LE byte helpers (frame headers are little-endian).
	sb.WriteString("        static uint ReadU32(byte[] b, int o) => (uint)b[o] | ((uint)b[o + 1] << 8) | ((uint)b[o + 2] << 16) | ((uint)b[o + 3] << 24);\n")
	sb.WriteString("        static ulong ReadU64(byte[] b, int o) { ulong v = 0; for (int i = 0; i < 8; i++) v |= (ulong)b[o + i] << (8 * i); return v; }\n")
	sb.WriteString("        static void WriteU32(byte[] b, int o, uint v) { for (int i = 0; i < 4; i++) b[o + i] = (byte)(v >> (8 * i)); }\n")
	sb.WriteString("        static void WriteU64(byte[] b, int o, ulong v) { for (int i = 0; i < 8; i++) b[o + i] = (byte)(v >> (8 * i)); }\n")
	sb.WriteString("        static byte[] Sub(byte[] b, int start, int len) { var r = new byte[len]; Array.Copy(b, start, r, 0, len); return r; }\n")

	sb.WriteString("    }\n}\n")
	return sb.String()
}
```

- [ ] **Step 2: Wire `Client.cs` into `OutputFiles`** in `cmd/sdkgen/backend_csharp.go` — after the operations block, before `return files`:

```go
	if len(schema.Entities) > 0 || len(schema.BroadcastTypes) > 0 || len(schema.ServerEventTypes) > 0 ||
		len(schema.ClientInputTypes) > 0 || len(schema.Operations) > 0 {
		files["Client.cs"] = func() string { return b.genClient(schema) }
	}
```

- [ ] **Step 3: Add emitter tests** to `cmd/sdkgen/backend_csharp_test.go`:

```go
func TestCsharpBackend_Client(t *testing.T) {
	out := csharpBackend{namespace: "Mmokit.Sdk"}.genClient(sampleEntitySchema())
	for _, want := range []string{
		"public sealed class DemoClient",
		"public void Connect(string host, int port, int handshakeTimeoutMs = 5000)",
		"_transport = UdpTransport.Connect(host, port, handshakeTimeoutMs);",
		"public DemoDeltaDecoder Decoder { get; } = new();",
		"public TypedDispatcher TypedEvents { get; } = new();",
		"public async Task<AuthLoginResponse> AuthLogin(AuthLoginRequest req)", // op method, "Request" stripped
		"byte[] raw = await CallOp(AuthLoginRequest.TypeID, req.Encode());",
		"return AuthLoginResponse.Decode(raw);",
		"public void SendSetMoveTarget(SetMoveTarget msg, bool reliable = false)", // per-input
		"public Action OnPong(Action<Pong> handler) => TypedEvents.On(Pong.TypeID, b => handler(Pong.Decode(b)));", // per-event
		"frame[0] = 0x01;", // op frame channel byte
		"frame[0] = 0x00;", // input frame channel byte
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("genClient missing %q in:\n%s", want, out)
		}
	}
	// The sample has no OperationError server event → no intercept emitted.
	if strings.Contains(out, "OperationError.TypeID") {
		t.Fatalf("genClient should not reference OperationError when absent from schema")
	}
}

func TestCsharpBackend_OutputFiles_IncludesClient(t *testing.T) {
	files := csharpBackend{namespace: "Mmokit.Sdk"}.OutputFiles(sampleEntitySchema())
	if _, ok := files["Client.cs"]; !ok {
		t.Fatalf("OutputFiles missing Client.cs")
	}
}
```

- [ ] **Step 4: Verify (Go) + commit**

Run: `go vet ./cmd/sdkgen/... && go test ./cmd/sdkgen/ 2>&1 | tail -3`
Expected: vet clean; all tests pass.

```bash
git add cmd/sdkgen/backend_csharp_client.go cmd/sdkgen/backend_csharp.go cmd/sdkgen/backend_csharp_test.go
git commit -m "feat(sdkgen): csharp Client.cs (stateless high-level facade)

<Game>Client over UdpTransport: receive pump (channel 0x00 events →
TypedDispatcher, 0x01 → pending-op completion), per-op Task<Res> methods
(incl. AuthLogin), per-input Send<Name>, per-event On<Name>. Exposes the
DeltaDecoder; consumer decodes WorldDelta.body (stateless SDK). Wired into
OutputFiles.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Compile gate the full generated SDK

With `Client.cs` in `OutputFiles`, the Plan 5 compile gate now compiles the entire generated SDK (EntityType/Entities/DeltaDecoder/Events/Inputs/Operations/Client) + all `_core` files as one assembly — the structural proof the facade ties together.

**Files:** none (verification) — unless the gate surfaces an emitter bug to fix in `backend_csharp_client.go`.

- [ ] **Step 1: Run the compile gate**

Run: `go test -tags=csharptest ./cmd/sdkgen/ -run TestCsharpSdk_Compiles -v 2>&1 | tail -30`
Expected: PASS — `Client.cs` compiles against `Operations.cs` (op Req/Res `TypeID`/`Encode`/`Decode`), `Inputs.cs` (`TypeID`/`Encode`), `Events.cs` (`TypedDispatcher`, event `TypeID`/`Decode`), `DeltaDecoder.cs` (`<Game>DeltaDecoder`), and `UdpTransport` (`Connect`/`Recv`/`SendReliable`/`SendUnreliable`/`Close`).

**If `dotnet build` fails:** fix `backend_csharp_client.go` (never weaken the gate). Likely suspects:
- `UdpTransport.Recv()` returns `byte[]?` — the pump's `byte[]? frame = t.Recv();` + null check must match.
- `TaskCompletionSource<byte[]>` (non-generic `TrySetResult`/`TrySetException` exist on the generic form) — fine on netstandard2.1.
- Op method name collision or a Request/Response class that doesn't exist (the emitter uses `csReflectClassName` on the op's type names — same as `genOperations`, so they match).
- `Interlocked.Increment(ref _nextReqId)` needs `_nextReqId` to be `long` (it is) — the result is cast to `ulong`.
- If the game DID register `OperationError`, confirm `Events.cs` emits it (it would, as a ServerEventType) so `OperationError.TypeID`/`Decode` resolve.

Re-run until it compiles; commit any fix with a `fix(sdkgen):` message.

- [ ] **Step 2: Confirm full suites**

Run: `cd csharp && dotnet test 2>&1 | tail -4` → all C# tests pass.
Run: `cd . && go test ./cmd/sdkgen/ 2>&1 | tail -3` → PASS.

- [ ] **Step 3: Commit** (only if Step 1 required an emitter fix).

---

## Self-Review

- **Spec coverage (§D Client.cs + stateless amendment):** `<Game>Client` over `UdpTransport`; receive pump routing events (`0x00`) + op responses (`0x01`); per-op `Task<Res>` calls (incl. `AuthLogin`/`AuthRegister` — they're ops); per-input `Send<Name>`; per-event `On<Name>`; exposes `Decoder` for consumer-driven `WorldDelta` decode (stateless — no managed world model). Compile-gated against the whole generated SDK. End-to-end decode/auth correctness vs a live server is the Plan 9 smoke. ✅
- **Placeholder scan:** Complete emitter code; the compile gate reuses Plan 5's harness. ✅
- **Type/name consistency:** Op/input/event class names via `csReflectClassName` (same as `genOperations`/`genInputs`/`genEvents`); `<Name>.TypeID`/`Encode`/`Decode` match those emitters; `<Game>DeltaDecoder`/`Clear` match Plan 7; `TypedDispatcher.On`/`Dispatch` match Plan 6; `UdpTransport.Connect`/`Recv`/`SendReliable`/`SendUnreliable`/`Close` match Plan 4. Frame headers are little-endian (matching the op/event/input wire format) via the local `ReadU32`/`ReadU64`/`WriteU32`/`WriteU64` helpers. `OperationError` referenced only when present (`hasServerEvent`). ✅

## Open items (resolve during planning, not blocking)

- `SendInputFrame` defaults to **unreliable** (natural for high-frequency game input); callers pass `reliable: true` when delivery matters. Ops always go reliable.
- The op-response intercept for `OperationError` is conditional on the schema registering it; the auth ops instead carry `ErrorCode`/`ErrorMessage` response fields, so failures surface as a decoded response, not an exception.
- End-to-end validation (real `AuthLogin` over UDP, real `WorldDelta` decode) is the Plan 9 smoke against a running 4node server — the compile gate here proves the facade builds against the full SDK.
