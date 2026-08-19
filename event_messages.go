package mmokit

import "reflect"

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
// FullEntry / DeltaEntry / Removed / Exited bytes + optional processed-input
// acknowledgement trailer). StreamEpoch is encoded after Body and scopes the
// body's frame sequence across replication-source restarts. Body bytes are
// opaque to the reflection codec — the schema generator and reflect codec
// fast-path []byte to a `bytes` encoding (`[u32 len][bytes]`) so the
// payload survives the round-trip without per-byte iteration on the
// client side.
type WorldDelta struct {
	Body        []byte
	StreamEpoch uint32 // session replication generation; scopes frame sequence across restarts
}

// ReplicationAck — client→server acknowledgement of one WorldDelta frame.
//
// StreamEpoch is echoed from WorldDelta.StreamEpoch; Seq is the big-endian
// uint32 at byte offset 4 of WorldDelta.Body (see pkg/quantize/wireformat.go
// for the 20-byte header layout). Both are needed because Seq restarts at one
// when a client's authority moves to another cell, so a delayed ACK from the
// previous stream must not commit the current one.
//
// Only datagram transports need this. The server latches explicit-ACK
// baseline advancement per connection from the transport's static delivery
// class, so reliable-ordered transports (WebSocket) never ask for it — the
// class appears in their generated SDK and is simply unused.
//
// Sending an unsolicited or stale ack is harmless: AckFrame promotes only the
// exact (StreamEpoch, Seq) currently in flight, and a connection may have at
// most one attempt in flight at a time.
type ReplicationAck struct {
	StreamEpoch uint32
	Seq         uint32
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
// that expose cell-coordinate state to clients. The MMOKIT examples no
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
	// SchemaHash is this process's structural protocol fingerprint.
	//
	// The connection-setup gate already refuses a client whose fingerprint
	// disagrees, so this is not the enforcing half. It covers the one case the
	// gate structurally cannot: an OLD server, which ignores the unknown
	// ?schema= query parameter and upgrades happily. Only the client can
	// notice that, and only by being told what the server actually is.
	//
	// It is also the only path that can produce a useful message in a browser.
	// A server-side 409 reaches page script as onclose(1006) with no code and
	// no body; a value here lets the SDK say which two builds disagree.
	SchemaHash uint32
}

// registerEngineTypedEvents registers each engine-level typed event on wire.
// Every game gets them for free.
//
// This was sync.Once-guarded on the stated grounds that "RegisterEvent panics
// on re-registration". It does not — RegisterServerEvent returns early for a
// type already in the set, which is the same property that makes registering
// from System.Init() safe. The guard was harmless while one registry existed
// and would be a bug now: a process-global Once means the second Process's
// registry never gets Pong.
func registerEngineTypedEvents(wire *WireRegistry) {
	wire.RegisterServerEvent(reflect.TypeFor[Pong]())
	wire.RegisterServerEvent(reflect.TypeFor[DebugInfo]())
	wire.RegisterServerEvent(reflect.TypeFor[WorldDelta]())
	wire.RegisterServerEvent(reflect.TypeFor[PlayerEntityAssigned]())
	wire.RegisterServerEvent(reflect.TypeFor[CellChange]())
	wire.RegisterServerEvent(reflect.TypeFor[ServerConfig]())
}
