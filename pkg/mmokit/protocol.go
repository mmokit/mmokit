package mmokit

import (
	"encoding/json"
	"io"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
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
	Game         string                     `json:"game"`
	ClientEvents []engine.ClientEventSchema `json:"clientEvents"`
	ServerEvents []ServerEventSchema        `json:"serverEvents"`
	Entities     []system.EntitySchema      `json:"entities"`
	Operations   []OperationSchema          `json:"operations,omitempty"`
	// ClientRenderMode tells client SDK generators which rendering
	// path to emit. Mirrors Config.ClientRenderMode. "interpolated"
	// or "snap".
	ClientRenderMode ClientRenderMode `json:"clientRenderMode"`
}

// Protocol collects the full client/server contract for a game.
type Protocol struct {
	game             string
	clientEvents     []engine.ClientEventSchema
	serverEvents     []ServerEventSchema
	operations       []OperationSchema
	entityNames      []entityNameEntry
	router           *engine.InputRouter
	replicators      *system.ReplicatorRegistry
	clientRenderMode ClientRenderMode
}

// NewProtocol creates a Protocol with the given game name.
// Engine-level server events (e.g. SE_SERVER_CONFIG) are auto-registered.
// ClientRenderMode defaults to ClientRenderSnap; games override via
// SetClientRenderMode to mirror their Config.ClientRenderMode.
func NewProtocol(game string) *Protocol {
	p := &Protocol{game: game, clientRenderMode: ClientRenderSnap}
	ServerEvent(p, enginepb.ServerEventCode_SE_SERVER_CONFIG, "serverConfig", "enginepb.ServerConfigMsg")
	return p
}

// SetClientRenderMode records the client render mode to emit on the
// exported schema. Games mirror Config.ClientRenderMode here so the
// generated TypeScript SDK inherits the same rendering contract.
func (p *Protocol) SetClientRenderMode(mode ClientRenderMode) {
	if mode == "" {
		mode = ClientRenderSnap
	}
	p.clientRenderMode = mode
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
	mode := p.clientRenderMode
	if mode == "" {
		mode = ClientRenderSnap
	}
	ps := ProtocolSchema{
		Game:             p.game,
		ClientEvents:     p.clientEvents,
		ServerEvents:     p.serverEvents,
		Operations:       p.operations,
		ClientRenderMode: mode,
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
