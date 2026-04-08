# Coordinator-Routed Console Commands Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Route console commands through coordinator knowledge instead of a single node reference. Merge `players`/`playerdb` into one command. Action commands target the correct node via `activeUsers` map.

**Architecture:** The coordinator already tracks `activeUsers[username] → nodeID` and shares a global `PlayerDB`. Data commands (`players`) read these directly without involving any node's game loop. Action commands (`tp`, `damage`, etc.) use a new `execOnPlayerNode` helper that finds the right node via `activeUsers`, then sends a closure to that node's `PendingAdminCmds` channel.

**Tech Stack:** Go, ECS (Ark)

---

### Task 1: Add Coordinator accessors for activeUsers

**Files:**
- Modify: `pkg/universe/coordinator.go`

- [ ] **Step 1: Add `ActiveUserNode` and `ActiveUsers` methods**

After the `notifySessionRemoved` method, add:

```go
// ActiveUserNode returns the nodeID for an active username, or "" if offline.
func (c *Coordinator) ActiveUserNode(username string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.activeUsers[username]
}

// ActiveUsers returns a snapshot of active usernames and their node IDs.
func (c *Coordinator) ActiveUsers() map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make(map[string]string, len(c.activeUsers))
	for k, v := range c.activeUsers {
		result[k] = v
	}
	return result
}
```

- [ ] **Step 2: Verify compilation**

Run: `go vet ./pkg/universe/`

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/coordinator.go
git commit -m "feat: add ActiveUserNode and ActiveUsers accessors to Coordinator"
```

---

### Task 2: Add `execOnPlayerNode` and `execOnEntityNode` helpers

**Files:**
- Modify: `internal/game/commands.go`

These helpers replace `resolvePlayer(gw, ...)` + `ExecOnGameLoop(...)` for action commands. They find the right node and execute there.

- [ ] **Step 1: Add helpers after `resolveEntity` (around line 904)**

```go
// execOnPlayerNode finds the node hosting a player and executes fn on its game loop.
// Returns the fn's result string, or an error message if the player is not found.
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
	gw.Engine.PendingAdminCmds <- func() {
		sess := gw.Players.ByUsername(strings.ToLower(username))
		if sess == nil || !gw.ECS.Alive(sess.Entity) {
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
	netID uint32,
	fn func(gw *GameWorld, entity ecs.Entity) string,
) string {
	for _, ni := range allNodes {
		gw := ni.World
		result := make(chan string, 1)
		gw.Engine.PendingAdminCmds <- func() {
			if entity, ok := gw.NetIDToEntity[netID]; ok && gw.ECS.Alive(entity) {
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
```

- [ ] **Step 2: Add `"time"` to imports** if not already present.

- [ ] **Step 3: Verify compilation**

Run: `go vet ./internal/game/` (may fail due to proto deps in worktree — verify with `go build ./internal/game/` on main checkout)

- [ ] **Step 4: Commit**

```bash
git add internal/game/commands.go
git commit -m "feat: add execOnPlayerNode and execOnEntityNode helpers"
```

---

### Task 3: Change `RegisterCommands` signature

**Files:**
- Modify: `internal/game/commands.go:126`
- Modify: `cmd/server/main.go:163`

- [ ] **Step 1: Update `RegisterCommands` signature**

Change line 126 from:
```go
func RegisterCommands(console *mmokit.Console, gw *GameWorld, store mmokit.Store, allNodes []NodeInfo) {
	gw.console = console
```
to:
```go
func RegisterCommands(console *mmokit.Console, coord *mmokit.Coordinator, playerDB *PlayerRepo, store mmokit.Store, allNodes []NodeInfo) {
```

Remove `gw.console = console` — the console is no longer tied to one GameWorld. The `updatePlayerCompletions` function in `lifecycle.go` still uses `gw.console`, but we'll address tab completions separately.

For commands that still need a `GameConfig` reference (e.g., `SettlementCurrencyID`), get it from any node:

```go
	var cfg *GameConfig
	for _, ni := range allNodes {
		cfg = &ni.World.Config
		break
	}
```

- [ ] **Step 2: Update the caller in `cmd/server/main.go`**

Change line 163 from:
```go
		game.RegisterCommands(console, anyWorld, store, allNodes)
```
to:
```go
		game.RegisterCommands(console, coordinator, playerDB, store, allNodes)
```

Remove `anyWorld` variable if no longer used after this change (it's still used by `RegisterBuiltins` above, so keep it).

- [ ] **Step 3: Verify compilation**

Run: `go vet ./cmd/server/ ./internal/game/` (or `go build`)

- [ ] **Step 4: Commit**

```bash
git add internal/game/commands.go cmd/server/main.go
git commit -m "refactor: change RegisterCommands to take Coordinator + PlayerDB instead of GameWorld"
```

---

### Task 4: Rewrite `players` command (merge with `playerdb`)

**Files:**
- Modify: `internal/game/commands.go`

Replace both `players`/`ps` (lines 136-198) and `playerdb`/`pdb` (lines 720-830) with one consolidated command.

- [ ] **Step 1: Replace the `players` command registration**

Delete the old `players` command block (lines 136-198). Replace with:

```go
	console.Register(mmokit.Command{
		Name: "players", Aliases: []string{"ps"},
		Category: "admin", Usage: "players [username] [--all|-a] [--live|-l]", Description: "list players or show details (--all includes offline, --live shows real-time ECS data)",
		Complete: func(args []string) []string {
			if len(args) == 0 {
				return playerComplete(args)
			}
			return nil
		},
		Fn: func(args []string) {
			showAll := false
			showLive := false
			var username string
			for _, arg := range args {
				switch arg {
				case "--all", "-a":
					showAll = true
				case "--live", "-l":
					showLive = true
				default:
					if username == "" {
						username = arg
					}
				}
			}

			if username != "" {
				// Single player detail view
				username = strings.ToLower(username)
				pd := playerDB.Get(username)
				if pd == nil {
					fmt.Printf("  player %q not found in DB\n", username)
					return
				}
				nodeID := coord.ActiveUserNode(username)
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
					liveData := execOnPlayerNode(coord, allNodes, username, func(gw *GameWorld, sess *mmokit.PlayerSession) string {
						var lsb strings.Builder
						entity := sess.Entity
						if gw.C.Position.HasAll(entity) && gw.C.CellCoord.HasAll(entity) {
							pos := gw.C.Position.Get(entity)
							sec := gw.C.CellCoord.Get(entity)
							fmt.Fprintf(&lsb, "  live position: %s\n", fmtCellPos(*sec, *pos))
						}
						if gw.C.Health.HasAll(entity) {
							h := gw.C.Health.Get(entity)
							fmt.Fprintf(&lsb, "  hp: %.0f/%.0f\n", h.Current, h.Max)
						}
						if gw.C.Shield.HasAll(entity) {
							s := gw.C.Shield.Get(entity)
							fmt.Fprintf(&lsb, "  shield: %.0f/%.0f\n", s.Current, s.Max)
						}
						if gw.C.Inventory.HasAll(entity) {
							inv := gw.C.Inventory.Get(entity)
							fmt.Fprintf(&lsb, "  cargo: mass %.0f/%.0f, %d items\n", inv.TotalMass(), inv.MaxMass, len(inv.Items))
						}
						return lsb.String()
					})
					sb.WriteString(liveData)
				}

				fmt.Print(sb.String())
				return
			}

			// List view
			active := coord.ActiveUsers()
			all := playerDB.All()
			if len(all) == 0 && len(active) == 0 {
				fmt.Println("  no players")
				return
			}

			nameW := len("USERNAME")
			for _, pd := range all {
				if len(pd.Username) > nameW {
					nameW = len(pd.Username)
				}
			}

			var sb strings.Builder
			rowFmt := fmt.Sprintf("  %%-%ds %%-8s %%-14s %%-8s %%-24s %%-20s\n", nameW)
			dataFmt := fmt.Sprintf("  %%-%ds %%-8s %%-14s %%-8d %%-24s %%-20s\n", nameW)
			fmt.Fprintf(&sb, rowFmt, "USERNAME", "STATUS", "NODE", "CURRENCY", "POSITION", "LAST LOGIN")

			for _, pd := range all {
				nodeID, online := active[pd.Username]
				if !showAll && !online {
					continue
				}
				status := "offline"
				node := "—"
				if online {
					status = "online"
					node = nodeID
				}
				bal := pd.GetCurrency(cfg.SettlementCurrencyID)
				lastLogin := pd.LastLogin.Format("2006-01-02 15:04")
				if pd.LastLogin.IsZero() {
					lastLogin = "never"
				}
				fmt.Fprintf(&sb, dataFmt,
					pd.Username, status, node, bal,
					fmtCellPosRaw(pd.CellX, pd.CellY, pd.X, pd.Y), lastLogin)
			}
			fmt.Print(sb.String())
		},
	})
```

- [ ] **Step 2: Delete the `playerdb`/`pdb` command** (the entire `console.Register(mmokit.Command{ Name: "playerdb", ...})` block).

- [ ] **Step 3: Verify compilation**

- [ ] **Step 4: Commit**

```bash
git add internal/game/commands.go
git commit -m "feat: consolidate players/playerdb into single players command with --all and --live flags"
```

---

### Task 5: Route action commands through `execOnPlayerNode`

**Files:**
- Modify: `internal/game/commands.go`

Update `damage`, `kill`, `kick`, `heal`, `tp`, `give`, `currency`, `spawnnpcs`, `tpto` to use `execOnPlayerNode` instead of `resolvePlayer(gw, ...)`.

For each command that takes a `<target>` that could be a player OR a netID entity:

Pattern: try `execOnPlayerNode` first. If it returns "not found", try `execOnEntityNode` as fallback.

- [ ] **Step 1: Rewrite `damage` command**

Replace the `Fn` body of `damage`:

```go
			targetArg := args[0]
			dmg := float32(amount)
			// Try player first
			result := execOnPlayerNode(coord, allNodes, targetArg, func(gw *GameWorld, sess *mmokit.PlayerSession) string {
				if !gw.C.Health.HasAll(sess.Entity) {
					return "  entity has no health component"
				}
				dealt := gw.ApplyDamage(sess.Entity, dmg, 0)
				h := gw.C.Health.Get(sess.Entity)
				return fmt.Sprintf("  dealt %.0f damage (hp: %.0f/%.0f)", dealt, h.Current, h.Max)
			})
			if strings.Contains(result, "not found") {
				// Try entity by netID
				if netID, err := strconv.ParseUint(targetArg, 10, 32); err == nil {
					result = execOnEntityNode(allNodes, uint32(netID), func(gw *GameWorld, entity ecs.Entity) string {
						if !gw.C.Health.HasAll(entity) {
							return "  entity has no health component"
						}
						dealt := gw.ApplyDamage(entity, dmg, 0)
						h := gw.C.Health.Get(entity)
						return fmt.Sprintf("  dealt %.0f damage (hp: %.0f/%.0f)", dealt, h.Current, h.Max)
					})
				}
			}
			fmt.Println(result)
```

- [ ] **Step 2: Rewrite `kill` command**

Similar pattern — `execOnPlayerNode` first, then `execOnEntityNode` fallback:

```go
			targetArg := args[0]
			result := execOnPlayerNode(coord, allNodes, targetArg, func(gw *GameWorld, sess *mmokit.PlayerSession) string {
				gw.MarkPlayerDeath(sess.Entity, 0)
				return fmt.Sprintf("  killed player %s", targetArg)
			})
			if strings.Contains(result, "not found") {
				if netID, err := strconv.ParseUint(targetArg, 10, 32); err == nil {
					result = execOnEntityNode(allNodes, uint32(netID), func(gw *GameWorld, entity ecs.Entity) string {
						gw.MarkNPCDeath(entity, 0)
						return fmt.Sprintf("  killed entity %s", targetArg)
					})
				}
			}
			fmt.Println(result)
```

- [ ] **Step 3: Rewrite `kick` command**

```go
			playerArg := args[0]
			result := execOnPlayerNode(coord, allNodes, playerArg, func(gw *GameWorld, sess *mmokit.PlayerSession) string {
				connID := sess.ConnID
				username := sess.Username
				gw.Players.Remove(sess)
				gw.ConnMgr.Remove(connID)
				return fmt.Sprintf("  kicked %s (conn %d)", username, connID)
			})
			fmt.Println(result)
```

- [ ] **Step 4: Rewrite `heal` command**

```go
			targetArg := args[0]
			result := execOnPlayerNode(coord, allNodes, targetArg, func(gw *GameWorld, sess *mmokit.PlayerSession) string {
				if gw.C.Health.HasAll(sess.Entity) {
					h := gw.C.Health.Get(sess.Entity)
					h.Current = h.Max
				}
				if gw.C.Shield.HasAll(sess.Entity) {
					s := gw.C.Shield.Get(sess.Entity)
					s.Current = s.Max
				}
				return "  fully healed"
			})
			if strings.Contains(result, "not found") {
				if netID, err := strconv.ParseUint(targetArg, 10, 32); err == nil {
					result = execOnEntityNode(allNodes, uint32(netID), func(gw *GameWorld, entity ecs.Entity) string {
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
				}
			}
			fmt.Println(result)
```

- [ ] **Step 5: Rewrite `tp` command**

The `tp` command resolves a target then sets position. Use `execOnPlayerNode` first, `execOnEntityNode` fallback. Keep the coordinate parsing logic as-is, just change the execution:

```go
				result := execOnPlayerNode(coord, allNodes, targetArg, func(gw *GameWorld, sess *mmokit.PlayerSession) string {
					pos := gw.C.Position.Get(sess.Entity)
					pos.X = fx
					pos.Y = fy
					if explicitCell && gw.C.CellCoord.HasAll(sess.Entity) {
						sec := gw.C.CellCoord.Get(sess.Entity)
						sec.CellX = sx
						sec.CellY = sy
					}
					if gw.C.Velocity.HasAll(sess.Entity) {
						vel := gw.C.Velocity.Get(sess.Entity)
						vel.X = 0
						vel.Y = 0
					}
					if gw.C.MoveTarget.HasAll(sess.Entity) {
						gw.C.MoveTarget.Get(sess.Entity).Active = false
					}
					var dsx, dsy int32
					if gw.C.CellCoord.HasAll(sess.Entity) {
						sec := gw.C.CellCoord.Get(sess.Entity)
						dsx, dsy = sec.CellX, sec.CellY
					}
					return fmt.Sprintf("  teleported to %s", fmtCellPosRaw(dsx, dsy, fx, fy))
				})
				if strings.Contains(result, "not found") {
					if netID, err := strconv.ParseUint(targetArg, 10, 32); err == nil {
						result = execOnEntityNode(allNodes, uint32(netID), func(gw *GameWorld, entity ecs.Entity) string {
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
							return fmt.Sprintf("  teleported to (%.0f, %.0f)", fx, fy)
						})
					}
				}
				fmt.Println(result)
```

- [ ] **Step 6: Rewrite `give` command**

```go
			result := execOnPlayerNode(coord, allNodes, playerArg, func(gw *GameWorld, sess *mmokit.PlayerSession) string {
				if !gw.C.Inventory.HasAll(sess.Entity) {
					return "  player has no inventory"
				}
				inv := gw.C.Inventory.Get(sess.Entity)
				inv.AddItem(itemID, amount)
				return fmt.Sprintf("  gave %s x%.0f to %s", def.Name, amount, sess.Username)
			})
			fmt.Println(result)
```

- [ ] **Step 7: Rewrite `currency` command**

```go
			result := execOnPlayerNode(coord, allNodes, playerArg, func(gw *GameWorld, sess *mmokit.PlayerSession) string {
				pdata := gw.PlayerDB.GetOrCreate(sess.Username)
				pdata.Currencies[curID] = amount
				gw.PlayerDB.MarkDirty(sess.Username)
				sendBankContentsAdmin(gw, sess.ConnID, pdata)
				return fmt.Sprintf("  set currency[%d] = %d for %s", curID, amount, sess.Username)
			})
			fmt.Println(result)
```

- [ ] **Step 8: Rewrite `spawnnpcs` command**

```go
			result := execOnPlayerNode(coord, allNodes, playerArg, func(gw *GameWorld, sess *mmokit.PlayerSession) string {
				if !gw.C.Position.HasAll(sess.Entity) {
					return "  player has no position"
				}
				pos := gw.C.Position.Get(sess.Entity)
				aoiR := gw.Config.AoIRadius
				for i := 0; i < count; i++ {
					angle := rand.Float64() * 2 * math.Pi
					dist := rand.Float32() * aoiR * 0.8
					nx := pos.X + float32(math.Cos(angle))*dist
					ny := pos.Y + float32(math.Sin(angle))*dist
					entity := gw.SpawnNPC(nx, ny)
					if moveFlag {
						if gw.C.Wander.HasAll(entity) {
							w := gw.C.Wander.Get(entity)
							w.Active = true
							w.OriginX = nx
							w.OriginY = ny
							w.Radius = aoiR * 0.5
						}
					}
				}
				return fmt.Sprintf("  spawned %d NPCs near %s", count, sess.Username)
			})
			fmt.Println(result)
```

- [ ] **Step 9: Rewrite `tpto` command**

This teleports one player near another. Both must be resolved. For now, both must be on the same node (cross-node tp deferred):

```go
			result := execOnPlayerNode(coord, allNodes, targetPlayer, func(gw *GameWorld, targetSess *mmokit.PlayerSession) string {
				// Find destination on same node
				destSess := gw.Players.ByUsername(strings.ToLower(destArg))
				var destEntity ecs.Entity
				var destOK bool
				if destSess != nil && gw.ECS.Alive(destSess.Entity) {
					destEntity = destSess.Entity
					destOK = true
				}
				if !destOK {
					// Try entity by netID
					destEntity, destOK = resolveEntity(gw, destArg)
				}
				if !destOK {
					return fmt.Sprintf("  destination %q not found on same node", destArg)
				}
				if !gw.C.Position.HasAll(destEntity) || !gw.C.Position.HasAll(targetSess.Entity) {
					return "  missing position component"
				}
				destPos := gw.C.Position.Get(destEntity)
				offset := float32(3 + rand.Intn(5))
				angle := rand.Float64() * 2 * math.Pi
				pos := gw.C.Position.Get(targetSess.Entity)
				pos.X = destPos.X + float32(math.Cos(angle))*offset
				pos.Y = destPos.Y + float32(math.Sin(angle))*offset
				if gw.C.CellCoord.HasAll(destEntity) && gw.C.CellCoord.HasAll(targetSess.Entity) {
					destSec := gw.C.CellCoord.Get(destEntity)
					sec := gw.C.CellCoord.Get(targetSess.Entity)
					sec.CellX = destSec.CellX
					sec.CellY = destSec.CellY
				}
				if gw.C.Velocity.HasAll(targetSess.Entity) {
					vel := gw.C.Velocity.Get(targetSess.Entity)
					vel.X = 0
					vel.Y = 0
				}
				if gw.C.MoveTarget.HasAll(targetSess.Entity) {
					gw.C.MoveTarget.Get(targetSess.Entity).Active = false
				}
				return fmt.Sprintf("  teleported %s near %s", targetPlayer, destArg)
			})
			fmt.Println(result)
```

- [ ] **Step 10: Remove `resolvePlayer` function**

Delete the `resolvePlayer` function (lines 864-889). It's no longer used — all commands use `execOnPlayerNode` or `execOnEntityNode`.

Keep `resolveEntity` — it's still used as a fallback inside `tpto` and potentially useful.

- [ ] **Step 11: Verify compilation**

- [ ] **Step 12: Commit**

```bash
git add internal/game/commands.go
git commit -m "refactor: route all action commands through execOnPlayerNode/execOnEntityNode"
```

---

### Task 6: Fix tab completions for players

**Files:**
- Modify: `internal/game/commands.go`
- Modify: `internal/game/lifecycle.go:155-173`

Tab completions for player names currently come from one node's game loop via `updatePlayerCompletions`. With coordinator tracking, we can get a global list.

- [ ] **Step 1: Update `playerComplete` in `RegisterCommands`**

Replace the `playerComplete` closure at the top of `RegisterCommands`:

```go
	playerComplete := func(args []string) []string {
		active := coord.ActiveUsers()
		names := make([]string, 0, len(active))
		for name := range active {
			names = append(names, name)
		}
		return names
	}
```

This reads from the coordinator's `activeUsers` map (thread-safe via `ActiveUsers()` snapshot). No game loop involvement.

- [ ] **Step 2: Remove `gw.console` field usage**

In `internal/game/lifecycle.go`, the `updatePlayerCompletions()` function sets completions on `gw.console`. Since completions now come from the coordinator, we can simplify this. However, removing it entirely would break the console's completion system if any other code depends on it.

For now, just make `updatePlayerCompletions` a no-op by removing its body:

Actually, the simpler fix: the `gw.console` field is set in `RegisterCommands`. Since we removed that line, `gw.console` is always nil, so `updatePlayerCompletions` already returns early at line 157 (`if gw.console == nil { return }`). No change needed.

- [ ] **Step 3: Also add DB completions for `--all` use**

Update the `players` command's `Complete` function to include DB usernames for `--all`:

```go
		Complete: func(args []string) []string {
			if len(args) == 0 {
				names := playerComplete(args)
				// Also include flags
				names = append(names, "--all", "--live")
				return names
			}
			return nil
		},
```

- [ ] **Step 4: Verify compilation**

- [ ] **Step 5: Commit**

```bash
git add internal/game/commands.go
git commit -m "feat: use coordinator activeUsers for player tab completions"
```

---

### Task 7: Verify and test

**Files:** None (verification only)

- [ ] **Step 1: Build**

Run: `go vet ./pkg/universe/ && go vet ./pkg/engine/`
Run: `go test ./pkg/universe/ ./pkg/engine/ -count=1`

- [ ] **Step 2: Manual test plan**

Start the server, log in as a player, then verify:
- `players` — shows online player with correct node
- `players --all` — shows offline players too
- `players xennion` — shows detailed info with correct online status
- `players xennion --live` — shows real-time HP/position
- `tp xennion 100 100` — works regardless of which node console is on
- `damage xennion 10` — applies damage on correct node
- `spawnnpcs 5 xennion` — spawns near player on correct node
- `heal xennion` — heals on correct node
- `kill xennion` — kills on correct node

- [ ] **Step 3: Commit any fixes**
