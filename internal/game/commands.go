package game

import (
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"strings"

	"github.com/mlange-42/ark/ecs"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// fmtCellPos formats a cell+position pair for display, e.g. "(1,2):(500, 300)".
func fmtCellPos(sec mmokit.CellCoord, pos mmokit.Position) string {
	return fmt.Sprintf("(%d,%d):(%.0f, %.0f)", sec.CellX, sec.CellY, pos.X, pos.Y)
}

// fmtCellPosRaw formats cell+position from raw values.
func fmtCellPosRaw(sx, sy int32, x, y float32) string {
	return fmt.Sprintf("(%d,%d):(%.0f, %.0f)", sx, sy, x, y)
}

// NodeInfo holds a reference to a node's game world for cross-node admin commands.
type NodeInfo struct {
	ID    string
	Cell  coords.CellCoord
	World *GameWorld
}

// BuildEntityOpts creates EntityOpts callbacks that query the game's ECS world.
func BuildEntityOpts(gw *GameWorld) *engine.EntityOpts {
	return &engine.EntityOpts{
		Summary: func() map[string]int {
			counts := make(map[string]int)
			filter := ecs.NewFilter1[mmokit.EntityKind](gw.ECS)
			query := filter.Query()
			for query.Next() {
				kind := query.Get()
				if def := gw.Registry.ByType(kind.Type); def != nil {
					counts[def.Name]++
				} else {
					counts["unknown"]++
				}
			}
			return counts
		},
		List: func(typeName string) []engine.EntityInfo {
			filter := ecs.NewFilter3[mmokit.EntityKind, mmokit.NetworkID, mmokit.Position](gw.ECS)
			query := filter.Query()
			var result []engine.EntityInfo
			for query.Next() {
				kind, nid, pos := query.Get()
				def := gw.Registry.ByType(kind.Type)
				if def == nil {
					continue
				}
				if typeName != "" && def.Name != typeName {
					continue
				}
				info := engine.EntityInfo{
					NetID:  nid.ID,
					NodeID: gw.NodeID,
					Type:   def.Name,
					X:      pos.X,
					Y:      pos.Y,
				}
				entity := query.Entity()
				if gw.C.CellCoord.HasAll(entity) {
					cell := gw.C.CellCoord.Get(entity)
					info.CellSX = cell.CellX
					info.CellSY = cell.CellY
				}
				result = append(result, info)
			}
			return result
		},
		Get: func(netID uint32) (engine.EntityInfo, bool) {
			entity, ok := gw.NetIDToEntity[netID]
			if !ok || !gw.ECS.Alive(entity) {
				return engine.EntityInfo{}, false
			}
			info := engine.EntityInfo{NetID: netID, NodeID: gw.NodeID}
			if gw.C.EntityKind.HasAll(entity) {
				kind := gw.C.EntityKind.Get(entity)
				if def := gw.Registry.ByType(kind.Type); def != nil {
					info.Type = def.Name
				}
			}
			if gw.C.Position.HasAll(entity) {
				pos := gw.C.Position.Get(entity)
				info.X = pos.X
				info.Y = pos.Y
			}
			if gw.C.Velocity.HasAll(entity) {
				vel := gw.C.Velocity.Get(entity)
				info.VX = vel.X
				info.VY = vel.Y
			}
			if gw.C.CellCoord.HasAll(entity) {
				cell := gw.C.CellCoord.Get(entity)
				info.CellSX = cell.CellX
				info.CellSY = cell.CellY
			}
			return info, true
		},
		Remove: func(netID uint32) bool {
			if entity, ok := gw.NetIDToEntity[netID]; ok && gw.ECS.Alive(entity) {
				gw.Engine.MarkForRemoval(entity)
				return true
			}
			return false
		},
	}
}

// RegisterCommands registers all game-specific admin commands on the console.
// allNodes provides access to all coordinator nodes for global commands (ps, entities).
// If nil, commands only show the local node.
func RegisterCommands(console *mmokit.Console, gw *GameWorld, store mmokit.Store, allNodes []NodeInfo) {
	gw.console = console

	// Set static completions
	console.SetCompletions("resources", []string{"ore", "crystal", "gas", "metal"})

	playerComplete := func(args []string) []string {
		return console.GetCompletions("players")
	}

	console.Register(mmokit.Command{
		Name: "players", Aliases: []string{"ps"},
		Category: "admin", Usage: "players", Description: "list connected players (all nodes)",
		Fn: func(args []string) {
			result := console.ExecOnGameLoop(func() string {
				// Collect players from all nodes
				nodes := allNodes
				if len(nodes) == 0 {
					nodes = []NodeInfo{{ID: gw.NodeID, World: gw}}
				}

				var sb strings.Builder
				totalPlayers := 0
				fmt.Fprintf(&sb, "  %-14s %-6s %-16s %-6s %-24s %-9s %-9s %-8s %-30s\n", "NODE", "CONN", "USERNAME", "NETID", "POSITION", "HP", "SHIELD", "CURRENCY", "CARGO")
				for _, ni := range nodes {
					w := ni.World
					w.Players.ForEach(mmokit.StateActive, func(sess *mmokit.PlayerSession) {
						entity := sess.Entity
						if !w.ECS.Alive(entity) {
							return
						}
						totalPlayers++
						username := sess.Username
						var netID uint32
						if w.C.NetworkID.HasAll(entity) {
							netID = w.C.NetworkID.Get(entity).ID
						}
						var posStr string
						if w.C.Position.HasAll(entity) && w.C.CellCoord.HasAll(entity) {
							pos := w.C.Position.Get(entity)
							sec := w.C.CellCoord.Get(entity)
							posStr = fmtCellPos(*sec, *pos)
						}
						var hp, shield string
						if w.C.Health.HasAll(entity) {
							h := w.C.Health.Get(entity)
							hp = fmt.Sprintf("%.0f/%.0f", h.Current, h.Max)
						}
						if w.C.Shield.HasAll(entity) {
							sh := w.C.Shield.Get(entity)
							shield = fmt.Sprintf("%.0f/%.0f", sh.Current, sh.Max)
						}
						var fluxStr string
						pdata := w.PlayerDB.Get(username)
						if pdata != nil {
							fluxStr = fmt.Sprintf("%d", pdata.GetCurrency(gw.Config.SettlementCurrencyID))
						}
						var cargoStr string
						if w.C.Inventory.HasAll(entity) {
							inv := w.C.Inventory.Get(entity)
							cargoStr = fmt.Sprintf("mass:%.0f/%.0f items:%d", inv.TotalMass(), inv.MaxMass, len(inv.Items))
						}
						fmt.Fprintf(&sb, "  %-14s %-6d %-16s %-6d %-24s %-9s %-9s %-8s %-30s\n", ni.ID, sess.ConnID, username, netID, posStr, hp, shield, fluxStr, cargoStr)
					})
				}
				if totalPlayers == 0 {
					return "  no players connected"
				}
				return sb.String()
			})
			fmt.Print(result)
		},
	})

	console.Register(mmokit.Command{
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

	console.Register(mmokit.Command{
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
					_, entity, ok := resolvePlayer(gw, targetArg)
					if ok {
						gw.MarkPlayerDeath(entity, 0)
						return fmt.Sprintf("  killed player %s", targetArg)
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

	console.Register(mmokit.Command{
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
					connID, _, ok := resolvePlayer(gw, playerArg)
					if !ok {
						return "  player not found"
					}
					sess := gw.Players.ByConnID(connID)
					if sess == nil {
						return "  player not found"
					}
					username := sess.Username
					gw.Players.Remove(sess)
					gw.ConnMgr.Remove(connID)
					return fmt.Sprintf("  kicked %s (conn %d)", username, connID)
				})
				fmt.Println(result)
			}
		},
	})

	console.Register(mmokit.Command{
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

	console.Register(mmokit.Command{
		Name: "tp", Category: "admin",
		Usage: "tp <target> <x> <y> | tp <target> <sx> <sy> <x> <y>", Description: "teleport player or entity (local or cell coords)",
		Complete: func(args []string) []string {
			if len(args) == 0 {
				return playerComplete(args)
			}
			return nil
		},
		Fn: func(args []string) {
			if len(args) < 3 {
				fmt.Println("  usage: tp <target> <x> <y>  (within current cell)")
				fmt.Println("         tp <target> <sx> <sy> <x> <y>  (explicit cell)")
			} else {
				targetArg := args[0]
				var sx, sy int32
				var fx, fy float32
				var explicitCell bool

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
					explicitCell = true
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
					if explicitCell && gw.C.CellCoord.HasAll(entity) {
						sec := gw.C.CellCoord.Get(entity)
						sec.CellX = sx
						sec.CellY = sy
					}
					if gw.C.Velocity.HasAll(entity) {
						vel := gw.C.Velocity.Get(entity)
						vel.X = 0
						vel.Y = 0
					}
					// Read back actual cell for display
					var dsx, dsy int32
					if gw.C.CellCoord.HasAll(entity) {
						sec := gw.C.CellCoord.Get(entity)
						dsx, dsy = sec.CellX, sec.CellY
					}
					return fmt.Sprintf("  teleported to %s", fmtCellPosRaw(dsx, dsy, fx, fy))
				})
				fmt.Println(result)
			}
		},
	})

	console.Register(mmokit.Command{
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
				itemID, ok := resolveResource(args[1])
				if !ok {
					fmt.Println("  unknown resource")
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

	console.Register(mmokit.Command{
		Name: "currency", Category: "admin",
		Usage: "currency <player> <amount> [currencyID]", Description: "set player currency balance (default: settlement currency)",
		Complete: func(args []string) []string {
			if len(args) == 0 {
				return playerComplete(args)
			}
			return nil
		},
		Fn: func(args []string) {
			if len(args) < 2 {
				fmt.Println("  usage: currency <player> <amount> [currencyID]")
			} else {
				amount, err := strconv.ParseInt(args[1], 10, 64)
				if err != nil {
					fmt.Println("  invalid amount")
				} else {
					curID := gw.Config.SettlementCurrencyID
					if len(args) >= 3 {
						parsed, err := strconv.ParseUint(args[2], 10, 32)
						if err != nil {
							fmt.Println("  invalid currencyID")
							return
						}
						curID = uint32(parsed)
					}
					playerArg := args[0]
					result := console.ExecOnGameLoop(func() string {
						connID, _, ok := resolvePlayer(gw, playerArg)
						if !ok {
							return "  player not found"
						}
						sess := gw.Players.ByConnID(connID)
						if sess == nil {
							return "  player not found"
						}
						username := sess.Username
						pdata := gw.PlayerDB.GetOrCreate(username)
						if pdata.Currencies == nil {
							pdata.Currencies = make(map[uint32]int64)
						}
						pdata.Currencies[curID] = amount
						gw.PlayerDB.MarkDirty(username)
						sendBankContentsAdmin(gw, connID, pdata)
						return fmt.Sprintf("  set %s currency[%d] to %d", username, curID, amount)
					})
					fmt.Println(result)
				}
			}
		},
	})

	console.Register(mmokit.Command{
		Name: "say", Category: "admin",
		Usage: "say <message>", Description: "broadcast server chat message",
		Fn: func(args []string) {
			if len(args) < 1 {
				fmt.Println("  usage: say <message>")
			} else {
				msg := strings.Join(args, " ")
				result := console.ExecOnGameLoop(func() string {
					mmokit.Enqueue(gw.Queue, &enginepb.ChatMsg{
						Username: "[SERVER]",
						Text:     msg,
					})
					return fmt.Sprintf("  broadcast: %s", msg)
				})
				fmt.Println(result)
			}
		},
	})

	console.Register(mmokit.Command{
		Name: "npcs", Category: "admin",
		Usage: "npcs", Description: "list all NPCs with net IDs",
		Fn: func(args []string) {
			result := console.ExecOnGameLoop(func() string {
				filter := ecs.NewFilter3[mmokit.EntityKind, mmokit.NetworkID, mmokit.Position](gw.ECS)
				query := filter.Query()
				var sb strings.Builder
				count := 0
				fmt.Fprintf(&sb, "  %-8s %-24s %-9s %-9s\n", "NETID", "POSITION", "HP", "SHIELD")
				for query.Next() {
					kind, netID, pos := query.Get()
					if kind.Type != gamecomp.TypeNPC {
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
					if gw.C.CellCoord.HasAll(entity) {
						sec := gw.C.CellCoord.Get(entity)
						posStr = fmtCellPos(*sec, *pos)
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

	console.Register(mmokit.Command{
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
					// Copy cell from target
					if gw.C.CellCoord.HasAll(srcEntity) && gw.C.CellCoord.HasAll(dstEntity) {
						srcSec := gw.C.CellCoord.Get(srcEntity)
						dstSec := gw.C.CellCoord.Get(dstEntity)
						srcSec.CellX = dstSec.CellX
						srcSec.CellY = dstSec.CellY
					}
					if gw.C.Velocity.HasAll(srcEntity) {
						vel := gw.C.Velocity.Get(srcEntity)
						vel.X = 0
						vel.Y = 0
					}
					var dsx, dsy int32
					if gw.C.CellCoord.HasAll(srcEntity) {
						sec := gw.C.CellCoord.Get(srcEntity)
						dsx, dsy = sec.CellX, sec.CellY
					}
					return fmt.Sprintf("  teleported %s near %s at %s", playerArg, targetArg, fmtCellPosRaw(dsx, dsy, nx, ny))
				})
				fmt.Println(result)
			}
		},
	})

	console.Register(mmokit.Command{
		Name: "spawnnpcs", Category: "admin",
		Usage: "spawnnpcs <count> <player> [--move]", Description: "spawn N NPCs around a player (within AoI) for load testing",
		Complete: func(args []string) []string {
			if len(args) == 1 {
				return playerComplete(args)
			}
			return nil
		},
		Fn: func(args []string) {
			if len(args) < 2 {
				fmt.Println("  usage: spawnnpcs <count> <player> [--move]")
			} else {
				count, err := strconv.Atoi(args[0])
				if err != nil || count < 1 {
					fmt.Println("  invalid count")
				} else {
					playerArg := args[1]
					move := len(args) >= 3 && args[2] == "--move"
					result := console.ExecOnGameLoop(func() string {
						_, entity, ok := resolvePlayer(gw, playerArg)
						if !ok {
							return "  player not found"
						}
						if !gw.C.Position.HasAll(entity) {
							return "  player has no position"
						}
						pos := gw.C.Position.Get(entity)
						wanderMap := ecs.NewMap1[gamecomp.Wander](gw.ECS)
						radius := gw.Config.AoIRadius * 0.8 // stay within AoI
						for i := 0; i < count; i++ {
							angle := rand.Float64() * 2 * math.Pi
							dist := float32(math.Sqrt(rand.Float64())) * radius // uniform distribution in circle
							nx := pos.X + dist*float32(math.Cos(angle))
							ny := pos.Y + dist*float32(math.Sin(angle))
							npcEntity := gw.SpawnNPC(nx, ny)
							if move {
								initAngle := rand.Float32() * 2 * math.Pi
								wanderMap.Add(npcEntity, &gamecomp.Wander{
									Speed:       8,
									Timer:       rand.Float32() * 2, // stagger initial direction changes
									Interval:    3,
									TargetAngle: initAngle,
									TurnRate:    1.5, // ~85°/s
								})
							}
						}
						suffix := ""
						if move {
							suffix = " (wandering)"
						}
						return fmt.Sprintf("  spawned %d NPCs around %s within %.0f units%s", count, playerArg, radius, suffix)
					})
					fmt.Println(result)
				}
			}
		},
	})

	console.Register(mmokit.Command{
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
					online := gw.Players.ByUsername(username) != nil
					var sb strings.Builder
					status := "offline"
					if online {
						status = "online"
					}
					fmt.Fprintf(&sb, "  player: %s (%s)\n", pd.Username, status)
					fmt.Fprintf(&sb, "  created: %s\n", pd.CreatedAt.Format("2006-01-02 15:04"))
					fmt.Fprintf(&sb, "  last login: %s\n", pd.LastLogin.Format("2006-01-02 15:04"))
					fmt.Fprintf(&sb, "  position: %s\n", fmtCellPosRaw(pd.CellX, pd.CellY, pd.X, pd.Y))
					if len(pd.Currencies) > 0 {
						fmt.Fprintf(&sb, "  currencies:\n")
						for curID, bal := range pd.Currencies {
							fmt.Fprintf(&sb, "    [%d]: %d\n", curID, bal)
						}
					}
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
					gw.Players.ForEach(mmokit.StateActive, func(s *mmokit.PlayerSession) {
						onlineUsers[s.Username] = true
					})
					gw.Players.ForEach(StateDocking, func(s *mmokit.PlayerSession) {
						onlineUsers[s.Username] = true
					})
					gw.Players.ForEach(StateDocked, func(s *mmokit.PlayerSession) {
						onlineUsers[s.Username] = true
					})
					nameW := len("USERNAME")
					for _, pd := range all {
						if len(pd.Username) > nameW {
							nameW = len(pd.Username)
						}
					}
					var sb strings.Builder
					rowFmt := fmt.Sprintf("  %%-%ds %%-8s %%-8s %%-24s %%-20s\n", nameW)
					fmt.Fprintf(&sb, rowFmt, "USERNAME", "STATUS", "CURRENCY", "POSITION", "LAST LOGIN")
					dataFmt := fmt.Sprintf("  %%-%ds %%-8s %%-8d %%-24s %%-20s\n", nameW)
					for _, pd := range all {
						status := "offline"
						if onlineUsers[pd.Username] {
							status = "online"
						}
						bal := pd.GetCurrency(gw.Config.SettlementCurrencyID)
						lastLogin := pd.LastLogin.Format("2006-01-02 15:04")
						if pd.LastLogin.IsZero() {
							lastLogin = "never"
						}
						fmt.Fprintf(&sb, dataFmt,
							pd.Username, status, bal,
							fmtCellPosRaw(pd.CellX, pd.CellY, pd.X, pd.Y), lastLogin)
					}
					return sb.String()
				})
				fmt.Print(result)
			}
		},
	})

	console.Register(mmokit.Command{
		Name: "grid", Aliases: []string{"sg"},
		Category: "debug", Usage: "grid", Description: "toggle cell grid lines on all clients",
		Fn: func(args []string) {
			result := console.ExecOnGameLoop(func() string {
				gw.DebugShowCellGrid = !gw.DebugShowCellGrid
				newVal := gw.DebugShowCellGrid
				broadcastDebugFlags(gw)

				// Propagate to all other nodes so players on any cell see the grid.
				for _, node := range allNodes {
					if node.World == gw {
						continue
					}
					nw := node.World
					nw.Engine.PendingAdminCmds <- func() {
						nw.DebugShowCellGrid = newVal
						broadcastDebugFlags(nw)
					}
				}

				if newVal {
					return "  cell grid: ON"
				}
				return "  cell grid: OFF"
			})
			fmt.Println(result)
		},
	})

}

// resolvePlayer finds a player by connID (numeric) or username (case-insensitive prefix).
func resolvePlayer(gw *GameWorld, input string) (connID uint32, entity ecs.Entity, ok bool) {
	// Try numeric connID first
	if id, err := strconv.ParseUint(input, 10, 32); err == nil {
		cid := uint32(id)
		if sess := gw.Players.ByConnID(cid); sess != nil && sess.State == mmokit.StateActive && gw.ECS.Alive(sess.Entity) {
			return cid, sess.Entity, true
		}
	}

	// Search by username (case-insensitive prefix)
	inputLower := strings.ToLower(input)
	var found *mmokit.PlayerSession
	gw.Players.ForEach(mmokit.StateActive, func(s *mmokit.PlayerSession) {
		if found != nil {
			return
		}
		if strings.HasPrefix(strings.ToLower(s.Username), inputLower) && gw.ECS.Alive(s.Entity) {
			found = s
		}
	})
	if found != nil {
		return found.ConnID, found.Entity, true
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

// resolveResource maps short resource names to item IDs.
func resolveResource(input string) (uint32, bool) {
	input = strings.ToLower(input)
	for _, def := range item.All() {
		if def.Category == item.CategoryResource && strings.HasPrefix(strings.ToLower(def.Name), input) {
			return def.ID, true
		}
	}
	return 0, false
}

// broadcastDebugFlags sends the current debug flag state to all logged-in players.
func broadcastDebugFlags(gw *GameWorld) {
	data := mmokit.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_DEBUG_FLAGS), &gamepb.DebugFlagsMsg{
		ShowCellGrid: gw.DebugShowCellGrid,
	})
	if data == nil {
		return
	}
	sendToAll := func(s *mmokit.PlayerSession) {
		gw.ConnMgr.SendReliable(s.ConnID, data)
	}
	gw.Players.ForEach(mmokit.StateActive, sendToAll)
	gw.Players.ForEach(StateDocking, sendToAll)
	gw.Players.ForEach(StateDocked, sendToAll)
}

// sendBankContentsAdmin sends a BankContentsMsg to a player (used by admin commands).
func sendBankContentsAdmin(gw *GameWorld, connID uint32, pdata *PlayerData) {
	var items []*gamepb.InventoryItem
	for id, qty := range pdata.Bank {
		if qty > 0 {
			items = append(items, &gamepb.InventoryItem{ItemId: id, Quantity: qty})
		}
	}
	var currencies []*gamepb.CurrencyBalance
	for curID, bal := range pdata.Currencies {
		if bal != 0 {
			currencies = append(currencies, &gamepb.CurrencyBalance{CurrencyId: curID, Balance: bal})
		}
	}
	data := mmokit.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_BANK_CONTENTS), &gamepb.BankContentsMsg{
		Items:      items,
		TotalMass:  pdata.BankTotalMass(),
		MaxMass:    gw.Config.BankMaxMass,
		Currencies: currencies,
	})
	if data != nil {
		gw.ConnMgr.SendReliable(connID, data)
	}
}
