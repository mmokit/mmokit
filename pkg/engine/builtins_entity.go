package engine

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

func (c *Console) registerEntityCommands(opts BuiltinOpts) {
	ent := opts.Entities
	reg := opts.Registry
	eng := opts.Engine
	onLoop := func(ctx context.Context, fn func() error) error {
		if eng != nil {
			return eng.RunOnLoop(ctx, fn)
		}
		return fn()
	}

	mustRegister := func(cmd cmdsys.Command, usage string) {
		if err := c.adapter.registerTyped(cmd, usage, nil); err != nil {
			panic(fmt.Sprintf("console: registerTyped %q: %v", cmd.Verb, err))
		}
	}

	if ent != nil && ent.Summary != nil {
		mustRegister(cmdsys.Command{
			Verb:        "entity.summary",
			Capability:  "entity.summary",
			Description: "show entity count by type",
			Route:       cmdsys.RouteLocal,
			Args:        nil,
			Result:      entitySummaryResult{},
			Handler: func(ctx context.Context, env *cmdsys.Env, args any) (any, error) {
				var result any
				err := onLoop(ctx, func() error {
					counts := ent.Summary()
					if len(counts) == 0 {
						result = entitySummaryResult{}
						return nil
					}
					names := make([]string, 0, len(counts))
					for name := range counts {
						names = append(names, name)
					}
					sort.Strings(names)
					total := 0
					var entries []entitySummaryEntry
					for _, name := range names {
						entries = append(entries, entitySummaryEntry{Type: name, Count: counts[name]})
						total += counts[name]
					}
					entries = append(entries, entitySummaryEntry{Type: "TOTAL", Count: total})
					result = entitySummaryResult{Entries: entries}
					return nil
				})
				return result, err
			},
		}, "entity summary")
	}

	if ent != nil && ent.List != nil {
		mustRegister(cmdsys.Command{
			Verb:        "entity.list",
			Capability:  "entity.list",
			Description: "list entities, optionally filtered by type",
			Route:       cmdsys.RouteLocal,
			Args:        entityListArgs{},
			Result:      entityListResult{},
			Handler: func(ctx context.Context, env *cmdsys.Env, args any) (any, error) {
				a := args.(entityListArgs)
				var result any
				err := onLoop(ctx, func() error {
					entities := ent.List(a.Type)
					sort.Slice(entities, func(i, j int) bool {
						if entities[i].Type != entities[j].Type {
							return entities[i].Type < entities[j].Type
						}
						return entities[i].NetID < entities[j].NetID
					})
					var entries []entityListEntry
					for _, e := range entities {
						entries = append(entries, entityListEntry{
							NetID:    e.NetID,
							CellID:   e.CellID,
							Type:     e.Type,
							Cell:     fmt.Sprintf("(%d,%d)", e.CellSX, e.CellSY),
							Position: fmt.Sprintf("(%.0f, %.0f)", e.X, e.Y),
						})
					}
					result = entityListResult{Entries: entries}
					return nil
				})
				return result, err
			},
		}, "entity list [type]")
	}

	if ent != nil && ent.Get != nil {
		mustRegister(cmdsys.Command{
			Verb:        "entity.get",
			Capability:  "entity.get",
			Description: "show details for a specific entity",
			Route:       cmdsys.RouteLocal,
			Args:        entityGetArgs{},
			Result:      entityGetResult{},
			Handler: func(ctx context.Context, env *cmdsys.Env, args any) (any, error) {
				a := args.(entityGetArgs)
				var result any
				err := onLoop(ctx, func() error {
					info, ok := ent.Get(a.NetID)
					if !ok {
						return fmt.Errorf("entity %d not found", a.NetID)
					}
					result = entityGetResult{
						NetID:  info.NetID,
						CellID: info.CellID,
						Type:   info.Type,
						Cell:  fmt.Sprintf("(%d,%d)", info.CellSX, info.CellSY),
						Pos:   fmt.Sprintf("(%.1f, %.1f)", info.X, info.Y),
						Vel:   fmt.Sprintf("(%.1f, %.1f)", info.VX, info.VY),
					}
					return nil
				})
				return result, err
			},
		}, "entity get <netID>")
	}

	if reg != nil {
		mustRegister(cmdsys.Command{
			Verb:        "entity.add",
			Capability:  "entity.add",
			Description: "spawn a new entity at position",
			Route:       cmdsys.RouteLocal,
			Args:        entityAddArgs{},
			Result:      entityAddResult{},
			Handler: func(ctx context.Context, env *cmdsys.Env, args any) (any, error) {
				a := args.(entityAddArgs)
				def, ok := reg.Get(a.Type)
				if !ok {
					return nil, fmt.Errorf("unknown entity type: %s", a.Type)
				}
				if !def.Spawnable {
					return nil, fmt.Errorf("entity type %s is not spawnable", a.Type)
				}
				var result any
				err := onLoop(ctx, func() error {
					def.Spawn(a.X, a.Y)
					result = entityAddResult{
						Type:    a.Type,
						X:       a.X,
						Y:       a.Y,
						Message: fmt.Sprintf("spawned %s at (%.0f, %.0f)", a.Type, a.X, a.Y),
					}
					return nil
				})
				return result, err
			},
		}, "entity add <type> <x> <y>")
	}

	if ent != nil && ent.Remove != nil {
		mustRegister(cmdsys.Command{
			Verb:        "entity.remove",
			Capability:  "entity.remove",
			Description: "despawn an entity by network ID",
			Route:       cmdsys.RouteLocal,
			Args:        entityRemoveArgs{},
			Result:      entityRemoveResult{},
			Handler: func(ctx context.Context, env *cmdsys.Env, args any) (any, error) {
				a := args.(entityRemoveArgs)
				var result any
				err := onLoop(ctx, func() error {
					ok := ent.Remove(a.NetID)
					result = entityRemoveResult{NetID: a.NetID, OK: ok}
					return nil
				})
				return result, err
			},
		}, "entity remove <netID>")
	}

	// Top-level "entity" group dispatcher.
	_ = c.adapter.registerGroupShim("entity", "inspect and manage entities")
}

// renderEntityRemoveResult formats the remove result.
func renderEntityRemoveResult(r entityRemoveResult) string {
	if r.OK {
		return fmt.Sprintf("  removed entity %d\n", r.NetID)
	}
	return fmt.Sprintf("  entity %d not found\n", r.NetID)
}

func init() {
	registerResultRenderer(entityAddResult{}, func(v any) string {
		r := v.(entityAddResult)
		return "  " + r.Message + "\n"
	})
	registerResultRenderer(entityRemoveResult{}, func(v any) string {
		return renderEntityRemoveResult(v.(entityRemoveResult))
	})
	registerResultRenderer(entityGetResult{}, func(v any) string {
		r := v.(entityGetResult)
		return fmt.Sprintf("  netID: %d\n  node:  %s\n  type:  %s\n  cell:  %s\n  pos:   %s\n  vel:   %s\n",
			r.NetID, r.CellID, r.Type, r.Cell, r.Pos, r.Vel)
	})
	registerResultRenderer(entitySummaryResult{}, func(v any) string {
		r := v.(entitySummaryResult)
		if len(r.Entries) == 0 {
			return "  no entities\n"
		}
		t := NewTable("Type", "Count")
		for _, e := range r.Entries {
			t.Row(e.Type, strconv.Itoa(e.Count))
		}
		return t.String()
	})
	registerResultRenderer(entityListResult{}, func(v any) string {
		r := v.(entityListResult)
		if len(r.Entries) == 0 {
			return "  no entities\n"
		}
		t := NewTable("NetID", "Node", "Type", "Cell", "Position")
		for _, e := range r.Entries {
			t.Row(e.NetID, e.CellID, e.Type, e.Cell, e.Position)
		}
		return t.String()
	})
}
