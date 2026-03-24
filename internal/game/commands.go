package game

import (
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/mlange-42/ark/ecs"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/internal/netutil"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/persist"
)

// fmtSectorPos formats a sector+position pair for display, e.g. "(1,2):(500, 300)".
func fmtSectorPos(sec component.SectorCoord, pos component.Position) string {
	return fmt.Sprintf("(%d,%d):(%.0f, %.0f)", sec.SX, sec.SY, pos.X, pos.Y)
}

// fmtSectorPosRaw formats sector+position from raw values.
func fmtSectorPosRaw(sx, sy int32, x, y float32) string {
	return fmt.Sprintf("(%d,%d):(%.0f, %.0f)", sx, sy, x, y)
}

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
				if len(gw.Players.Entities) == 0 {
					return "  no players connected"
				}
				var sb strings.Builder
				fmt.Fprintf(&sb, "  %-6s %-16s %-6s %-24s %-9s %-9s %-8s %-30s\n", "CONN", "USERNAME", "NETID", "POSITION", "HP", "SHIELD", "FLUX", "CARGO")
				for connID, entity := range gw.Players.Entities {
					if !gw.ECS.Alive(entity) {
						continue
					}
					username := gw.Players.Usernames[connID]
					var netID uint32
					if gw.C.NetworkID.HasAll(entity) {
						netID = gw.C.NetworkID.Get(entity).ID
					}
					var posStr string
					if gw.C.Position.HasAll(entity) && gw.C.SectorCoord.HasAll(entity) {
						pos := gw.C.Position.Get(entity)
						sec := gw.C.SectorCoord.Get(entity)
						posStr = fmtSectorPos(*sec, *pos)
					}
					var hp, shield string
					if gw.C.Health.HasAll(entity) {
						h := gw.C.Health.Get(entity)
						hp = fmt.Sprintf("%.0f/%.0f", h.Current, h.Max)
					}
					if gw.C.Shield.HasAll(entity) {
						s := gw.C.Shield.Get(entity)
						shield = fmt.Sprintf("%.0f/%.0f", s.Current, s.Max)
					}
					var fluxStr string
					pdata := gw.PlayerDB.Get(username)
					if pdata != nil {
						fluxStr = fmt.Sprintf("%d", pdata.Flux)
					}
					var cargoStr string
					if gw.C.Inventory.HasAll(entity) {
						inv := gw.C.Inventory.Get(entity)
						cargoStr = fmt.Sprintf("mass:%.0f/%.0f items:%d", inv.TotalMass(), inv.MaxMass, len(inv.Items))
					}
					fmt.Fprintf(&sb, "  %-6d %-16s %-6d %-24s %-9s %-9s %-8s %-30s\n", connID, username, netID, posStr, hp, shield, fluxStr, cargoStr)
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
						if !gw.C.Health.HasAll(entity) {
							return "  entity has no health component"
						}
						dealt := gw.ApplyDamage(entity, dmg, 0)
						h := gw.C.Health.Get(entity)
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
						username := gw.Players.Usernames[connID]
						gw.MarkPlayerDeath(entity, 0)
						return fmt.Sprintf("  killed player %s", username)
					}
					entity, ok = resolveEntity(gw, targetArg)
					if !ok {
						return "  entity not found"
					}
					gw.MarkNPCDeath(entity, 0)
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
					username := gw.Players.Usernames[connID]
					if gw.ECS.Alive(entity) {
						gw.SavePlayerState(connID, entity)
						gw.MarkForRemoval(entity)
					}
					delete(gw.Players.Entities, connID)
					delete(gw.Players.Dead, connID)
					delete(gw.Players.Usernames, connID)
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
					if gw.C.Health.HasAll(entity) {
						h := gw.C.Health.Get(entity)
						h.Current = h.Max
					}
					if gw.C.Shield.HasAll(entity) {
						s := gw.C.Shield.Get(entity)
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
		Usage: "tp <target> <x> <y> | tp <target> <sx> <sy> <x> <y>", Description: "teleport player or entity (local or sector coords)",
		Complete: func(args []string) []string {
			if len(args) == 0 {
				return playerComplete(args)
			}
			return nil
		},
		Fn: func(args []string) {
			if len(args) < 3 {
				fmt.Println("  usage: tp <target> <x> <y>  (within current sector)")
				fmt.Println("         tp <target> <sx> <sy> <x> <y>  (explicit sector)")
			} else {
				targetArg := args[0]
				var sx, sy int32
				var fx, fy float32
				var explicitSector bool

				if len(args) >= 5 {
					// tp <target> <sx> <sy> <x> <y>
					sxv, err1 := strconv.ParseInt(args[1], 10, 32)
					syv, err2 := strconv.ParseInt(args[2], 10, 32)
					x, err3 := strconv.ParseFloat(args[3], 32)
					y, err4 := strconv.ParseFloat(args[4], 32)
					if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
						fmt.Println("  invalid coordinates")
						return
					}
					sx, sy = int32(sxv), int32(syv)
					fx, fy = float32(x), float32(y)
					explicitSector = true
				} else {
					// tp <target> <x> <y>
					x, err1 := strconv.ParseFloat(args[1], 32)
					y, err2 := strconv.ParseFloat(args[2], 32)
					if err1 != nil || err2 != nil {
						fmt.Println("  invalid coordinates")
						return
					}
					fx, fy = float32(x), float32(y)
				}

				result := console.ExecOnGameLoop(func() string {
					_, entity, ok := resolvePlayer(gw, targetArg)
					if !ok {
						entity, ok = resolveEntity(gw, targetArg)
					}
					if !ok {
						return "  entity not found"
					}
					if !gw.C.Position.HasAll(entity) {
						return "  entity has no position"
					}
					pos := gw.C.Position.Get(entity)
					pos.X = fx
					pos.Y = fy
					if explicitSector && gw.C.SectorCoord.HasAll(entity) {
						sec := gw.C.SectorCoord.Get(entity)
						sec.SX = sx
						sec.SY = sy
					}
					if gw.C.Velocity.HasAll(entity) {
						vel := gw.C.Velocity.Get(entity)
						vel.X = 0
						vel.Y = 0
					}
					// Read back actual sector for display
					var dsx, dsy int32
					if gw.C.SectorCoord.HasAll(entity) {
						sec := gw.C.SectorCoord.Get(entity)
						dsx, dsy = sec.SX, sec.SY
					}
					return fmt.Sprintf("  teleported to %s", fmtSectorPosRaw(dsx, dsy, fx, fy))
				})
				fmt.Println(result)
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
					amount, err := strconv.ParseInt(args[2], 10, 32)
					if err != nil {
						fmt.Println("  invalid amount")
					} else {
						playerArg := args[0]
						amt := int32(amount)
						result := console.ExecOnGameLoop(func() string {
							_, entity, ok := resolvePlayer(gw, playerArg)
							if !ok {
								return "  player not found"
							}
							if !gw.C.Inventory.HasAll(entity) {
								return "  player has no inventory"
							}
							inv := gw.C.Inventory.Get(entity)
							itemID := item.ResourceItemID(resIdx)
							added := inv.AddItem(itemID, amt)
							def := item.Get(itemID)
							name := "unknown"
							if def != nil {
								name = def.Name
							}
							return fmt.Sprintf("  gave %d %s (added: %d)", amt, name, added)
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
				amount, err := strconv.ParseInt(args[1], 10, 64)
				if err != nil {
					fmt.Println("  invalid amount")
				} else {
					playerArg := args[0]
					result := console.ExecOnGameLoop(func() string {
						connID, _, ok := resolvePlayer(gw, playerArg)
						if !ok {
							return "  player not found"
						}
						username := gw.Players.Usernames[connID]
						pdata := gw.PlayerDB.GetOrCreate(username)
						pdata.Flux = amount
						gw.PlayerDB.MarkDirty(username)
						sendBankContentsAdmin(gw, connID, pdata)
						return fmt.Sprintf("  set %s flux to %d", username, amount)
					})
					fmt.Println(result)
				}
			}
		},
	})

	console.Register(engine.Command{
		Name: "spawn", Category: "admin",
		Usage: "spawn <type> <x> <y> | spawn <type> <sx> <sy> <x> <y>", Description: "spawn entity at position (local or sector coords)",
		Complete: func(args []string) []string {
			if len(args) == 0 {
				return console.GetCompletions("spawn_types")
			}
			return nil
		},
		Fn: func(args []string) {
			if len(args) < 3 {
				fmt.Printf("  usage: spawn <type> <x> <y>  (in sector 0,0)\n")
				fmt.Printf("         spawn <type> <sx> <sy> <x> <y>  (explicit sector)\n")
				fmt.Printf("  types: %s\n", strings.Join(gw.Registry.SpawnableNames(), ", "))
			} else {
				def, ok := gw.Registry.Get(args[0])
				if !ok || !def.Spawnable {
					fmt.Printf("  unknown spawn type (available: %s)\n",
						strings.Join(gw.Registry.SpawnableNames(), ", "))
				} else {
					var sx, sy int32
					var fx, fy float32
					if len(args) >= 5 {
						sxv, err1 := strconv.ParseInt(args[1], 10, 32)
						syv, err2 := strconv.ParseInt(args[2], 10, 32)
						x, err3 := strconv.ParseFloat(args[3], 32)
						y, err4 := strconv.ParseFloat(args[4], 32)
						if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
							fmt.Println("  invalid coordinates")
							return
						}
						sx, sy = int32(sxv), int32(syv)
						// Offset local coords by sector so the entity spawns in the right sector.
						// SectorBoundarySystem will normalize into the correct sector on next tick.
						fx = float32(sx)*coords.SectorSize + float32(x)
						fy = float32(sy)*coords.SectorSize + float32(y)
					} else {
						x, err1 := strconv.ParseFloat(args[1], 32)
						y, err2 := strconv.ParseFloat(args[2], 32)
						if err1 != nil || err2 != nil {
							fmt.Println("  invalid coordinates")
							return
						}
						fx, fy = float32(x), float32(y)
					}
					result := console.ExecOnGameLoop(func() string {
						def.Spawn(fx, fy)
						return fmt.Sprintf("  spawned %s at %s", def.Name, fmtSectorPosRaw(sx, sy, fx-float32(sx)*coords.SectorSize, fy-float32(sy)*coords.SectorSize))
					})
					fmt.Println(result)
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
					engine.Enqueue(gw.Queue, &gamepb.ChatMsg{
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
				fmt.Fprintf(&sb, "  %-8s %-24s %-9s %-9s\n", "NETID", "POSITION", "HP", "SHIELD")
				for query.Next() {
					kind, netID, pos := query.Get()
					if kind.Type != component.TypeNPC {
						continue
					}
					entity := query.Entity()
					count++
					var hp, shield string
					if gw.C.Health.HasAll(entity) {
						h := gw.C.Health.Get(entity)
						hp = fmt.Sprintf("%.0f/%.0f", h.Current, h.Max)
					}
					if gw.C.Shield.HasAll(entity) {
						s := gw.C.Shield.Get(entity)
						shield = fmt.Sprintf("%.0f/%.0f", s.Current, s.Max)
					}
					var posStr string
					if gw.C.SectorCoord.HasAll(entity) {
						sec := gw.C.SectorCoord.Get(entity)
						posStr = fmtSectorPos(*sec, *pos)
					} else {
						posStr = fmt.Sprintf("%.0f, %.0f", pos.X, pos.Y)
					}
					fmt.Fprintf(&sb, "  %-8d %-24s %-9s %-9s\n",
						netID.ID, posStr, hp, shield)
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
					if !gw.C.Position.HasAll(srcEntity) || !gw.C.Position.HasAll(dstEntity) {
						return "  entity missing position"
					}
					dstPos := gw.C.Position.Get(dstEntity)
					// Offset by ~150 units in a random direction to avoid collision
					angle := rand.Float64() * 2 * math.Pi
					offsetDist := float32(150)
					nx := dstPos.X + offsetDist*float32(math.Cos(angle))
					ny := dstPos.Y + offsetDist*float32(math.Sin(angle))
					srcPos := gw.C.Position.Get(srcEntity)
					srcPos.X = nx
					srcPos.Y = ny
					// Copy sector from target
					if gw.C.SectorCoord.HasAll(srcEntity) && gw.C.SectorCoord.HasAll(dstEntity) {
						srcSec := gw.C.SectorCoord.Get(srcEntity)
						dstSec := gw.C.SectorCoord.Get(dstEntity)
						srcSec.SX = dstSec.SX
						srcSec.SY = dstSec.SY
					}
					if gw.C.Velocity.HasAll(srcEntity) {
						vel := gw.C.Velocity.Get(srcEntity)
						vel.X = 0
						vel.Y = 0
					}
					var dsx, dsy int32
					if gw.C.SectorCoord.HasAll(srcEntity) {
						sec := gw.C.SectorCoord.Get(srcEntity)
						dsx, dsy = sec.SX, sec.SY
					}
					return fmt.Sprintf("  teleported %s near %s at %s", playerArg, targetArg, fmtSectorPosRaw(dsx, dsy, nx, ny))
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
						if !gw.C.Position.HasAll(entity) {
							return "  player has no position"
						}
						pos := gw.C.Position.Get(entity)
						// Offset spawn coords by player sector so NPCs land in the right sector.
						// SectorBoundarySystem will normalize into the correct sector.
						var sectorOffX, sectorOffY float32
						if gw.C.SectorCoord.HasAll(entity) {
							sec := gw.C.SectorCoord.Get(entity)
							sectorOffX = float32(sec.SX) * coords.SectorSize
							sectorOffY = float32(sec.SY) * coords.SectorSize
						}
						radius := gw.Config.AoIRadius * 0.8 // stay within AoI
						for i := 0; i < count; i++ {
							angle := rand.Float64() * 2 * math.Pi
							dist := float32(math.Sqrt(rand.Float64())) * radius // uniform distribution in circle
							nx := sectorOffX + pos.X + dist*float32(math.Cos(angle))
							ny := sectorOffY + pos.Y + dist*float32(math.Sin(angle))
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
					_, online := gw.Players.Usernames[0] // dummy; check below
					online = false
					for _, u := range gw.Players.Usernames {
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
					fmt.Fprintf(&sb, "  position: %s\n", fmtSectorPosRaw(pd.SectorX, pd.SectorY, pd.X, pd.Y))
					flux := pd.Flux
					fmt.Fprintf(&sb, "  flux: %d\n", flux)
					if len(pd.Cargo) > 0 {
						fmt.Fprintf(&sb, "  cargo:\n")
						for id, qty := range pd.Cargo {
							def := item.Get(id)
							name := fmt.Sprintf("item#%d", id)
							if def != nil {
								name = def.Name
							}
							fmt.Fprintf(&sb, "    %-16s %d\n", name, qty)
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
							fmt.Fprintf(&sb, "    %-16s %d\n", name, qty)
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
					for _, u := range gw.Players.Usernames {
						onlineUsers[u] = true
					}
					nameW := len("USERNAME")
					for _, pd := range all {
						if len(pd.Username) > nameW {
							nameW = len(pd.Username)
						}
					}
					var sb strings.Builder
					rowFmt := fmt.Sprintf("  %%-%ds %%-8s %%-8s %%-24s %%-20s\n", nameW)
					fmt.Fprintf(&sb, rowFmt, "USERNAME", "STATUS", "FLUX", "POSITION", "LAST LOGIN")
					dataFmt := fmt.Sprintf("  %%-%ds %%-8s %%-8d %%-24s %%-20s\n", nameW)
					for _, pd := range all {
						status := "offline"
						if onlineUsers[pd.Username] {
							status = "online"
						}
						flux := pd.Flux
						lastLogin := pd.LastLogin.Format("2006-01-02 15:04")
						if pd.LastLogin.IsZero() {
							lastLogin = "never"
						}
						fmt.Fprintf(&sb, dataFmt,
							pd.Username, status, flux,
							fmtSectorPosRaw(pd.SectorX, pd.SectorY, pd.X, pd.Y), lastLogin)
					}
					return sb.String()
				})
				fmt.Print(result)
			}
		},
	})

	console.Register(engine.Command{
		Name: "grid", Aliases: []string{"sg"},
		Category: "debug", Usage: "grid", Description: "toggle sector grid lines on all clients",
		Fn: func(args []string) {
			result := console.ExecOnGameLoop(func() string {
				gw.DebugShowSectorGrid = !gw.DebugShowSectorGrid
				broadcastDebugFlags(gw)
				if gw.DebugShowSectorGrid {
					return "  sector grid: ON"
				}
				return "  sector grid: OFF"
			})
			fmt.Println(result)
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
		if e, exists := gw.Players.Entities[cid]; exists && gw.ECS.Alive(e) {
			return cid, e, true
		}
	}

	// Search by username (case-insensitive prefix)
	inputLower := strings.ToLower(input)
	for cid, username := range gw.Players.Usernames {
		if strings.HasPrefix(strings.ToLower(username), inputLower) {
			if e, exists := gw.Players.Entities[cid]; exists && gw.ECS.Alive(e) {
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

// broadcastDebugFlags sends the current debug flag state to all logged-in players.
func broadcastDebugFlags(gw *GameWorld) {
	data := netutil.MakeEvent(uint32(gamepb.ServerEventCode_SE_DEBUG_FLAGS), &gamepb.DebugFlagsMsg{
		ShowSectorGrid: gw.DebugShowSectorGrid,
	})
	if data == nil {
		return
	}
	for connID := range gw.Players.Usernames {
		gw.ConnMgr.SendReliable(connID, data)
	}
}

// sendBankContentsAdmin sends a BankContentsMsg to a player (used by admin commands).
func sendBankContentsAdmin(gw *GameWorld, connID uint32, pdata *PlayerData) {
	var items []*gamepb.InventoryItem
	for id, qty := range pdata.Bank {
		if qty > 0 {
			items = append(items, &gamepb.InventoryItem{ItemId: id, Quantity: qty})
		}
	}
	data := netutil.MakeEvent(uint32(gamepb.ServerEventCode_SE_BANK_CONTENTS), &gamepb.BankContentsMsg{
		Items:       items,
		TotalMass:   pdata.BankTotalMass(),
		MaxMass:     gw.Config.BankMaxMass,
		FluxBalance: pdata.Flux,
	})
	if data != nil {
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
