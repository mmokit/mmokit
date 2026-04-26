package service

import (
	"context"

	"github.com/zenion/mmoserver/pkg/ops"
)

// Service is the runtime interface a kind's instance implements.
//
// Game code typically embeds nothing and declares the three methods
// directly on a struct returned by Kind.Factory.
type Service interface {
	// Init runs once after Factory returns and after the engine has
	// validated dependencies. Use it for slow startup work (DB warm,
	// schema validation, etc).
	Init(ctx *Context) error

	// RegisterOps wires handlers into the process op router. Engine
	// calls this exactly once after a successful Init. Engine cross-
	// checks the *exact set* of registered op codes against Kind.OpCodes
	// — any difference (missing or extra) is a fatal startup error.
	RegisterOps(router *ops.Router) error

	// Shutdown is called on graceful process exit. Block until in-flight
	// handlers drain (engine provides a deadline via ctx).
	Shutdown(ctx context.Context) error
}
