package mmokit

import (
	"sort"

	"google.golang.org/protobuf/proto"

	"github.com/zenion/mmoserver/pkg/engine"
)

// ClientEvents is a typed registry of client→server event codes mapped to
// their proto payload types — declared by games for events that bypass the
// runtime InputRouter (e.g. CE_LOGIN handled by LoginHandler, CE_PING handled
// inline by EventInterceptor) or that were registered via low-level
// router.Handle without proto-name capture. Schema-export only; clients use
// the metadata to construct outbound messages.
type ClientEvents struct {
	entries map[uint32]clientEventEntry
}

type clientEventEntry struct {
	code      uint32
	protoName string
	enumName  string
}

// NewClientEvents creates an empty client-event registry.
func NewClientEvents() *ClientEvents {
	return &ClientEvents{entries: make(map[uint32]clientEventEntry)}
}

// RegisterClientEvent declares a client→server event with its proto payload
// type. Later registrations replace earlier ones (last-wins) so games can
// override the engine-default registrations installed by NewProtocol.
func RegisterClientEvent[T any, P interface {
	*T
	proto.Message
}, C engine.EventCode](e *ClientEvents, code C) {
	var zero P = new(T)
	enumName := enumConstantName(code)
	entry := clientEventEntry{
		code:      uint32(code),
		protoName: string(proto.MessageName(zero)),
		enumName:  enumName,
	}
	// Last registration wins — lets games override engine-default
	// registrations installed by NewProtocol.
	e.entries[entry.code] = entry
}

// Schema returns registered events sorted by code.
func (e *ClientEvents) Schema() []engine.ClientEventSchema {
	out := make([]engine.ClientEventSchema, 0, len(e.entries))
	for _, entry := range e.entries {
		out = append(out, engine.ClientEventSchema{
			Code:      entry.code,
			ProtoName: entry.protoName,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}
