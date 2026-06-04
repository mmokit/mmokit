package mmokit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/zenion/mmoserver/pkg/cmdsys"
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

// ── runtime cmdsys verbs: wasm.list / load / unload / swap ──────────────────

// wasmNameArgs is the shared argument shape for load/unload/swap: a required
// registered-system name plus optional --node / --cell filters.
type wasmNameArgs struct {
	Name string `cmd:"help=registered wasm system name"`
	Node string `cmd:"optional,name=node,help=limit to one host id,complete=hosts"`
	Cell string `cmd:"optional,name=cell,help=limit to one cell id,complete=cells"`
}

// wasmListArgs is the argument shape for wasm.list: optional --node / --cell.
type wasmListArgs struct {
	Node string `cmd:"optional,name=node,help=limit to one host id,complete=hosts"`
	Cell string `cmd:"optional,name=cell,help=limit to one cell id,complete=cells"`
}

// wasmOpRow is one per-cell result row for the wasm verbs.
type wasmOpRow struct {
	Cell   string
	Name   string
	Status string
	Ticks  uint64
}

// wasmOpResult is the table result shared by every wasm verb.
type wasmOpResult struct {
	Rows []wasmOpRow `cmd:"table"`
}

// decodeTicks8 reads the little-endian uint64 the wasm adapter ships as its
// snapshot blob (an 8-byte tick counter). Non-8-byte snapshots decode to 0.
func decodeTicks8(b []byte) uint64 {
	if len(b) != 8 {
		return 0
	}
	var v uint64
	for i := 0; i < 8; i++ {
		v |= uint64(b[i]) << (8 * i)
	}
	return v
}

// snapshotIfSwappable returns the system's serialized state if it implements
// SwappableSystem, else nil.
func snapshotIfSwappable(s System) []byte {
	if sw, ok := s.(SwappableSystem); ok {
		b, _ := sw.Snapshot()
		return b
	}
	return nil
}

// restoreIfSwappable restores state onto a SwappableSystem when both the
// system supports it and state is non-empty.
func restoreIfSwappable(s System, state []byte) error {
	if sw, ok := s.(SwappableSystem); ok && len(state) > 0 {
		return sw.Restore(state)
	}
	return nil
}

// closeIfCloser releases a system's resources if it exposes Close() error
// (the wasm adapter does — frees its instance/runtime).
func closeIfCloser(s System) {
	if c, ok := s.(interface{ Close() error }); ok {
		_ = c.Close()
	}
}

// registryEntry looks up a registered wasm system by name.
func registryEntry(p *universe.Process, name string) (wasmRegEntry, bool) {
	reg := wasmRegistryFor(p)
	reg.mu.Lock()
	defer reg.mu.Unlock()
	e, ok := reg.entries[name]
	return e, ok
}

// registeredNames returns the sorted set of registered wasm system names.
func registeredNames(p *universe.Process) []string {
	reg := wasmRegistryFor(p)
	reg.mu.Lock()
	defer reg.mu.Unlock()
	names := make([]string, 0, len(reg.entries))
	for k := range reg.entries {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// cellsMatching returns the process's local cells matching the optional
// node / cell filters. An empty filter matches everything. The cell filter
// accepts either a canonical mesh id or a short form (e.g. "0_0").
func cellsMatching(proc *universe.Process, node, cell string) []*universe.Cell {
	var wantCell string
	if cell != "" {
		if canon, err := ParseCellID(cell); err == nil {
			wantCell = string(canon.MeshID())
		} else {
			wantCell = cell
		}
	}
	var out []*universe.Cell
	for id, c := range proc.Cells {
		if node != "" && proc.HostIDForCell(c) != node {
			continue
		}
		if cell != "" && string(id) != wantCell {
			continue
		}
		out = append(out, c)
	}
	return out
}

// registerWasmVerbs registers the wasm.list/load/unload/swap verbs on the
// process command registry. All four are RouteAllHosts: in distributed mode
// they fan out to every host, which iterates its own local cells. Handlers
// close over proc and resolve cells live at invocation time. Safe to call
// once per process (Register errors on a duplicate verb).
func registerWasmVerbs(proc *universe.Process) error {
	reg := proc.CmdRegistry()

	// wasm.load — instantiate a registered wasm system onto matching cells.
	if err := reg.Register(cmdsys.Command{
		Verb: "wasm.load", Capability: "wasm.load",
		Description: "load a registered wasm system onto cells (default: all cells on all nodes)",
		Examples:    []string{"wasm load pulse", "wasm load pulse --cell 0_0"},
		Route:       cmdsys.RouteAllHosts, Args: wasmNameArgs{}, Result: wasmOpResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(wasmNameArgs)
			entry, ok := registryEntry(proc, args.Name)
			if !ok {
				return nil, fmt.Errorf("unknown wasm system %q (registered: %s)", args.Name, strings.Join(registeredNames(proc), ", "))
			}
			var rows []wasmOpRow
			for _, cell := range cellsMatching(proc, args.Node, args.Cell) {
				newSys, err := entry.build(entry.path)
				if err != nil {
					return nil, err
				}
				row, err := CmdOnLoop(ctx, cell.Engine, func() (wasmOpRow, error) {
					key := string(cell.MeshID)
					if _, present := cell.Loop.SystemByName(args.Name); present {
						closeIfCloser(newSys)
						return wasmOpRow{Cell: key, Name: args.Name, Status: "already-loaded"}, nil
					}
					WireSystem(newSys, cell.Stage.ECSWorld(), cell.Engine, cell.Stage)
					cell.Loop.AddSystemLive(args.Name, newSys)
					if src, ok := tunableSourceFor(newSys); ok {
						syncSource(tuneRegistryFor(proc), args.Name, src)
					}
					return wasmOpRow{Cell: key, Name: args.Name, Status: "loaded"}, nil
				})
				if err != nil {
					closeIfCloser(newSys)
					return nil, err
				}
				rows = append(rows, row)
			}
			return wasmOpResult{Rows: rows}, nil
		},
	}); err != nil {
		return fmt.Errorf("wasm.load: %w", err)
	}

	// wasm.unload — snapshot (for tick report), remove, and close on matching cells.
	if err := reg.Register(cmdsys.Command{
		Verb: "wasm.unload", Capability: "wasm.unload",
		Description: "unload a wasm system from cells (default: all cells on all nodes)",
		Examples:    []string{"wasm unload pulse"},
		Route:       cmdsys.RouteAllHosts, Args: wasmNameArgs{}, Result: wasmOpResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(wasmNameArgs)
			var rows []wasmOpRow
			for _, cell := range cellsMatching(proc, args.Node, args.Cell) {
				row, err := CmdOnLoop(ctx, cell.Engine, func() (wasmOpRow, error) {
					key := string(cell.MeshID)
					old, ok := cell.Loop.SystemByName(args.Name)
					if !ok {
						return wasmOpRow{Cell: key, Name: args.Name, Status: "not-loaded"}, nil
					}
					state := snapshotIfSwappable(old)
					cell.Loop.RemoveSystemLive(args.Name)
					closeIfCloser(old)
					return wasmOpRow{Cell: key, Name: args.Name, Status: "unloaded", Ticks: decodeTicks8(state)}, nil
				})
				if err != nil {
					return nil, err
				}
				rows = append(rows, row)
			}
			return wasmOpResult{Rows: rows}, nil
		},
	}); err != nil {
		return fmt.Errorf("wasm.unload: %w", err)
	}

	// wasm.swap — rebuild from disk and hot-swap in place, carrying state over.
	if err := reg.Register(cmdsys.Command{
		Verb: "wasm.swap", Capability: "wasm.swap",
		Description: "rebuild a registered wasm system from disk and hot-swap in place, preserving state (default: all cells/all nodes)",
		Examples:    []string{"wasm swap pulse", "wasm swap pulse --node host-b"},
		Route:       cmdsys.RouteAllHosts, Args: wasmNameArgs{}, Result: wasmOpResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(wasmNameArgs)
			entry, ok := registryEntry(proc, args.Name)
			if !ok {
				return nil, fmt.Errorf("unknown wasm system %q (registered: %s)", args.Name, strings.Join(registeredNames(proc), ", "))
			}
			var rows []wasmOpRow
			for _, cell := range cellsMatching(proc, args.Node, args.Cell) {
				newSys, err := entry.build(entry.path)
				if err != nil {
					return nil, err
				}
				row, err := CmdOnLoop(ctx, cell.Engine, func() (wasmOpRow, error) {
					key := string(cell.MeshID)
					old, ok := cell.Loop.SystemByName(args.Name)
					if !ok {
						WireSystem(newSys, cell.Stage.ECSWorld(), cell.Engine, cell.Stage)
						cell.Loop.AddSystemLive(args.Name, newSys)
						if src, ok := tunableSourceFor(newSys); ok {
							syncSource(tuneRegistryFor(proc), args.Name, src)
						}
						return wasmOpRow{Cell: key, Name: args.Name, Status: "loaded"}, nil
					}
					state := snapshotIfSwappable(old)
					if err := restoreIfSwappable(newSys, state); err != nil {
						return wasmOpRow{}, fmt.Errorf("restore: %w", err)
					}
					WireSystem(newSys, cell.Stage.ECSWorld(), cell.Engine, cell.Stage)
					cell.Loop.ReplaceSystemLive(args.Name, newSys)
					if src, ok := tunableSourceFor(newSys); ok {
						syncSource(tuneRegistryFor(proc), args.Name, src)
					}
					closeIfCloser(old)
					return wasmOpRow{Cell: key, Name: args.Name, Status: "swapped", Ticks: decodeTicks8(state)}, nil
				})
				if err != nil {
					closeIfCloser(newSys)
					return nil, err
				}
				rows = append(rows, row)
			}
			return wasmOpResult{Rows: rows}, nil
		},
	}); err != nil {
		return fmt.Errorf("wasm.swap: %w", err)
	}

	// wasm.list — registered systems × matching cells with per-cell load state.
	if err := reg.Register(cmdsys.Command{
		Verb: "wasm.list", Capability: "wasm.list",
		Description: "list registered wasm systems and per-cell load state",
		Examples:    []string{"wasm list"},
		Route:       cmdsys.RouteAllHosts, Args: wasmListArgs{}, Result: wasmOpResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(wasmListArgs)
			names := registeredNames(proc)
			var rows []wasmOpRow
			for _, cell := range cellsMatching(proc, args.Node, args.Cell) {
				for _, name := range names {
					row, _ := CmdOnLoop(ctx, cell.Engine, func() (wasmOpRow, error) {
						key := string(cell.MeshID)
						sys, ok := cell.Loop.SystemByName(name)
						if !ok {
							return wasmOpRow{Cell: key, Name: name, Status: "unloaded"}, nil
						}
						return wasmOpRow{Cell: key, Name: name, Status: "loaded", Ticks: decodeTicks8(snapshotIfSwappable(sys))}, nil
					})
					rows = append(rows, row)
				}
			}
			return wasmOpResult{Rows: rows}, nil
		},
	}); err != nil {
		return fmt.Errorf("wasm.list: %w", err)
	}

	return nil
}
