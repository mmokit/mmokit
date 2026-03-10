# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Run

```bash
make build          # compile to bin/server
make run            # build + run
make proto          # regenerate protobuf (buf generate)
make clean          # remove bin/
```

The web test client is served at `http://localhost:8080` automatically.

There are no tests in this project.

## Architecture

2D space MMORPG server in Go. Server-authoritative — the Unity client (and web canvas test client) are dumb renderers. The server uses an ECS architecture with WebSocket transport and protobuf serialization.

### Game Loop (20Hz fixed timestep in `internal/world/tick.go`)

Each tick runs in this order:

1. Process connect/disconnect events
2. Drain admin commands from console
3. Process login requests
4. Run all systems (in registration order)
5. Send death notifications
6. Flush entity removals
7. Spawn loot crates from deaths
8. Process respawn requests

### Systems (executed in order, defined in `cmd/server/main.go`)

Input → ShipControl → Mining → Economy → Combat → Physics → Lifetime → Spatial → Damage → Network

Each system implements `System.Update(w *World, dt float32)`.

### ECS (Ark v0.7.1)

- `Map1[A]` through `Map12[...]` for entity creation and component access
- `Filter2[A,B]` etc. for queries; **always call `query.Close()` if breaking early**
- Use `HasAll()` not `Has()` to check components
- `world.Alive(entity)` before accessing removed entities
- Never spawn/remove entities during query iteration — collect in a slice, process after

### Networking

- WebSocket via `github.com/coder/websocket`, protobuf binary frames
- Area of Interest culling: only entities within `AoIRadius` (2000 units) are sent
- `NetworkSystem` tracks per-player visibility for proper remove notifications
- Entity state is normalized (health/shield sent as 0-1 fractions)

### Proto Codegen

Source of truth: `proto/game.proto`. Run `buf generate` (or `make proto`) to regenerate:

- `gen/go/game.pb.go` — Go (package `gamepb`, import as `gamepb "github.com/zenion/gameserver/gen/go"`)
- `gen/csharp/Game.cs` — Unity client
- `gen/es/game_pb.js` — Web test client (ES modules via `@bufbuild/protobuf`)

### Thread Safety

The ECS world is **not thread-safe**. The console runs on the main goroutine while the game loop runs on its own goroutine. Admin commands from the console are sent via `World.PendingAdminCmds` channel and drained on the game loop tick. All ECS reads/writes must happen on the game loop goroutine.

### Key Mappings

- `World.PlayerEntities`: connID → ECS entity
- `World.ConnToUsername`: connID → username
- `World.NetIDToEntity`: netID → entity (rebuilt each tick by SpatialSystem)
- `World.PlayerDB`: username → persistent PlayerData (flux, position, inventory)

### Config

All tunable game parameters are in `internal/config/config.go`. The `Config` struct supports reflection-based get/set for runtime tweaking via the server console (`config`, `set` commands). Values copied into components at spawn time (e.g. `ShieldRegenRate`) only affect newly spawned entities.

### Web Client

`web/client.js` (~1600 lines) — full canvas-based game client. Uses ES module imports from esm.sh for protobuf. Interpolates between 20Hz server ticks for smooth rendering.

### Usernames

Usernames are forced lowercase everywhere. Duplicate usernames are rejected at login with a `LoginRejectedMsg`.
