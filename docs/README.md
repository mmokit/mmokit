# Documentation

This directory separates current documentation from historical design material. For behavior and API details, current source, tests, and `justfile` recipes remain authoritative.

## Start here

- [Project README](../README.md) — project overview, prerequisites, and common commands
- [Current architecture](architecture.md) — process roles, cell runtime, networking, replication, and persistence
- [Editable architecture diagram](../architecture.excalidraw) — Excalidraw overview of runtime roles, per-cell execution, and SDK generation
- [MMOKIT guide](../pkg/mmokit/README.md) — game-facing API and composition model
- [Simple example](../examples/simple/README.md) — smallest runnable game
- [Distributed example](../examples/4node-basic/README.md) — roles, meshing, generated SDK, services, and WASM
- [Space server composition](../cmd/server/README.md) — production game entry point

## Maintained reference

- [Engine internals](../pkg/engine/README.md)
- [Networking](../pkg/net/README.md)
- [Spatial index](../pkg/spatial/README.md)
- [Logging](../pkg/logger/README.md)
- [Space-game internals](../internal/game/README.md)
- [Space-game components](../internal/component/README.md)

## Active tracking

- [Core engine improvement roadmap](roadmaps/core-engine-improvements.md)

## Historical design records

[`docs/planning/README.md`](planning/README.md) explains which former summary
documents were retired and where active tracking now belongs.

`docs/superpowers/` contains dated specs, plans, and audits owned by the Superpowers/Claude workflow. They explain how features were designed at a point in time, but they are not current API documentation and are intentionally left unchanged during normal documentation maintenance.
