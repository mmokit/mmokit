# Documentation

This directory separates current documentation from historical design material. For behavior and API details, current source, tests, and `justfile` recipes remain authoritative.

## Start here

- [Project README](../README.md) — project overview, prerequisites, and common commands
- [Roadmap and vision](roadmap.md) — what MMOKIT is for, where it is going, and what it will not do
- [Current architecture](architecture.md) — process roles, cell runtime, networking, replication, and persistence
- [Editable architecture diagram](../architecture.excalidraw) — Excalidraw overview of runtime roles, per-cell execution, and SDK generation
- [MMOKIT guide](../pkg/mmokit/README.md) — game-facing API and composition model
- [Simple example](../examples/simple/README.md) — smallest runnable game
- [Distributed example](../examples/4node-basic/README.md) — roles, meshing, generated SDK, services, and WASM
- [Space game](../examples/space/README.md) — the reference game's composition root and layout

`roadmap.md` describes where the project is going and holds all active tracking; `architecture.md` describes what exists today. Neither restates the other.

## Maintained reference

- [Engine internals](../pkg/engine/README.md)
- [Networking](../pkg/net/README.md)
- [Spatial index](../pkg/spatial/README.md)
- [Logging](../pkg/logger/README.md)
- [Space-game internals](../examples/space/internal/game/README.md)
- [Space-game components](../examples/space/internal/component/README.md)

## Historical design records

`docs/superpowers/` contains dated specs, plans, and audits owned by the Superpowers/Claude workflow. They explain how features were designed at a point in time, but they are not current API documentation and are intentionally left unchanged during normal documentation maintenance.
