package mmokit

import "sync"

// Engine-level typed server→client event messages — registered via
// RegisterEvent[T] and emitted with SendEvent. Channel 0x00 carries
// the typed reflection-codec wire format only; there is no protobuf
// envelope.
//
// Wire format: pkguniverse.EncodeTypedEventFrame — fields encoded in source
// declaration order, little-endian, no padding (mirrors the codec defined in
// pkg/universe/reflect_marshal.go). String fields are length-prefixed (uint16
// LE length + UTF-8 bytes).
//
// NewProtocol auto-registers every type below so games pick them up for
// free.

// Ping — client→server liveness check. Server responds with a typed Pong
// echoing the client timestamp. The engine registers a default
// HandleClient[Ping] handler via universe.New that sends the Pong reply
// automatically; games never need to wire Ping themselves.
type Ping struct {
	ClientTime int64 // millisecond client clock; echoed back in Pong.ClientTime
}

// Pong — server response to a client Ping. Carries both timestamps so the
// client can compute one-way latency.
type Pong struct {
	ClientTime int64 // echoed back from the matching Ping
	ServerTime int64 // server clock at the moment Pong was built
}

// CellInfo — per-cell layout for debug overlays.
type CellInfo struct {
	CellX   int32
	CellY   int32
	Depth   uint32
	Size    float32
	OriginX float32
	OriginY float32
	NodeID  string
}

// CellTopology — full cluster cell layout for the debug overlay. Sent at
// most once per topology change, gated by DebugTopology flag.
type CellTopology struct {
	Cells        []CellInfo
	GridW        int32
	GridH        int32
	BaseCellSize float32
}

// DebugInfo — per-player debug-overlay payload, gated by DebugFlags
// bits. Empty Topology.Cells means topology is not currently enabled
// (or topology is empty); AoIRadius == 0 means the AoI overlay is off.
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
// client side.
type WorldDelta struct {
	Body []byte
}

// PlayerEntityAssigned — sent once per session right after the player's
// entity is spawned, telling the client its authoritative entity NetID and
// world-space position.
type PlayerEntityAssigned struct {
	EntityNetID uint32
	WorldX      float32
	WorldY      float32
}

// CellChange — informs the client that its authoritative entity has
// migrated to a different cell. Reserved framework hint; emitted by games
// that expose cell-coordinate state to clients. The mmoserver examples no
// longer emit this event — the topology-transparent protocol handles
// cross-cell handoffs without a dedicated client-visible message — but
// the type stays available for games that want to surface cell
// coordinates.
type CellChange struct {
	CellX int32
	CellY int32
}

// ServerConfig — sent on connect with engine-level configuration the
// client needs to drive its tick-based interpolation math. Emitted by the
// gateway on every successful connect handshake.
type ServerConfig struct {
	TickRate uint32
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
		RegisterEvent[PlayerEntityAssigned]()
		RegisterEvent[CellChange]()
		RegisterEvent[ServerConfig]()
	})
}

var engineTypedEventsOnce sync.Once
