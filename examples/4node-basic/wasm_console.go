package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"sync"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

const (
	wasmSysName     = "Wasm:shieldregen"
	wasmDefaultPath = "dist/wasmmods/shieldregen.wasm"
)

// loadedWasm tracks the currently-loaded wasm system per cell (keyed by mesh
// cell id) so swap/unload can snapshot the live instance. Demo state; single
// process only (Phase 0).
var (
	loadedWasmMu sync.Mutex
	loadedWasm   = map[string]mmokit.SwappableSystem{}
)

// decodeWasmTicks reads the module's internal tick counter from an 8-byte
// little-endian snapshot blob. Returns 0 for empty/short snapshots (stateless
// module or no state yet).
func decodeWasmTicks(b []byte) uint64 {
	if len(b) != 8 {
		return 0
	}
	return binary.LittleEndian.Uint64(b)
}

// buildWasmInstance creates a fresh wasm system instance OFF the loop goroutine,
// converting NewWasmSystem/Factory panics into errors so a bad path can't crash
// a cell's game loop.
func buildWasmInstance(path string) (sys mmokit.System, swap mmokit.SwappableSystem, err error) {
	if _, statErr := os.Stat(path); statErr != nil {
		return nil, nil, fmt.Errorf("module not found at %s (run `just wasm-build` first): %w", path, statErr)
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("load wasm module %s: %v", path, r)
		}
	}()
	def := mmokit.NewWasmSystem[gamecomp.Shield](path)
	s := def.Factory()
	return s, s.(mmokit.SwappableSystem), nil
}

func resolveWasmPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return wasmDefaultPath
	}
	return p
}

// registerWasmCommands wires the `wasm load/unload/swap <cellID>` typed commands
// onto the coordinator's cmdsys registry. Called from main.go via
// coord.OnConsoleReady. Demonstrates zero-downtime hot-load of the ShieldRegen
// wasm module onto a running cell, with internal state preserved across swap.
//
// Commands:
//
//	wasm.load <cellID> [path]    — hot-load the ShieldRegen wasm system onto a cell
//	wasm.unload <cellID>         — unload it, reporting the final tick counter
//	wasm.swap <cellID> [path]    — hot-swap, preserving the module's internal state
func registerWasmCommands(coord *mmokit.Process, reg *mmokit.CommandRegistry) error {
	if err := reg.Register(mmokit.Command{
		Verb:        "wasm.load",
		Capability:  "wasm.load",
		Description: "hot-load the ShieldRegen wasm system onto a cell (zero downtime)",
		Examples:    []string{"wasm load 0_0", "wasm load cell_0_0"},
		Route:       mmokit.RouteSpecificCell,
		Args:        wasmLoadArgs{},
		Result:      wasmStatusResult{},
		Handler: func(ctx context.Context, env *mmokit.CommandEnv, raw any) (any, error) {
			args := raw.(wasmLoadArgs)
			cell := resolveCell(coord, strings.TrimSpace(args.CellID))
			if cell == nil {
				return nil, fmt.Errorf("unknown cell %q — use `cell list` to see available cells", args.CellID)
			}
			// Build the instance OFF the loop goroutine: NewWasmSystem/Factory
			// panic on a bad path/ABI, and that panic must not reach the tick.
			sys, swap, err := buildWasmInstance(resolveWasmPath(args.Path))
			if err != nil {
				return nil, err
			}
			return mmokit.CmdOnLoop(ctx, cell.Engine, func() (wasmStatusResult, error) {
				loadedWasmMu.Lock()
				defer loadedWasmMu.Unlock()
				key := string(cell.MeshID)
				if _, ok := loadedWasm[key]; ok {
					return wasmStatusResult{}, fmt.Errorf("wasm system already loaded on %s (unload or swap first)", key)
				}
				mmokit.WireSystem(sys, cell.Stage.ECSWorld(), cell.Engine, cell.Stage)
				cell.Loop.AddSystemLive(wasmSysName, sys)
				loadedWasm[key] = swap
				return wasmStatusResult{Cell: key, Status: "loaded", Ticks: 0}, nil
			})
		},
	}); err != nil {
		return fmt.Errorf("wasm.load: %w", err)
	}

	if err := reg.Register(mmokit.Command{
		Verb:        "wasm.unload",
		Capability:  "wasm.unload",
		Description: "unload the ShieldRegen wasm system from a cell",
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
				state, _ := old.Snapshot()
				cell.Loop.RemoveSystemLive(wasmSysName)
				delete(loadedWasm, key)
				return wasmStatusResult{Cell: key, Status: "unloaded", Ticks: decodeWasmTicks(state)}, nil
			})
		},
	}); err != nil {
		return fmt.Errorf("wasm.unload: %w", err)
	}

	if err := reg.Register(mmokit.Command{
		Verb:        "wasm.swap",
		Capability:  "wasm.swap",
		Description: "hot-swap the ShieldRegen wasm system, preserving internal state",
		Examples:    []string{"wasm swap 0_0"},
		Route:       mmokit.RouteSpecificCell,
		Args:        wasmLoadArgs{},
		Result:      wasmStatusResult{},
		Handler: func(ctx context.Context, env *mmokit.CommandEnv, raw any) (any, error) {
			args := raw.(wasmLoadArgs)
			cell := resolveCell(coord, strings.TrimSpace(args.CellID))
			if cell == nil {
				return nil, fmt.Errorf("unknown cell %q", args.CellID)
			}
			// Build the replacement OFF the loop (panic-safe), then snapshot the
			// old + restore into the new + swap, all inside the loop closure.
			newSys, newSwap, err := buildWasmInstance(resolveWasmPath(args.Path))
			if err != nil {
				return nil, err
			}
			return mmokit.CmdOnLoop(ctx, cell.Engine, func() (wasmStatusResult, error) {
				loadedWasmMu.Lock()
				defer loadedWasmMu.Unlock()
				key := string(cell.MeshID)
				old, ok := loadedWasm[key]
				if !ok {
					return wasmStatusResult{}, fmt.Errorf("no wasm system loaded on %s (load first)", key)
				}
				state, err := old.Snapshot()
				if err != nil {
					return wasmStatusResult{}, fmt.Errorf("snapshot: %w", err)
				}
				if err := newSwap.Restore(state); err != nil {
					return wasmStatusResult{}, fmt.Errorf("restore: %w", err)
				}
				mmokit.WireSystem(newSys, cell.Stage.ECSWorld(), cell.Engine, cell.Stage)
				cell.Loop.RemoveSystemLive(wasmSysName)
				cell.Loop.AddSystemLive(wasmSysName, newSys)
				loadedWasm[key] = newSwap
				return wasmStatusResult{Cell: key, Status: "swapped", Ticks: decodeWasmTicks(state)}, nil
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
	Path   string `cmd:"optional,help=path to .wasm module (default dist/wasmmods/shieldregen.wasm)"`
}

type wasmCellArgs struct {
	CellID string `cmd:"help=target cell ID,complete=cells"`
}

type wasmStatusResult struct {
	Cell   string
	Status string
	Ticks  uint64
}
