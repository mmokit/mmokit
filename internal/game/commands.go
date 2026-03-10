package game

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mlange-42/ark/ecs"
	"google.golang.org/protobuf/proto"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/persist"
)

// RegisterCommands registers all game-specific admin commands on the console.
func RegisterCommands(console *engine.Console, gw *GameWorld, store persist.Store) {
	gw.console = console

	// Set static completions
	console.SetCompletions("resources", []string{"ore", "crystal", "gas", "metal"})
	console.SetCompletions("config_fields", gw.Config.Fields())
	console.SetCompletions("spawn_types", gw.Registry.SpawnableNames())

	playerComplete := func(args []string) []string {
		return console.GetCompletions("players")
	}

	console.Register(engine.Command{
		Name: "config", Aliases: []string{"cfg"},
		Category: "config", Usage: "config [field]", Description: "list or show config value",
		Complete: func(args []string) []string {
			return console.GetCompletions("config_fields")
		},
		Fn: func(args []string) {
			if len(args) < 1 {
				result := console.ExecOnGameLoop(func() string {
					var sb strings.Builder
					fmt.Fprintf(&sb, "  %-22s %s\n", "FIELD", "VALUE")
					for _, name := range gw.Config.Fields() {
						val, _ := gw.Config.GetField(name)
						fmt.Fprintf(&sb, "  %-22s %s\n", name, val)
					}
					return sb.String()
				})
				fmt.Print(result)
			} else {
				fieldName := args[0]
				result := console.ExecOnGameLoop(func() string {
					val, err := gw.Config.GetField(fieldName)
					if err != nil {
						return fmt.Sprintf("  %s", err)
					}
					return fmt.Sprintf("  %s = %s", fieldName, val)
				})
				fmt.Println(result)
			}
		},
	})

	console.Register(engine.Command{
		Name: "set", Category: "config",
		Usage: "set <field> <value>", Description: "change a config value at runtime",
		Complete: func(args []string) []string {
			if len(args) == 0 {
				return console.GetCompletions("config_fields")
			}
			return nil
		},
		Fn: func(args []string) {
			if len(args) < 2 {
				fmt.Println("  usage: set <field> <value>")
			} else {
				fieldName := args[0]
				value := args[1]
				result := console.ExecOnGameLoop(func() string {
					old, err := gw.Config.GetField(fieldName)
					if err != nil {
						return fmt.Sprintf("  %s", err)
					}
					if err := gw.Config.SetField(fieldName, value); err != nil {
						return fmt.Sprintf("  %s", err)
					}
					return fmt.Sprintf("  %s: %s -> %s", fieldName, old, value)
				})
				fmt.Println(result)
			}
		},
	})

	console.Register(engine.Command{
		Name: "save", Category: "config",
		Usage: "save", Description: "persist current config to database",
		Fn: func(args []string) {
			result := console.ExecOnGameLoop(func() string {
				if err := SaveConfig(store, &gw.Config); err != nil {
					return fmt.Sprintf("  failed to save config: %s", err)
				}
				return "  config saved to database"
			})
			fmt.Println(result)
		},
	})

	console.Register(engine.Command{
		Name: "resetconfig", Category: "config",
		Usage: "resetconfig", Description: "reset all config values to defaults",
		Fn: func(args []string) {
			result := console.ExecOnGameLoop(func() string {
				gw.Config = DefaultGameConfig()
				return "  config reset to defaults"
			})
			fmt.Println(result)
		},
	})

	console.Register(engine.Command{
		Name: "players", Aliases: []string{"ps"},
		Category: "admin", Usage: "players", Description: "list connected players",
		Fn: func(args []string) {
			result := console.ExecOnGameLoop(func() string {
				if len(gw.PlayerEntities) == 0 {
					return "  no players connected"
				}
				var sb strings.Builder
				fmt.Fprintf(&sb, "  %-6s %-16s %-6s %-16s %-9s %-9s %-8s %-30s\n", "CONN", "USERNAME", "NETID", "POSITION", "HP", "SHIELD", "FLUX", "CARGO")
				for connID, entity := range gw.PlayerEntities {
					if !gw.ECS.Alive(entity) {
						continue
					}
					username := gw.ConnToUsername[connID]
					var netID uint32
					if gw.NetworkIDMap.HasAll(entity) {
						netID = gw.NetworkIDMap.Get(entity).ID
					}
					var posStr string
					if gw.PositionMap.HasAll(entity) {
						pos := gw.PositionMap.Get(entity)
						posStr = fmt.Sprintf("%.0f, %.0f", pos.X, pos.Y)
					}
					var hp, shield string
					if gw.HealthMap.HasAll(entity) {
						h := gw.HealthMap.Get(entity)
						hp = fmt.Sprintf("%.0f/%.0f", h.Current, h.Max)
					}
					if gw.ShieldMap.HasAll(entity) {
						s := gw.ShieldMap.Get(entity)
						shield = fmt.Sprintf("%.0f/%.0f", s.Current, s.Max)
					}
					var fluxStr string
					pdata := gw.PlayerDB.Get(username)
					if pdata != nil {
						fluxStr = fmt.Sprintf("%.0f", pdata.Flux)
					}
					var cargoStr string
					if gw.InventoryMap.HasAll(entity) {
						inv := gw.InventoryMap.Get(entity)
						r := inv.Resources
						cargoStr = fmt.Sprintf("O:%.0f C:%.0f G:%.0f M:%.0f", r[0], r[1], r[2], r[3])
					}
					fmt.Fprintf(&sb, "  %-6d %-16s %-6d %-16s %-9s %-9s %-8s %-30s\n", connID, username, netID, posStr, hp, shield, fluxStr, cargoStr)
				}
				return sb.String()
			})
			fmt.Print(result)
		},
	})

	console.Register(engine.Command{
		Name: "damage", Category: "admin",
		Usage: "damage <target> <amount>", Description: "deal damage to player or entity by net ID",
		Complete: func(args []string) []string {
			if len(args) == 0 {
				return playerComplete(args)
			}
			return nil
		},
		Fn: func(args []string) {
			if len(args) < 2 {
				fmt.Println("  usage: damage <player|netID> <amount>")
			} else {
				amount, err := strconv.ParseFloat(args[1], 32)
				if err != nil {
					fmt.Println("  invalid amount")
				} else {
					targetArg := args[0]
					dmg := float32(amount)
					result := console.ExecOnGameLoop(func() string {
						_, entity, ok := resolvePlayer(gw, targetArg)
						if !ok {
							entity, ok = resolveEntity(gw, targetArg)
						}
						if !ok {
							return "  entity not found"
						}
						if !gw.HealthMap.HasAll(entity) {
							return "  entity has no health component"
						}
						h := gw.HealthMap.Get(entity)
						remaining := dmg
						shieldAbsorbed := float32(0)
						if gw.ShieldMap.HasAll(entity) {
							s := gw.ShieldMap.Get(entity)
							s.DamageCooldown = s.RegenDelay
							if s.Current > 0 {
								shieldAbsorbed = min(s.Current, remaining)
								s.Current -= shieldAbsorbed
								remaining -= shieldAbsorbed
							}
						}
						h.Current -= remaining
						if h.Current < 0 {
							h.Current = 0
						}
						return fmt.Sprintf("  dealt %.0f damage (shield: %.0f absorbed, hp: %.0f/%.0f)", dmg, shieldAbsorbed, h.Current, h.Max)
					})
					fmt.Println(result)
				}
			}
		},
	})

	console.Register(engine.Command{
		Name: "kill", Category: "admin",
		Usage: "kill <target>", Description: "instantly kill player or entity by net ID",
		Complete: func(args []string) []string {
			if len(args) == 0 {
				return playerComplete(args)
			}
			return nil
		},
		Fn: func(args []string) {
			if len(args) < 1 {
				fmt.Println("  usage: kill <player|netID>")
			} else {
				targetArg := args[0]
				result := console.ExecOnGameLoop(func() string {
					connID, entity, ok := resolvePlayer(gw, targetArg)
					if ok {
						username := gw.ConnToUsername[connID]
						gw.MarkPlayerDeath(entity, 0)
						return fmt.Sprintf("  killed player %s", username)
					}
					entity, ok = resolveEntity(gw, targetArg)
					if !ok {
						return "  entity not found"
					}
					gw.MarkForRemoval(entity)
					return fmt.Sprintf("  killed entity %s", targetArg)
				})
				fmt.Println(result)
			}
		},
	})

	console.Register(engine.Command{
		Name: "kick", Category: "admin",
		Usage: "kick <player>", Description: "force disconnect player",
		Complete: func(args []string) []string {
			if len(args) == 0 {
				return playerComplete(args)
			}
			return nil
		},
		Fn: func(args []string) {
			if len(args) < 1 {
				fmt.Println("  usage: kick <player>")
			} else {
				playerArg := args[0]
				result := console.ExecOnGameLoop(func() string {
					connID, entity, ok := resolvePlayer(gw, playerArg)
					if !ok {
						return "  player not found"
					}
					username := gw.ConnToUsername[connID]
					if gw.ECS.Alive(entity) {
						gw.SavePlayerState(connID, entity)
						gw.MarkForRemoval(entity)
					}
					delete(gw.PlayerEntities, connID)
					delete(gw.DeadPlayers, connID)
					delete(gw.ConnToUsername, connID)
					gw.ConnMgr.Remove(connID)
					return fmt.Sprintf("  kicked %s (conn %d)", username, connID)
				})
				fmt.Println(result)
			}
		},
	})

	console.Register(engine.Command{
		Name: "heal", Category: "admin",
		Usage: "heal <target>", Description: "restore full HP and shield on player or entity",
		Complete: func(args []string) []string {
			if len(args) == 0 {
				return playerComplete(args)
			}
			return nil
		},
		Fn: func(args []string) {
			if len(args) < 1 {
				fmt.Println("  usage: heal <player|netID>")
			} else {
				targetArg := args[0]
				result := console.ExecOnGameLoop(func() string {
					_, entity, ok := resolvePlayer(gw, targetArg)
					if !ok {
						entity, ok = resolveEntity(gw, targetArg)
					}
					if !ok {
						return "  entity not found"
					}
					if gw.HealthMap.HasAll(entity) {
						h := gw.HealthMap.Get(entity)
						h.Current = h.Max
					}
					if gw.ShieldMap.HasAll(entity) {
						s := gw.ShieldMap.Get(entity)
						s.Current = s.Max
					}
					return "  fully healed"
				})
				fmt.Println(result)
			}
		},
	})

	console.Register(engine.Command{
		Name: "tp", Category: "admin",
		Usage: "tp <target> <x> <y>", Description: "teleport player or entity by net ID",
		Complete: func(args []string) []string {
			if len(args) == 0 {
				return playerComplete(args)
			}
			return nil
		},
		Fn: func(args []string) {
			if len(args) < 3 {
				fmt.Println("  usage: tp <player|netID> <x> <y>")
			} else {
				x, err1 := strconv.ParseFloat(args[1], 32)
				y, err2 := strconv.ParseFloat(args[2], 32)
				if err1 != nil || err2 != nil {
					fmt.Println("  invalid coordinates")
				} else {
					targetArg := args[0]
					fx, fy := float32(x), float32(y)
					result := console.ExecOnGameLoop(func() string {
						_, entity, ok := resolvePlayer(gw, targetArg)
						if !ok {
							entity, ok = resolveEntity(gw, targetArg)
						}
						if !ok {
							return "  entity not found"
						}
						if !gw.PositionMap.HasAll(entity) {
							return "  entity has no position"
						}
						pos := gw.PositionMap.Get(entity)
						pos.X = fx
						pos.Y = fy
						if gw.VelocityMap.HasAll(entity) {
							vel := gw.VelocityMap.Get(entity)
							vel.X = 0
							vel.Y = 0
						}
						return fmt.Sprintf("  teleported to (%.0f, %.0f)", fx, fy)
					})
					fmt.Println(result)
				}
			}
		},
	})

	console.Register(engine.Command{
		Name: "give", Category: "admin",
		Usage: "give <player> <res> <amt>", Description: "add resource (ore/crystal/gas/metal)",
		Complete: func(args []string) []string {
			switch len(args) {
			case 0:
				return console.GetCompletions("players")
			case 1:
				return console.GetCompletions("resources")
			default:
				return nil
			}
		},
		Fn: func(args []string) {
			if len(args) < 3 {
				fmt.Println("  usage: give <player> <resource> <amount>")
			} else {
				resIdx, ok := resolveResource(args[1])
				if !ok {
					fmt.Println("  unknown resource (ore/crystal/gas/metal)")
				} else {
					amount, err := strconv.ParseFloat(args[2], 32)
					if err != nil {
						fmt.Println("  invalid amount")
					} else {
						playerArg := args[0]
						amt := float32(amount)
						result := console.ExecOnGameLoop(func() string {
							_, entity, ok := resolvePlayer(gw, playerArg)
							if !ok {
								return "  player not found"
							}
							if !gw.InventoryMap.HasAll(entity) {
								return "  player has no inventory"
							}
							inv := gw.InventoryMap.Get(entity)
							inv.Resources[resIdx] += amt
							resNames := [4]string{"ore", "crystal", "gas", "metal"}
							return fmt.Sprintf("  gave %.0f %s (total: %.0f)", amt, resNames[resIdx], inv.Resources[resIdx])
						})
						fmt.Println(result)
					}
				}
			}
		},
	})

	console.Register(engine.Command{
		Name: "flux", Category: "admin",
		Usage: "flux <player> <amount>", Description: "set player FLUX balance",
		Complete: func(args []string) []string {
			if len(args) == 0 {
				return playerComplete(args)
			}
			return nil
		},
		Fn: func(args []string) {
			if len(args) < 2 {
				fmt.Println("  usage: flux <player> <amount>")
			} else {
				amount, err := strconv.ParseFloat(args[1], 64)
				if err != nil {
					fmt.Println("  invalid amount")
				} else {
					playerArg := args[0]
					result := console.ExecOnGameLoop(func() string {
						connID, _, ok := resolvePlayer(gw, playerArg)
						if !ok {
							return "  player not found"
						}
						username := gw.ConnToUsername[connID]
						pdata := gw.PlayerDB.GetOrCreate(username)
						earned := amount - pdata.Flux
						pdata.Flux = amount
						gw.PlayerDB.MarkDirty(username)
						msg := &gamepb.ServerMessage{
							Msg: &gamepb.ServerMessage_SellResult{
								SellResult: &gamepb.SellResultMsg{
									FluxEarned: float32(earned),
									TotalFlux:  float32(amount),
								},
							},
						}
						if data, err := proto.Marshal(msg); err == nil {
							gw.ConnMgr.SendReliable(connID, data)
						}
						return fmt.Sprintf("  set %s flux to %.0f", username, amount)
					})
					fmt.Println(result)
				}
			}
		},
	})

	console.Register(engine.Command{
		Name: "spawn", Category: "admin",
		Usage: "spawn <type> <x> <y>", Description: "spawn entity at position",
		Complete: func(args []string) []string {
			if len(args) == 0 {
				return console.GetCompletions("spawn_types")
			}
			return nil
		},
		Fn: func(args []string) {
			if len(args) < 3 {
				fmt.Printf("  usage: spawn <type> <x> <y> (types: %s)\n",
					strings.Join(gw.Registry.SpawnableNames(), ", "))
			} else {
				def, ok := gw.Registry.Get(args[0])
				if !ok || !def.Spawnable {
					fmt.Printf("  unknown spawn type (available: %s)\n",
						strings.Join(gw.Registry.SpawnableNames(), ", "))
				} else {
					x, err1 := strconv.ParseFloat(args[1], 32)
					y, err2 := strconv.ParseFloat(args[2], 32)
					if err1 != nil || err2 != nil {
						fmt.Println("  invalid coordinates")
					} else {
						fx, fy := float32(x), float32(y)
						result := console.ExecOnGameLoop(func() string {
							def.Spawn(fx, fy)
							return fmt.Sprintf("  spawned %s at (%.0f, %.0f)", def.Name, fx, fy)
						})
						fmt.Println(result)
					}
				}
			}
		},
	})

	console.Register(engine.Command{
		Name: "say", Category: "admin",
		Usage: "say <message>", Description: "broadcast server chat message",
		Fn: func(args []string) {
			if len(args) < 1 {
				fmt.Println("  usage: say <message>")
			} else {
				msg := strings.Join(args, " ")
				result := console.ExecOnGameLoop(func() string {
					gw.PendingChat = append(gw.PendingChat, &gamepb.ChatMsg{
						Username: "[SERVER]",
						Text:     msg,
					})
					return fmt.Sprintf("  broadcast: %s", msg)
				})
				fmt.Println(result)
			}
		},
	})

	console.Register(engine.Command{
		Name: "entities", Category: "admin",
		Usage: "entities", Description: "show entity count by type",
		Fn: func(args []string) {
			result := console.ExecOnGameLoop(func() string {
				counts := make(map[string]int)
				filter := ecs.NewFilter1[component.EntityKind](gw.ECS)
				query := filter.Query()
				for query.Next() {
					kind := query.Get()
					if def := gw.Registry.ByType(kind.Type); def != nil {
						counts[def.Name]++
					} else {
						counts["unknown"]++
					}
				}
				var sb strings.Builder
				fmt.Fprintf(&sb, "  entities:\n")
				for name, count := range counts {
					fmt.Fprintf(&sb, "    %-12s %d\n", name, count)
				}
				return sb.String()
			})
			fmt.Print(result)
		},
	})

	console.Register(engine.Command{
		Name: "npcs", Category: "admin",
		Usage: "npcs", Description: "list all NPCs with net IDs",
		Fn: func(args []string) {
			result := console.ExecOnGameLoop(func() string {
				filter := ecs.NewFilter3[component.EntityKind, component.NetworkID, component.Position](gw.ECS)
				query := filter.Query()
				var sb strings.Builder
				count := 0
				fmt.Fprintf(&sb, "  %-8s %-16s %-9s %-9s\n", "NETID", "POSITION", "HP", "SHIELD")
				for query.Next() {
					kind, netID, pos := query.Get()
					if kind.Type != component.TypeNPC {
						continue
					}
					entity := query.Entity()
					count++
					var hp, shield string
					if gw.HealthMap.HasAll(entity) {
						h := gw.HealthMap.Get(entity)
						hp = fmt.Sprintf("%.0f/%.0f", h.Current, h.Max)
					}
					if gw.ShieldMap.HasAll(entity) {
						s := gw.ShieldMap.Get(entity)
						shield = fmt.Sprintf("%.0f/%.0f", s.Current, s.Max)
					}
					fmt.Fprintf(&sb, "  %-8d %-16s %-9s %-9s\n",
						netID.ID, fmt.Sprintf("%.0f, %.0f", pos.X, pos.Y), hp, shield)
				}
				if count == 0 {
					return "  no NPCs alive"
				}
				return sb.String()
			})
			fmt.Print(result)
		},
	})
}

// resolvePlayer finds a player by connID (numeric) or username (case-insensitive prefix).
func resolvePlayer(gw *GameWorld, input string) (connID uint32, entity ecs.Entity, ok bool) {
	// Try numeric connID first
	if id, err := strconv.ParseUint(input, 10, 32); err == nil {
		cid := uint32(id)
		if e, exists := gw.PlayerEntities[cid]; exists && gw.ECS.Alive(e) {
			return cid, e, true
		}
	}

	// Search by username (case-insensitive prefix)
	inputLower := strings.ToLower(input)
	for cid, username := range gw.ConnToUsername {
		if strings.HasPrefix(strings.ToLower(username), inputLower) {
			if e, exists := gw.PlayerEntities[cid]; exists && gw.ECS.Alive(e) {
				return cid, e, true
			}
		}
	}
	return 0, ecs.Entity{}, false
}

// resolveEntity finds any entity by network ID. Returns the ECS entity and true if found.
func resolveEntity(gw *GameWorld, input string) (ecs.Entity, bool) {
	id, err := strconv.ParseUint(input, 10, 32)
	if err != nil {
		return ecs.Entity{}, false
	}
	netID := uint32(id)

	// Check NetIDToEntity map (rebuilt each tick by SpatialSystem)
	if entity, exists := gw.NetIDToEntity[netID]; exists && gw.ECS.Alive(entity) {
		return entity, true
	}
	return ecs.Entity{}, false
}

// resolveResource maps short resource names to indices.
func resolveResource(input string) (uint8, bool) {
	input = strings.ToLower(input)
	resources := []struct {
		name string
		idx  uint8
	}{
		{"ore", component.ResourceOre},
		{"crystal", component.ResourceCrystal},
		{"gas", component.ResourceGas},
		{"metal", component.ResourceMetal},
	}
	for _, r := range resources {
		if strings.HasPrefix(r.name, input) {
			return r.idx, true
		}
	}
	return 0, false
}
