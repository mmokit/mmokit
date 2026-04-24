package mmokit

import (
	"fmt"
	"reflect"
	"sort"

	"google.golang.org/protobuf/proto"

	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/net"
)

// ServerEvents is a typed registry of server→client event codes mapped to
// their proto payload types. Each (code, protoType) pair is declared once at
// game wiring time and consumed by both the runtime emit path (Build/Send)
// and the schema dump (Schema). Validates at Build that the caller's payload
// matches the registered type — eliminates the silent drift that ad-hoc
// MakeEvent calls allow today.
type ServerEvents struct {
	entries map[uint32]serverEventEntry
}

type serverEventEntry struct {
	code      uint32
	name      string
	protoName string
	protoType reflect.Type // pointer type, e.g. *enginepb.SpawnedMsg
	enumName  string       // raw enum constant for diagnostics
}

// ServerEventOption customizes a server-event registration.
type ServerEventOption func(*serverEventEntry)

// WithEventName overrides the auto-derived camelCase name. Use when the
// derived name collides or reads poorly.
func WithEventName(name string) ServerEventOption {
	return func(e *serverEventEntry) { e.name = name }
}

// NewServerEvents creates an empty server-event registry.
func NewServerEvents() *ServerEvents {
	return &ServerEvents{entries: make(map[uint32]serverEventEntry)}
}

// RegisterServerEvent declares a server→client event with its proto payload
// type. Panics on duplicate code. Name auto-derives from the enum constant
// (SE_PLAYER_SPAWNED → "playerSpawned"); override via WithEventName.
func RegisterServerEvent[T any, P interface {
	*T
	proto.Message
}, C engine.EventCode](e *ServerEvents, code C, opts ...ServerEventOption) {
	var zero P = new(T)
	enumName := enumConstantName(code)
	entry := serverEventEntry{
		code:      uint32(code),
		name:      deriveEventName(enumName),
		protoName: string(proto.MessageName(zero)),
		protoType: reflect.TypeOf(zero),
		enumName:  enumName,
	}
	for _, opt := range opts {
		opt(&entry)
	}
	if existing, ok := e.entries[entry.code]; ok {
		panic(fmt.Sprintf("ServerEvents: duplicate registration for code %d (%s and %s)",
			entry.code, existing.enumName, entry.enumName))
	}
	e.entries[entry.code] = entry
}

// Build marshals msg, validates it matches the registered type for code, and
// returns a channel-0x00 wire frame. Panics if the code wasn't registered or
// if the payload type doesn't match. Use when broadcasting a single frame to
// many connections.
func (e *ServerEvents) Build(code uint32, msg proto.Message) []byte {
	entry, ok := e.entries[code]
	if !ok {
		panic(fmt.Sprintf("ServerEvents: code %d not registered — call RegisterServerEvent first", code))
	}
	if got := reflect.TypeOf(msg); got != entry.protoType {
		panic(fmt.Sprintf("ServerEvents: code %d (%s) registered as %v, but Build called with %v",
			code, entry.enumName, entry.protoType, got))
	}
	return MakeEvent(code, msg)
}

// Send builds the frame and writes it to the given connection. Convenience
// wrapper for the common single-recipient case.
func (e *ServerEvents) Send(connMgr *net.ConnManager, connID uint32, code uint32, msg proto.Message) {
	frame := e.Build(code, msg)
	if frame != nil {
		connMgr.SendReliable(connID, frame)
	}
}

// Schema returns the registered events as a deterministically-ordered slice
// (sorted by code) for schema export.
func (e *ServerEvents) Schema() []ServerEventSchema {
	out := make([]ServerEventSchema, 0, len(e.entries))
	for _, entry := range e.entries {
		out = append(out, ServerEventSchema{
			Code:      entry.code,
			Name:      entry.name,
			ProtoName: entry.protoName,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// enumConstantName returns the proto enum constant name (e.g. "SE_PLAYER_SPAWNED")
// for a typed enum value. Falls back to the numeric form if the type doesn't
// implement the Stringer interface (which all proto enums do via their generated
// String() method).
func enumConstantName[C engine.EventCode](code C) string {
	type stringer interface{ String() string }
	if s, ok := any(code).(stringer); ok {
		return s.String()
	}
	return fmt.Sprintf("%d", code)
}
