package mmokit

import (
	"fmt"
	"reflect"
	"sort"
	"sync"
)

// RouteKind classifies where a typed-op handler runs in the cluster.
//
// RouteGatewayLocal — handler runs on the gateway, no cell forwarding.
// Examples: login, ping, anything that doesn't need ECS access.
//
// RoutePlayerCell — handler runs on the player's authoritative cell
// (the cell currently owning the player's entity). Examples: marketplace,
// bank, anything that mutates ECS state. Phase 1 of Plan 2 wires the
// gateway-local path only; cell routing comes in Phase 3.
type RouteKind uint8

const (
	RouteGatewayLocal RouteKind = iota
	RoutePlayerCell
)

// String returns a stable, human-readable name. Used by diagnostics, logs,
// and SDK schema export.
func (k RouteKind) String() string {
	switch k {
	case RouteGatewayLocal:
		return "gateway-local"
	case RoutePlayerCell:
		return "player-cell"
	default:
		return "unknown"
	}
}

// OperationError is the framework-defined response the typed-op dispatcher
// returns when an op fails before reaching, or inside, a registered handler.
// Codes are stable; the message is informational and may change. Clients
// should branch on Code, not on Message.
type OperationError struct {
	Code    uint32
	Message string
}

// OperationError codes. Values are stable wire identifiers.
const (
	OpErrorUnknownTypeID uint32 = 1 // no handler registered for the request typeID
	OpErrorHandlerFailed uint32 = 2 // handler returned a non-nil error
	OpErrorDecodeFailed  uint32 = 3 // request body failed to decode via the reflection codec
)

var registerFrameworkOpsOnce sync.Once

// registerFrameworkOps lazily registers framework-owned typed-op response
// types so the dispatcher can encode them by typeID. Idempotent.
func registerFrameworkOps() {
	registerFrameworkOpsOnce.Do(func() {
		RegisterEvent[OperationError]()
	})
}

func init() {
	registerFrameworkOps()
}

// TypedOpEntry is the registry record for one typed-op binding. Created by
// RegisterOp[Req, Res] and looked up by request typeID by the dispatcher.
type TypedOpEntry struct {
	Kind           RouteKind
	RequestType    reflect.Type
	ResponseType   reflect.Type
	ResponseTypeID uint32
	// Handler is the originally-registered func(*OpContext, *Req) (*Res, error)
	// stored as `any`. The dispatcher uses reflect.Call to invoke it.
	Handler any
}

var (
	typedOpMu  sync.RWMutex
	typedOps   = map[uint32]*TypedOpEntry{}
	typedOpSet = map[reflect.Type]struct{}{}
)

// RegisterOp registers a typed operation handler. The wire typeID is
// derived from the Request type via TypeIDOf; the response typeID is
// derived from Res. Each handler signature is
//
//	func(*OpContext, *Req) (*Res, error)
//
// — the dispatcher allocates a fresh *Req, decodes the body via the
// reflection codec, calls the handler, and encodes (*Res) on the
// outbound 0x01 frame. A nil response (with a nil error) emits a frame
// with an empty body.
//
// Panics on duplicate registration of the same Request type, or on a
// typeID collision between two distinct types (collision should never
// happen at codebase scale; rename one type if it does).
func RegisterOp[Req any, Res any](kind RouteKind, handler func(*OpContext, *Req) (*Res, error)) {
	reqType := reflect.TypeFor[Req]()
	resType := reflect.TypeFor[Res]()
	reqID := TypeIDOf(reqType)
	resID := TypeIDOf(resType)

	typedOpMu.Lock()
	defer typedOpMu.Unlock()
	if _, exists := typedOpSet[reqType]; exists {
		panic(fmt.Sprintf("RegisterOp: request type %s already registered", reqType.String()))
	}
	if existing, ok := typedOps[reqID]; ok && existing.RequestType != reqType {
		panic(fmt.Sprintf("RegisterOp: typeID collision between %s and %s (id=%#x)",
			existing.RequestType.String(), reqType.String(), reqID))
	}
	typedOps[reqID] = &TypedOpEntry{
		Kind:           kind,
		RequestType:    reqType,
		ResponseType:   resType,
		ResponseTypeID: resID,
		Handler:        handler,
	}
	typedOpSet[reqType] = struct{}{}
}

// LookupTypedOp returns the registry entry for the given request typeID,
// or (nil, false) if none.
func LookupTypedOp(reqTypeID uint32) (*TypedOpEntry, bool) {
	typedOpMu.RLock()
	defer typedOpMu.RUnlock()
	e, ok := typedOps[reqTypeID]
	return e, ok
}

// RegisteredTypedOps returns all registered entries in deterministic
// (request type name) order. Used by sdkgen and protocol-schema export.
func RegisteredTypedOps() []*TypedOpEntry {
	typedOpMu.RLock()
	defer typedOpMu.RUnlock()
	out := make([]*TypedOpEntry, 0, len(typedOps))
	for _, e := range typedOps {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RequestType.String() < out[j].RequestType.String()
	})
	return out
}

// ResetTypedOpRegistryForTest is exported for tests only.
func ResetTypedOpRegistryForTest() {
	typedOpMu.Lock()
	defer typedOpMu.Unlock()
	typedOps = map[uint32]*TypedOpEntry{}
	typedOpSet = map[reflect.Type]struct{}{}
}
