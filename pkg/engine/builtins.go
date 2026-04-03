package engine

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/zenion/mmoserver/pkg/metrics"
)

// NodeRef represents a node that the console can execute commands on.
type NodeRef struct {
	ID      string
	Exec    func(fn func() string) string // runs fn on that node's game loop, returns result
	Metrics *metrics.NodeMetrics           // may be nil
}

// EntityInfo holds generic entity data for console display.
// Game layer populates this from ECS components.
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
	// Summary returns entity count by type name.
	Summary func() map[string]int
	// List returns all entities, optionally filtered by type name (empty = all).
	List func(typeName string) []EntityInfo
	// Get returns info for a single entity by network ID.
	Get func(netID uint32) (EntityInfo, bool)
	// Remove despawns an entity by network ID. Returns true if found.
	Remove func(netID uint32) bool
}

// BuiltinOpts configures which built-in command groups to register.
// Each non-nil field enables the corresponding commands.
type BuiltinOpts struct {
	Config      Configurable   // enables "config" group
	ConfigSave  func() error   // optional: enables "config save" subcommand
	ConfigReset func()         // optional: enables "config reset" subcommand
	Registry    *EntityRegistry // enables "entity add" subcommand
	Entities    *EntityOpts     // enables "entity" group (summary, list, get, add, remove)
	Nodes       []NodeRef       // enables "node" group
}

// RegisterBuiltins registers opt-in command groups based on which fields are set.
func (c *Console) RegisterBuiltins(opts BuiltinOpts) {
	if opts.Config != nil {
		c.registerConfigGroup(opts)
	}
	if opts.Entities != nil || opts.Registry != nil {
		c.registerEntityGroup(opts)
	}
	if len(opts.Nodes) > 0 {
		c.registerNodeGroup(opts.Nodes)
	}
	c.snapshotBuiltinCategories()
}

func (c *Console) registerConfigGroup(opts BuiltinOpts) {
	cfg := opts.Config
	c.SetCompletions("config_fields", cfg.Fields())

	fieldComplete := func(args []string) []string {
		return c.GetCompletions("config_fields")
	}

	g := NewCommandGroup("config", "config", "view and modify configuration")

	g.Add(Command{
		Name:        "list",
		Usage:       "config list",
		Description: "list all config fields and values",
		Fn: func(args []string) {
			output := c.ExecOnGameLoop(func() string {
				t := NewTable("Field", "Value")
				for _, field := range cfg.Fields() {
					val, err := cfg.GetField(field)
					if err != nil {
						val = fmt.Sprintf("<error: %v>", err)
					}
					t.Row(field, val)
				}
				return t.String()
			})
			c.Print(output)
		},
	})

	g.Add(Command{
		Name:        "get",
		Usage:       "config get <field>",
		Description: "show a single config field value",
		Complete:    fieldComplete,
		Fn: func(args []string) {
			if len(args) < 1 {
				c.Print("  usage: config get <field>\n")
				return
			}
			field := args[0]
			output := c.ExecOnGameLoop(func() string {
				val, err := cfg.GetField(field)
				if err != nil {
					return fmt.Sprintf("  error: %v\n", err)
				}
				return fmt.Sprintf("  %s = %s\n", field, val)
			})
			c.Print(output)
		},
	})

	g.Add(Command{
		Name:        "set",
		Usage:       "config set <field> <value>",
		Description: "change a config field at runtime",
		Complete:    fieldComplete,
		Fn: func(args []string) {
			if len(args) < 2 {
				c.Print("  usage: config set <field> <value>\n")
				return
			}
			field := args[0]
			value := args[1]
			output := c.ExecOnGameLoop(func() string {
				old, err := cfg.GetField(field)
				if err != nil {
					return fmt.Sprintf("  error: %v\n", err)
				}
				if err := cfg.SetField(field, value); err != nil {
					return fmt.Sprintf("  error: %v\n", err)
				}
				return fmt.Sprintf("  %s: %s -> %s\n", field, old, value)
			})
			c.Print(output)
		},
	})

	if opts.ConfigSave != nil {
		saveFn := opts.ConfigSave
		g.Add(Command{
			Name:        "save",
			Usage:       "config save",
			Description: "save current config to disk",
			Fn: func(args []string) {
				output := c.ExecOnGameLoop(func() string {
					if err := saveFn(); err != nil {
						return fmt.Sprintf("  config save failed: %v\n", err)
					}
					return "  config saved\n"
				})
				c.Print(output)
			},
		})
	}

	if opts.ConfigReset != nil {
		resetFn := opts.ConfigReset
		g.Add(Command{
			Name:        "reset",
			Usage:       "config reset",
			Description: "reset config to defaults",
			Fn: func(args []string) {
				output := c.ExecOnGameLoop(func() string {
					resetFn()
					return "  config reset to defaults\n"
				})
				c.Print(output)
			},
		})
	}

	origFn := g.commands["list"].Fn
	c.RegisterGroup(g)

	if synth, ok := c.commands["config"]; ok {
		innerFn := synth.Fn
		synth.Fn = func(args []string) {
			if len(args) == 0 {
				origFn(nil)
				return
			}
			innerFn(args)
		}
	}
}

func (c *Console) registerEntityGroup(opts BuiltinOpts) {
	ent := opts.Entities
	reg := opts.Registry

	g := NewCommandGroup("entity", "server", "inspect and manage entities")

	// entity summary (default when no subcommand)
	if ent != nil && ent.Summary != nil {
		g.Add(Command{
			Name:        "summary",
			Usage:       "entity summary",
			Description: "show entity count by type",
			Fn: func(args []string) {
				output := c.ExecOnGameLoop(func() string {
					counts := ent.Summary()
					if len(counts) == 0 {
						return "  no entities\n"
					}
					t := NewTable("Type", "Count")
					total := 0
					names := make([]string, 0, len(counts))
					for name := range counts {
						names = append(names, name)
					}
					sort.Strings(names)
					for _, name := range names {
						t.Row(name, counts[name])
						total += counts[name]
					}
					t.Row("TOTAL", total)
					return t.String()
				})
				c.Print(output)
			},
		})
	}

	// entity list [type]
	if ent != nil && ent.List != nil {
		typeComplete := func(args []string) []string {
			if reg != nil && len(args) == 0 {
				return reg.SpawnableNames()
			}
			return nil
		}
		g.Add(Command{
			Name:        "list",
			Usage:       "entity list [type]",
			Description: "list entities, optionally filtered by type",
			Complete:    typeComplete,
			Fn: func(args []string) {
				typeName := ""
				if len(args) > 0 {
					typeName = args[0]
				}
				output := c.ExecOnGameLoop(func() string {
					entities := ent.List(typeName)
					if len(entities) == 0 {
						return "  no entities\n"
					}
					sort.Slice(entities, func(i, j int) bool {
						if entities[i].Type != entities[j].Type {
							return entities[i].Type < entities[j].Type
						}
						return entities[i].NetID < entities[j].NetID
					})
					t := NewTable("NetID", "Node", "Type", "Cell", "Position")
					for _, e := range entities {
						t.Row(e.NetID, e.NodeID, e.Type, fmt.Sprintf("(%d,%d)", e.CellSX, e.CellSY), fmt.Sprintf("(%.0f, %.0f)", e.X, e.Y))
					}
					return t.String()
				})
				c.Print(output)
			},
		})
	}

	// entity get <netID>
	if ent != nil && ent.Get != nil {
		g.Add(Command{
			Name:        "get",
			Usage:       "entity get <netID>",
			Description: "show details for a specific entity",
			Fn: func(args []string) {
				if len(args) < 1 {
					c.Print("  usage: entity get <netID>\n")
					return
				}
				id, err := strconv.ParseUint(args[0], 10, 32)
				if err != nil {
					c.Printf("  invalid netID: %v\n", err)
					return
				}
				netID := uint32(id)
				output := c.ExecOnGameLoop(func() string {
					info, ok := ent.Get(netID)
					if !ok {
						return fmt.Sprintf("  entity %d not found\n", netID)
					}
					return fmt.Sprintf("  netID: %d\n  node:  %s\n  type:  %s\n  cell:  (%d,%d)\n  pos:   (%.1f, %.1f)\n  vel:   (%.1f, %.1f)\n",
						info.NetID, info.NodeID, info.Type, info.CellSX, info.CellSY, info.X, info.Y, info.VX, info.VY)
				})
				c.Print(output)
			},
		})
	}

	// entity add <type> <x> <y>
	if reg != nil {
		g.Add(Command{
			Name:        "add",
			Usage:       "entity add <type> <x> <y>",
			Description: "spawn a new entity at position",
			Complete: func(args []string) []string {
				if len(args) == 0 {
					return reg.SpawnableNames()
				}
				return nil
			},
			Fn: func(args []string) {
				if len(args) < 3 {
					c.Print("  usage: entity add <type> <x> <y>\n")
					return
				}
				typeName := args[0]
				x, err := strconv.ParseFloat(args[1], 32)
				if err != nil {
					c.Printf("  invalid x: %v\n", err)
					return
				}
				y, err := strconv.ParseFloat(args[2], 32)
				if err != nil {
					c.Printf("  invalid y: %v\n", err)
					return
				}
				output := c.ExecOnGameLoop(func() string {
					def, ok := reg.Get(typeName)
					if !ok {
						return fmt.Sprintf("  unknown entity type: %s\n", typeName)
					}
					if !def.Spawnable {
						return fmt.Sprintf("  entity type %s is not spawnable\n", typeName)
					}
					def.Spawn(float32(x), float32(y))
					return fmt.Sprintf("  spawned %s at (%.0f, %.0f)\n", typeName, x, y)
				})
				c.Print(output)
			},
		})
	}

	// entity remove <netID>
	if ent != nil && ent.Remove != nil {
		g.Add(Command{
			Name:        "remove",
			Usage:       "entity remove <netID>",
			Description: "despawn an entity by network ID",
			Fn: func(args []string) {
				if len(args) < 1 {
					c.Print("  usage: entity remove <netID>\n")
					return
				}
				id, err := strconv.ParseUint(args[0], 10, 32)
				if err != nil {
					c.Printf("  invalid netID: %v\n", err)
					return
				}
				netID := uint32(id)
				output := c.ExecOnGameLoop(func() string {
					if ent.Remove(netID) {
						return fmt.Sprintf("  removed entity %d\n", netID)
					}
					return fmt.Sprintf("  entity %d not found\n", netID)
				})
				c.Print(output)
			},
		})
	}

	c.RegisterGroup(g)

	// Bare "entity" defaults to "entity summary" if available
	if ent != nil && ent.Summary != nil {
		if synth, ok := c.commands["entity"]; ok {
			summaryFn := g.commands["summary"].Fn
			innerFn := synth.Fn
			synth.Fn = func(args []string) {
				if len(args) == 0 {
					summaryFn(nil)
					return
				}
				innerFn(args)
			}
		}
	}
}

func (c *Console) registerNodeGroup(nodes []NodeRef) {
	g := NewCommandGroup("node", "server", "node management and monitoring")

	g.Add(Command{
		Name:        "list",
		Usage:       "node list",
		Description: "list all nodes",
		Fn: func(args []string) {
			t := NewTable("Node")
			for _, n := range nodes {
				t.Row(n.ID)
			}
			c.Print(t.String())
		},
	})

	nodeIDs := make([]string, len(nodes))
	nodeMap := make(map[string]*NodeRef, len(nodes))
	for i := range nodes {
		nodeIDs[i] = nodes[i].ID
		nodeMap[nodes[i].ID] = &nodes[i]
	}

	g.Add(Command{
		Name:        "load",
		Usage:       "node load [nodeID]",
		Description: "show load snapshot per node",
		Complete: func(args []string) []string {
			return nodeIDs
		},
		Fn: func(args []string) {
			t := NewTable("Node", "Load", "Tick Avg", "Tick P95", "Entities", "Players", "Conns")

			if len(args) > 0 {
				ref, ok := nodeMap[args[0]]
				if !ok {
					c.Printf("  unknown node: %s\n", args[0])
					return
				}
				addNodeLoadRow(t, ref)
			} else {
				for i := range nodes {
					addNodeLoadRow(t, &nodes[i])
				}
			}
			c.Print(t.String())
		},
	})

	c.RegisterGroup(g)

	if synth, ok := c.commands["node"]; ok {
		listFn := g.commands["list"].Fn
		innerFn := synth.Fn
		synth.Fn = func(args []string) {
			if len(args) == 0 {
				listFn(nil)
				return
			}
			innerFn(args)
		}
	}
}

func addNodeLoadRow(t *Table, ref *NodeRef) {
	if ref.Metrics == nil {
		t.Row(ref.ID, "n/a", "n/a", "n/a", "n/a", "n/a", "n/a")
		return
	}
	snap := ref.Metrics.Snapshot()
	t.Row(
		ref.ID,
		fmt.Sprintf("%.2f", snap.CompositeLoad),
		FmtDuration(snap.Tick.AvgDuration),
		FmtDuration(snap.Tick.P95Duration),
		snap.Entities.Real,
		snap.Entities.Players,
		snap.Network.Connections,
	)
}
