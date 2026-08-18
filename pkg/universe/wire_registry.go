package universe

import (
	"fmt"
	"reflect"
	"sort"
	"sync"
)

// WireRegistry owns every client-facing message registration a process
// serves: the types clients may send (HandleClient), the types the server
// may send back (RegisterEvent and the broadcast-eligible set), and the
// request/response bindings for typed operations (RegisterOp).
//
// These four lived as package-global maps in the mmokit facade, reached
// from here through five hook structs that existed purely so a global
// registry could call back into the facade without an import cycle. Global
// meant binary-scoped, and binary-scoped is wrong the moment one binary
// builds two Processes: RegisterOp's duplicate path ends with
// `existing.Handler = handler`, and the auth handlers are closures over
// their own *Process, so the first Process ended up dispatching auth into
// the second's service.
//
// A registry is created per Process and injected into each Stage, exactly
// as the base cell size now is. Registration stays open for the life of the
// process rather than sealing at Build(): initSystems runs on remote cell
// assignment and on cell SPLIT as well as at Build, System.Init() is a
// documented place to register from, and sealing would turn that into a
// panic on a background goroutine minutes into a run. Re-registering the
// same type on the same registry is idempotent, which is what makes a
// split safe.
//
// All four maps sit behind one RWMutex. Registration happens at startup;
// the per-frame paths (inbound typeID lookup, broadcast eligibility) are
// all RLock readers and do not contend with each other.
type WireRegistry struct {
	mu sync.RWMutex

	// Client inputs — types a client may send on channel 0x00. Keyed both
	// ways: by type for the registration idempotence check, by typeID for
	// inbound frame dispatch.
	ciSet    map[reflect.Type]struct{}
	ciByType map[uint32]reflect.Type

	// Server events — types the server may send on channel 0x00.
	seSet    map[reflect.Type]struct{}
	seByType map[uint32]reflect.Type

	// Broadcast-eligible types (HandleAll, or RegisterBroadcastType direct).
	// HandleAllInternal types are deliberately absent.
	brSet map[reflect.Type]struct{}

	// Typed ops — channel 0x01 request/response bindings, keyed by request
	// typeID.
	typedOps map[uint32]*TypedOpEntry

	// downstreamIDs indexes every typeID a client may see on channel 0x00,
	// across BOTH server-to-client registries.
	//
	// The two registries are separate on the server and a single namespace on
	// the client: generated clients dispatch broadcasts and server events
	// through one `TypedEvents.Dispatch(typeID, …)` switch. A per-registry
	// collision check therefore cannot see the collision that actually
	// matters — a broadcast type and an event type hashing alike — and each
	// registry would report itself clean while the client silently routed one
	// type's bytes into the other's decoder. This index is the namespace the
	// client actually has.
	downstreamIDs map[uint32]reflect.Type

	// enc encodes the framework's own wire types. See FrameworkEncoders.
	enc FrameworkEncoders
}

// FrameworkEncoders carries the facade's encoders for framework-owned wire
// types. universe routes and frames those types but cannot construct them:
// OperationError, PlayerEntityAssigned and ServerConfig are declared in the
// mmokit package, and their wire identity is fnv32a("mmokit.OperationError")
// and friends. Moving the declarations down here to remove the indirection
// would rename every one of them on the wire.
//
// So the facade installs three closures per registry instead. Unlike the
// package-global hook structs this replaces, these are per-process values
// set once during construction, and a registry with none of them set is a
// working registry for everything except the framework's own frames — which
// is the correct behaviour for a universe-only test that never imports
// mmokit.
type FrameworkEncoders struct {
	// OperationErrorTypeID is the wire-stable typeID for mmokit.OperationError,
	// cached so the typed-op dispatcher's error path does not repeatedly walk
	// the reflection stack.
	OperationErrorTypeID uint32

	// MakeOperationErrorBody encodes an OperationError{code, message} via the
	// reflection codec. The dispatcher wraps the result in a 0x01 typed-op
	// frame keyed at OperationErrorTypeID.
	MakeOperationErrorBody func(code uint32, message string) []byte

	// PlayerEntityAssigned builds the typed-event frame announcing the
	// player's authoritative entity NetID and world position. Used by
	// Stage.SpawnPlayer.
	PlayerEntityAssigned func(netID uint32, worldX, worldY float32) []byte

	// ServerConfig builds the typed-event frame carrying engine-level
	// configuration (tick rate). Sent on connect by the gateway.
	ServerConfig func(tickRate uint32) []byte
}

// A nil *WireRegistry is a valid receiver for every READ method, and reads as
// empty: no type registered, no op found, no framework encoder installed.
// That is deliberate and exact — it is the same behaviour the five hook
// structs had when nothing had populated them, which is the state a fixture
// built inside pkg/universe with no Process behind it is in. Registration on a
// nil registry panics, because that one is always a bug.

// NewWireRegistry returns an empty registry with every map allocated.
func NewWireRegistry() *WireRegistry {
	return &WireRegistry{
		ciSet:         map[reflect.Type]struct{}{},
		ciByType:      map[uint32]reflect.Type{},
		seSet:         map[reflect.Type]struct{}{},
		seByType:      map[uint32]reflect.Type{},
		brSet:         map[reflect.Type]struct{}{},
		typedOps:      map[uint32]*TypedOpEntry{},
		downstreamIDs: map[uint32]reflect.Type{},
	}
}

// SetFrameworkEncoders installs the facade's encoders for framework-owned
// wire types. Called once per registry during process construction.
func (w *WireRegistry) SetFrameworkEncoders(e FrameworkEncoders) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.enc = e
}

// FrameworkEncoders returns the installed encoders. Zero-valued fields mean
// the facade never wired them (a universe-only test); every caller checks
// the field it needs for nil.
func (w *WireRegistry) FrameworkEncoders() FrameworkEncoders {
	if w == nil {
		return FrameworkEncoders{}
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.enc
}

// ─── client inputs (mmokit.HandleClient) ──────────────────────────────────────

// RegisterClientInput marks t as a HandleClient-eligible type and indexes it
// by typeID for inbound-frame lookup. Idempotent for the same Go type.
//
// Two distinct types hashing to the same typeID is a panic, matching
// RegisterServerEvent and RegisterTypedOp. This registry previously
// overwrote silently, which is the worst outcome available: the loser's
// handler simply stops being reachable, and because this is the one registry
// decoded straight off a client socket, the symptom is a client input that is
// accepted and then ignored rather than an error anyone can see. Rename one
// type if it ever fires.
func (w *WireRegistry) RegisterClientInput(t reflect.Type) {
	// Registration-time shape check. This is the one registry whose types are
	// decoded straight off a client socket, so an unsupported field here is a
	// wire bug reachable by an unauthenticated peer rather than a codegen one.
	ValidateMessageType(t)
	id := TypeIDOf(t)

	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.ciSet[t]; ok {
		return
	}
	if existing, ok := w.ciByType[id]; ok && existing != t {
		panic(fmt.Sprintf("HandleClient: typeID collision between %s and %s (id=%#x)",
			existing.String(), t.String(), id))
	}
	w.ciSet[t] = struct{}{}
	w.ciByType[id] = t
}

// ClientInputRegistered reports whether t was registered via HandleClient.
func (w *WireRegistry) ClientInputRegistered(t reflect.Type) bool {
	if w == nil {
		return false
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	_, ok := w.ciSet[t]
	return ok
}

// ClientInputType resolves a wire typeID back to its registered Go type.
// Returns nil for unknown typeIDs, which the dispatch paths treat as
// untrusted and drop with a log.
func (w *WireRegistry) ClientInputType(typeID uint32) reflect.Type {
	if w == nil {
		return nil
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.ciByType[typeID]
}

// ClientInputTypes returns the registered client-input types in deterministic
// order (sorted by reflect.Type.String()). Used by sdkgen to emit TS class
// declarations for client-bound message types.
func (w *WireRegistry) ClientInputTypes() []reflect.Type {
	if w == nil {
		return nil
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return sortedTypes(w.ciSet)
}

// ─── server events (mmokit.RegisterEvent) ─────────────────────────────────────

// RegisterServerEvent registers t as a server→client typed event. Subsequent
// SendEventTyped calls use the FNV-1a typeID derived from t to identify the
// frame on the wire; SDK codegen iterates this registry to emit per-event
// decoder classes and per-event onXxx handlers.
//
// Idempotent for the same Go type — safe to call from per-cell System.Init()
// where it would otherwise fire N times. Two distinct types hashing to the
// same typeID is a panic; rename one type if it ever fires.
func (w *WireRegistry) RegisterServerEvent(t reflect.Type) {
	// Registration-time shape check. ValidateMessageType, never
	// ValidateComponentType: twelve production wire types carry slice fields
	// and the component validator rejects every one of them.
	ValidateMessageType(t)
	id := TypeIDOf(t)

	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.seSet[t]; ok {
		return
	}
	if existing, ok := w.seByType[id]; ok && existing != t {
		panic(fmt.Sprintf("RegisterEvent: typeID collision between %s and %s (id=%#x)",
			existing.String(), t.String(), id))
	}
	// Also claim the id across the shared downstream namespace, which catches
	// the collision this registry cannot see on its own: a broadcast type
	// hashing to the same id. Clients dispatch both through one switch.
	w.claimDownstreamLocked("RegisterEvent", id, t)
	w.seByType[id] = t
	w.seSet[t] = struct{}{}
}

// ServerEventRegistered reports whether t was registered via RegisterEvent.
func (w *WireRegistry) ServerEventRegistered(t reflect.Type) bool {
	if w == nil {
		return false
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	_, ok := w.seSet[t]
	return ok
}

// ServerEventType returns the Go type registered for the given typeID, or
// (nil, false) if none.
func (w *WireRegistry) ServerEventType(id uint32) (reflect.Type, bool) {
	if w == nil {
		return nil, false
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	t, ok := w.seByType[id]
	return t, ok
}

// ServerEventTypes returns the registered types in deterministic
// (alphabetical) order. Used by sdkgen and protocol-schema export.
func (w *WireRegistry) ServerEventTypes() []reflect.Type {
	if w == nil {
		return nil
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return sortedTypes(w.seSet)
}

// ResetServerEventsForTest is exported for tests only.
func (w *WireRegistry) ResetServerEventsForTest() {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Release this registry's claims on the shared downstream namespace too.
	// Without it, a test that resets and then registers a DIFFERENT type
	// hashing to a previously-claimed id would panic on a collision with a
	// registration that no longer exists.
	for id, t := range w.seByType {
		if w.downstreamIDs[id] == t {
			delete(w.downstreamIDs, id)
		}
	}
	w.seByType = map[uint32]reflect.Type{}
	w.seSet = map[reflect.Type]struct{}{}
}

// ─── broadcasts (mmokit.HandleAll) ────────────────────────────────────────────

// RegisterBroadcast marks t as broadcast-eligible. Called from HandleAll[T],
// the broadcast-on-by-default registration verb. HandleAllInternal[T]
// explicitly does not call this — that is how server-internal types stay out
// of the broadcast registry. Idempotent.
func (w *WireRegistry) RegisterBroadcast(t reflect.Type) {
	// Registration-time shape check, same validator as the other three wire
	// registries: a broadcast body is encoded by ReflectMarshal and decoded by
	// the generated SDKs, so it obeys the same field rules.
	ValidateMessageType(t)
	id := TypeIDOf(t)

	w.mu.Lock()
	defer w.mu.Unlock()
	// This registry computed no typeID at all before, so a broadcast collision
	// was not merely unguarded — it was undetectable.
	w.claimDownstreamLocked("RegisterBroadcastType", id, t)
	w.brSet[t] = struct{}{}
}

// BroadcastEligible reports whether typed messages of type t should
// auto-broadcast — true iff t was registered via HandleAll (or directly via
// RegisterBroadcast). HandleAllInternal types are absent and return false.
func (w *WireRegistry) BroadcastEligible(t reflect.Type) bool {
	if w == nil {
		return false
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	_, ok := w.brSet[t]
	return ok
}

// BroadcastTypes returns the registered broadcast-eligible types in
// deterministic order (sorted by reflect.Type.String()). Used by sdkgen to
// emit TS class declarations.
func (w *WireRegistry) BroadcastTypes() []reflect.Type {
	if w == nil {
		return nil
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return sortedTypes(w.brSet)
}

// claimDownstreamLocked reserves id for t in the client's channel-0x00
// dispatch namespace. Idempotent for the same type; panics when a different
// type already holds the id. verb names the registration call in the message
// so the panic points at the code to change. Caller holds w.mu.
func (w *WireRegistry) claimDownstreamLocked(verb string, id uint32, t reflect.Type) {
	if existing, ok := w.downstreamIDs[id]; ok && existing != t {
		panic(fmt.Sprintf("%s: typeID collision between %s and %s (id=%#x) — "+
			"broadcasts and server events share one client dispatch namespace; rename one type",
			verb, existing.String(), t.String(), id))
	}
	w.downstreamIDs[id] = t
}

// ─── typed ops (mmokit.RegisterOp) ────────────────────────────────────────────

// RegisterTypedOp registers a typed operation handler under the request
// type's wire typeID. handler is the originally-registered
// func(*OpContext, *Req) (*Res, error), stored as `any` and invoked by the
// dispatcher via reflect.Call.
//
// Re-registration of the same request type with the same kind + response type
// is idempotent (last-writer-wins on the handler closure) so games can call
// RegisterOp from a setup function that runs many times across tests without
// juggling reset boilerplate. Now that a registry belongs to one Process, that
// last write can no longer reach across into a sibling Process's handler.
//
// Panics on:
//   - Re-registration of a type with a different kind or response type
//     (genuine programmer error — the wire schema would change silently).
//   - typeID collision between two distinct request types.
func (w *WireRegistry) RegisterTypedOp(kind RouteKind, reqType, resType reflect.Type, handler any) {
	// Both halves: the request is decoded from a client body, the response is
	// decoded by the generated SDKs and by the console's `service call`. An
	// unsupported field in either is a wire bug.
	ValidateMessageType(reqType)
	ValidateMessageType(resType)
	reqID := TypeIDOf(reqType)
	resID := TypeIDOf(resType)

	w.mu.Lock()
	defer w.mu.Unlock()
	if existing, ok := w.typedOps[reqID]; ok {
		if existing.RequestType != reqType {
			panic(fmt.Sprintf("RegisterOp: typeID collision between %s and %s (id=%#x)",
				existing.RequestType.String(), reqType.String(), reqID))
		}
		if existing.Kind != kind || existing.ResponseType != resType {
			panic(fmt.Sprintf("RegisterOp: %s re-registered with different shape "+
				"(was kind=%s res=%s; now kind=%s res=%s)",
				reqType.String(), existing.Kind, existing.ResponseType, kind, resType))
		}
		// Same type, same kind, same response — refresh the handler closure.
		existing.Handler = handler
		return
	}
	w.typedOps[reqID] = &TypedOpEntry{
		Kind:           kind,
		RequestType:    reqType,
		ResponseType:   resType,
		ResponseTypeID: resID,
		Handler:        handler,
	}
}

// LookupTypedOp returns the registry entry for the given request typeID, or
// (nil, false) if none.
func (w *WireRegistry) LookupTypedOp(reqTypeID uint32) (*TypedOpEntry, bool) {
	if w == nil {
		return nil, false
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	e, ok := w.typedOps[reqTypeID]
	return e, ok
}

// TypedOps returns every registered entry in deterministic (request type
// name) order. Used by sdkgen, protocol-schema export, and the admin console.
func (w *WireRegistry) TypedOps() []*TypedOpEntry {
	if w == nil {
		return nil
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]*TypedOpEntry, 0, len(w.typedOps))
	for _, e := range w.typedOps {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RequestType.String() < out[j].RequestType.String()
	})
	return out
}

// ResetTypedOpsForTest is exported for tests only.
func (w *WireRegistry) ResetTypedOpsForTest() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.typedOps = map[uint32]*TypedOpEntry{}
}

// sortedTypes returns set's keys ordered by reflect.Type.String(). Every
// schema-export path depends on this order being stable: --dump-schema is
// byte-compared against a golden.
func sortedTypes(set map[reflect.Type]struct{}) []reflect.Type {
	out := make([]reflect.Type, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}
