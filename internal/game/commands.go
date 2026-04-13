package game

import (
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"time"

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
	Cell  mmokit.CellID
	World *GameWorld
}

// BuildEntityOpts creates EntityOpts callbacks that query the game's ECS world.
func BuildEntityOpts(gw *GameWorld) *engine.EntityOpts {
	return &engine.EntityOpts{
		Summary: func() map[string]int {
			counts := make(map[string]int)
			filter := ecs.NewFilter1[mmokit.EntityKind](gw.eng.ECS)
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
			filter := ecs.NewFilter3[mmokit.EntityKind, mmokit.NetworkID, mmokit.Position](gw.eng.ECS)
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
					NodeID: gw.NodeID(),
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
			if !ok || !gw.eng.ECS.Alive(entity) {
				return engine.EntityInfo{}, false
			}
			info := engine.EntityInfo{NetID: netID, NodeID: gw.NodeID()}
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
			if entity, ok := gw.NetIDToEntity[netID]; ok && gw.eng.ECS.Alive(entity) {
				gw.eng.MarkForRemoval(entity)
				return true
			}
			return false
		},
	}
}

// RegisterCommands registers all game-specific admin commands on the console.
// allNodes provides access to all coordinator nodes for global commands (ps, entities).
func RegisterCommands(console *mmokit.Console, coord *mmokit.Coordinator, playerDB *PlayerRepo, allNodes []NodeInfo) {
	var cfg *GameConfig
	var firstWorld *GameWorld
	for _, ni := range allNodes {
		cfg = ni.World.Config
		firstWorld = ni.World
		break
	}

	// Set static completions
	console.SetCompletions("resources", []string{"ore", "crystal", "gas", "metal"})

	playerComplete := func(_ []string) []string {
		active := coord.ActiveUsers()
		names := make([]string, 0, len(active))
		for name := range active {
			names = append(names, name)
		}
		return names
	}

	console.Register(mmokit.Command{
		Name: "players", Aliases: []string{"ps"},
		Category: "admin", Usage: "players [--all|-a] [username] [--live]",
		Description: "list players or show details (--all includes offline, --live queries node ECS)",
		Complete: func(args []string) []string {
			if len(args) == 0 {
				return playerComplete(args)
			}
			return nil
		},
		Fn: func(args []string) {
			var showAll, showLive bool
			var targetUser string
			for _, a := range args {
				switch a {
				case "--all", "-a":
					showAll = true
				case "--live":
					showLive = true
				default:
					targetUser = strings.ToLower(a)
				}
			}

			if targetUser != "" {
				// Detail view for a single player
				pd := playerDB.Get(targetUser)
				if pd == nil {
					fmt.Printf("  player %q not found in DB\n", targetUser)
					return
				}
				nodeID := coord.ActiveUserNode(targetUser)
				status := "offline"
				if nodeID != "" {
					status = fmt.Sprintf("online (%s)", nodeID)
				}
				var sb strings.Builder
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

				if showLive && nodeID != "" {
					liveResult := execOnPlayerNode(coord, allNodes, targetUser, func(gw *GameWorld, sess *mmokit.PlayerSession) string {
						entity := sess.Entity
						var liveSB strings.Builder
						fmt.Fprintf(&liveSB, "  --- live ECS data (node %s, node root cell (%d,%d), depth=%d) ---\n",
							gw.NodeID(), gw.RootCell.CellX, gw.RootCell.CellY, gw.Cell().Depth)
						if gw.C.NetworkID.HasAll(entity) {
							fmt.Fprintf(&liveSB, "  netID: %d\n", gw.C.NetworkID.Get(entity).ID)
						}
						if gw.C.Position.HasAll(entity) && gw.C.CellCoord.HasAll(entity) {
							pos := gw.C.Position.Get(entity)
							sec := gw.C.CellCoord.Get(entity)
							worldX := float32(sec.CellX)*coords.CellSize + pos.X
							worldY := float32(sec.CellY)*coords.CellSize + pos.Y
							fmt.Fprintf(&liveSB, "  live pos: cell %s local (%.1f, %.1f) world (%.1f, %.1f)\n",
								fmtCellPos(*sec, *pos), pos.X, pos.Y, worldX, worldY)
						}
						if gw.C.Velocity.HasAll(entity) {
							v := gw.C.Velocity.Get(entity)
							fmt.Fprintf(&liveSB, "  vel: (%.2f, %.2f) speed=%.2f\n", v.X, v.Y, math.Sqrt(float64(v.X*v.X+v.Y*v.Y)))
						}
						if gw.C.Rotation.HasAll(entity) {
							r := gw.C.Rotation.Get(entity)
							fmt.Fprintf(&liveSB, "  rot: %.3f rad (%.0f deg)\n", r.Angle, r.Angle*180/math.Pi)
						}
						if gw.C.Health.HasAll(entity) {
							h := gw.C.Health.Get(entity)
							fmt.Fprintf(&liveSB, "  hp: %.0f/%.0f\n", h.Current, h.Max)
						}
						if gw.C.Shield.HasAll(entity) {
							s := gw.C.Shield.Get(entity)
							fmt.Fprintf(&liveSB, "  shield: %.0f/%.0f\n", s.Current, s.Max)
						}
						if gw.C.Inventory.HasAll(entity) {
							inv := gw.C.Inventory.Get(entity)
							fmt.Fprintf(&liveSB, "  cargo: mass=%.0f/%.0f items=%d\n", inv.TotalMass(), inv.MaxMass, len(inv.Items))
						}
						return liveSB.String()
					})
					fmt.Fprint(&sb, liveResult)
				}
				fmt.Print(sb.String())
				return
			}

			// List view — no game loop needed
			active := coord.ActiveUsers()
			all := playerDB.All()

			if !showAll && len(active) == 0 {
				fmt.Println("  no players online")
				return
			}

			var sb strings.Builder
			fmt.Fprintf(&sb, "  %-16s %-8s %-14s %-8s %-24s %-20s\n", "USERNAME", "STATUS", "NODE", "CURRENCY", "POSITION", "LAST LOGIN")

			printRow := func(pd *PlayerData, status, nodeID string) {
				var curID uint32
				if cfg != nil {
					curID = cfg.SettlementCurrencyID
				}
				bal := pd.GetCurrency(curID)
				lastLogin := pd.LastLogin.Format("2006-01-02 15:04")
				if pd.LastLogin.IsZero() {
					lastLogin = "never"
				}
				fmt.Fprintf(&sb, "  %-16s %-8s %-14s %-8d %-24s %-20s\n",
					pd.Username, status, nodeID, bal,
					fmtCellPosRaw(pd.CellX, pd.CellY, pd.X, pd.Y), lastLogin)
			}

			if showAll {
				for _, pd := range all {
					nodeID := active[pd.Username]
					status := "offline"
					if nodeID != "" {
						status = "online"
					}
					printRow(pd, status, nodeID)
				}
			} else {
				for username, nodeID := range active {
					pd := playerDB.Get(username)
					if pd == nil {
						continue
					}
					printRow(pd, "online", nodeID)
				}
			}
			fmt.Print(sb.String())
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
				return
			}
			amount, err := strconv.ParseFloat(args[1], 32)
			if err != nil {
				fmt.Println("  invalid amount")
				return
			}
			targetArg := args[0]
			dmg := float32(amount)
			result := execOnPlayerNode(coord, allNodes, targetArg, func(gw *GameWorld, sess *mmokit.PlayerSession) string {
				entity := sess.Entity
				if !gw.C.Health.HasAll(entity) {
					return "  entity has no health component"
				}
				dealt := gw.ApplyDamage(entity, dmg, 0)
				h := gw.C.Health.Get(entity)
				return fmt.Sprintf("  dealt %.0f damage (hp: %.0f/%.0f)", dealt, h.Current, h.Max)
			})
			if strings.Contains(result, "not found") {
				result = execOnEntityNode(allNodes, targetArg, func(gw *GameWorld, entity ecs.Entity) string {
					if !gw.C.Health.HasAll(entity) {
						return "  entity has no health component"
					}
					dealt := gw.ApplyDamage(entity, dmg, 0)
					h := gw.C.Health.Get(entity)
					return fmt.Sprintf("  dealt %.0f damage (hp: %.0f/%.0f)", dealt, h.Current, h.Max)
				})
			}
			fmt.Println(result)
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
				return
			}
			targetArg := args[0]
			result := execOnPlayerNode(coord, allNodes, targetArg, func(gw *GameWorld, sess *mmokit.PlayerSession) string {
				gw.MarkPlayerDeath(sess.Entity, 0)
				return fmt.Sprintf("  killed player %s", targetArg)
			})
			if strings.Contains(result, "not found") {
				result = execOnEntityNode(allNodes, targetArg, func(gw *GameWorld, entity ecs.Entity) string {
					gw.MarkNPCDeath(entity, 0)
					return fmt.Sprintf("  killed entity %s", targetArg)
				})
			}
			fmt.Println(result)
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
				return
			}
			playerArg := args[0]
			result := execOnPlayerNode(coord, allNodes, playerArg, func(gw *GameWorld, sess *mmokit.PlayerSession) string {
				username := sess.Username
				connID := sess.ConnID
				gw.Players.Remove(sess)
				// ConnSender is a narrow interface; Remove is gateway-only.
				// In all-in-one mode the type assertion succeeds and the socket
				// is closed immediately. In multi-process mode this becomes a
				// no-op until T8 wires cross-process disconnect propagation.
				if remover, ok := gw.eng.ConnMgr.(interface{ Remove(uint32) }); ok {
					remover.Remove(connID)
				}
				return fmt.Sprintf("  kicked %s (conn %d)", username, connID)
			})
			fmt.Println(result)
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
				return
			}
			targetArg := args[0]
			healFn := func(gw *GameWorld, entity ecs.Entity) string {
				if gw.C.Health.HasAll(entity) {
					h := gw.C.Health.Get(entity)
					h.Current = h.Max
				}
				if gw.C.Shield.HasAll(entity) {
					s := gw.C.Shield.Get(entity)
					s.Current = s.Max
				}
				return "  fully healed"
			}
			result := execOnPlayerNode(coord, allNodes, targetArg, func(gw *GameWorld, sess *mmokit.PlayerSession) string {
				return healFn(gw, sess.Entity)
			})
			if strings.Contains(result, "not found") {
				result = execOnEntityNode(allNodes, targetArg, func(gw *GameWorld, entity ecs.Entity) string {
					return healFn(gw, entity)
				})
			}
			fmt.Println(result)
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
				return
			}
			targetArg := args[0]
			var sx, sy int32
			var fx, fy float32
			var explicitCell bool

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
				fx, fy = float32(x), float32(y)
				explicitCell = true
			} else {
				x, err1 := strconv.ParseFloat(args[1], 32)
				y, err2 := strconv.ParseFloat(args[2], 32)
				if err1 != nil || err2 != nil {
					fmt.Println("  invalid coordinates")
					return
				}
				fx, fy = float32(x), float32(y)
			}

			tpFn := func(gw *GameWorld, entity ecs.Entity) string {
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
				if gw.C.MoveTarget.HasAll(entity) {
					gw.C.MoveTarget.Get(entity).Active = false
				}
				var dsx, dsy int32
				if gw.C.CellCoord.HasAll(entity) {
					sec := gw.C.CellCoord.Get(entity)
					dsx, dsy = sec.CellX, sec.CellY
				}
				return fmt.Sprintf("  teleported to %s", fmtCellPosRaw(dsx, dsy, fx, fy))
			}
			result := execOnPlayerNode(coord, allNodes, targetArg, func(gw *GameWorld, sess *mmokit.PlayerSession) string {
				return tpFn(gw, sess.Entity)
			})
			if strings.Contains(result, "not found") {
				result = execOnEntityNode(allNodes, targetArg, func(gw *GameWorld, entity ecs.Entity) string {
					return tpFn(gw, entity)
				})
			}
			fmt.Println(result)
		},
	})

	console.Register(mmokit.Command{
		Name: "give", Category: "admin",
		Usage: "give <player> <res> <amt>", Description: "add resource (ore/crystal/gas/metal)",
		Complete: func(args []string) []string {
			switch len(args) {
			case 0:
				return playerComplete(args)
			case 1:
				return console.GetCompletions("resources")
			default:
				return nil
			}
		},
		Fn: func(args []string) {
			if len(args) < 3 {
				fmt.Println("  usage: give <player> <resource> <amount>")
				return
			}
			itemID, ok := resolveResource(args[1])
			if !ok {
				fmt.Println("  unknown resource")
				return
			}
			amount, err := strconv.ParseInt(args[2], 10, 32)
			if err != nil {
				fmt.Println("  invalid amount")
				return
			}
			playerArg := args[0]
			amt := int32(amount)
			result := execOnPlayerNode(coord, allNodes, playerArg, func(gw *GameWorld, sess *mmokit.PlayerSession) string {
				entity := sess.Entity
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
				return
			}
			amount, err := strconv.ParseInt(args[1], 10, 64)
			if err != nil {
				fmt.Println("  invalid amount")
				return
			}
			var curID uint32
			if cfg != nil {
				curID = cfg.SettlementCurrencyID
			}
			if len(args) >= 3 {
				parsed, err := strconv.ParseUint(args[2], 10, 32)
				if err != nil {
					fmt.Println("  invalid currencyID")
					return
				}
				curID = uint32(parsed)
			}
			playerArg := args[0]
			result := execOnPlayerNode(coord, allNodes, playerArg, func(gw *GameWorld, sess *mmokit.PlayerSession) string {
				username := sess.Username
				pdata := playerDB.GetOrCreate(username)
				if pdata.Currencies == nil {
					pdata.Currencies = make(map[uint32]int64)
				}
				pdata.Currencies[curID] = amount
				playerDB.MarkDirty(username)
				sendBankContentsAdmin(gw, sess.ConnID, pdata)
				return fmt.Sprintf("  set %s currency[%d] to %d", username, curID, amount)
			})
			fmt.Println(result)
		},
	})

	console.Register(mmokit.Command{
		Name: "say", Category: "admin",
		Usage: "say <message>", Description: "broadcast server chat message",
		Fn: func(args []string) {
			if len(args) < 1 {
				fmt.Println("  usage: say <message>")
				return
			}
			msg := strings.Join(args, " ")
			result := console.ExecOnGameLoop(func() string {
				// Uses first node; chat relays via bridge to all nodes.
				if len(allNodes) == 0 {
					return "  no nodes available"
				}
				gw := allNodes[0].World
				mmokit.Enqueue(gw.Queue, &enginepb.ChatMsg{
					Username: "[SERVER]",
					Text:     msg,
				})
				return fmt.Sprintf("  broadcast: %s", msg)
			})
			fmt.Println(result)
		},
	})

	console.Register(mmokit.Command{
		Name: "npcs", Category: "admin",
		Usage: "npcs", Description: "list all NPCs with net IDs",
		Fn: func(args []string) {
			result := console.ExecOnGameLoop(func() string {
				filter := ecs.NewFilter3[mmokit.EntityKind, mmokit.NetworkID, mmokit.Position](firstWorld.eng.ECS)
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
					if firstWorld.C.Health.HasAll(entity) {
						h := firstWorld.C.Health.Get(entity)
						hp = fmt.Sprintf("%.0f/%.0f", h.Current, h.Max)
					}
					if firstWorld.C.Shield.HasAll(entity) {
						s := firstWorld.C.Shield.Get(entity)
						shield = fmt.Sprintf("%.0f/%.0f", s.Current, s.Max)
					}
					var posStr string
					if firstWorld.C.CellCoord.HasAll(entity) {
						sec := firstWorld.C.CellCoord.Get(entity)
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
				return
			}
			playerArg := args[0]
			targetArg := args[1]
			result := execOnPlayerNode(coord, allNodes, playerArg, func(gw *GameWorld, sess *mmokit.PlayerSession) string {
				srcEntity := sess.Entity
				// Resolve destination on same node
				var dstEntity ecs.Entity
				var ok bool
				if dstSess := gw.Players.ByUsername(strings.ToLower(targetArg)); dstSess != nil && gw.eng.ECS.Alive(dstSess.Entity) {
					dstEntity = dstSess.Entity
					ok = true
				}
				if !ok {
					dstEntity, ok = resolveEntity(gw, targetArg)
				}
				if !ok {
					return fmt.Sprintf("  destination %q not found on same node", targetArg)
				}
				if !gw.C.Position.HasAll(srcEntity) || !gw.C.Position.HasAll(dstEntity) {
					return "  entity missing position"
				}
				dstPos := gw.C.Position.Get(dstEntity)
				angle := rand.Float64() * 2 * math.Pi
				offsetDist := float32(150)
				nx := dstPos.X + offsetDist*float32(math.Cos(angle))
				ny := dstPos.Y + offsetDist*float32(math.Sin(angle))
				srcPos := gw.C.Position.Get(srcEntity)
				srcPos.X = nx
				srcPos.Y = ny
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
				if gw.C.MoveTarget.HasAll(srcEntity) {
					gw.C.MoveTarget.Get(srcEntity).Active = false
				}
				var dsx, dsy int32
				if gw.C.CellCoord.HasAll(srcEntity) {
					sec := gw.C.CellCoord.Get(srcEntity)
					dsx, dsy = sec.CellX, sec.CellY
				}
				return fmt.Sprintf("  teleported %s near %s at %s", playerArg, targetArg, fmtCellPosRaw(dsx, dsy, nx, ny))
			})
			fmt.Println(result)
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
					result := execOnPlayerNode(coord, allNodes, playerArg, func(gw *GameWorld, sess *mmokit.PlayerSession) string {
						entity := sess.Entity
						if !gw.C.Position.HasAll(entity) {
							return "  player has no position"
						}
						pos := gw.C.Position.Get(entity)
						wanderMap := ecs.NewMap1[gamecomp.Wander](gw.eng.ECS)
						radius := gw.Config.AoIRadius * 0.8
						for range count {
							angle := rand.Float64() * 2 * math.Pi
							dist := float32(math.Sqrt(rand.Float64())) * radius
							nx := pos.X + dist*float32(math.Cos(angle))
							ny := pos.Y + dist*float32(math.Sin(angle))
							npcEntity := gw.SpawnNPC(nx, ny)
							if move {
								initAngle := rand.Float32() * 2 * math.Pi
								wanderMap.Add(npcEntity, &gamecomp.Wander{
									Speed:       8,
									Timer:       rand.Float32() * 2,
									Interval:    3,
									TargetAngle: initAngle,
									TurnRate:    1.5,
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

}

// execOnPlayerNode finds the node hosting a player and executes fn on its game loop.
func execOnPlayerNode(
	coord *mmokit.Coordinator,
	allNodes []NodeInfo,
	username string,
	fn func(gw *GameWorld, sess *mmokit.PlayerSession) string,
) string {
	nodeID := coord.ActiveUserNode(strings.ToLower(username))
	if nodeID == "" {
		return fmt.Sprintf("  player %q not found (offline?)", username)
	}
	var target *NodeInfo
	for i := range allNodes {
		if allNodes[i].ID == nodeID {
			target = &allNodes[i]
			break
		}
	}
	if target == nil {
		return fmt.Sprintf("  node %s not found", nodeID)
	}
	gw := target.World
	result := make(chan string, 1)
	gw.eng.PendingAdminCmds <- func() {
		sess := gw.Players.ByUsername(strings.ToLower(username))
		if sess == nil || !gw.eng.ECS.Alive(sess.Entity) {
			result <- fmt.Sprintf("  player %q session not found on %s", username, nodeID)
			return
		}
		result <- fn(gw, sess)
	}
	select {
	case r := <-result:
		return r
	case <-time.After(5 * time.Second):
		return "  game loop not responding (timeout)"
	}
}

// execOnEntityNode finds an entity by netID across all nodes and executes fn on its node.
func execOnEntityNode(
	allNodes []NodeInfo,
	targetArg string,
	fn func(gw *GameWorld, entity ecs.Entity) string,
) string {
	netID, err := strconv.ParseUint(targetArg, 10, 32)
	if err != nil {
		return "  invalid net ID"
	}
	for _, ni := range allNodes {
		gw := ni.World
		result := make(chan string, 1)
		gw.eng.PendingAdminCmds <- func() {
			if entity, ok := gw.NetIDToEntity[uint32(netID)]; ok && gw.eng.ECS.Alive(entity) {
				result <- fn(gw, entity)
			} else {
				result <- ""
			}
		}
		select {
		case r := <-result:
			if r != "" {
				return r
			}
		case <-time.After(5 * time.Second):
			continue
		}
	}
	return "  entity not found"
}

// resolveEntity finds any entity by network ID. Returns the ECS entity and true if found.
func resolveEntity(gw *GameWorld, input string) (ecs.Entity, bool) {
	id, err := strconv.ParseUint(input, 10, 32)
	if err != nil {
		return ecs.Entity{}, false
	}
	netID := uint32(id)

	// Check NetIDToEntity map (rebuilt each tick by SpatialSystem)
	if entity, exists := gw.NetIDToEntity[netID]; exists && gw.eng.ECS.Alive(entity) {
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
		gw.eng.ConnMgr.SendReliable(connID, data)
	}
}
