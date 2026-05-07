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
	RouteLocal             RouteKind = iota // handle on the current process
	RouteCoordinator                        // dispatch to the coordinator
	RouteAllHosts                           // fan-out to every host
	RoutePlayerOwner                        // dispatch to the host owning the player
	RouteEntityOwner                        // dispatch to the host owning the entity
	RouteAllGateways                        // fan-out to every gateway
	RouteSpecificHost                       // dispatch to a named host
	RouteSpecificCell                       // dispatch to a named cell
	RoutePlayerHomeOrOwner                  // online → owner host; offline → DB-bearing host
	RouteService                            // dispatch to the host running a named service kind (Command.Service)
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
	case RoutePlayerHomeOrOwner:
		return "player_home_or_owner"
	case RouteService:
		return "service"
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
// objects. The dispatcher populates Process at Invoke time when a
// concrete LocalProcess implementation is available; unit tests leave
// it nil and bypass any helper that requires it.
type LocalContext struct {
	Process LocalProcess
}

// LocalProcess is the minimal surface cmdsys exposes to handlers from
// the running process. Implemented by *universe.Process at the universe
// layer via embedding LocalProcessMarker, which keeps cmdsys a leaf
// package (no import of universe).
type LocalProcess interface {
	isLocalProcess()
}

// LocalProcessMarker is an embeddable zero-size struct that satisfies
// LocalProcess. Embed it in *universe.Process (or any concrete type that
// should be treated as a local process) to fulfill the interface from
// outside this package.
type LocalProcessMarker struct{}

func (LocalProcessMarker) isLocalProcess() {}

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

	// Usage is an optional one-line usage hint shown by the help renderer,
	// e.g. "cell split <cellID> [--bypass]". When empty, RenderHelp
	// auto-derives one from the Args schema.
	Usage string

	// Aliases lists alternate display names. Routing always uses Verb;
	// aliases are display-only (e.g. ["h", "?"] on the "help" command).
	Aliases []string

	// Examples are concrete invocations rendered under the EXAMPLES section
	// of per-command help. Empty slice → no EXAMPLES section.
	Examples []string

	// Service is the service kind name used by RouteService routing. Required
	// when Route == RouteService; ignored otherwise. e.g., "chat" or "auth".
	// The route resolver uses this to look up the host running the named
	// service via the cluster service registry.
	Service string
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
