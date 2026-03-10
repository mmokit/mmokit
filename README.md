# gameserver

A server-authoritative 2D space MMORPG server written in Go. The server owns all game state and physics — clients (Unity and a web canvas test client) are dumb renderers that send input and display the world.

## Architecture

- **ECS** — Entity Component System via [Ark](https://github.com/mlange-42/ark) v0.7.1
- **20Hz fixed timestep** game loop with 10 ordered systems: Input → ShipControl → Mining → Economy → Combat → Physics → Lifetime → Spatial → Damage → Network
- **WebSocket + UDP transport** with protobuf binary serialization
- **Area of Interest culling** — only nearby entities (within 2000 units) are sent to each player
- **Memory-first persistence** with async writes to BBolt (Albion Online pattern)

### Package layout

```text
pkg/engine/      Generic MMO engine (ECS, game loop, console, hooks)
pkg/net/         Transport interface, WebSocket + UDP implementations
pkg/persist/     Store interface, BoltStore, async writer
pkg/spatial/     Spatial hash grid
pkg/logger/      Category-based debug logger
internal/game/   Game-specific logic (world, spawning, lifecycle, commands, config)
internal/component/  ECS components
internal/system/     Game systems
proto/           Protobuf schema (source of truth)
gen/             Generated code (Go, C#, JS)
web/             Browser-based canvas test client
cmd/server/      Server entrypoint
```

## Prerequisites

- Go 1.25+
- [Buf](https://buf.build/) (for protobuf codegen, optional)

## Build & Run

```bash
make build    # compile to bin/server
make run      # build + run
make proto    # regenerate protobuf (buf generate)
make clean    # remove bin/
```

The web test client is served at `http://localhost:8080` automatically when the server starts.

## Proto Codegen

The protobuf schema lives in `proto/game.proto`. Running `make proto` (or `buf generate`) produces:

- `gen/go/game.pb.go` — Go (package `gamepb`)
- `gen/csharp/Game.cs` — Unity client
- `gen/es/game_pb.js` — Web test client (ES modules)
