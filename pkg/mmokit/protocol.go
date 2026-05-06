package mmokit

import (
	"encoding/json"
	"io"
	"sort"

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
	Game             string                     `json:"game"`
	ClientEvents     []engine.ClientEventSchema `json:"clientEvents"`
	ServerEvents     []ServerEventSchema        `json:"serverEvents"`
	Entities         []system.EntitySchema      `json:"entities"`
	Operations       []OperationSchema          `json:"operations,omitempty"`
	BroadcastTypes   []BroadcastTypeSchema      `json:"broadcast_types,omitempty"`
	ClientInputTypes []ClientInputTypeSchema    `json:"client_input_types,omitempty"`
	// ServerEventTypes lists every type registered via mmokit.RegisterEvent[T].
	// Wire layout mirrors broadcast types (reflection codec); sdkgen emits a
	// TS class per entry plus a client.onXxx(handler) method that subscribes
	// against the typed-event dispatcher. Empty until games migrate from the
	// proto-based ServerEvents path to the typed RegisterEvent[T] path; until
	// then sdkgen emits zero per-event code from this section.
	ServerEventTypes []ServerEventTypeSchema `json:"server_event_types,omitempty"`
}

// ServerEventTypeSchema describes a typed server→client event registered via
// mmokit.RegisterEvent[T]. Same shape as BroadcastTypeSchema (reflection-codec
// wire layout); aliased to keep JSON output identical and to share the field-
// encoding logic. Sdkgen emits a TS class with a static `decode(buf)` method
// mirror of the broadcast classes, plus a client.onXxx(handler) wrapper.
type ServerEventTypeSchema = BroadcastTypeSchema

// Protocol collects the full client/server contract for a game.
type Protocol struct {
	game         string
	clientEvents []engine.ClientEventSchema
	serverEvents []ServerEventSchema
	operations   []OperationSchema
	entityNames  []entityNameEntry
	replicators  *system.ReplicatorRegistry
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

// NewProtocol creates a Protocol with the given game name. Universal
// engine-level events are auto-registered — every game gets them for
// free. Games can override any auto-registered event by calling
// RegisterServerEvent / RegisterClientEvent for the same code with a
// different payload type (e.g. the space game overrides
// SE_PLAYER_SPAWNED with gamepb.PlayerSpawnedMsg to ship
// inventory/equipment alongside the spawn).
func NewProtocol(game string) *Protocol {
	p := &Protocol{
		game:                 game,
		serverEventsRegistry: NewServerEvents(),
		clientEventsRegistry: NewClientEvents(),
	}
	// Universal server→client events — engine-defined codes with
	// engine-defined payload shapes that every game uses as-is.
	RegisterServerEvent[enginepb.ServerConfigMsg](p.serverEventsRegistry, enginepb.ServerEventCode_SE_SERVER_CONFIG)
	RegisterServerEvent[enginepb.PongMsg](p.serverEventsRegistry, enginepb.ServerEventCode_SE_PONG)
	registerEngineTypedEvents()
	RegisterServerEvent[enginepb.LoginRejectedMsg](p.serverEventsRegistry, enginepb.ServerEventCode_SE_LOGIN_REJECTED)
	RegisterServerEvent[enginepb.CellChangeMsg](p.serverEventsRegistry, enginepb.ServerEventCode_SE_CELL_CHANGE)
	RegisterServerEvent[enginepb.DebugInfoMsg](p.serverEventsRegistry, enginepb.ServerEventCode_SE_DEBUG_INFO)
	// SE_PLAYER_SPAWNED default is the engine's bare SpawnedMsg. Games
	// like the space shooter override this via RegisterServerEvent with
	// their own richer payload.
	RegisterServerEvent[enginepb.SpawnedMsg](p.serverEventsRegistry, enginepb.ServerEventCode_SE_PLAYER_SPAWNED)

	// Universal client→server events (CE_PING is handled inline in the
	// engine's EventInterceptor; registering it here exposes it on the
	// generated SDK's client surface).
	RegisterClientEvent[enginepb.PingMsg](p.clientEventsRegistry, enginepb.ClientEventCode_CE_PING)

	// SE_DELTA_WORLD_UPDATE is gone — per-tick replication migrated to the
	// typed mmokit.WorldDelta event (registered in event_messages.go's
	// registerEngineTypedEvents()). The SDK now emits onWorldDelta(handler).
	return p
}

// ServerEvents declares the server→client events for this protocol.
// The callback receives the registry pre-populated with engine defaults;
// register game-specific events or override engine defaults via
// RegisterServerEvent[T]. Returns the protocol for chaining.
func (p *Protocol) ServerEvents(fn func(*ServerEvents)) *Protocol {
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
// proto-name capture. The callback receives the registry pre-populated
// with engine defaults; register game-specific events or override engine
// defaults via RegisterClientEvent[T]. Returns the protocol for chaining.
func (p *Protocol) ClientEvents(fn func(*ClientEvents)) *Protocol {
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
// custom binary-encoded events (the SDK generator emits a binary-decoder
// method instead of a proto Subscribe wrapper). The engine itself no
// longer uses this path — per-tick replication moved to the typed
// mmokit.WorldDelta event in Phase 4 — but games may still emit custom
// binary frames over a ServerEventCode this way.
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
	serverEvents := p.serverEvents
	if p.serverEventsRegistry != nil {
		// Registry-sourced events take precedence; manual entries (from the
		// legacy ServerEvent function) appended for the migration window
		// until every game declares via the registry.
		serverEvents = append(p.serverEventsRegistry.Schema(), p.serverEvents...)
	}
	ps := ProtocolSchema{
		Game:         p.game,
		ClientEvents: p.clientEvents,
		ServerEvents: serverEvents,
		Operations:   p.operations,
	}
	// Merge client events: registry entries (with typed proto names) take
	// precedence. Router entries for codes already declared in the registry
	// are skipped to avoid duplicate codes in the schema output.
	if p.clientEventsRegistry != nil {
		ps.ClientEvents = append(ps.ClientEvents, p.clientEventsRegistry.Schema()...)
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
	// Broadcast-eligible typed messages: every type registered via
	// HandleAll[T] gets a schema entry here so sdkgen can emit a matching
	// TS class + decoder. HandleAllInternal[T] types are excluded.
	for _, t := range BroadcastTypes() {
		ps.BroadcastTypes = append(ps.BroadcastTypes, BroadcastTypeOf(t))
	}
	// Client-input types: every type registered via HandleClient[T] gets a
	// schema entry here so sdkgen can emit a matching TS class with an
	// encode() instance method + a static typeID.
	for _, t := range ClientInputTypes() {
		ps.ClientInputTypes = append(ps.ClientInputTypes, ClientInputTypeOf(t))
	}
	// Typed server events: every type registered via RegisterEvent[T] gets a
	// schema entry here so sdkgen can emit a matching TS class with a static
	// decode(buf) method + a client.onXxx(handler) wrapper. Wire layout
	// reuses BroadcastTypeOf (the reflection codec is identical to the
	// broadcast/client-input path; only the dispatch direction differs).
	for _, t := range RegisteredServerEventTypes() {
		ps.ServerEventTypes = append(ps.ServerEventTypes, BroadcastTypeOf(t))
	}
	// Final sort by code: InputRouter.Schema() iterates a map in random
	// Go-map-order, so without this the generated TypeScript SDK methods
	// would be reordered between successive --dump-schema runs, producing
	// noisy git diffs with no semantic change.
	sort.Slice(ps.ClientEvents, func(i, j int) bool { return ps.ClientEvents[i].Code < ps.ClientEvents[j].Code })
	return ps
}

// WriteSchema writes the protocol schema as JSON to w.
func (p *Protocol) WriteSchema(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(p.Schema())
}

// AssembleFromProcess hydrates the Protocol with runtime-discovered registries:
// operations from the OpRouter, and entity replicators from any cell's
// EntityKindDefs. Client-input types (HandleClient[T]) are harvested
// directly in Schema() via the global mmokit registry.
//
// Called by the engine's --dump-schema path after Build() has populated
// every registry but before Start has begun the game loop.
func (p *Protocol) AssembleFromProcess(proc *universe.Process) {
	if op := proc.OpRouter(); op != nil {
		p.operations = append(p.operations, fromOpsSchemas(op.Schema())...)
	}
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

// fromOpsSchemas converts ops.OperationSchema to mmokit.OperationSchema. They
// have identical layout but distinct Go types (ops can't import mmokit).
func fromOpsSchemas(in []ops.OperationSchema) []OperationSchema {
	out := make([]OperationSchema, len(in))
	for i, s := range in {
		out[i] = OperationSchema(s)
	}
	return out
}
