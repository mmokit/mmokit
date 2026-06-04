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
	_, readWrite := probe.Query(context.Background())
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
