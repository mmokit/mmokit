package mmokit

import (
	"encoding/json"
	"io"

	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/system"
	"github.com/mmokit/mmokit/pkg/universe"
)

// ---------------------------------------------------------------------------
// Protocol — aggregates the full client/server contract for schema export.
// Games wire in the router + replicators and register typed events via
// mmokit.RegisterEvent[T] / RegisterOp[Req, Res] / HandleClient[T] /
// HandleAll[T]. The resulting ProtocolSchema is exported via
// --dump-schema for client codegen.
// ---------------------------------------------------------------------------

// ProtocolSchema is the complete machine-readable protocol description.
type ProtocolSchema struct {
	Game             string                  `json:"game"`
	Entities         []system.EntitySchema   `json:"entities"`
	BroadcastTypes   []BroadcastTypeSchema   `json:"broadcast_types,omitempty"`
	ClientInputTypes []ClientInputTypeSchema `json:"client_input_types,omitempty"`
	// ServerEventTypes lists every type registered via mmokit.RegisterEvent[T].
	// Wire layout mirrors broadcast types (reflection codec); sdkgen emits a
	// TS class per entry plus a client.onXxx(handler) method that subscribes
	// against the typed-event dispatcher.
	ServerEventTypes []ServerEventTypeSchema `json:"server_event_types,omitempty"`
	// Operations lists every RegisterOp[Req, Res any] registration. Sdkgen
	// emits operations.ts (per-op Request + Response classes) plus a Promise
	// correlator on the generated <Game>Client.
	Operations []OperationSchema `json:"operations,omitempty"`
}

// ServerEventTypeSchema describes a typed server→client event registered via
// mmokit.RegisterEvent[T]. Same shape as BroadcastTypeSchema (reflection-codec
// wire layout); aliased to keep JSON output identical and to share the field-
// encoding logic. Sdkgen emits a TS class with a static `decode(buf)` method
// mirror of the broadcast classes, plus a client.onXxx(handler) wrapper.
type ServerEventTypeSchema = BroadcastTypeSchema

// OperationSchema describes one RegisterOp[Req, Res any] registration.
// Both Request + Response use the same reflection-codec wire layout as
// broadcasts/server-events/client-inputs (reuses BroadcastFieldSchema for
// the per-field shape). Sdkgen consumes this section to emit operations.ts:
// per-op Request class (with encode() instance method) + Response class
// (with static decode), plus a client.<opName>(req): Promise<Res> wrapper.
type OperationSchema struct {
	Kind             string                 `json:"kind"` // "gateway-local" or "player-cell"
	RequestTypeID    uint32                 `json:"request_type_id"`
	RequestTypeName  string                 `json:"request_type_name"`
	RequestFields    []BroadcastFieldSchema `json:"request_fields"`
	ResponseTypeID   uint32                 `json:"response_type_id"`
	ResponseTypeName string                 `json:"response_type_name"`
	ResponseFields   []BroadcastFieldSchema `json:"response_fields"`
}

// Protocol collects the full client/server contract for a game.
type Protocol struct {
	game        string
	wire        *WireRegistry
	entityNames []entityNameEntry
	replicators *system.ReplicatorRegistry
}

// NewProtocol creates a Protocol exporting p's registry under the given game
// name. The universal engine-level typed events (Pong, DebugInfo, WorldDelta,
// PlayerEntityAssigned, CellChange, ServerConfig) and the universal
// client→server Ping input are already on that registry: New installs them via
// bootstrapWire before calling this. Games never wire them themselves.
func NewProtocol(p *Process, game string) *Protocol {
	return &Protocol{game: game, wire: p.Wire()}
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
	ps := ProtocolSchema{Game: p.game}
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
	// Broadcast-eligible typed messages: every type registered via
	// HandleAll[T] gets a schema entry here so sdkgen can emit a matching
	// TS class + decoder. HandleAllInternal[T] types are excluded.
	for _, t := range p.wire.BroadcastTypes() {
		ps.BroadcastTypes = append(ps.BroadcastTypes, BroadcastTypeOf(t))
	}
	// Client-input types: every type registered via HandleClient[T] gets a
	// schema entry here so sdkgen can emit a matching TS class with an
	// encode() instance method + a static typeID.
	for _, t := range p.wire.ClientInputTypes() {
		ps.ClientInputTypes = append(ps.ClientInputTypes, ClientInputTypeOf(t))
	}
	// Typed server events: every type registered via RegisterEvent[T] gets a
	// schema entry here so sdkgen can emit a matching TS class with a static
	// decode(buf) method + a client.onXxx(handler) wrapper. Wire layout
	// reuses BroadcastTypeOf (the reflection codec is identical to the
	// broadcast/client-input path; only the dispatch direction differs).
	for _, t := range p.wire.ServerEventTypes() {
		ps.ServerEventTypes = append(ps.ServerEventTypes, BroadcastTypeOf(t))
	}
	// Operations: every RegisterOp[Req, Res any] entry exposes its
	// request + response types here. Sdkgen emits operations.ts (per-op
	// Request/Response classes) + a Promise correlator on the generated
	// client. Wire layout reuses BroadcastTypeOf for both halves.
	for _, e := range p.wire.TypedOps() {
		reqType := BroadcastTypeOf(e.RequestType)
		resType := BroadcastTypeOf(e.ResponseType)
		ps.Operations = append(ps.Operations, OperationSchema{
			Kind:             e.Kind.String(),
			RequestTypeID:    TypeIDOf(e.RequestType),
			RequestTypeName:  e.RequestType.String(),
			RequestFields:    reqType.Fields,
			ResponseTypeID:   e.ResponseTypeID,
			ResponseTypeName: e.ResponseType.String(),
			ResponseFields:   resType.Fields,
		})
	}
	return ps
}

// WriteSchema writes the protocol schema as JSON to w.
func (p *Protocol) WriteSchema(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(p.Schema())
}

// AssembleFromProcess hydrates the Protocol with runtime-discovered
// entity replicators from any cell's EntityKindDefs. Client-input types
// (HandleClient[T]) and typed operations (RegisterOp[Req, Res]) are
// harvested in Schema() from the process's own WireRegistry.
//
// Called by the engine's --dump-schema path after Build() has populated
// every registry but before Start has begun the game loop.
func (p *Protocol) AssembleFromProcess(proc *universe.Process) {
	// Entity replicators: build from the first cell's EntityKindDefs against
	// a throwaway ECS world. The dump path doesn't run the game loop, so the
	// registry only needs schema metadata, not real entities.
	for _, cell := range proc.Cells {
		defs := cell.Stage.EntityKindDefs()
		if len(defs) == 0 {
			continue
		}
		defSlice := make([]universe.EntityKindDef, 0, len(defs))
		for _, def := range defs {
			defSlice = append(defSlice, *def)
			p.EntityName(def.Kind, def.Name)
		}
		w := ecs.NewWorld()
		p.SetReplicators(BuildReplicators(w, proc, defSlice...))
		break
	}
}
