package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// wasmSysName is the single per-cell name under which any hot-loaded demo wasm
// system is registered on the loop (one demo system per cell at a time).
const wasmSysName = "WasmTest"

// wasmModule binds a demo module name to its default artifact path and a typed
// builder that pins the POD component T the module reads/writes. T must match
// the component the module declares in its Query() (Phase 0 doesn't cross-check).
type wasmModule struct {
	defaultPath string
	build       func(path string) (mmokit.System, mmokit.SwappableSystem, error)
}

// wasmModules is the registry of loadable demo systems. Add an entry here to
// expose a new module to `wasm load/swap <cell> <module>`.
var wasmModules = map[string]wasmModule{
	// ShieldRegen: regenerates Shield.Current (not visually rendered in this
	// example; proves the mechanism + state continuity).
	"shield": {"dist/wasmmods/shieldregen.wasm", buildWasmInstance[gamecomp.Shield]},
	// Pulse: oscillates Collider.Radius → the client circles visibly breathe.
	// Edit examples/4node-basic/wasmmods/pulse/main.go, `just wasm-build`, then
	// `wasm swap <cell> pulse` to see the change live.
	"pulse": {"dist/wasmmods/pulse.wasm", buildWasmInstance[component.Collider]},
}

func knownModules() string {
	names := make([]string, 0, len(wasmModules))
	for k := range wasmModules {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// loadedWasmEntry records the live instance + which module it came from so
// unload/swap can snapshot it and report the module. Demo state; single process
// only (Phase 0).
type loadedWasmEntry struct {
	swap   mmokit.SwappableSystem
	module string
}

var (
	loadedWasmMu sync.Mutex
	loadedWasm   = map[string]loadedWasmEntry{} // keyed by mesh cell id
)

// decodeWasmTicks reads the module's internal tick counter from an 8-byte
// little-endian snapshot blob. Returns 0 for empty/short snapshots.
func decodeWasmTicks(b []byte) uint64 {
	if len(b) != 8 {
		return 0
	}
	return binary.LittleEndian.Uint64(b)
}

// buildWasmInstance creates a fresh wasm system instance OFF the loop goroutine,
// converting NewWasmSystem/Factory panics into errors so a bad path/ABI can't
// crash a cell's game loop. T pins the POD component the module operates on.
func buildWasmInstance[T any](path string) (sys mmokit.System, swap mmokit.SwappableSystem, err error) {
	if _, statErr := os.Stat(path); statErr != nil {
		return nil, nil, fmt.Errorf("module not found at %s (run `just wasm-build` first): %w", path, statErr)
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("load wasm module %s: %v", path, r)
		}
	}()
	def := mmokit.NewWasmSystem[T](path)
	s := def.Factory()
	return s, s.(mmokit.SwappableSystem), nil
}

// resolveModule looks up a registry entry and resolves the artifact path
// (explicit override or the module's default).
func resolveModule(moduleArg, pathArg string) (spec wasmModule, path string, err error) {
	name := strings.TrimSpace(moduleArg)
	spec, ok := wasmModules[name]
	if !ok {
		return wasmModule{}, "", fmt.Errorf("unknown module %q (known: %s)", name, knownModules())
	}
	path = strings.TrimSpace(pathArg)
	if path == "" {
		path = spec.defaultPath
	}
	return spec, path, nil
}

// registerWasmCommands wires the `wasm load/unload/swap` typed commands onto the
// coordinator's cmdsys registry. Called from main.go via coord.OnConsoleReady.
// Demonstrates zero-downtime hot-load of a demo wasm system onto a running cell,
// with the module's internal state preserved across a swap.
//
// Commands:
//
//	wasm.load <cellID> <module> [path]   — hot-load a demo wasm system onto a cell
//	wasm.unload <cellID>                 — unload it, reporting the final tick counter
//	wasm.swap <cellID> <module> [path]   — hot-swap, preserving the module's internal state
//
// <module> is one of the registry keys (shield, pulse). The `pulse` module is
// the visible one: it breathes every entity's radius.
func registerWasmCommands(coord *mmokit.Process, reg *mmokit.CommandRegistry) error {
	if err := reg.Register(mmokit.Command{
		Verb:        "wasm.load",
		Capability:  "wasm.load",
		Description: "hot-load a demo wasm system (shield|pulse) onto a cell (zero downtime)",
		Examples:    []string{"wasm load 0_0 pulse", "wasm load cell_0_0 shield"},
		Route:       mmokit.RouteSpecificCell,
		Args:        wasmLoadArgs{},
		Result:      wasmStatusResult{},
		Handler: func(ctx context.Context, env *mmokit.CommandEnv, raw any) (any, error) {
			args := raw.(wasmLoadArgs)
			cell := resolveCell(coord, strings.TrimSpace(args.CellID))
			if cell == nil {
				return nil, fmt.Errorf("unknown cell %q — use `cell list` to see available cells", args.CellID)
			}
			spec, path, err := resolveModule(args.Module, args.Path)
			if err != nil {
				return nil, err
			}
			// Build OFF the loop goroutine: NewWasmSystem/Factory panic on a bad
			// path/ABI, and that panic must not reach the tick.
			sys, swap, err := spec.build(path)
			if err != nil {
				return nil, err
			}
			module := strings.TrimSpace(args.Module)
			return mmokit.CmdOnLoop(ctx, cell.Engine, func() (wasmStatusResult, error) {
				loadedWasmMu.Lock()
				defer loadedWasmMu.Unlock()
				key := string(cell.MeshID)
				if cur, ok := loadedWasm[key]; ok {
					return wasmStatusResult{}, fmt.Errorf("wasm system %q already loaded on %s (unload or swap first)", cur.module, key)
				}
				mmokit.WireSystem(sys, cell.Stage.ECSWorld(), cell.Engine, cell.Stage)
				cell.Loop.AddSystemLive(wasmSysName, sys)
				loadedWasm[key] = loadedWasmEntry{swap: swap, module: module}
				return wasmStatusResult{Cell: key, Module: module, Status: "loaded", Ticks: 0}, nil
			})
		},
	}); err != nil {
		return fmt.Errorf("wasm.load: %w", err)
	}

	if err := reg.Register(mmokit.Command{
		Verb:        "wasm.unload",
		Capability:  "wasm.unload",
		Description: "unload the demo wasm system from a cell",
		Examples:    []string{"wasm unload 0_0"},
		Route:       mmokit.RouteSpecificCell,
		Args:        wasmCellArgs{},
		Result:      wasmStatusResult{},
		Handler: func(ctx context.Context, env *mmokit.CommandEnv, raw any) (any, error) {
			args := raw.(wasmCellArgs)
			cell := resolveCell(coord, strings.TrimSpace(args.CellID))
			if cell == nil {
				return nil, fmt.Errorf("unknown cell %q", args.CellID)
			}
			return mmokit.CmdOnLoop(ctx, cell.Engine, func() (wasmStatusResult, error) {
				loadedWasmMu.Lock()
				defer loadedWasmMu.Unlock()
				key := string(cell.MeshID)
				old, ok := loadedWasm[key]
				if !ok {
					return wasmStatusResult{}, fmt.Errorf("no wasm system loaded on %s", key)
				}
				// Snapshot of the LIVE instance must happen on the loop goroutine
				// (the wasm instance is single-threaded and the loop may tick it).
				state, _ := old.swap.Snapshot()
				cell.Loop.RemoveSystemLive(wasmSysName)
				delete(loadedWasm, key)
				return wasmStatusResult{Cell: key, Module: old.module, Status: "unloaded", Ticks: decodeWasmTicks(state)}, nil
			})
		},
	}); err != nil {
		return fmt.Errorf("wasm.unload: %w", err)
	}

	if err := reg.Register(mmokit.Command{
		Verb:        "wasm.swap",
		Capability:  "wasm.swap",
		Description: "hot-swap a demo wasm system, preserving its internal state (phase)",
		Examples:    []string{"wasm swap 0_0 pulse"},
		Route:       mmokit.RouteSpecificCell,
		Args:        wasmLoadArgs{},
		Result:      wasmStatusResult{},
		Handler: func(ctx context.Context, env *mmokit.CommandEnv, raw any) (any, error) {
			args := raw.(wasmLoadArgs)
			cell := resolveCell(coord, strings.TrimSpace(args.CellID))
			if cell == nil {
				return nil, fmt.Errorf("unknown cell %q", args.CellID)
			}
			spec, path, err := resolveModule(args.Module, args.Path)
			if err != nil {
				return nil, err
			}
			// Build the replacement OFF the loop (panic-safe), then snapshot the
			// old + restore into the new + swap, all inside the loop closure.
			newSys, newSwap, err := spec.build(path)
			if err != nil {
				return nil, err
			}
			module := strings.TrimSpace(args.Module)
			return mmokit.CmdOnLoop(ctx, cell.Engine, func() (wasmStatusResult, error) {
				loadedWasmMu.Lock()
				defer loadedWasmMu.Unlock()
				key := string(cell.MeshID)
				old, ok := loadedWasm[key]
				if !ok {
					return wasmStatusResult{}, fmt.Errorf("no wasm system loaded on %s (load first)", key)
				}
				state, err := old.swap.Snapshot()
				if err != nil {
					return wasmStatusResult{}, fmt.Errorf("snapshot: %w", err)
				}
				if err := newSwap.Restore(state); err != nil {
					return wasmStatusResult{}, fmt.Errorf("restore: %w", err)
				}
				mmokit.WireSystem(newSys, cell.Stage.ECSWorld(), cell.Engine, cell.Stage)
				cell.Loop.RemoveSystemLive(wasmSysName)
				cell.Loop.AddSystemLive(wasmSysName, newSys)
				loadedWasm[key] = loadedWasmEntry{swap: newSwap, module: module}
				return wasmStatusResult{Cell: key, Module: module, Status: "swapped", Ticks: decodeWasmTicks(state)}, nil
			})
		},
	}); err != nil {
		return fmt.Errorf("wasm.swap: %w", err)
	}

	return nil
}

// ── arg/result types ─────────────────────────────────────────────────────────

type wasmLoadArgs struct {
	CellID string `cmd:"help=target cell ID,complete=cells"`
	Module string `cmd:"help=demo module to load (shield|pulse)"`
	Path   string `cmd:"optional,help=override path to .wasm module (default: per-module)"`
}

type wasmCellArgs struct {
	CellID string `cmd:"help=target cell ID,complete=cells"`
}

type wasmStatusResult struct {
	Cell   string
	Module string
	Status string
	Ticks  uint64
}
