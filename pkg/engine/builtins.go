package engine

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

// EntityInfo holds generic entity data for console display.
type EntityInfo struct {
	NetID    uint32
	NodeID   string
	Type     string
	X, Y     float32
	VX, VY   float32
	CellSX   int32
	CellSY   int32
}

// EntityOpts configures the "entity" command group callbacks.
// All callbacks are called from the game loop via ExecOnGameLoop.
type EntityOpts struct {
	Summary func() map[string]int
	List    func(typeName string) []EntityInfo
	Get     func(netID uint32) (EntityInfo, bool)
	Remove  func(netID uint32) bool
}

// BuiltinOpts configures which built-in command groups to register.
type BuiltinOpts struct {
	Config          Configurable
	ConfigSave      func() error
	ConfigReset     func()
	ConfigOnChanged func(field string)
	Registry        *EntityRegistry
	Entities        *EntityOpts
}

// RegisterBuiltins registers opt-in command groups based on which fields are set.
func (c *Console) RegisterBuiltins(opts BuiltinOpts) {
	if opts.Config != nil {
		c.registerConfigCommands(opts)
	}
	if opts.Entities != nil || opts.Registry != nil {
		c.registerEntityCommands(opts)
	}
	c.snapshotBuiltinCategories()
}

// ---------------------------------------------------------------------------
// Config typed args/results
// ---------------------------------------------------------------------------

type configGetArgs struct {
	Field string
}

type configGetResult struct {
	Field string
	Value string
}

type configSetArgs struct {
	Field string
	Value string
}

type configSetResult struct {
	Field string
	Old   string
	New   string
}

type configEntry struct {
	Field string
	Value string
}

type configListResult struct {
	Entries []configEntry `cmd:"table"`
}

type configSaveResult struct {
	OK      bool
	Message string
}

type configResetResult struct {
	OK bool
}

func (c *Console) registerConfigCommands(opts BuiltinOpts) {
	cfg := opts.Config
	c.SetCompletions("config_fields", cfg.Fields())

	mustRegister := func(cmd cmdsys.Command, usage string) {
		if err := c.adapter.registerTyped(cmd, usage, nil); err != nil {
			panic(fmt.Sprintf("console: registerTyped %q: %v", cmd.Verb, err))
		}
	}

	mustRegister(cmdsys.Command{
		Verb:        "config.list",
		Capability:  "config.list",
		Description: "list all config fields and values",
		Route:       cmdsys.RouteLocal,
		Args:        nil,
		Result:      configListResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, args any) (any, error) {
			var entries []configEntry
			for _, field := range cfg.Fields() {
				val, err := cfg.GetField(field)
				if err != nil {
					val = fmt.Sprintf("<error: %v>", err)
				}
				entries = append(entries, configEntry{Field: field, Value: val})
			}
			return configListResult{Entries: entries}, nil
		},
	}, "config list")

	mustRegister(cmdsys.Command{
		Verb:        "config.get",
		Capability:  "config.get",
		Description: "show a single config field value",
		Route:       cmdsys.RouteLocal,
		Args:        configGetArgs{},
		Result:      configGetResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, args any) (any, error) {
			a := args.(configGetArgs)
			val, err := cfg.GetField(a.Field)
			if err != nil {
				return nil, err
			}
			return configGetResult{Field: a.Field, Value: val}, nil
		},
	}, "config get <field>")

	onChanged := opts.ConfigOnChanged
	mustRegister(cmdsys.Command{
		Verb:        "config.set",
		Capability:  "config.set",
		Description: "change a config field at runtime",
		Route:       cmdsys.RouteLocal,
		Args:        configSetArgs{},
		Result:      configSetResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, args any) (any, error) {
			a := args.(configSetArgs)
			old, err := cfg.GetField(a.Field)
			if err != nil {
				return nil, err
			}
			if err := cfg.SetField(a.Field, a.Value); err != nil {
				return nil, err
			}
			if onChanged != nil {
				onChanged(a.Field)
			}
			return configSetResult{Field: a.Field, Old: old, New: a.Value}, nil
		},
	}, "config set <field> <value>")

	if opts.ConfigSave != nil {
		saveFn := opts.ConfigSave
		mustRegister(cmdsys.Command{
			Verb:        "config.save",
			Capability:  "config.save",
			Description: "save current config to disk",
			Route:       cmdsys.RouteLocal,
			Args:        nil,
			Result:      configSaveResult{},
			Handler: func(ctx context.Context, env *cmdsys.Env, args any) (any, error) {
				if err := saveFn(); err != nil {
					return configSaveResult{OK: false, Message: err.Error()}, nil
				}
				return configSaveResult{OK: true, Message: "config saved"}, nil
			},
		}, "config save")
	}

	if opts.ConfigReset != nil {
		resetFn := opts.ConfigReset
		mustRegister(cmdsys.Command{
			Verb:        "config.reset",
			Capability:  "config.reset",
			Description: "reset config to defaults",
			Route:       cmdsys.RouteLocal,
			Args:        nil,
			Result:      configResetResult{},
			Handler: func(ctx context.Context, env *cmdsys.Env, args any) (any, error) {
				resetFn()
				return configResetResult{OK: true}, nil
			},
		}, "config reset")
	}

	// Top-level "config" group dispatcher.
	_ = c.adapter.registerGroupShim("config", "view and modify configuration")
}

// ---------------------------------------------------------------------------
// Entity typed args/results
// ---------------------------------------------------------------------------

type entitySummaryEntry struct {
	Type  string
	Count int
}

type entitySummaryResult struct {
	Entries []entitySummaryEntry `cmd:"table"`
}

type entityListArgs struct {
	Type string `cmd:"optional"`
}

type entityListEntry struct {
	NetID    uint32
	Node     string
	Type     string
	Cell     string
	Position string
}

type entityListResult struct {
	Entries []entityListEntry `cmd:"table"`
}

type entityGetArgs struct {
	NetID uint32
}

type entityGetResult struct {
	NetID  uint32
	Node   string
	Type   string
	Cell   string
	Pos    string
	Vel    string
}

type entityAddArgs struct {
	Type string
	X    float32
	Y    float32
}

type entityAddResult struct {
	Type    string
	X, Y    float32
	Message string
}

type entityRemoveArgs struct {
	NetID uint32
}

type entityRemoveResult struct {
	NetID uint32
	OK    bool
}

func (c *Console) registerEntityCommands(opts BuiltinOpts) {
	ent := opts.Entities
	reg := opts.Registry

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
				counts := ent.Summary()
				if len(counts) == 0 {
					return entitySummaryResult{}, nil
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
				return entitySummaryResult{Entries: entries}, nil
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
						Node:     e.NodeID,
						Type:     e.Type,
						Cell:     fmt.Sprintf("(%d,%d)", e.CellSX, e.CellSY),
						Position: fmt.Sprintf("(%.0f, %.0f)", e.X, e.Y),
					})
				}
				return entityListResult{Entries: entries}, nil
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
				info, ok := ent.Get(a.NetID)
				if !ok {
					return nil, fmt.Errorf("entity %d not found", a.NetID)
				}
				return entityGetResult{
					NetID: info.NetID,
					Node:  info.NodeID,
					Type:  info.Type,
					Cell:  fmt.Sprintf("(%d,%d)", info.CellSX, info.CellSY),
					Pos:   fmt.Sprintf("(%.1f, %.1f)", info.X, info.Y),
					Vel:   fmt.Sprintf("(%.1f, %.1f)", info.VX, info.VY),
				}, nil
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
				def.Spawn(a.X, a.Y)
				return entityAddResult{
					Type:    a.Type,
					X:       a.X,
					Y:       a.Y,
					Message: fmt.Sprintf("spawned %s at (%.0f, %.0f)", a.Type, a.X, a.Y),
				}, nil
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
				ok := ent.Remove(a.NetID)
				return entityRemoveResult{NetID: a.NetID, OK: ok}, nil
			},
		}, "entity remove <netID>")
	}

	// Top-level "entity" group dispatcher.
	_ = c.adapter.registerGroupShim("entity", "inspect and manage entities")
}

// renderConfigGetResult formats a configGetResult for human display.
func renderConfigGetResult(r configGetResult) string {
	return fmt.Sprintf("  %s = %s\n", r.Field, r.Value)
}

// renderConfigSetResult formats a configSetResult for human display.
func renderConfigSetResult(r configSetResult) string {
	return fmt.Sprintf("  %s: %s -> %s\n", r.Field, r.Old, r.New)
}

// renderEntityRemoveResult formats the remove result.
func renderEntityRemoveResult(r entityRemoveResult) string {
	if r.OK {
		return fmt.Sprintf("  removed entity %d\n", r.NetID)
	}
	return fmt.Sprintf("  entity %d not found\n", r.NetID)
}

// init registers custom renderers for builtin result types.
// These override the generic renderResult reflection path.
func init() {
	registerResultRenderer(configGetResult{}, func(v any) string {
		return renderConfigGetResult(v.(configGetResult))
	})
	registerResultRenderer(configSetResult{}, func(v any) string {
		return renderConfigSetResult(v.(configSetResult))
	})
	registerResultRenderer(configSaveResult{}, func(v any) string {
		r := v.(configSaveResult)
		return "  " + r.Message + "\n"
	})
	registerResultRenderer(configResetResult{}, func(v any) string {
		return "  config reset to defaults\n"
	})
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
			r.NetID, r.Node, r.Type, r.Cell, r.Pos, r.Vel)
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
			t.Row(e.NetID, e.Node, e.Type, e.Cell, e.Position)
		}
		return t.String()
	})
}
