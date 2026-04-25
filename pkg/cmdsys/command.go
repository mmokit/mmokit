package cmdsys

import (
	"context"
	"errors"

	"github.com/zenion/mmoserver/pkg/logger"
)

// Sentinel errors returned by Dispatcher and related functions.
var (
	ErrNoDeadline           = errors.New("cmdsys: context must carry a deadline")
	ErrUnknownVerb          = errors.New("cmdsys: unknown verb")
	ErrRBACDenied           = errors.New("cmdsys: RBAC denied")
	ErrNotYetWired          = errors.New("cmdsys: route not yet wired (future phase)")
	ErrRefuseGlobalWildcard = errors.New("cmdsys: *.* grants are reserved for NewOperatorIdentity")
)

// RouteKind describes how a command should be dispatched.
type RouteKind uint8

const (
	RouteLocal        RouteKind = iota // handle on the current process
	RouteCoordinator                   // dispatch to the coordinator
	RouteAllHosts                      // fan-out to every host
	RoutePlayerOwner                   // dispatch to the host owning the player
	RouteEntityOwner                   // dispatch to the host owning the entity
	RouteAllGateways                   // fan-out to every gateway
	RouteSpecificHost                  // dispatch to a named host
	RouteSpecificCell                  // dispatch to a named cell
)

// String returns a human-readable name for the RouteKind used in JSON output.
func (r RouteKind) String() string {
	switch r {
	case RouteLocal:
		return "local"
	case RouteCoordinator:
		return "coordinator"
	case RouteAllHosts:
		return "all_hosts"
	case RoutePlayerOwner:
		return "player_owner"
	case RouteEntityOwner:
		return "entity_owner"
	case RouteAllGateways:
		return "all_gateways"
	case RouteSpecificHost:
		return "specific_host"
	case RouteSpecificCell:
		return "specific_cell"
	default:
		return "unknown"
	}
}

// CallerSource identifies how a Caller connected.
type CallerSource uint8

const (
	SourceConsole     CallerSource = iota // interactive server console
	SourceMeshControl                     // arriving via MeshControl gRPC
	SourceTest                            // in-process test caller
	// SourceChat reserved for post-chat-rework
)

// Capability is a dot-separated permission string, e.g. "cell.split".
type Capability string

// Grant is a single RBAC rule: allow or deny a capability pattern.
type Grant struct {
	Pattern string
	Allow   bool
}

// Caller identifies who issued a command.
type Caller struct {
	ID     string
	Source CallerSource
	Grants []Grant
}

// HandlerFunc is the function registered for a command's local execution.
type HandlerFunc func(ctx context.Context, env *Env, args any) (any, error)

// Env carries per-invocation context passed to every HandlerFunc.
type Env struct {
	Caller  Caller
	TraceID string
	// ParentTraceID is set on nested dispatches made via Dispatcher.InvokeInternal.
	// Empty on top-level invocations. Lets the audit log tie a handler-initiated
	// fan-out back to the outer command.
	ParentTraceID string
	Local         *LocalContext
	Logger        *logger.Logger
}

// LocalContext is an opaque per-invocation handle for infrastructure
// objects. C3/C4 will populate it with coordinator, gameworld, and gateway
// references. C1 leaves it empty.
type LocalContext struct{}

// Command is a registered command definition including its handler and
// schema hashes computed at registration time.
type Command struct {
	Verb             string
	Capability       Capability
	Description      string
	Route            RouteKind
	Args             any // zero-value prototype for the args struct
	Result           any // zero-value prototype for the result struct
	Handler          HandlerFunc
	ArgsSchemaHash   uint64
	ResultSchemaHash uint64
	// Hidden suppresses the verb from help listings and tab completion. The
	// command remains fully dispatchable — power users can still call it by
	// name. Used for internal worker verbs that user-facing frontends fan
	// out to (e.g. perf.snapshot, perf.reset behind `perf`).
	Hidden bool
}

// Result is the aggregate outcome of a Dispatcher.Invoke call.
type Result struct {
	Verb       string
	Caller     Caller
	TraceID    string
	PerTarget  []TargetResult
	DurationMS int64
}

// TargetResult holds the outcome for a single dispatch target.
type TargetResult struct {
	TargetID string
	OK       bool
	Result   any
	Error    string
}
