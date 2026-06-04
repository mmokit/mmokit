package mmokit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/zenion/mmoserver/pkg/universe"
)

// wasmRegEntry binds a registered wasm system's artifact path to a type-erased,
// panic-guarded builder (the POD component T is captured at AddWasmSystem time).
type wasmRegEntry struct {
	path  string
	build func(path string) (System, error) // fresh per-cell instance, or error
}

type wasmRegistry struct {
	mu      sync.Mutex
	entries map[string]wasmRegEntry
}

var (
	wasmRegMu  sync.Mutex
	wasmRegMap = map[*universe.Process]*wasmRegistry{}
)

func wasmRegistryFor(p *universe.Process) *wasmRegistry {
	wasmRegMu.Lock()
	defer wasmRegMu.Unlock()
	r, ok := wasmRegMap[p]
	if !ok {
		r = &wasmRegistry{entries: map[string]wasmRegEntry{}}
		wasmRegMap[p] = r
	}
	return r
}

// deriveWasmName turns a module path into a logical name: the base filename
// without its extension. "dist/wasmmods/pulse.wasm" -> "pulse".
func deriveWasmName(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// AddWasmSystem registers a hot-swappable wasm system over POD component T,
// naming it after the module file (e.g. ".../pulse.wasm" -> "pulse"). It is
// added to the normal system set (so it boots into every cell on every node)
// AND recorded in the per-process registry for runtime load/swap/unload by name.
// T must match the component the module declares in its Query().
func AddWasmSystem[T any](p *universe.Process, path string) {
	AddWasmSystemNamed[T](p, deriveWasmName(path), path)
}

// AddWasmSystemNamed is AddWasmSystem with an explicit logical name decoupled
// from the filename.
func AddWasmSystemNamed[T any](p *universe.Process, name, path string) {
	reg := wasmRegistryFor(p)
	reg.mu.Lock()
	reg.entries[name] = wasmRegEntry{
		path:  path,
		build: func(pth string) (System, error) { return buildWasmSystemInstance[T](pth) },
	}
	reg.mu.Unlock()

	// Boot into every cell via the existing systemDefs path, named so the loop
	// and the runtime verbs find it by the same logical name.
	p.AddSystem(NewWasmSystem[T](path).Named(name))
}

// buildWasmSystemInstance creates one fresh per-cell wasm instance, converting
// NewWasmSystem/Factory panics (bad path/ABI) into errors so a runtime load
// can't crash a tick.
func buildWasmSystemInstance[T any](path string) (sys System, err error) {
	if _, statErr := os.Stat(path); statErr != nil {
		return nil, fmt.Errorf("wasm module not found at %s (run `just wasm-build` first): %w", path, statErr)
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("load wasm module %s: %v", path, r)
		}
	}()
	return NewWasmSystem[T](path).Factory(), nil
}
