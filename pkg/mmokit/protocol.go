package mmokit

import (
	"encoding/json"
	"io"

	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/system"
)

// ---------------------------------------------------------------------------
// Protocol — aggregates the full client/server contract for schema export.
// Games register their server events and wire in the router + replicators.
// The resulting ProtocolSchema is exported via --dump-schema for client codegen.
// ---------------------------------------------------------------------------

// ServerEventSchema describes one server→client event for schema export.
type ServerEventSchema struct {
	Code      uint32 `json:"code"`
	Name      string `json:"name"`      // camelCase method name hint (e.g. "playerSpawned")
	ProtoName string `json:"protoName"` // fully qualified proto name; empty = custom binary
}

// OperationSchema describes one request/response operation for schema export.
type OperationSchema struct {
	Code          uint32 `json:"code"`
	Name          string `json:"name"`
	RequestProto  string `json:"requestProto"`
	ResponseProto string `json:"responseProto"`
}

// ProtocolSchema is the complete machine-readable protocol description.
type ProtocolSchema struct {
	Game         string                `json:"game"`
	ClientEvents []engine.ClientEventSchema `json:"clientEvents"`
	ServerEvents []ServerEventSchema   `json:"serverEvents"`
	Entities     []system.EntitySchema `json:"entities"`
	Operations   []OperationSchema     `json:"operations,omitempty"`
}

// Protocol collects the full client/server contract for a game.
type Protocol struct {
	game         string
	clientEvents []engine.ClientEventSchema
	serverEvents []ServerEventSchema
	operations   []OperationSchema
	entityNames  []entityNameEntry
	router       *engine.InputRouter
	replicators  *system.ReplicatorRegistry
}

// NewProtocol creates a Protocol with the given game name.
func NewProtocol(game string) *Protocol {
	return &Protocol{game: game}
}

// ClientEvent registers a client→server event manually (bypassing InputRouter).
// Use this when building schema without a running InputRouter.
func ClientEvent[C engine.EventCode](p *Protocol, code C, protoName string) {
	p.clientEvents = append(p.clientEvents, engine.ClientEventSchema{
		Code:      uint32(code),
		ProtoName: protoName,
	})
}

// ServerEvent registers a server→client event. Pass an empty protoName for
// binary-encoded events (e.g. SE_DELTA_WORLD_UPDATE).
func ServerEvent[C engine.EventCode](p *Protocol, code C, name string, protoName string) {
	p.serverEvents = append(p.serverEvents, ServerEventSchema{
		Code:      uint32(code),
		Name:      name,
		ProtoName: protoName,
	})
}

// Operation registers a request/response operation.
func Operation[C engine.EventCode](p *Protocol, code C, name string, requestProto, responseProto string) {
	p.operations = append(p.operations, OperationSchema{
		Code:          uint32(code),
		Name:          name,
		RequestProto:  requestProto,
		ResponseProto: responseProto,
	})
}

// SetRouter wires in the InputRouter for client event schema extraction.
func (p *Protocol) SetRouter(r *engine.InputRouter) {
	p.router = r
}

// SetReplicators wires in the ReplicatorRegistry for entity schema extraction.
func (p *Protocol) SetReplicators(r *system.ReplicatorRegistry) {
	p.replicators = r
}

// EntityName sets a display name for an entity kind (used in generated code).
func (p *Protocol) EntityName(kind uint8, name string) {
	p.entityNames = append(p.entityNames, entityNameEntry{kind: kind, name: name})
}

type entityNameEntry struct {
	kind uint8
	name string
}

// Schema builds the complete ProtocolSchema from all registered sources.
func (p *Protocol) Schema() ProtocolSchema {
	ps := ProtocolSchema{
		Game:         p.game,
		ClientEvents: p.clientEvents,
		ServerEvents: p.serverEvents,
		Operations:   p.operations,
	}
	if p.router != nil {
		ps.ClientEvents = append(ps.ClientEvents, p.router.Schema()...)
	}
	if p.replicators != nil {
		ps.Entities = p.replicators.Schema()
		// Apply entity names.
		for i := range ps.Entities {
			for _, en := range p.entityNames {
				if ps.Entities[i].Kind == en.kind {
					ps.Entities[i].Name = en.name
				}
			}
		}
	}
	return ps
}

// WriteSchema writes the protocol schema as JSON to w.
func (p *Protocol) WriteSchema(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(p.Schema())
}
