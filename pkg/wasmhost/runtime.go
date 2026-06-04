// Package wasmhost embeds a WASM runtime and bridges raw component-column
// bytes in and out of guest linear memory. It is ECS-agnostic: callers
// (the mmokit adapter) own all gather/scatter against the world.
package wasmhost

import (
	"context"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// Runtime wraps a wazero runtime. Compile modules once, instantiate per cell.
type Runtime struct {
	rt wazero.Runtime
}

// New creates a Runtime with the WASI preview1 host functions Go's wasip1
// port requires.
func New(ctx context.Context) *Runtime {
	rt := wazero.NewRuntime(ctx)
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)
	return &Runtime{rt: rt}
}

// Instantiate loads a wasm image as a reactor: _start is suppressed so the
// module does not run-and-exit; _initialize is invoked so package init and
// the wasmsys.Register call run, leaving exports callable.
func (r *Runtime) Instantiate(ctx context.Context, wasm []byte) (api.Module, error) {
	cfg := wazero.NewModuleConfig().WithStartFunctions("_initialize")
	return r.rt.InstantiateWithConfig(ctx, wasm, cfg)
}

func (r *Runtime) Close(ctx context.Context) error { return r.rt.Close(ctx) }
