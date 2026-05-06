package mmokit

import (
	"reflect"
	"sort"

	"google.golang.org/protobuf/proto"

	"github.com/zenion/mmoserver/pkg/engine"
)

// ServerEvents is a typed registry of server→client event codes mapped to
// their proto payload types. After Plan 1 Phase 7 it is purely a schema
// hint surface for the SDK generator — every game-specific server event
// rides the typed reflection-codec channel via mmokit.RegisterEvent[T].
// The protobuf-residue cleanup Phase 1 migrated every engine-default
// framework event off this registry to typed reflection-codec events
// (PlayerEntityAssigned, CellChange, ServerConfig — see
// pkg/mmokit/event_messages.go). The registry remains as a future hook
// for any game that wants to expose proto-encoded events on a
// game-defined ServerEventCode, though no engine-level code uses it now.
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
// type. Later registrations replace earlier ones (last-wins) so games can
// override the engine-default registrations installed by NewProtocol. Name
// auto-derives from the enum constant (SE_PLAYER_SPAWNED → "playerSpawned");
// override via WithEventName.
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
	// Last registration wins — lets games override engine-default
	// registrations installed by NewProtocol (e.g. replacing
	// enginepb.SpawnedMsg with a richer game-specific payload).
	e.entries[entry.code] = entry
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

