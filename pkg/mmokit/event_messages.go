package mmokit

import "sync"

// Engine-level typed server→client event messages — registered via
// RegisterEvent[T] and emitted with SendEvent. Replaces the legacy
// enginepb.* protobuf-envelope path on the 0x00 channel.
//
// Wire format: pkguniverse.EncodeTypedEventFrame — fields encoded in source
// declaration order, little-endian, no padding (mirrors the codec defined in
// pkg/universe/reflect_marshal.go). String fields are length-prefixed (uint16
// LE length + UTF-8 bytes).
//
// These mirror the engine's universal event payloads. NewProtocol auto-
// registers every type below alongside the legacy proto registrations so
// games pick them up for free.

// Pong — server response to a client Ping. Carries both timestamps so the
// client can compute one-way latency. Replaces enginepb.PongMsg.
type Pong struct {
	ClientTime int64 // echoed back from the matching Ping
	ServerTime int64 // server clock at the moment Pong was built
}

// CellInfo — per-cell layout for debug overlays.
// Replaces enginepb.CellInfo.
type CellInfo struct {
	CellX   int32
	CellY   int32
	Depth   uint32
	Size    float32
	OriginX float32
	OriginY float32
	NodeID  string
}

// CellTopology — full cluster cell layout for the debug overlay.
// Replaces enginepb.CellTopologyMsg. Sent at most once per topology
// change, gated by DebugTopology flag.
type CellTopology struct {
	Cells        []CellInfo
	GridW        int32
	GridH        int32
	BaseCellSize float32
}

// DebugInfo — per-player debug-overlay payload, gated by DebugFlags
// bits. Empty Topology.Cells means topology is not currently enabled
// (or topology is empty); AoIRadius == 0 means the AoI overlay is off.
// Replaces enginepb.DebugInfoMsg.
type DebugInfo struct {
	Topology  CellTopology
	AoIRadius float32
}

// WorldDelta — per-tick entity-state delta. Body is the custom binary
// frame produced by pkg/quantize.FrameEncoder (20-byte header + per-entity
// FullEntry / DeltaEntry / Removed / Exited bytes). Body bytes are opaque
// to the reflection codec — the schema generator and reflect codec
// fast-path []byte to a `bytes` encoding (`[u32 len][bytes]`) so the
// payload survives the round-trip without per-byte iteration on the
// client side. Replaces the legacy SE_DELTA_WORLD_UPDATE proto-envelope
// path.
type WorldDelta struct {
	Body []byte
}

// registerEngineTypedEvents registers each engine-level typed event
// exactly once, regardless of how many Protocol instances the process
// creates. NewProtocol calls this on every invocation; the sync.Once
// makes repeated calls safe (RegisterEvent panics on re-registration).
func registerEngineTypedEvents() {
	engineTypedEventsOnce.Do(func() {
		RegisterEvent[Pong]()
		RegisterEvent[DebugInfo]()
		RegisterEvent[WorldDelta]()
	})
}

var engineTypedEventsOnce sync.Once
