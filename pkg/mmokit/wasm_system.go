package mmokit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/wasmabi"
	"github.com/zenion/mmoserver/pkg/wasmhost"
)

// catWasmSystem is the log category for wasm-system runtime errors. Registered
// per-instance in (*wasmSystem).Init so operators can enable/see it.
const catWasmSystem = "wasm:system"

// wasmRuntime is a process-wide runtime shared by all wasm systems; module
// instances are per-cell and independent.
var wasmRuntime = wasmhost.New(context.Background())

// NewWasmSystem builds a SystemDef whose per-cell instances run a hot-loadable
// WASM module over the POD component T. T must be a value-type (no pointers/
// slices/maps) — its in-memory layout is memcpy'd across the boundary.
//
// The module bytes are read and ABI-checked once at registration; each cell
// instantiates its own module with independent linear memory/state.
//
// Phase 0 caveat: T drives the gather/scatter element size, so the CALLER is
// responsible for matching T to the component the module declares in its
// Query(). The module declares its column's byte size (auto-derived from its
// own component type), and this constructor VALIDATES that the declared size
// matches sizeof(T) at load — so a differing-size mismatch (e.g.
// NewWasmSystem[WrongType] against a module declaring a different column)
// panics here instead of silently corrupting memory via a misaligned scatter.
// A same-size-but-different-meaning type (e.g. a 4-byte PodVal over a float32
// column) still passes validation and remains the caller's responsibility.
func NewWasmSystem[T any](modulePath string) SystemDef {
	wasm, err := os.ReadFile(modulePath)
	if err != nil {
		panic(fmt.Sprintf("mmokit.NewWasmSystem: %v", err))
	}
	// One-time ABI handshake against a throwaway instance.
	probe, err := wasmhost.Load(context.Background(), wasmRuntime, wasm)
	if err != nil {
		panic(fmt.Sprintf("mmokit.NewWasmSystem: load %s: %v", modulePath, err))
	}
	if v := probe.ABIVersion(context.Background()); v != wasmabi.ABIVersion {
		probe.Close(context.Background())
		panic(fmt.Sprintf("mmokit.NewWasmSystem: %s ABI v%d, host v%d", modulePath, v, wasmabi.ABIVersion))
	}
	sz, readWrite := probe.Query(context.Background())
	if want := wasmabi.ElemSize[T](); sz != want {
		probe.Close(context.Background())
		var z T
		panic(fmt.Sprintf("mmokit.NewWasmSystem: %s declares a %d-byte column but registered type %T is %d bytes — T must match the module's component", modulePath, sz, z, want))
	}
	probe.Close(context.Background())

	return SystemDef{
		Name: "Wasm:" + filepath.Base(modulePath),
		Factory: func() System {
			// TODO(phase1): wazero recompiles the wasm bytes on every Load;
			// precompile once via wazero.CompileModule at registration and
			// instantiate per cell.
			mod, err := wasmhost.Load(context.Background(), wasmRuntime, wasm)
			if err != nil {
				panic(fmt.Sprintf("mmokit.NewWasmSystem: per-cell load: %v", err))
			}
			return &wasmSystem[T]{mod: mod, readWrite: readWrite}
		},
	}
}

type wasmSystem[T any] struct {
	SystemBase
	mod       *wasmhost.Module
	readWrite bool
	col       []T // reused gather buffer
}

// SwappableSystem is implemented by hot-loadable systems that can serialize
// their internal state across an unload/swap. The live-swap protocol snapshots
// the outgoing instance and restores it into the incoming one so internal
// state (caches, counters, in-flight machines) survives the code swap.
type SwappableSystem interface {
	Snapshot() ([]byte, error)
	Restore(state []byte) error
}

// Snapshot serializes the module's internal state (empty if the module is
// stateless). Safe to call between ticks on the loop goroutine.
func (s *wasmSystem[T]) Snapshot() ([]byte, error) {
	return s.mod.Snapshot(context.Background())
}

// Restore loads previously-snapshotted internal state into a freshly-loaded
// module instance. Call before the instance's first tick.
func (s *wasmSystem[T]) Restore(state []byte) error {
	return s.mod.Restore(context.Background(), state)
}

// Close releases the underlying wasm module instance (true unload). Call after
// the system has been removed from / replaced on the loop.
func (s *wasmSystem[T]) Close() error {
	return s.mod.Close(context.Background())
}

func (s *wasmSystem[T]) Init() {
	s.Stage().Engine().Log.RegisterCategories(catWasmSystem)
}

func (s *wasmSystem[T]) Update(dt float32) {
	stage := s.Stage()
	s.col = s.col[:0]
	ForEach1(stage, func(_ Entity, c *T) { s.col = append(s.col, *c) })
	n := len(s.col)
	if n == 0 {
		return
	}
	size := int(unsafe.Sizeof(s.col[0]))
	in := unsafe.Slice((*byte)(unsafe.Pointer(&s.col[0])), n*size)

	out, err := s.mod.Update(context.Background(), uint32(n), dt, in)
	if err != nil {
		stage.Engine().Log.Log(catWasmSystem, "update failed: %v", err)
		return
	}
	if !s.readWrite {
		return
	}
	res := unsafe.Slice((*T)(unsafe.Pointer(&out[0])), n)
	i := 0
	ForEach1(stage, func(_ Entity, c *T) { *c = res[i]; i++ })
}

// Compile-time assertion that wasmSystem satisfies the engine System contract.
var _ engine.System = (*wasmSystem[struct{}])(nil)
var _ SwappableSystem = (*wasmSystem[struct{}])(nil)
