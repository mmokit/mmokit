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

// LoginRejected — terminal login failure. The client should display the
// reason and return to the login screen. Replaces enginepb.LoginRejectedMsg.
type LoginRejected struct {
	Reason string
}

// registerEngineTypedEvents registers each engine-level typed event
// exactly once, regardless of how many Protocol instances the process
// creates. NewProtocol calls this on every invocation; the sync.Once
// makes repeated calls safe (RegisterEvent panics on re-registration).
func registerEngineTypedEvents() {
	engineTypedEventsOnce.Do(func() {
		RegisterEvent[Pong]()
		RegisterEvent[LoginRejected]()
	})
}

var engineTypedEventsOnce sync.Once
