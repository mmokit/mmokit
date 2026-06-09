# C# SDK — Plan 1: UDP Transport Op-Channel Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the UDP transport demux channel `0x01` (operations) so UDP clients can run typed ops — unblocking auth-over-UDP for the C# SDK.

**Architecture:** `UDPTransport.routePayload` currently routes channel `0x00` (events) to the inbound queue and dumps everything else there too; `DrainOpInput()` hard-returns `nil`. Add a parallel `opInbound` queue, route `ChannelOperation` (`0x01`) frames into it on both the reliable and unreliable inbound paths (both already funnel through `routePayload`), and implement `DrainOpInput()` to drain it — exactly mirroring the WebSocket `Conn` (`pkg/net/conn.go`). The gateway's op-router path is already transport-agnostic, so no gateway change is needed.

**Tech Stack:** Go, `pkg/net` (no external deps), `go test`.

**Spec:** [docs/superpowers/specs/2026-06-06-csharp-sdk-unity-design.md](../specs/2026-06-06-csharp-sdk-unity-design.md) §B.

**Encryption-seam note (from spec §B "Future requirement"):** This change operates on the **decrypted** payload — channel demux stays above the (future) transport-encryption boundary. Do not couple the channel byte to packet framing; keep it a property of the payload handed to `routePayload`. No action needed now beyond not violating this.

---

## File Structure

- **Modify:** `pkg/net/udp_transport.go`
  - Add `opInbound [][]byte` field to `UDPTransport` (guarded by the existing `inMu`).
  - Rewrite `routePayload` to switch on the leading channel byte: `0x00` → events, `0x01` → ops, anything else → legacy event (bytes intact).
  - Replace the `DrainOpInput()` stub with a real drain of `opInbound`.
  - Update the two stale comments that claim op channel is unsupported.
- **Create:** `pkg/net/udp_transport_test.go`
  - White-box tests (package `net`) constructing `&UDPTransport{}` directly (no goroutine/server needed — `routePayload`, `DrainInput`, `DrainOpInput`, `handleReliable` touch only the in-queues + recv tracking).

---

### Task 1: Demux the op channel on UDP inbound

**Files:**
- Modify: `pkg/net/udp_transport.go` (struct field ~line 52; `DrainOpInput` lines 107-108; `routePayload` lines 169-188; struct comment lines 46-50)
- Create: `pkg/net/udp_transport_test.go`

- [ ] **Step 1: Write the failing tests**

Create `pkg/net/udp_transport_test.go`:

```go
package net

import (
	"slices"
	"testing"
)

// A channel-0x01 frame must land in the op queue (channel byte stripped),
// a channel-0x00 frame in the event queue, and the queues must be
// independent. This is the path auth + every typed op rides.
func TestUDPTransport_RoutePayloadDemuxesChannels(t *testing.T) {
	tr := &UDPTransport{}

	tr.routePayload([]byte{ChannelOperation, 0xAA, 0xBB}) // op
	tr.routePayload([]byte{ChannelEvent, 0x11, 0x22})     // event

	ops := tr.DrainOpInput()
	if len(ops) != 1 {
		t.Fatalf("DrainOpInput: got %d msgs, want 1", len(ops))
	}
	if !slices.Equal(ops[0], []byte{0xAA, 0xBB}) {
		t.Fatalf("op payload = %v, want [170 187]", ops[0])
	}

	evs := tr.DrainInput()
	if len(evs) != 1 {
		t.Fatalf("DrainInput: got %d msgs, want 1", len(evs))
	}
	if !slices.Equal(evs[0], []byte{0x11, 0x22}) {
		t.Fatalf("event payload = %v, want [17 34]", evs[0])
	}

	// Draining clears the queue.
	if got := tr.DrainOpInput(); got != nil {
		t.Fatalf("DrainOpInput after drain = %v, want nil", got)
	}
}

// A frame whose leading byte is neither 0x00 nor 0x01 is a legacy
// pre-channel-prefix event — it must go to the event queue with bytes
// intact and never into the op queue.
func TestUDPTransport_RoutePayloadLegacyNoPrefix(t *testing.T) {
	tr := &UDPTransport{}
	tr.routePayload([]byte{0x42, 0x99})

	evs := tr.DrainInput()
	if len(evs) != 1 || !slices.Equal(evs[0], []byte{0x42, 0x99}) {
		t.Fatalf("legacy event routing = %v, want [[66 153]]", evs)
	}
	if got := tr.DrainOpInput(); got != nil {
		t.Fatalf("legacy frame leaked into op queue: %v", got)
	}
}

// The reliable inbound path (handleReliable) is what auth ops use. Verify
// an op frame delivered reliably surfaces via DrainOpInput.
func TestUDPTransport_ReliableOpFrameReachesOpQueue(t *testing.T) {
	tr := &UDPTransport{}
	tr.handleReliable(1, []byte{ChannelOperation, 0x01, 0x02, 0x03})

	ops := tr.DrainOpInput()
	if len(ops) != 1 || !slices.Equal(ops[0], []byte{0x01, 0x02, 0x03}) {
		t.Fatalf("reliable op frame routing = %v, want [[1 2 3]]", ops)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./pkg/net/ -run TestUDPTransport_ -v`
Expected: FAIL — `TestUDPTransport_RoutePayloadDemuxesChannels` and `TestUDPTransport_ReliableOpFrameReachesOpQueue` fail because `DrainOpInput()` returns `nil` (the op queue is never populated). (`TestUDPTransport_RoutePayloadLegacyNoPrefix` may already pass.)

- [ ] **Step 3: Add the op queue, demux, and drain**

In `pkg/net/udp_transport.go`, add the field after `inbound [][]byte` (~line 52). Replace:

```go
	inMu    sync.Mutex
	inbound [][]byte
```

with:

```go
	inMu      sync.Mutex
	inbound   [][]byte // channel 0x00 (events + typed client-input)
	opInbound [][]byte // channel 0x01 (operations); drained via DrainOpInput
```

Update the stale block comment just above (lines ~46-50) — replace:

```go
	// Inbound message queue. Payloads carry a leading channel byte
	// matching the WebSocket conn convention: 0x00 → inbound (game
	// events + typed-input mmokit.HandleClient frames after
	// Plan 1 Phase 5). Channel 0x01 (operations) is not yet supported
	// on UDP.
```

with:

```go
	// Inbound message queues. Payloads carry a leading channel byte
	// matching the WebSocket conn convention: 0x00 → inbound (game
	// events + typed-input mmokit.HandleClient frames), 0x01 →
	// opInbound (typed ops: auth, marketplace, etc.).
```

Replace the `DrainOpInput` stub (lines 107-108):

```go
// DrainOpInput returns nil — UDP transport does not support operation messages.
func (t *UDPTransport) DrainOpInput() [][]byte { return nil }
```

with:

```go
// DrainOpInput returns all queued operation messages (channel 0x01) and
// clears the queue. Mirrors Conn.DrainOpInput for the WebSocket transport.
func (t *UDPTransport) DrainOpInput() [][]byte {
	t.inMu.Lock()
	if len(t.opInbound) == 0 {
		t.inMu.Unlock()
		return nil
	}
	msgs := t.opInbound
	t.opInbound = make([][]byte, 0, 8)
	t.inMu.Unlock()
	return msgs
}
```

Replace `routePayload` (lines 169-188) — update both the doc comment and the body:

```go
// routePayload buckets a non-empty inbound payload onto the matching queue
// by its leading channel byte: ChannelEvent (0x00) → event queue,
// ChannelOperation (0x01) → op queue (both stripped of the channel byte).
// An unknown leading byte is a legacy pre-channel-prefix event and routes
// to the event queue with bytes left intact.
func (t *UDPTransport) routePayload(payload []byte) {
	switch payload[0] {
	case ChannelEvent:
		body := make([]byte, len(payload)-1)
		copy(body, payload[1:])
		t.inMu.Lock()
		t.inbound = append(t.inbound, body)
		t.inMu.Unlock()
	case ChannelOperation:
		body := make([]byte, len(payload)-1)
		copy(body, payload[1:])
		t.inMu.Lock()
		t.opInbound = append(t.opInbound, body)
		t.inMu.Unlock()
	default:
		// Unknown leading byte — legacy channel-0x00 event, bytes intact.
		body := make([]byte, len(payload))
		copy(body, payload)
		t.inMu.Lock()
		t.inbound = append(t.inbound, body)
		t.inMu.Unlock()
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/net/ -run TestUDPTransport_ -v`
Expected: PASS — all three tests green.

- [ ] **Step 5: Vet and commit**

Run: `go vet ./pkg/net/...`
Expected: no output (clean).

```bash
git add pkg/net/udp_transport.go pkg/net/udp_transport_test.go
git commit -m "feat(net): demux op channel (0x01) on UDP transport

UDPTransport now routes channel-0x01 frames to an opInbound queue and
implements DrainOpInput, mirroring the WebSocket Conn. Unblocks typed
ops (auth, marketplace) over UDP — required for the C# Unity SDK to
authenticate over the op channel.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Regression-check the broader net + gateway paths

**Files:** none modified — verification only.

- [ ] **Step 1: Run the full pkg/net test suite**

Run: `go test ./pkg/net/...`
Expected: PASS — the new op-channel routing does not regress existing connmanager/transport behavior.

- [ ] **Step 2: Run the universe gateway tests (transport-agnostic op path)**

Run: `go test ./pkg/universe/ -run 'Gateway|Auth|S6' -count=1`
Expected: PASS — the gateway op-router path is unchanged; this confirms nothing depended on `DrainOpInput` returning `nil` for UDP.

- [ ] **Step 3: Vet the whole module**

Run: `go vet ./...`
Expected: no output (clean). (Per CLAUDE.md: never `go build ./...` — `go vet` is the compile check.)

No commit — verification task. If any test fails, stop and investigate before proceeding to Plan 2.

---

## Self-Review

- **Spec coverage (§B):** Task 1 implements all three required changes — `opInbound` field, `0x01` routing in `routePayload` (covering both reliable and unreliable inbound, since both call `routePayload`), and a real `DrainOpInput`. The encryption-seam constraint is respected (demux operates on the decrypted payload; no coupling to packet framing). ✅
- **Placeholder scan:** No TBD/TODO; every code step shows complete code. ✅
- **Type consistency:** `opInbound` field, `ChannelOperation`/`ChannelEvent` constants (defined in `pkg/net/conn.go:30-31`), and `DrainOpInput` signature `[][]byte` match the `Transport` interface and the WS `Conn` mirror. `handleReliable`/`handleUnreliable` already route through `routePayload` (verified at udp_transport.go:166 and :221). ✅
