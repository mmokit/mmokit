package service

import (
	"context"
)

// Service is the runtime interface a kind's instance implements.
//
// Game code typically embeds nothing and declares the methods directly
// on a struct returned by Kind.Factory. After Plan 2 the typed-op
// channel registers handlers globally at package init via
// mmokit.RegisterOp[Req, Res] — no per-instance wiring step is needed.
type Service interface {
	// Init runs once after Factory returns and after the engine has
	// validated dependencies. Use it for slow startup work (DB warm,
	// schema validation, etc).
	Init(ctx *Context) error

	// Shutdown is called on graceful process exit. Block until in-flight
	// handlers drain (engine provides a deadline via ctx).
	Shutdown(ctx context.Context) error
}
