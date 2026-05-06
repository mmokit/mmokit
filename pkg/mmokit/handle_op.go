package mmokit

import (
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
