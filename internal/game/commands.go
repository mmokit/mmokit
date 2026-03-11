package game

import (
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/mlange-42/ark/ecs"
	"google.golang.org/protobuf/proto"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
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
						fluxStr = fmt.Sprintf("%.0f", pdata.Bank[item.FluxItemID])
					}
					var cargoStr string
					if gw.InventoryMap.HasAll(entity) {
						inv := gw.InventoryMap.Get(entity)
						cargoStr = fmt.Sprintf("mass:%.0f/%.0f items:%d", inv.TotalMass(), inv.MaxMass, len(inv.Items))
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
						dealt := gw.ApplyDamage(entity, dmg, 0)
						h := gw.HealthMap.Get(entity)
						return fmt.Sprintf("  dealt %.0f damage (hp: %.0f/%.0f)", dealt, h.Current, h.Max)
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
							itemID := item.ResourceItemID(resIdx)
							added := inv.AddItem(itemID, amt)
							def := item.Get(itemID)
							name := "unknown"
							if def != nil {
								name = def.Name
							}
							return fmt.Sprintf("  gave %.0f %s (added: %.0f)", amt, name, added)
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
						if pdata.Bank == nil {
							pdata.Bank = make(map[uint32]float32)
						}
						pdata.Bank[item.FluxItemID] = float32(amount)
						gw.PlayerDB.MarkDirty(username)
						sendBankContentsAdmin(gw, connID, pdata)
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

	console.Register(engine.Command{
		Name: "tpto", Category: "admin",
		Usage: "tpto <player> <target>", Description: "teleport player near another player or entity (by username or net ID)",
		Complete: func(args []string) []string {
			if len(args) <= 1 {
				return playerComplete(args)
			}
			return nil
		},
		Fn: func(args []string) {
			if len(args) < 2 {
				fmt.Println("  usage: tpto <player> <target>  (target = username or net ID)")
			} else {
				playerArg := args[0]
				targetArg := args[1]
				result := console.ExecOnGameLoop(func() string {
					_, srcEntity, ok := resolvePlayer(gw, playerArg)
					if !ok {
						return "  source player not found"
					}
					// Resolve target: try player first, then any entity by net ID
					var dstEntity ecs.Entity
					_, dstEntity, ok = resolvePlayer(gw, targetArg)
					if !ok {
						dstEntity, ok = resolveEntity(gw, targetArg)
					}
					if !ok {
						return "  target not found (use username or net ID)"
					}
					if !gw.PositionMap.HasAll(srcEntity) || !gw.PositionMap.HasAll(dstEntity) {
						return "  entity missing position"
					}
					dstPos := gw.PositionMap.Get(dstEntity)
					// Offset by ~150 units in a random direction to avoid collision
					angle := rand.Float64() * 2 * math.Pi
					offsetDist := float32(150)
					nx := dstPos.X + offsetDist*float32(math.Cos(angle))
					ny := dstPos.Y + offsetDist*float32(math.Sin(angle))
					srcPos := gw.PositionMap.Get(srcEntity)
					srcPos.X = nx
					srcPos.Y = ny
					if gw.VelocityMap.HasAll(srcEntity) {
						vel := gw.VelocityMap.Get(srcEntity)
						vel.X = 0
						vel.Y = 0
					}
					return fmt.Sprintf("  teleported %s near %s at (%.0f, %.0f)", playerArg, targetArg, nx, ny)
				})
				fmt.Println(result)
			}
		},
	})

	console.Register(engine.Command{
		Name: "spawnnpcs", Category: "admin",
		Usage: "spawnnpcs <count> <player>", Description: "spawn N NPCs around a player (within AoI) for load testing",
		Complete: func(args []string) []string {
			if len(args) == 1 {
				return playerComplete(args)
			}
			return nil
		},
		Fn: func(args []string) {
			if len(args) < 2 {
				fmt.Println("  usage: spawnnpcs <count> <player>")
			} else {
				count, err := strconv.Atoi(args[0])
				if err != nil || count < 1 {
					fmt.Println("  invalid count")
				} else {
					playerArg := args[1]
					result := console.ExecOnGameLoop(func() string {
						_, entity, ok := resolvePlayer(gw, playerArg)
						if !ok {
							return "  player not found"
						}
						if !gw.PositionMap.HasAll(entity) {
							return "  player has no position"
						}
						pos := gw.PositionMap.Get(entity)
						radius := gw.Config.AoIRadius * 0.8 // stay within AoI
						for i := 0; i < count; i++ {
							angle := rand.Float64() * 2 * math.Pi
							dist := float32(math.Sqrt(rand.Float64())) * radius // uniform distribution in circle
							nx := pos.X + dist*float32(math.Cos(angle))
							ny := pos.Y + dist*float32(math.Sin(angle))
							gw.SpawnNPC(nx, ny)
						}
						return fmt.Sprintf("  spawned %d NPCs around %s within %.0f units", count, playerArg, radius)
					})
					fmt.Println(result)
				}
			}
		},
	})

	console.Register(engine.Command{
		Name: "playerdb", Aliases: []string{"pdb"},
		Category: "admin", Usage: "playerdb [username]", Description: "list all players in DB or show details for one",
		Complete: func(args []string) []string {
			if len(args) == 0 {
				all := gw.PlayerDB.All()
				names := make([]string, 0, len(all))
				for name := range all {
					names = append(names, name)
				}
				return names
			}
			return nil
		},
		Fn: func(args []string) {
			if len(args) >= 1 {
				// Show details for a specific player
				username := strings.ToLower(args[0])
				result := console.ExecOnGameLoop(func() string {
					pd := gw.PlayerDB.Get(username)
					if pd == nil {
						return fmt.Sprintf("  player %q not found in DB", username)
					}
					_, online := gw.ConnToUsername[0] // dummy; check below
					online = false
					for _, u := range gw.ConnToUsername {
						if u == username {
							online = true
							break
						}
					}
					var sb strings.Builder
					status := "offline"
					if online {
						status = "online"
					}
					fmt.Fprintf(&sb, "  player: %s (%s)\n", pd.Username, status)
					fmt.Fprintf(&sb, "  created: %s\n", pd.CreatedAt.Format("2006-01-02 15:04"))
					fmt.Fprintf(&sb, "  last login: %s\n", pd.LastLogin.Format("2006-01-02 15:04"))
					fmt.Fprintf(&sb, "  position: %.0f, %.0f\n", pd.X, pd.Y)
					flux := pd.Bank[item.FluxItemID]
					fmt.Fprintf(&sb, "  flux: %.0f\n", flux)
					if len(pd.Cargo) > 0 {
						fmt.Fprintf(&sb, "  cargo:\n")
						for id, qty := range pd.Cargo {
							def := item.Get(id)
							name := fmt.Sprintf("item#%d", id)
							if def != nil {
								name = def.Name
							}
							fmt.Fprintf(&sb, "    %-16s %.1f\n", name, qty)
						}
					}
					if len(pd.Bank) > 0 {
						fmt.Fprintf(&sb, "  bank:\n")
						for id, qty := range pd.Bank {
							def := item.Get(id)
							name := fmt.Sprintf("item#%d", id)
							if def != nil {
								name = def.Name
							}
							fmt.Fprintf(&sb, "    %-16s %.1f\n", name, qty)
						}
					}
					return sb.String()
				})
				fmt.Print(result)
			} else {
				// List all players
				result := console.ExecOnGameLoop(func() string {
					all := gw.PlayerDB.All()
					if len(all) == 0 {
						return "  no players in database"
					}
					onlineUsers := make(map[string]bool)
					for _, u := range gw.ConnToUsername {
						onlineUsers[u] = true
					}
					nameW := len("USERNAME")
					for _, pd := range all {
						if len(pd.Username) > nameW {
							nameW = len(pd.Username)
						}
					}
					var sb strings.Builder
					rowFmt := fmt.Sprintf("  %%-%ds %%-8s %%-8s %%-16s %%-20s\n", nameW)
					fmt.Fprintf(&sb, rowFmt, "USERNAME", "STATUS", "FLUX", "POSITION", "LAST LOGIN")
					dataFmt := fmt.Sprintf("  %%-%ds %%-8s %%-8.0f %%-16s %%-20s\n", nameW)
					for _, pd := range all {
						status := "offline"
						if onlineUsers[pd.Username] {
							status = "online"
						}
						flux := pd.Bank[item.FluxItemID]
						lastLogin := pd.LastLogin.Format("2006-01-02 15:04")
						if pd.LastLogin.IsZero() {
							lastLogin = "never"
						}
						fmt.Fprintf(&sb, dataFmt,
							pd.Username, status, flux,
							fmt.Sprintf("%.0f, %.0f", pd.X, pd.Y), lastLogin)
					}
					return sb.String()
				})
				fmt.Print(result)
			}
		},
	})

	console.Register(engine.Command{
		Name: "perf", Category: "admin",
		Usage: "perf [reset]", Description: "show tick performance stats (per-system breakdown)",
		Fn: func(args []string) {
			if len(args) > 0 && args[0] == "reset" {
				result := console.ExecOnGameLoop(func() string {
					gw.Engine.Perf.Reset()
					return "  perf stats reset"
				})
				fmt.Println(result)
				return
			}
			result := console.ExecOnGameLoop(func() string {
				stats := gw.Engine.Perf.Stats()
				if stats.SampleCount == 0 {
					return "  no samples collected yet"
				}
				var sb strings.Builder
				tickBudget := time.Duration(1000/gw.Engine.Config.TickRate) * time.Millisecond
				fmt.Fprintf(&sb, "  %-14s %8s %8s %8s %8s %8s %8s\n",
					"SYSTEM", "LATEST", "AVG", "P50", "P95", "P99", "MAX")
				fmt.Fprintf(&sb, "  %s\n", strings.Repeat("─", 70))
				for i, name := range stats.SystemNames {
					s := stats.Systems[i]
					fmt.Fprintf(&sb, "  %-14s %8s %8s %8s %8s %8s %8s\n",
						name, fmtDur(s.Latest), fmtDur(s.Avg), fmtDur(s.P50),
						fmtDur(s.P95), fmtDur(s.P99), fmtDur(s.Max))
				}
				fmt.Fprintf(&sb, "  %s\n", strings.Repeat("─", 70))
				t := stats.Total
				fmt.Fprintf(&sb, "  %-14s %8s %8s %8s %8s %8s %8s\n",
					"TOTAL", fmtDur(t.Latest), fmtDur(t.Avg), fmtDur(t.P50),
					fmtDur(t.P95), fmtDur(t.P99), fmtDur(t.Max))
				pct := float64(t.Avg) / float64(tickBudget) * 100
				fmt.Fprintf(&sb, "  tick budget: %s (%.1f%% avg used)  samples: %d\n",
					fmtDur(tickBudget), pct, stats.SampleCount)
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

// sendBankContentsAdmin sends a BankContentsMsg to a player (used by admin commands).
func sendBankContentsAdmin(gw *GameWorld, connID uint32, pdata *PlayerData) {
	var items []*gamepb.InventoryItem
	for id, qty := range pdata.Bank {
		if qty > 0 {
			items = append(items, &gamepb.InventoryItem{ItemId: id, Quantity: qty})
		}
	}
	msg := &gamepb.ServerMessage{
		Msg: &gamepb.ServerMessage_BankContents{
			BankContents: &gamepb.BankContentsMsg{
				Items:     items,
				TotalMass: pdata.BankTotalMass(),
				MaxMass:   gw.Config.BankMaxMass,
			},
		},
	}
	if data, err := proto.Marshal(msg); err == nil {
		gw.ConnMgr.SendReliable(connID, data)
	}
}

// fmtDur formats a duration as milliseconds with appropriate precision.
func fmtDur(d time.Duration) string {
	ms := float64(d.Microseconds()) / 1000.0
	if ms >= 10 {
		return fmt.Sprintf("%.1fms", ms)
	}
	return fmt.Sprintf("%.2fms", ms)
}
