package mmokit

import (
	"encoding/json"
	"io"

	"github.com/mlange-42/ark/ecs"
	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/ops"
	"github.com/zenion/mmoserver/pkg/system"
	"github.com/zenion/mmoserver/pkg/universe"
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
	// serverEventsRegistry holds the typed server-event registry when the
	// game uses Protocol.ServerEvents(fn) to declare its events. Schema()
	// pulls from here when set; manual registrations via ServerEvent (the
	// legacy function) are still appended for the migration window.
	serverEventsRegistry *ServerEvents
	// clientEventsRegistry holds the typed client-event registry when the
	// game uses Protocol.ClientEvents(fn) to declare events that bypass the
	// runtime InputRouter (e.g. CE_LOGIN, CE_PING) or are registered via
	// low-level router.Handle without proto-name capture.
	clientEventsRegistry *ClientEvents
}

// NewProtocol creates a Protocol with the given game name.
// Engine-level server events (SE_SERVER_CONFIG, SE_DELTA_WORLD_UPDATE) are
// auto-registered — every game gets them for free. ClientRenderMode defaults
// to ClientRenderSnap; games override via SetClientRenderMode to mirror
// their Config.ClientRenderMode.
func NewProtocol(game string) *Protocol {
	p := &Protocol{game: game, clientRenderMode: ClientRenderSnap}
	ServerEvent(p, enginepb.ServerEventCode_SE_SERVER_CONFIG, "serverConfig", "enginepb.ServerConfigMsg")
	// SE_DELTA_WORLD_UPDATE is the engine's binary delta channel — emitted by
	// the BinaryFrameWriter, never via ServerEvents.Send. Empty protoName
	// signals the SDK generator to emit a binary-decoder method instead of
	// a proto Subscribe wrapper.
	ServerEvent(p, enginepb.ServerEventCode_SE_DELTA_WORLD_UPDATE, "deltaWorldUpdate", "")
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

// ServerEvents declares the server→client events for this protocol.
// The callback receives a fresh registry; register every event your game
// emits via RegisterServerEvent[T]. Returns the protocol for chaining.
func (p *Protocol) ServerEvents(fn func(*ServerEvents)) *Protocol {
	if p.serverEventsRegistry == nil {
		p.serverEventsRegistry = NewServerEvents()
	}
	fn(p.serverEventsRegistry)
	return p
}

// ServerEventsRegistry returns the underlying registry — used by the engine
// at runtime emit time. Games normally don't call this directly.
func (p *Protocol) ServerEventsRegistry() *ServerEvents {
	return p.serverEventsRegistry
}

// ClientEvents declares the client→server events that bypass the runtime
// InputRouter or were registered via low-level router.Handle without
// proto-name capture. The callback receives a fresh registry; register every
// such event via RegisterClientEvent[T]. Returns the protocol for chaining.
func (p *Protocol) ClientEvents(fn func(*ClientEvents)) *Protocol {
	if p.clientEventsRegistry == nil {
		p.clientEventsRegistry = NewClientEvents()
	}
	fn(p.clientEventsRegistry)
	return p
}

// ClientEventsRegistry returns the underlying client-event registry.
// Games normally don't call this directly.
func (p *Protocol) ClientEventsRegistry() *ClientEvents {
	return p.clientEventsRegistry
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
	serverEvents := p.serverEvents
	if p.serverEventsRegistry != nil {
		// Registry-sourced events take precedence; manual entries (from the
		// legacy ServerEvent function) appended for the migration window
		// until every game declares via the registry.
		serverEvents = append(p.serverEventsRegistry.Schema(), p.serverEvents...)
	}
	ps := ProtocolSchema{
		Game:             p.game,
		ClientEvents:     p.clientEvents,
		ServerEvents:     serverEvents,
		Operations:       p.operations,
		ClientRenderMode: mode,
	}
	// Merge client events: registry entries (with typed proto names) take
	// precedence. Router entries for codes already declared in the registry
	// are skipped to avoid duplicate codes in the schema output.
	registryCodes := make(map[uint32]struct{})
	if p.clientEventsRegistry != nil {
		for _, ev := range p.clientEventsRegistry.Schema() {
			registryCodes[ev.Code] = struct{}{}
		}
		ps.ClientEvents = append(ps.ClientEvents, p.clientEventsRegistry.Schema()...)
	}
	if p.router != nil {
		for _, ev := range p.router.Schema() {
			if _, skip := registryCodes[ev.Code]; skip {
				continue
			}
			ps.ClientEvents = append(ps.ClientEvents, ev)
		}
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

// AssembleFromProcess hydrates the Protocol with runtime-discovered registries:
// client events from the process's InputRouter (any cell's router suffices —
// all cells in the same world register the same handlers), operations from
// the OpRouter, and entity replicators from any cell's EntityKindDefs.
//
// Called by the engine's --dump-schema path after Build() has populated
// every registry but before Start has begun the game loop.
func (p *Protocol) AssembleFromProcess(proc *universe.Process) {
	if r := proc.AnyInputRouter(); r != nil {
		p.SetRouter(r)
	}
	if op := proc.OpRouter(); op != nil {
		p.operations = append(p.operations, fromOpsSchemas(op.Schema())...)
	}
	// Entity replicators: build from the first cell's EntityKindDefs against
	// a throwaway ECS world. The dump path doesn't run the game loop, so the
	// registry only needs schema metadata, not real entities.
	for _, cell := range proc.Cells {
		defs := cell.Base.EntityKindDefs()
		if len(defs) == 0 {
			continue
		}
		defSlice := make([]universe.EntityKindDef, 0, len(defs))
		for _, def := range defs {
			defSlice = append(defSlice, *def)
			p.EntityName(def.Kind, def.Name)
		}
		w := ecs.NewWorld()
		p.SetReplicators(BuildReplicators(w, nil, defSlice...))
		break
	}
}

// fromOpsSchemas converts ops.OperationSchema to mmokit.OperationSchema. They
// have identical layout but distinct Go types (ops can't import mmokit).
func fromOpsSchemas(in []ops.OperationSchema) []OperationSchema {
	out := make([]OperationSchema, len(in))
	for i, s := range in {
		out[i] = OperationSchema(s)
	}
	return out
}
