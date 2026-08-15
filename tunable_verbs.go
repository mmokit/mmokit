package mmokit

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mmokit/mmokit/pkg/cmdsys"
	"github.com/mmokit/mmokit/pkg/tunable"
	"github.com/mmokit/mmokit/pkg/universe"
)

const catTune = "tune"

type tuneSetArgs struct {
	System string `cmd:"help=system name"`
	Field  string `cmd:"help=tunable field name"`
	Value  string `cmd:"help=new value"`
	Node   string `cmd:"optional,name=node,help=limit to one host id,complete=hosts"`
	Cell   string `cmd:"optional,name=cell,help=limit to one cell id,complete=cells"`
}

type tuneGetArgs struct {
	System string `cmd:"help=system name"`
	Field  string `cmd:"help=tunable field name"`
}

type tuneResetArgs struct {
	System string `cmd:"help=system name"`
	Field  string `cmd:"optional,help=field to reset (default: all fields)"`
	Node   string `cmd:"optional,name=node,help=limit to one host id,complete=hosts"`
	Cell   string `cmd:"optional,name=cell,help=limit to one cell id,complete=cells"`
}

type tuneListArgs struct {
	System string `cmd:"optional,name=system,help=limit to one system"`
	Node   string `cmd:"optional,name=node,help=limit to one host id,complete=hosts"`
	Cell   string `cmd:"optional,name=cell,help=limit to one cell id,complete=cells"`
}

type tuneRow struct {
	System  string
	Field   string
	Value   string
	Default string
	Min     string
	Max     string
	Kind    string
}

type tuneResult struct {
	Rows []tuneRow `cmd:"table"`
}

type tuneGetResult struct {
	System string
	Field  string
	Value  string
}

// publishTunables pushes a system's current tunable rows on the admin "tunables"
// SSE topic so open /tunables dashboards update live when any operator changes a
// value. No-op when no admin subscribers are listening (PublishAdminTopic guards
// that). Called after tune.set / tune.reset mutate the registry.
func publishTunables(p *universe.Process, system string) {
	PublishAdminTopic(p, "tunables", map[string]any{
		"system": system,
		"rows":   rowsFor(tuneRegistryFor(p), system),
	})
}

// registerTuneVerbs registers tune.list/get/set/reset on the process registry.
// All RouteAllHosts: fan out to every host, each iterating its local cells.
func registerTuneVerbs(proc *universe.Process) error {
	reg := proc.CmdRegistry()
	proc.Log.RegisterCategories(catTune)

	applySet := func(ctx context.Context, system, field, value string, node, cell string) error {
		for _, c := range cellsMatching(proc, node, cell) {
			cc := c
			if _, err := CmdOnLoop(ctx, cc.Engine, func() (struct{}, error) {
				sys, ok := cc.Loop.SystemByName(system)
				if !ok {
					return struct{}{}, nil
				}
				src, ok := tunableSourceFor(sys)
				if !ok {
					return struct{}{}, nil
				}
				if err := src.Set(field, value); err != nil {
					return struct{}{}, err
				}
				return struct{}{}, nil
			}); err != nil {
				return err
			}
		}
		return nil
	}

	if err := reg.Register(cmdsys.Command{
		Verb: "tune.set", Capability: "tune.set",
		Description: "set a system tunable at runtime (default: all cells/all nodes)",
		Examples:    []string{"tune set Field amplitude 420", "tune set Field amplitude 300 --cell 0_0"},
		Route:       cmdsys.RouteAllHosts, Args: tuneSetArgs{}, Result: tuneResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			a := raw.(tuneSetArgs)
			reg := tuneRegistryFor(proc)
			defs := reg.defs(a.System)
			d, ok := findDef(defs, a.Field)
			if !ok {
				return nil, fmt.Errorf("system %q has no tunable %q (known: %s)", a.System, a.Field, fieldNames(defs))
			}
			if err := d.Validate(a.Value); err != nil {
				return nil, err
			}
			// Thread the canonical field name (d.Name) downstream so the
			// registry key and the per-cell Source.Set match exactly even when
			// the operator typed a different case.
			if a.Cell == "" && a.Node == "" {
				reg.setValue(a.System, d.Name, a.Value)
			}
			if err := applySet(ctx, a.System, d.Name, a.Value, a.Node, a.Cell); err != nil {
				return nil, err
			}
			publishTunables(proc, a.System)
			proc.Log.Log(catTune, "tune set %s.%s=%s (cell=%q node=%q)", a.System, d.Name, a.Value, a.Cell, a.Node)
			return tuneResult{Rows: rowsFor(reg, a.System)}, nil
		},
	}); err != nil {
		return fmt.Errorf("tune.set: %w", err)
	}

	// tune.list / tune.get are RouteAllHosts like the mutating verbs: the
	// per-process tunable registry is populated by SyncCellTunables from
	// locally-owned cells only, so a pure-coordinator process (distributed
	// mode) has an empty registry and a RouteLocal read there returns nothing.
	// Fanning out reads each cell-bearing host's registry instead; in
	// single-process mode the colocated host short-circuits to InvokeLocal, so
	// behavior is unchanged there.
	if err := reg.Register(cmdsys.Command{
		Verb: "tune.get", Capability: "tune.get",
		Description: "show one system tunable's current value",
		Examples:    []string{"tune get Field amplitude"},
		Route:       cmdsys.RouteAllHosts, Args: tuneGetArgs{}, Result: tuneGetResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			a := raw.(tuneGetArgs)
			reg := tuneRegistryFor(proc)
			d, ok := findDef(reg.defs(a.System), a.Field)
			if !ok {
				return nil, fmt.Errorf("no tunable %s.%s", a.System, a.Field)
			}
			v, _ := reg.value(a.System, d.Name)
			return tuneGetResult{System: a.System, Field: d.Name, Value: v}, nil
		},
	}); err != nil {
		return fmt.Errorf("tune.get: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb: "tune.reset", Capability: "tune.reset",
		Description: "reset a system tunable (or all its tunables) to tag defaults",
		Examples:    []string{"tune reset Field", "tune reset Field amplitude"},
		Route:       cmdsys.RouteAllHosts, Args: tuneResetArgs{}, Result: tuneResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			a := raw.(tuneResetArgs)
			reg := tuneRegistryFor(proc)
			for _, d := range reg.defs(a.System) {
				if a.Field != "" && !strings.EqualFold(d.Name, a.Field) {
					continue
				}
				if d.Default == "" {
					continue
				}
				if a.Cell == "" && a.Node == "" {
					reg.setValue(a.System, d.Name, d.Default)
				}
				if err := applySet(ctx, a.System, d.Name, d.Default, a.Node, a.Cell); err != nil {
					return nil, err
				}
			}
			publishTunables(proc, a.System)
			return tuneResult{Rows: rowsFor(reg, a.System)}, nil
		},
	}); err != nil {
		return fmt.Errorf("tune.reset: %w", err)
	}

	// RouteAllHosts — see tune.get: each host reports its own registry; the
	// admin SPA merges targets[] (tunables.ts), the console renders one table
	// per host.
	if err := reg.Register(cmdsys.Command{
		Verb: "tune.list", Capability: "tune.list",
		Description: "list system tunables and current values",
		Examples:    []string{"tune list", "tune list --system Field"},
		Route:       cmdsys.RouteAllHosts, Args: tuneListArgs{}, Result: tuneResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			a := raw.(tuneListArgs)
			reg := tuneRegistryFor(proc)
			names := reg.systemNames()
			if a.System != "" {
				names = []string{a.System}
			}
			var rows []tuneRow
			for _, name := range names {
				rows = append(rows, rowsFor(reg, name)...)
			}
			return tuneResult{Rows: rows}, nil
		},
	}); err != nil {
		return fmt.Errorf("tune.list: %w", err)
	}

	return nil
}

func rowsFor(reg *tuneRegistry, system string) []tuneRow {
	defs := reg.defs(system)
	rows := make([]tuneRow, 0, len(defs))
	for _, d := range defs {
		rows = append(rows, tuneRow{
			System: system, Field: d.Name, Value: d.Value,
			Default: d.Default, Min: d.Min, Max: d.Max, Kind: d.Kind.String(),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Field < rows[j].Field })
	return rows
}

// findDef resolves a user-supplied field name to its descriptor
// case-insensitively (field names are PascalCase Go identifiers; operators type
// them however they like). The returned Def carries the canonical Name, which
// callers thread downstream so the registry stays consistently keyed.
func findDef(defs []tunable.Def, field string) (tunable.Def, bool) {
	for _, d := range defs {
		if strings.EqualFold(d.Name, field) {
			return d, true
		}
	}
	return tunable.Def{}, false
}

func fieldNames(defs []tunable.Def) string {
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Name
	}
	return strings.Join(names, ", ")
}
