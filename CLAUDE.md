# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Run

```bash
make build          # compile to bin/server
make run            # build + run
make dev            # build + run server & web-pixi vite dev server
make proto          # regenerate protobuf (buf generate)
make clean          # remove bin/
```

The web test client is served at `http://localhost:8080` automatically.

There are no tests in this project.

## Architecture

2D space MMORPG server in Go (`github.com/zenion/mmoserver`). Server-authoritative — the Unity client (and web canvas test client) are dumb renderers. Uses a decoupled engine (`pkg/engine/`) with ECS, WebSocket + UDP transport, and protobuf serialization. Game logic lives in `internal/game/` where `GameWorld` embeds `*engine.Engine`.

### Package Layout

- `cmd/server/` — entry point, wires engine + game + systems
- `pkg/engine/` — generic MMO engine (ECS world, game loop, console, hooks)
- `pkg/net/` — transport interfaces + WebSocket/UDP implementations
- `pkg/persist/` — Store interface + BoltStore + AsyncWriter
- `pkg/spatial/` — spatial hash grid
- `pkg/logger/` — category-based debug logging
- `internal/game/` — GameWorld, entity files, lifecycle, commands, config, player DB
- `internal/component/` — ECS components
- `internal/system/` — 12 game systems (executed in registration order)

### Game Loop (20Hz fixed timestep in `pkg/engine/loop.go`)

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

Input → TargetLock → ShipControl → Mining → Economy → Ability → StatusEffect → Physics → Lifetime → Spatial → Damage → Network

Each system implements `System.Update(dt float32)`. Systems capture `*game.GameWorld` at construction time.

### ECS (Ark v0.7.1)

- `Map1[A]` through `Map12[...]` for entity creation and component access
- `Filter2[A,B]` etc. for queries; **always call `query.Close()` if breaking early**
- Use `HasAll()` not `Has()` to check components
- `world.Alive(entity)` before accessing removed entities
- Never spawn/remove entities during query iteration — collect in a slice, process after

### Entity Files

Each entity type has its own file (`internal/game/entity_*.go`) containing:

- A typed mappers struct (e.g., `shipMappers`)
- An `initXxxEntity(gw)` function that creates mappers and registers with `EntityRegistry`
- Spawn methods on `GameWorld` (e.g., `SpawnPlayer`, `SpawnAsteroid`)

Current entity types: ship, asteroid, lootcrate, npc, station.

`EntityRegistry` (`internal/game/registry.go`) maps entity names to definitions for admin commands.

### Networking

- WebSocket via `github.com/coder/websocket`, protobuf binary frames
- Area of Interest culling: only entities within `AoIRadius` (3000 units) are sent
- `NetworkSystem` tracks per-player visibility for proper remove notifications
- Entity state is normalized (health/shield sent as 0-1 fractions)

### Proto Codegen

Source of truth: `proto/game.proto`. Run `buf generate` (or `make proto`) to regenerate:

- `gen/go/game.pb.go` — Go (package `gamepb`, import as `gamepb "github.com/zenion/mmoserver/gen/go"`)
- `gen/csharp/Game.cs` — Unity client
- `gen/es/game_pb.js` — Web test client (ES modules via `@bufbuild/protobuf`)

### Thread Safety

The ECS world is **not thread-safe**. All ECS reads/writes must happen on the game loop goroutine. The console uses `engine.ExecOnGameLoop()` to schedule closures that run on the game tick. Admin commands capture `*GameWorld` in closures.

### Key Mappings

- `GameWorld.PlayerEntities`: connID → ECS entity
- `GameWorld.ConnToUsername`: connID → username
- `GameWorld.NetIDToEntity`: netID → entity (rebuilt each tick by SpatialSystem)
- `GameWorld.PlayerDB`: PlayerRepo — memory-first with async persistence to BoltDB

### Persistence

Memory-first with async writes: `PlayerRepo` (in-memory map) is authoritative. `MarkDirty()` flags changed players; `FlushDirty()` runs every 300 ticks (~15s) via `AsyncWriter` to BoltDB (`data/gameserver.db`).

### Config

All tunable game parameters are in `internal/game/config.go`. The `GameConfig` struct supports reflection-based get/set for runtime tweaking via the server console (`config`, `set` commands). Values copied into components at spawn time (e.g. `ShieldRegenRate`) only affect newly spawned entities.

### Web Client

`web-pixi/` — TypeScript/PixiJS game client built with Vite. Run via `make dev` during development. Uses protobuf for server communication. Interpolates between 20Hz server ticks for smooth rendering.

### Usernames

Usernames are forced lowercase everywhere. Duplicate usernames are rejected at login with a `LoginRejectedMsg`.
