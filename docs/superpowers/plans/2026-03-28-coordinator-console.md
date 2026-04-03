# Coordinator-Managed Console Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the interactive console a default part of the Coordinator lifecycle — games just provide game-specific commands, not console boilerplate.

**Architecture:** Add ConsoleOpts and lifecycle options to Coordinator. Start() becomes blocking — it creates the console, registers node builtins automatically, applies game-provided config/entity builtins, runs the console (or waits for signal in headless mode), then shuts down.

**Tech Stack:** Go, pkg/universe, pkg/engine

**Spec:** `docs/superpowers/specs/2026-03-28-coordinator-console-design.md`

---

## File Map

```
pkg/universe/coordinator.go   # Add console fields, options, update Start
pkg/mmokit/mmokit.go          # Export new types
cmd/server/main.go            # Simplify: use WithConsole + WithOnConsoleReady
examples/slither/main.go      # Remove signal handler, Start blocks
examples/4node-basic/main.go  # Remove signal handler, Start blocks
```

---

### Task 1: Add Console Options to Coordinator

**Files:**
- Modify: `pkg/universe/coordinator.go`

- [ ] **Step 1: Add ConsoleOpts type and option functions**

Add after the existing `WithAoIRadius` function (after line 56):

```go
// ConsoleOpts provides game-specific console configuration.
// All fields are optional — omit what your game doesn't need.
type ConsoleOpts struct {
	Config        engine.Configurable   // enables "config list/get/set"
	ConfigSave    func() error          // enables "config save"
	ConfigReset   func()                // enables "config reset"
	Entities      *engine.EntityOpts    // enables "entity summary/list/get/remove"
	Registry      *engine.EntityRegistry // enables "entity add"
}

// WithConsole provides game-specific console configuration.
// The console is created by default — this adds game builtins.
func WithConsole(opts ConsoleOpts) CoordinatorOption {
	return func(o *coordOpts) { o.console = &opts }
}

// WithHeadless disables the interactive console entirely.
// Use for tests, containers, or headless deployments.
// Takes precedence over WithConsole if both are provided.
func WithHeadless() CoordinatorOption {
	return func(o *coordOpts) { o.headless = true }
}

// WithOnConsoleReady registers a callback invoked after the console
// is created and builtins are registered, but before console.Run().
// Use to register game-specific commands.
func WithOnConsoleReady(fn func(c *engine.Console)) CoordinatorOption {
	return func(o *coordOpts) { o.onConsoleReady = fn }
}
```

- [ ] **Step 2: Update coordOpts struct**

Replace the existing `coordOpts` struct (lines 37-41) with:

```go
type coordOpts struct {
	connMgr        *net.ConnManager
	log            *logger.Logger
	aoiRadius      float32
	console        *ConsoleOpts   // nil = console with no game builtins
	headless       bool           // true = no console at all
	onConsoleReady func(c *engine.Console)
}
```

- [ ] **Step 3: Add console field to Coordinator struct**

Add to the Coordinator struct (after the `Log` field on line 65):

```go
	console  *engine.Console // nil if headless
```

- [ ] **Step 4: Add Console accessor**

Add after the `DefaultNode` method:

```go
// Console returns the Coordinator's interactive console, or nil if headless.
func (c *Coordinator) Console() *engine.Console { return c.console }
```

- [ ] **Step 5: Rewrite Start to block and manage console lifecycle**

Replace the existing `Start` method (lines 221-229) with:

```go
// Start launches all node goroutines, the event router, and — unless headless —
// the interactive console. Start blocks until the context is cancelled or the
// user types "quit" in the console. On return all nodes have been shut down.
func (c *Coordinator) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go c.routeEvents(ctx)

	for _, node := range c.Nodes {
		go node.Run(ctx)
	}
	log.Printf("coordinator: all %d nodes started", len(c.Nodes))

	// Install signal handler.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigCh:
			log.Println("shutting down...")
			cancel()
		case <-ctx.Done():
		}
		signal.Stop(sigCh)
	}()

	if !c.opts.headless {
		c.startConsole(ctx)
	} else {
		<-ctx.Done()
	}

	c.Shutdown()
}

// startConsole creates the console, registers builtins, and runs it (blocking).
func (c *Coordinator) startConsole(ctx context.Context) {
	defaultNode := c.DefaultNode()
	c.console = engine.NewConsole(defaultNode.Engine, c.Log)

	// Auto-wire node builtins from coordinator's node map.
	nodeRefs := c.buildNodeRefs()

	builtinOpts := engine.BuiltinOpts{
		Nodes: nodeRefs,
	}

	// Merge game-provided builtins if WithConsole was called.
	if c.opts.console != nil {
		co := c.opts.console
		builtinOpts.Config = co.Config
		builtinOpts.ConfigSave = co.ConfigSave
		builtinOpts.ConfigReset = co.ConfigReset
		builtinOpts.Entities = co.Entities
		builtinOpts.Registry = co.Registry
	}

	c.console.RegisterBuiltins(builtinOpts)

	// Let game register custom commands.
	if c.opts.onConsoleReady != nil {
		c.opts.onConsoleReady(c.console)
	}

	c.console.Run(ctx)
}

// buildNodeRefs creates NodeRef entries from the coordinator's node map.
func (c *Coordinator) buildNodeRefs() []engine.NodeRef {
	refs := make([]engine.NodeRef, 0, len(c.Nodes))
	for _, node := range c.Nodes {
		n := node
		refs = append(refs, engine.NodeRef{
			ID: n.ID,
			Exec: func(fn func() string) string {
				result := make(chan string, 1)
				n.Engine.PendingAdminCmds <- func() { result <- fn() }
				select {
				case r := <-result:
					return r
				case <-time.After(5 * time.Second):
					return "  node not responding (timeout)\n"
				}
			},
			Metrics: n.Metrics,
		})
	}
	return refs
}
```

- [ ] **Step 6: Store opts on Coordinator**

In `NewCoordinator`, the `coordOpts` need to be stored on the Coordinator so `Start` can access them. Add an `opts` field to the Coordinator struct:

```go
	opts coordOpts
```

And at the end of `NewCoordinator` (before the return), add:

```go
	c.opts = o
```

- [ ] **Step 7: Add required imports**

Add to the import block in coordinator.go:

```go
	"os"
	"os/signal"
	"syscall"
	"time"
```

- [ ] **Step 8: Verify compilation**

Run: `go vet ./pkg/universe/...`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add pkg/universe/coordinator.go
git commit -m "feat(universe): add console lifecycle management to Coordinator

Console is created by default. Games provide builtins via WithConsole(opts).
Node builtins auto-wired. WithHeadless() to disable. Start() now blocks.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Export New Types in mmokit Facade

**Files:**
- Modify: `pkg/mmokit/mmokit.go`

- [ ] **Step 1: Add ConsoleOpts type alias**

In the Universe section (around line 106, after the existing type aliases), add:

```go
type ConsoleOpts = universe.ConsoleOpts
```

- [ ] **Step 2: Add new option function exports**

In the Constants/var section (around line 296, where WithConnManager etc. are), add:

```go
	WithConsole        = universe.WithConsole
	WithHeadless       = universe.WithHeadless
	WithOnConsoleReady = universe.WithOnConsoleReady
```

- [ ] **Step 3: Verify compilation**

Run: `go vet ./pkg/mmokit/...`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add pkg/mmokit/mmokit.go
git commit -m "feat(mmokit): export ConsoleOpts, WithConsole, WithHeadless, WithOnConsoleReady

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Update Main Game Server

**Files:**
- Modify: `cmd/server/main.go`

The main game has the most complex console setup — it uses WithOnConsoleReady for game.RegisterCommands.

- [ ] **Step 1: Remove manual console setup and signal handler**

Replace lines 203-273 (from `ctx, cancel := ...` to end) with:

```go
	ctx := context.Background()

	// Start all node game loops
	coordinator.Start(ctx)

	// Shutdown sequence (runs after console quit or signal)
	writer.Flush()
	marketWriter.Flush()
	store.Close()
	marketStore.Close()
	log.Println("shutdown complete")
```

Wait — that loses the console setup. We need to move the console config into the coordinator options. Replace the `coordinator := mmokit.NewCoordinator(...)` call and everything after it.

Actually, let me be more precise. Replace from line 108 (`coordinator := mmokit.NewCoordinator(...)`) through the end of main with:

```go
	coordinator := mmokit.NewCoordinator(grid, platformCfg, factory,
		mmokit.WithConnManager(connMgr), mmokit.WithLogger(gameLog),
		mmokit.WithConsole(mmokit.ConsoleOpts{
			Config:      &gameCfg,
			ConfigSave:  func() error { return game.SaveConfig(store, &gameCfg) },
			ConfigReset: func() { gameCfg = game.DefaultGameConfig() },
			Registry:    nil, // set via OnConsoleReady after worlds are created
			Entities:    nil, // set via OnConsoleReady after worlds are created
		}),
		mmokit.WithOnConsoleReady(func(console *mmokit.Console) {
			// Build node info list for cross-node admin commands
			var allNodes []game.NodeInfo
			for _, node := range coordinator.Nodes {
				allNodes = append(allNodes, game.NodeInfo{
					ID:    node.ID,
					Cell:  node.Cell,
					World: internaluniverse.UnwrapGameWorld(node.World),
				})
			}
			defaultWorld := internaluniverse.UnwrapGameWorld(coordinator.DefaultNode().World)
			game.RegisterCommands(console, defaultWorld, store, allNodes)
		}),
	)
	game.InitDropTables()

	opRouter := mmokit.NewOpRouter(connMgr, playerSessions, 2,
		func(raw []byte) (mmokit.ParsedRequest, error) {
			var req enginepb.OperationRequest
			if err := proto.Unmarshal(raw, &req); err != nil {
				return mmokit.ParsedRequest{}, err
			}
			return mmokit.ParsedRequest{Code: req.Code, RequestID: req.RequestId, Data: req.Data}, nil
		},
		mmokit.MakeOpResponse,
	)

	// Marketplace service (same as before — lines 124-201 unchanged)
	marketStore, err := mmokit.OpenBolt("data/marketplace.db")
	if err != nil {
		log.Fatalf("failed to open marketplace database: %v", err)
	}
	marketWriter := mmokit.NewAsyncWriter(marketStore, 4096)
	marketWriter.Start()

	marketCfg := mmokit.OrderBookConfig{
		TaxPct:      gameCfg.MarketTaxPct,
		OrderExpiry: int64(gameCfg.MarketOrderExpiry * 3600),
		MinPrice:    gameCfg.MarketMinPrice,
		MaxOrders:   gameCfg.MarketMaxOrders,
	}
	obSvc := mmokit.NewOrderBookService(marketCfg)
	marketSvc := marketplace.NewSettlement(
		obSvc,
		marketplace.BankOps{
			GetBankBalance: playerDB.GetBankBalance,
			ModifyBank:     playerDB.ModifyBank,
			GetCurrency:    playerDB.GetCurrency,
			ModifyCurrency: playerDB.ModifyCurrency,
			MarkDirty:      playerDB.MarkDirty,
			SendBankUpdate: func(username string) {
				connID := opRouter.ConnIDForUsername(username)
				if connID == 0 {
					return
				}
				pdata := playerDB.Get(username)
				if pdata == nil {
					return
				}
				var items []*gamepb.InventoryItem
				for id, qty := range pdata.Bank {
					if qty > 0 {
						items = append(items, &gamepb.InventoryItem{ItemId: id, Quantity: qty})
					}
				}
				var cargoItems []*gamepb.InventoryItem
				for id, qty := range pdata.Cargo {
					if qty > 0 {
						cargoItems = append(cargoItems, &gamepb.InventoryItem{ItemId: id, Quantity: qty})
					}
				}
				var currencies []*gamepb.CurrencyBalance
				for curID, bal := range pdata.Currencies {
					if bal != 0 {
						currencies = append(currencies, &gamepb.CurrencyBalance{CurrencyId: curID, Balance: bal})
					}
				}
				frame := mmokit.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_BANK_CONTENTS), &gamepb.BankContentsMsg{
					Items:        items,
					TotalMass:    pdata.BankTotalMass(),
					MaxMass:      gameCfg.BankMaxMass,
					CargoItems:   cargoItems,
					CargoMass:    pdata.CargoTotalMass(),
					MaxCargoMass: gameCfg.MaxCargo,
					Currencies:   currencies,
				})
				if frame != nil {
					connMgr.SendReliable(connID, frame)
				}
			},
		},
		marketCfg,
		gameCfg.SettlementCurrencyID,
		gameLog,
		marketWriter,
		func(username string, code uint32, payload []byte) {
			connID := opRouter.ConnIDForUsername(username)
			if connID != 0 {
				opRouter.SendPush(connID, code, payload)
			}
		},
	)
	if err := marketSvc.LoadAll(marketStore); err != nil {
		log.Fatalf("failed to load marketplace data: %v", err)
	}
	marketplace.RegisterHandlers(opRouter, marketSvc, 1)

	ctx := context.Background()

	// Start operation router
	go opRouter.Run(ctx)

	// Periodic marketplace order expiry
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				marketSvc.ExpireOrders()
			}
		}
	}()

	// Start WebSocket server
	go func() {
		if err := connMgr.ListenAndServe(ctx, platformCfg.ListenAddr); err != nil {
			log.Printf("websocket server stopped: %v", err)
		}
	}()

	// Start UDP server
	udpServer, err := mmokit.NewUDPServer(platformCfg.UDPAddr, connMgr)
	if err != nil {
		log.Fatalf("failed to start UDP server: %v", err)
	}
	log.Printf("udp server listening on %s", platformCfg.UDPAddr)
	go udpServer.Run(ctx)

	// Blocks: runs console + handles signals + shuts down nodes
	coordinator.Start(ctx)

	// Post-shutdown cleanup
	writer.Flush()
	marketWriter.Flush()
	store.Close()
	marketStore.Close()
	log.Println("shutdown complete")
```

- [ ] **Step 2: Remove unused imports**

Remove from imports:
```go
	"os/signal"
	"syscall"
```

Keep `"os"` (used for MkdirAll) and `"context"`.

- [ ] **Step 3: Note about game.RegisterCommands**

The game's `RegisterCommands` currently calls `console.RegisterBuiltins(opts)` internally with `Nodes: buildNodeRefs(allNodes)`. Since the Coordinator now auto-registers node builtins, this will double-register the node group. The fix is to remove the `Nodes` field from `RegisterCommands`'s `RegisterBuiltins` call in `internal/game/commands.go`. See Task 4.

- [ ] **Step 4: Verify compilation**

Run: `go vet ./cmd/server/...`
Expected: may fail until Task 4 fixes the double-registration

- [ ] **Step 5: Commit**

```bash
git add cmd/server/main.go
git commit -m "refactor(server): use Coordinator-managed console

Remove manual console creation, signal handling, and shutdown.
Coordinator.Start() now handles the full lifecycle.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Remove Node Builtin from game.RegisterCommands

**Files:**
- Modify: `internal/game/commands.go`

Since the Coordinator now auto-registers the `node` command group, the game layer should stop passing `Nodes` to `RegisterBuiltins`. The game also currently passes `Config` and `Entities` — these are now handled by `WithConsole(ConsoleOpts{...})` at the Coordinator level. So `RegisterCommands` should only register game-specific commands, not call `RegisterBuiltins` at all.

- [ ] **Step 1: Remove RegisterBuiltins call from RegisterCommands**

In `internal/game/commands.go`, remove lines 152-160 (the `console.RegisterBuiltins(...)` call). The function should go straight to registering game-specific commands.

Also remove the `buildNodeRefs` function (lines 120-142) since it's no longer needed.

- [ ] **Step 2: Update cmd/server/main.go WithConsole to include Config, Entities, Registry**

The `WithConsole(ConsoleOpts{...})` in main.go needs to include the EntityOpts and Registry that were previously passed via `RegisterCommands`. Update the WithOnConsoleReady callback to build these from the default world:

In the `WithOnConsoleReady` callback, after building `allNodes` and calling `game.RegisterCommands`, also do:

```go
			// Register config/entity builtins that need game world access
			defaultWorld := internaluniverse.UnwrapGameWorld(coordinator.DefaultNode().World)
			console.RegisterBuiltins(mmokit.BuiltinOpts{
				Config:      &defaultWorld.Config,
				ConfigSave:  func() error { return game.SaveConfig(store, &defaultWorld.Config) },
				ConfigReset: func() { defaultWorld.Config = game.DefaultGameConfig() },
				Registry:    defaultWorld.Registry,
				Entities:    game.BuildEntityOpts(defaultWorld),
			})
```

Wait — but the Coordinator already calls `RegisterBuiltins` with the `ConsoleOpts` in `startConsole`. If we also call it in `OnConsoleReady`, that's fine — `RegisterBuiltins` just adds command groups. But the Config/Entities from `ConsoleOpts` would need to be nil, and we'd set them in `OnConsoleReady` instead.

Actually, the cleanest approach: remove `Config`/`Entities` from the `WithConsole` call in main.go, and set them in `WithOnConsoleReady`. This way all game-specific console wiring happens in one place.

So main.go's coordinator creation simplifies to just:

```go
	coordinator := mmokit.NewCoordinator(grid, platformCfg, factory,
		mmokit.WithConnManager(connMgr), mmokit.WithLogger(gameLog),
		mmokit.WithOnConsoleReady(func(console *mmokit.Console) {
			var allNodes []game.NodeInfo
			for _, node := range coordinator.Nodes {
				allNodes = append(allNodes, game.NodeInfo{
					ID:    node.ID,
					Cell:  node.Cell,
					World: internaluniverse.UnwrapGameWorld(node.World),
				})
			}
			defaultWorld := internaluniverse.UnwrapGameWorld(coordinator.DefaultNode().World)

			// Register game builtins (config, entity)
			console.RegisterBuiltins(mmokit.BuiltinOpts{
				Config:      &defaultWorld.Config,
				ConfigSave:  func() error { return game.SaveConfig(store, &defaultWorld.Config) },
				ConfigReset: func() { defaultWorld.Config = game.DefaultGameConfig() },
				Registry:    defaultWorld.Registry,
				Entities:    game.BuildEntityOpts(defaultWorld),
			})

			// Register game-specific commands (players, damage, etc.)
			game.RegisterCommands(console, defaultWorld, store, allNodes)
		}),
	)
```

This means `WithConsole` isn't even needed for the main game — `WithOnConsoleReady` alone is sufficient. The `ConsoleOpts` is useful for games that have simple config/entities but no custom commands.

- [ ] **Step 3: Export BuildEntityOpts from game package**

The `buildEntityOpts` function in `internal/game/commands.go` is currently unexported. Rename it to `BuildEntityOpts` so main.go can call it.

- [ ] **Step 4: Verify compilation**

Run: `go vet ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/game/commands.go cmd/server/main.go
git commit -m "refactor(game): move builtin registration to WithOnConsoleReady

Game's RegisterCommands no longer calls RegisterBuiltins.
Node builtins auto-wired by Coordinator. Config/entity builtins
registered in WithOnConsoleReady callback.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Update Slither Example

**Files:**
- Modify: `examples/slither/main.go`

- [ ] **Step 1: Remove signal handler and manual shutdown**

Replace lines 65-113 (from `ctx, cancel := ...` to end of main) with:

```go
	ctx := context.Background()

	// Spawn initial food and bots on each node (must happen before Start blocks)
	// Use a WithOnConsoleReady-style callback, or spawn before Start.
	// Since coord.Start now blocks, we need to spawn before calling it.
	// But nodes aren't running yet... we need to queue the spawns.

	// HTTP server: WebSocket + static files
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", cm.HandleWebSocket)

	webDir := "web/dist"
	if _, err := os.Stat(webDir); os.IsNotExist(err) {
		webDir = "web"
	}
	mux.Handle("/", http.FileServer(http.Dir(webDir)))

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("slither server starting on http://localhost%s", addr)
	log.Printf("grid: %dx%d nodes, %d bots per node, %d food per node",
		*gridSize, *gridSize, cfg.BotsPerNode, cfg.FoodPerNode)

	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("FATAL: http server: %v (is port %d already in use?)", err, *port)
			os.Exit(1)
		}
	}()

	// Blocks: runs console + handles signals + shuts down nodes
	coord.Start(ctx)
```

Wait — there's a problem. The slither example spawns initial food/bots via `PendingAdminCmds` after `coord.Start()`. But now Start blocks. The spawns need to happen between node creation and Start, or be queued to execute once nodes are running.

The cleanest fix: use `WithOnConsoleReady` to spawn initial entities, since by that point nodes are running.

Actually, looking at the code more carefully — `PendingAdminCmds` is a buffered channel. The nodes drain it each tick. If we queue the commands before Start, they'll sit in the channel buffer and be processed on the first tick. The buffer is `make(chan func(), ...)` — let me check the size.

From `engine.go`: `PendingAdminCmds: make(chan func(), 16)`. Buffer size 16. We're sending one command per node. For a 2x2 grid that's 4 commands, well within buffer. So queuing before Start works.

Revised approach: keep the spawn code before `coord.Start(ctx)`, just remove the signal handler.

```go
	ctx := context.Background()

	// Queue initial food and bot spawns on each node.
	// PendingAdminCmds is buffered, so these will be processed on the first tick.
	for _, node := range coord.Nodes {
		n := node
		n.Engine.PendingAdminCmds <- func() {
			gw := n.World.(*SlitherWorld)
			gw.SpawnInitialFood()
			cellSize := mmokit.CellSize()
			for i := 0; i < cfg.BotsPerNode; i++ {
				x := rand.Float32()*cellSize*0.6 + cellSize*0.2
				y := rand.Float32()*cellSize*0.6 + cellSize*0.2
				gw.SpawnBotSnake(x, y)
			}
		}
	}

	// HTTP server: WebSocket + static files
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", cm.HandleWebSocket)

	webDir := "web/dist"
	if _, err := os.Stat(webDir); os.IsNotExist(err) {
		webDir = "web"
	}
	mux.Handle("/", http.FileServer(http.Dir(webDir)))

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("slither server starting on http://localhost%s", addr)
	log.Printf("grid: %dx%d nodes, %d bots per node, %d food per node",
		*gridSize, *gridSize, cfg.BotsPerNode, cfg.FoodPerNode)

	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("FATAL: http server: %v (is port %d already in use?)", err, *port)
			os.Exit(1)
		}
	}()

	// Blocks: runs console + handles signals + shuts down nodes
	coord.Start(ctx)
```

- [ ] **Step 2: Remove unused imports**

Remove:
```go
	"os/signal"
	"syscall"
```

Remove `"context"` (we use `context.Background()` — keep it). Actually `context` is still needed. Just remove signal/syscall.

- [ ] **Step 3: Verify compilation**

Run: `go vet ./examples/slither/...`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add examples/slither/main.go
git commit -m "refactor(slither): use Coordinator-managed console and lifecycle

Remove manual signal handling. Coordinator.Start() blocks and
handles console + signals + shutdown.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Update 4node-basic Example

**Files:**
- Modify: `examples/4node-basic/main.go`

- [ ] **Step 1: Simplify main.go**

Replace the entire file with:

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

func main() {
	port := flag.Int("port", 8080, "HTTP server port")
	flag.Parse()

	mmokit.SetCellSize(GridCellSize)

	cm := mmokit.NewConnManager()
	logger := mmokit.NewLogger()

	coord := mmokit.NewCoordinator(
		mmokit.GridConfig{
			MinSX: GridMinSX, MaxSX: GridMaxSX,
			MinSY: GridMinSY, MaxSY: GridMaxSY,
		},
		mmokit.EngineConfig{TickRate: TickRate},
		func(base *mmokit.WorldBase) (mmokit.GameWorld, []mmokit.System) {
			gw := NewBasicWorld(base)

			systems := []mmokit.System{
				registerInputHandlers(base.Engine(), gw),
				NewMovementSystem(gw.ECSWorld()),
				mmokit.NewPhysicsSystem(gw.ECSWorld()),
				NewSpatialSystem(gw),
				NewNetworkSystem(gw),
			}
			return gw, systems
		},
		mmokit.WithConnManager(cm),
		mmokit.WithLogger(logger),
		mmokit.WithAoIRadius(AoIRadius),
	)

	gridW := GridMaxSX - GridMinSX + 1
	gridH := GridMaxSY - GridMinSY + 1

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", cm.HandleWebSocket)
	mux.Handle("/", http.FileServer(http.Dir("web")))

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("4node-basic starting on http://localhost%s", addr)
	log.Printf("grid: %dx%d nodes, cell size: %.0f, AoI: %.0f", gridW, gridH, GridCellSize, AoIRadius)

	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("FATAL: http server: %v", err)
			os.Exit(1)
		}
	}()

	coord.Start(context.Background())
}
```

- [ ] **Step 2: Verify compilation**

Run: `go vet ./examples/4node-basic/...`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add examples/4node-basic/main.go
git commit -m "refactor(4node-basic): use Coordinator-managed console and lifecycle

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Full Verification

- [ ] **Step 1: Compile everything**

Run: `go vet ./...`
Expected: PASS

- [ ] **Step 2: Test 4node-basic starts with console**

Run: `cd examples/4node-basic && timeout 3 go run . -port 8084 2>&1`
Expected: Server starts, shows "4node-basic starting", console prompt appears, clean shutdown on timeout

- [ ] **Step 3: Test slither starts with console**

Run: `cd examples/slither && timeout 3 go run . -port 8085 2>&1`
Expected: Server starts, shows "slither server starting", console prompt appears, clean shutdown

- [ ] **Step 4: Test main game starts with console**

Run: `timeout 3 go run ./cmd/server 2>&1`
Expected: Server starts with console, builtins work (help shows node, config, entity groups)

- [ ] **Step 5: Fix any issues found**

- [ ] **Step 6: Final commit if needed**

```bash
git add -A
git commit -m "fix: address issues found during console verification

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```
