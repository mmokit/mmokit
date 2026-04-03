# Plan: Delta Compression & Position Quantization (Roadmap #3)

## Context

Both games send full entity state every tick for every visible entity whose hash changed. Slither sends 10-60KB per player per tick. Positions use float32 (4 bytes each) when cell-local coordinates only need uint16. Static entities (food, asteroids, stations) cost bandwidth every full-refresh cycle even though nothing changed. This feature targets 50-70% bandwidth reduction via three layers: quantization, field-level delta encoding, and per-component dirty tracking.

**Key constraints**:

- Cell sizes are dynamic (`SetCellSize()` at init). All quantization must take cellSize as a parameter — no hardcoded constants or static assumptions about cell size.
- mmokit must support both reliable (WebSocket/TCP) and unreliable (UDP) transports. The delta compression system must implement acknowledged baselines (Valve/Gaffer pattern) so it works correctly over UDP with packet loss. On reliable transport, baseline advancement is automatic.

---

## Industry Research & Validation

Research into Unreal Iris, Unity DOTS Netcode, Valve Source, Gaffer on Games, and SpatialOS confirms the approach. Key findings:

### What the industry does

1. **Quantize-then-delta** (Iris, Unity DOTS): Quantize floats to integers first, cache quantized state, then delta against cached quantized state. This avoids the "slippy floats" problem — floating-point non-determinism causing false diffs when the same physical value produces different raw bits across ticks. Our plan does this correctly: Snapshot() quantizes into bytes, DeltaEncoder compares quantized bytes.

2. **Per-field change bitmask** (Unity `GhostField.Composite=false`, Gaffer on Games): Each replicated field gets its own change-bit. Only changed fields are transmitted. Our DeltaEncoder with per-field bitmask matches this pattern.

3. **Centralized quantization** (Iris): Quantize once per entity, share across connections, then per-connection delta. Iris's key insight is separating "what changed" (shared) from "what this client knows" (per-connection). Our Hash() serves the "what changed" role (shared, computed once), and per-connection baseline storage handles "what this client knows."

4. **Static optimization** (Unity): "Static" ghosts aren't sent at all when unchanged. Our hash-based pre-check already achieves this — unchanged entities cost 0 bytes after initial send.

5. **Acknowledged baselines** (Valve Source, Gaffer): The server encodes deltas relative to the last snapshot the client has **confirmed receiving** (acked). On packet loss, deltas grow (covering all changes since the older acked baseline) but remain decodable. The client continuously sends ack packets stating the most recent snapshot received. This is the industry-standard approach for UDP transports. On TCP (reliable, ordered), the last-sent snapshot IS the acked baseline implicitly.

6. **Variable-length field encoding** (Gaffer): Position deltas could use fewer bits when movement is small (5 bits for [-16,+15] vs 18 bits for full range). This requires bit-level packing. **Deferred** — byte-aligned fields are simpler and the major wins come from quantization + delta, not bit-packing.

7. **Huffman compression on deltas** (Unity DOTS): Unity applies Huffman encoding on top of quantized deltas for additional compression. **Deferred** — adds codebook complexity. Marginal benefit over quantize+delta.

### What we align with

| Pattern | Industry Source | Our Approach | Status |
| ------- | -------------- | ------------ | ------ |
| Quantize-then-delta | Iris, Unity DOTS | Snapshot bytes are quantized, delta compares bytes | Matches |
| Per-field bitmask | Unity, Gaffer | DeltaEncoder per-field comparison | Matches |
| Acknowledged baselines | Valve Source, Gaffer | Per-connection per-entity acked baseline + sequence tracking | Matches |
| Hash as fast pre-check | Iris IsEqual | Hash() skips Snapshot() if unchanged | Matches |
| Transport-separated from ECS | Iris, Unreal | SnapshotWriter produces transport bytes, ECS keeps float32 | Matches |
| Static entities zero-cost | Unity static ghosts | Hash unchanged = skip entirely | Matches |
| Custom binary, not protobuf | Gaffer bit-packing, Iris FNetBitStreamWriter | Binary wire format inside protobuf envelope | Matches |
| InitialData / COND_InitialOnly | Unreal, Unity spawn data | Separate InitialData() for one-time fields | Matches |

### What we intentionally defer

| Pattern | Why deferred | When needed |
| ------- | ------------ | ----------- |
| Bit-level packing / variable-length fields | Complexity; major wins from quantize+delta already | Future optimization if bandwidth still insufficient |
| Huffman / entropy coding | Complexity; marginal gain | Future optimization |
| Per-component update rates | Requires hierarchical interest (roadmap #8) | Phase 3 |

Sources: [Gaffer on Games: Snapshot Compression](https://gafferongames.com/post/snapshot_compression/), [Unity DOTS Ghost Snapshots](https://docs.unity3d.com/Packages/com.unity.netcode@1.4/manual/ghost-snapshots.html), [Unity DOTS Data Compression](https://docs.unity3d.com/Packages/com.unity.netcode@1.4/manual/compression.html), [Iris Network Serializers](https://vorixo.github.io/devtricks/iris-netserializers/), [Valve Source Networking](https://developer.valvesoftware.com/wiki/Source_Multiplayer_Networking), [MMO Netcode Optimizations](https://wirepair.org/2025/12/20/netcode-optimizations-for-mmorpgs/)

---

## Architecture Overview

### Data Flow

```text
ECS Components (float32, runtime)
    ↓
Hash() — fast "anything changed?" check (Iris IsEqual equivalent)
    ↓ (only if hash changed)
Snapshot() — quantized compact binary buffer (Iris Quantize equivalent)
    ↓
DeltaEncoder.Encode(acked_baseline, curr) — bitmask + changed fields vs ACKED state
    ↓
Binary wire frame via FrameWriter (tagged with sequence number)
    ↓
Client — decode bitmask, apply delta to baseline, dequantize, send ack
```

### Acknowledged Baseline Model (Valve/Gaffer pattern)

The delta is always encoded against the **acked baseline** — the last snapshot the client has confirmed receiving. This is the critical design for UDP support:

```text
                    acked baseline (tick 100)
                           |
Server tick 105:  delta(baseline@100, state@105) → send as seq=105
Server tick 106:  delta(baseline@100, state@106) → send as seq=106  (no ack yet, still relative to 100)
                           |
Client acks seq=106 ←------+
                           |
Server tick 107:  delta(baseline@106, state@107) → send as seq=107  (smaller delta now)
```

**On reliable transport (WebSocket/TCP)**: Every sent frame is guaranteed to arrive in order. The system auto-advances the acked baseline after each send. Equivalent to `depth=1` ring buffer. Zero overhead vs the naive "last-sent" approach.

**On unreliable transport (UDP)**: The acked baseline advances only when the client explicitly acks. Between acks, deltas grow (covering all changes since last ack) but are always decodable. Ring buffer stores recent sent snapshots so the baseline can advance to the correct state when ack arrives.

### Key Design Decisions

1. **Single unified `EntityReplicator` interface** — replaces the current interface. `Serialize() any` removed. All replicators implement `Hash + Snapshot + SnapshotLayout + InitialData`. One code path.
2. **Acknowledged baseline per-connection per-entity** — Valve/Gaffer pattern. Supports both reliable and unreliable transports. On reliable, auto-advances (zero overhead). On unreliable, ack-driven.
3. **Quantization functions take cellSize as parameter** — no global state, supports different cell sizes per game
4. **Snapshots are opaque `[]byte` with game-defined layout** — engine stores them per-player per-entity
5. **DeltaEncoder is generic** — takes field sizes, compares byte ranges, outputs bitmask + changed fields. Lives in `pkg/`, zero game imports
6. **Hash kept as fast pre-check** — avoids snapshot buffer allocation when nothing changed
7. **Custom binary wire format** — protobuf overhead defeats tight packing. ServerEvent envelope stays protobuf, inner payload becomes binary
8. **Position encoding**: Viewer-relative int16 (`QRel`) for main game (cross-cell), unsigned uint16 (`QPos`) for slither (single-cell)
9. **InitialData separate from Snapshot** — one-time fields (name, skin) don't inflate keyframes

---

## Step 1: Quantization Utilities

**New package**: `pkg/quantize/`

Pure functions, no state, no game imports. All take parameters (no globals).

```go
package quantize

// Position: cell-local float32 [0, cellSize) ↔ uint16 [0, 65535]
func Pos(pos, cellSize float32) uint16
func UnPos(q uint16, cellSize float32) float32

// Angle: radians [-π, π] ↔ uint16 [0, 65535]
func Angle(radians float32) uint16
func UnAngle(q uint16) float32

// Norm: normalized [0, 1] ↔ uint8 [0, 255]
func Norm(v float32) uint8
func UnNorm(q uint8) float32

// Velocity: float32 ↔ int16 (signed, fixed-point with configurable scale)
func Vel(v, scale float32) int16
func UnVel(q int16, scale float32) float32

// Relative position: viewer-relative float32 [-range, +range] ↔ int16
func Rel(offset, halfRange float32) int16
func UnRel(q int16, halfRange float32) float32
```

`SnapshotWriter` / `SnapshotReader` helpers for building fixed-layout buffers:

```go
type SnapshotWriter struct { buf []byte; pos int }

func (w *SnapshotWriter) Uint8(v uint8)
func (w *SnapshotWriter) Uint16(v uint16)
func (w *SnapshotWriter) Uint32(v uint32)
func (w *SnapshotWriter) Int16(v int16)
func (w *SnapshotWriter) Float32(v float32)
func (w *SnapshotWriter) QPos(pos, cellSize float32)     // writes uint16
func (w *SnapshotWriter) QRel(offset, halfRange float32) // writes int16
func (w *SnapshotWriter) QAngle(radians float32)          // writes uint16
func (w *SnapshotWriter) QNorm(v float32)                  // writes uint8
func (w *SnapshotWriter) QVel(v, scale float32)            // writes int16
func (w *SnapshotWriter) Bool(v bool)                       // writes uint8
func (w *SnapshotWriter) Bytes() []byte
func (w *SnapshotWriter) Len() int
func (w *SnapshotWriter) Reset()
```

**Unit tests**: Round-trip quantize/dequantize, precision bounds at various cell sizes (1024, 4096, 8192, 16384), edge cases (0, cellSize-epsilon, negative angles, clamping).

**Files**:

- `pkg/quantize/quantize.go`
- `pkg/quantize/quantize_test.go`
- `pkg/quantize/snapshot.go` (SnapshotWriter/Reader)
- `pkg/quantize/snapshot_test.go`

---

## Step 2: DeltaEncoder

**New file**: `pkg/quantize/delta.go`

Generic field-level delta encoder. Knows field boundaries (sizes), compares byte ranges, builds bitmask + changed fields. Implements Unity's per-field change bitmask with `Composite=false`.

```go
type DeltaEncoder struct {
    fieldOffsets []int
    fieldSizes   []int
    fixedSize    int   // sum of all field sizes
    bitmaskSize  int   // ceil(fieldCount / 8)
}

func NewDeltaEncoder(fieldSizes ...int) *DeltaEncoder

// Encode compares prev and curr snapshot bytes field-by-field.
// Returns bitmask + changed field values appended to out, or nil if identical.
func (d *DeltaEncoder) Encode(prev, curr, out []byte) []byte

// Decode applies a delta to a baseline snapshot, mutating base in place.
// After Decode, base contains the current state (baseline is advanced).
func (d *DeltaEncoder) Decode(base, delta []byte)

// FullSize returns the fixed snapshot size.
func (d *DeltaEncoder) FullSize() int

// BitmaskSize returns the bitmask byte count.
func (d *DeltaEncoder) BitmaskSize() int
```

**Variable-length tails** (snake segments): The last "field" can be variable-length. In the snapshot, it's stored as uint16 length prefix + data. DeltaEncoder treats the entire var-tail as one field — if any byte changes, the whole tail is re-sent. This is the right trade-off because snake segments shift every tick anyway.

**Files**:

- `pkg/quantize/delta.go`
- `pkg/quantize/delta_test.go`

---

## Step 3: Unified EntityReplicator Interface + Baseline Management

**Modified file**: `pkg/system/replication.go`

Replace the current `EntityReplicator` interface. Remove `Serialize() any`. Single code path.

### New interface

```go
type EntityReplicator interface {
    EntityType() uint8

    // Hash writes all diff-relevant fields into the hasher.
    // Fast pre-check: if hash unchanged and not keyframe, Snapshot() is not called.
    Hash(h *Hasher, viewer *ViewerInfo, entry spatial.Entry)

    // Snapshot writes the entity's quantized transport state into a compact binary buffer.
    // Fields must match SnapshotLayout order.
    Snapshot(w *quantize.SnapshotWriter, viewer *ViewerInfo, entry spatial.Entry)

    // SnapshotLayout returns field sizes for the DeltaEncoder.
    // Called once at registration time and cached.
    SnapshotLayout() []int

    // InitialData returns one-time data for newly-visible entities (name, skin, etc).
    // Returns nil if no initial data. Equivalent to Unreal's COND_InitialOnly.
    InitialData(viewer *ViewerInfo, entry spatial.Entry) []byte
}
```

`RelevancyProvider` and `PriorityProvider` optional interfaces stay unchanged.

### Acknowledged Baseline State

Per-connection baseline management following the Valve/Gaffer pattern:

```go
// baselineState tracks per-connection per-entity acknowledged state.
type baselineState struct {
    // acked is the snapshot the client has confirmed having.
    // Deltas are always encoded against this.
    acked []byte

    // sent is a ring buffer of recently sent snapshots, indexed by frame sequence.
    // When the client acks a sequence, the corresponding snapshot becomes the new acked baseline.
    // On reliable transport (auto-ack), this has depth 1 and is promoted immediately.
    sent ringBuffer
}

// connectionState tracks all per-connection replication state.
type connectionState struct {
    ackedSeq   uint32                    // last sequence the client confirmed
    nextSeq    uint32                    // sequence counter for outgoing frames
    baselines  map[uint32]*baselineState // netID -> baseline tracking
    lastHash   map[uint32]uint64         // netID -> last computed hash (for fast pre-check)
}
```

### ReplicationConfig additions

```go
type ReplicationConfig struct {
    // ... existing fields ...

    // AckMode controls baseline advancement.
    // AckReliable: baseline auto-advances after each send (TCP/WebSocket).
    // AckExplicit: baseline advances only when AckSequence() is called (UDP).
    AckMode AckMode

    // SentHistoryDepth is the number of recent sent snapshots to retain per entity
    // for ack-based baseline advancement. Only relevant for AckExplicit mode.
    // Default: 32 (~1.6 seconds at 20Hz). On AckReliable, always 1.
    SentHistoryDepth int
}

type AckMode uint8

const (
    AckReliable AckMode = iota // TCP/WebSocket — auto-advance after send
    AckExplicit                // UDP — advance on explicit client ack
)
```

### ReplicationSystem public API additions

```go
// AckSequence advances the acked baseline for a connection.
// Called when the server receives an ack from the client (UDP mode).
// On AckReliable mode, this is a no-op (baselines auto-advance).
func (s *ReplicationSystem) AckSequence(connID, seq uint32)
```

### ReplicationFrame changes

```go
type ReplicationFrame struct {
    Tick    uint32
    Seq     uint32          // frame sequence number (for client ack tracking)
    Viewer  *ViewerInfo
    Full    []FullPayload   // new or keyframe entities
    Deltas  []DeltaPayload  // entities with delta-encoded changes
    Exited  []uint32
    Removed []uint32
}

type FullPayload struct {
    NetID       uint32
    Type        uint8
    Snapshot    []byte // full snapshot bytes
    InitialData []byte // nil unless IsNew
}

type DeltaPayload struct {
    NetID uint32
    Type  uint8
    Data  []byte // bitmask + changed fields from DeltaEncoder
}

type FrameWriter interface {
    WriteFrame(frame *ReplicationFrame)
}
```

### Update loop per entity

1. `Hash()` -> same as lastHash and not keyframe? -> skip entirely
2. `Snapshot(writer)` -> curr bytes
3. Look up `baselines[netID].acked` for this connection
4. If no acked baseline (entity is new to this viewer):
   - Emit as FullPayload with InitialData
   - Store curr as acked baseline (reliable) or in sent ring buffer (unreliable)
5. If keyframe:
   - Emit as FullPayload (no InitialData)
   - Store curr
6. Else:
   - `deltaEncoder.Encode(acked_baseline, curr, out)` -> delta bytes
   - If nil (identical to acked baseline): skip
   - Emit as DeltaPayload
   - Store curr in sent ring buffer
7. On AckReliable: immediately promote curr to acked baseline
8. On AckExplicit: keep in sent ring buffer until `AckSequence()` is called

### Memory cost

- AckReliable: ~50 bytes * 100 entities * 50 players = 250KB (just acked baseline, same as before)
- AckExplicit: + ring buffer of 32 frames * ~20 changed entities per frame * 50 bytes * 50 players = ~1.6MB additional. Acceptable.

**Files**:

- `pkg/system/replication.go` (modified — new interface, baseline management, ack support)
- `pkg/system/baseline.go` (new — baselineState, connectionState, ring buffer)
- `pkg/system/replication_test.go` (updated)

---

## Step 4: Binary Wire Format + Proto Change

**New file**: `pkg/quantize/wireformat.go`

Binary frame encoder/decoder for delta world updates:

```text
Delta World Update Frame:
[4] tick (uint32)
[4] seq (uint32)            — frame sequence for client ack
[2] fullCount (uint16)
[2] deltaCount (uint16)
[2] removedCount (uint16)
[2] exitedCount (uint16)

Full entities (new/keyframe):
  per entity:
    [4] netID (uint32)
    [1] entityType (uint8)
    [2] snapshotLen (uint16)
    [N] snapshot bytes
    [2] initialDataLen (uint16, 0 if none)
    [M] initial data bytes

Delta entities:
  per entity:
    [4] netID (uint32)
    [1] entityType (uint8)
    [2] deltaLen (uint16)
    [D] bitmask + changed fields

Removed IDs: [4] * removedCount
Exited IDs: [4] * exitedCount
```

Client ack message (client -> server):

```text
[4] ackedSeq (uint32) — most recently fully processed frame sequence
```

Add `MakeEventRaw(code uint32, data []byte) []byte` to `pkg/mmokit/` — wraps raw bytes in ServerEvent envelope.

**Proto changes**:

- Add `SE_DELTA_WORLD_UPDATE` to `enginepb.ServerEventCode`
- Add `CE_ACK_SNAPSHOT` to `enginepb.ClientEventCode` (client ack message)

**Files**:

- `pkg/quantize/wireformat.go`
- `pkg/quantize/wireformat_test.go`
- `pkg/mmokit/mmokit.go` (add MakeEventRaw)
- `proto/enginepb/engine.proto` (add SE_DELTA_WORLD_UPDATE, CE_ACK_SNAPSHOT)
- Regenerate: `make proto`

---

## Step 5: Slither Migration

Migrate slither as the first consumer (simpler, faster iteration). Uses `AckReliable` mode (WebSocket).

### Server-side

Replace existing replicators with new `EntityReplicator` implementations:

**foodReplicator** — snapshot layout (4 fields, 6 bytes):

| Field | Type | Bytes | Notes |
| ----- | ---- | ----- | ----- |
| posX | uint16 | 2 | quantized cell-local via QPos(pos, cellSize) |
| posY | uint16 | 2 | quantized cell-local via QPos(pos, cellSize) |
| value | uint8 | 1 | quantized [0,1]->[0,255] |
| colorIdx | uint8 | 1 | raw |
| **Total** | | **6** | vs ~16 bytes protobuf today |

SnapshotLayout: `[]int{2, 2, 1, 1}`. InitialData: nil.
Food is static after spawn -> delta = 0 bytes per tick after initial send. **Biggest win.**

**snakeReplicator** — snapshot layout (7 fixed fields + var tail):

| Field | Type | Bytes | Notes |
| ----- | ---- | ----- | ----- |
| posX | uint16 | 2 | quantized head position |
| posY | uint16 | 2 | |
| angle | uint16 | 2 | quantized radians |
| speed | uint16 | 2 | quantized with scale |
| mass | uint16 | 2 | quantized with scale |
| skinID | uint8 | 1 | |
| flags | uint8 | 1 | boosting + other bools packed |
| segments | var | 2+4*N | uint16 count + uint16 pairs per segment |
| **Fixed** | | **12** | vs ~28+ bytes protobuf |
| **Per segment** | | **4** | vs 8 bytes protobuf (50% reduction) |

SnapshotLayout: `[]int{2, 2, 2, 2, 2, 1, 1, -1}` (where -1 = variable tail).
InitialData: name encoded as length-prefixed UTF-8 bytes.

### Web client

New TypeScript decoder in `examples/slither/web/src/`:

- Parse binary delta frame header (including seq number)
- For full entities: decode snapshot into local state, store as baseline
- For delta entities: apply bitmask + changed fields to stored baseline (Decode advances baseline)
- Dequantize positions using cellSize from server config
- Send ack with seq of last processed frame (on WebSocket this is optional since server auto-advances, but good practice)
- Client needs cellSize — add to `SlitherSpawnedMsg` or send as connect-time config

### Measurement

Before/after bandwidth measurement:

- Log bytes sent per player per tick (add counter in FrameWriter)
- Target: 500 food static after initial -> ~0 ongoing. 30 snakes at 4 bytes/segment instead of 8.

**Files**:

- `examples/slither/replication.go` (rewrite replicators to new interface)
- `examples/slither/frame_writer.go` (new binary FrameWriter)
- `examples/slither/web/src/delta-decoder.ts` (new)
- `examples/slither/web/src/network.ts` (modified — handle SE_DELTA_WORLD_UPDATE, send ack)

---

## Step 6: Main Game Migration

Same pattern as slither but with more entity types.

**Entity snapshot layouts**:

Ship (~28 bytes fixed):

| Field | Bytes | Notes |
| ----- | ----- | ----- |
| relPosX, relPosY | 4 | int16 pair (viewer-relative, QRel with AoI half-range) |
| velX, velY | 4 | int16 pair |
| rotation | 2 | uint16 |
| health, shield | 2 | uint8 normalized |
| radius | 2 | uint16 |
| flags | 1 | mining active, beam mask |
| combatTarget | 4 | uint32 netID |
| miningTarget | 4 | uint32 netID |
| lockedByID | 4 | uint32 |
| lockedByProgress | 1 | uint8 normalized |

InitialData: pilot name (length-prefixed UTF-8).

Asteroid (~14 bytes): pos, itemID, remaining, radius, lockedBy. InitialData: nil.
Station (~8 bytes): pos, radius. Mostly static — near-zero delta cost. InitialData: station name/type.
NPC (~24 bytes): similar to ship. InitialData: NPC name/type.
LootCrate (~10 bytes): pos, radius, item count. InitialData: item list.

**Files**:

- `internal/system/replication_adapters.go` (rewrite to new interface)
- `internal/system/nethandler_ship.go` (rewrite: Hash + Snapshot + SnapshotLayout + InitialData)
- `internal/system/nethandler_asteroid.go` (same pattern)
- `internal/system/nethandler_npc.go` (same)
- `internal/system/nethandler_station.go` (same)
- `internal/system/nethandler_lootcrate.go` (same)
- `internal/system/nethandler_shared.go` (update shared helpers for snapshot writing)
- `internal/system/frame_writer.go` (new binary FrameWriter)
- Web/Unity client decoders (separate scope)

---

## Verification

1. **Unit tests**: quantization round-trips, delta encoder correctness, wire format encode/decode, baseline management (both ack modes), ring buffer advancement
2. **`go vet ./...`** after each step
3. **Slither build + run**: `cd examples/slither && go build ./...` then `make dev`
4. **Slither web client**: connect in browser, verify gameplay works with delta frames
5. **Main game build**: `make build`
6. **Bandwidth measurement**: add per-tick byte counter logging, compare before/after
7. **Baseline management test**: simulate packet loss scenarios in unit tests (explicit ack mode with delayed acks, verify delta growth and correct baseline advancement)
8. **Expected results**:
   - Slither food: ~0 bytes/tick after initial send (currently ~6KB for 500 food)
   - Slither snakes: ~50% reduction from quantized segments
   - Main game static entities (asteroids, stations): near-zero after initial send
   - Overall: 50-70% bandwidth reduction target

---

## Implementation Order

```text
Step 1: Quantization utilities          — standalone, testable in isolation
Step 2: DeltaEncoder                    — standalone, testable in isolation
Step 3: ReplicationSystem integration   — unified interface, baseline management, ack support
Step 4: Binary wire format + proto      — frame encoder, MakeEventRaw, ack message, proto changes
Step 5: Slither migration               — first consumer (AckReliable mode), end-to-end proof
Step 6: Main game migration             — second consumer, validates generality
```

Steps 1-2 are independent and can be built in parallel. Step 3 depends on both. Step 4 depends on 3. Steps 5-6 depend on 4 and are independent of each other.
