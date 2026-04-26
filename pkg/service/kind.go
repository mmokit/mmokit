// Package service is the pluggable services framework. Game devs
// register service Kinds via Process.RegisterService; the engine
// instantiates them on processes whose --mode includes "service" and
// whose --services list names them. The gateway routes ops to service
// instances via op-code claims announced through PeerList.
//
// v1 is stateless: services may hold local caches but cross-instance
// authoritative state is unsupported (no peer broadcast primitive in
// Context). Use the DB for any state that must be coherent across
// instances. See docs/superpowers/specs/2026-04-27-pluggable-services-design.md.
package service

// Kind is the descriptor for a service that game code registers with
// the engine. The engine validates Kinds at startup and routes ops by
// code via the announced (kind, opCodes) tuples in PeerList.
//
// Each services-role process instantiates exactly one Service per
// listed Kind via Factory.
type Kind struct {
	// Name is the unique kind identifier; matches the token used in
	// --services= and shown in console / metrics.
	Name string

	// OpCodes are the op codes this kind handles. Engine validates at
	// startup that:
	//   - no two registered Kinds claim the same code
	//   - all codes here are actually registered by RegisterOps()
	OpCodes []uint32

	// Factory constructs a Service. Called once per process at services-
	// role startup. Must not block (do real init in Service.Init).
	Factory func(ctx *Context) Service

	// RequiresDB causes Build() to error when DB is not configured.
	RequiresDB bool

	// MetricsPrefix is the prometheus metric prefix for this kind's
	// per-instance metrics. Defaults to Name when empty.
	MetricsPrefix string

	// HealthCheck is an optional liveness probe. Called only on demand
	// by `service info` fanout and /health aggregation — not periodically.
	// Default = nil = "healthy if Init succeeded".
	HealthCheck func(svc Service) error

	// Description is human-readable text shown in `service info <name>`.
	Description string
}
