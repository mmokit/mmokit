# MMOKIT

[![CI](https://github.com/mmokit/mmokit/actions/workflows/ci.yml/badge.svg)](https://github.com/mmokit/mmokit/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/mmokit/mmokit.svg)](https://pkg.go.dev/github.com/mmokit/mmokit)
[![Go Version](https://img.shields.io/github/go-mod/go-version/mmokit/mmokit)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**Build a multiplayer game world that outgrows a single server — without writing the distributed systems part yourself.**

MMOKIT is a Go framework for persistent multiplayer worlds. You write game logic
as ordinary systems over entities. It handles the parts that usually eat the
project: keeping the server authoritative, splitting the world across machines
as it fills up, moving players between those machines mid-flight without them
noticing, and sending each player only what they can actually see.

## What you write

```go
package main

import "github.com/mmokit/mmokit/pkg/mmokit"

// A system is a plain struct. Declare the components you care about and the
// framework hands you every matching entity, every tick, on every machine.
type Movement struct {
	mmokit.SystemBase
	entities mmokit.Query[struct {
		Pos *mmokit.Position
		Vel *mmokit.Velocity
	}]
}

func (s *Movement) Update(dt float32) {
	for _, e := range s.entities.Iter {
		e.Pos.X += e.Vel.X * dt
		e.Pos.Y += e.Vel.Y * dt
	}
}

func main() {
	game := mmokit.New(mmokit.Config{Name: "demo", AnonymousAuth: true})
	game.AddSystem(mmokit.NewSystem(&Movement{}))
	game.Start()
}
```

```bash
go get github.com/mmokit/mmokit
```

That is a running game server: a 20 Hz simulation loop, a WebSocket endpoint for
clients, an operator console, and a `/metrics` endpoint. Add a second machine and
the same code partitions the world across both.

## What it does for you

**Authority stays on the server.** Clients send input, not positions. What they
render is what the server decided.

**The world splits when it gets busy.** A region under load divides into four;
quiet regions merge back. Regions move between machines while players are inside
them. You write none of this, and your systems never learn it happened.

**Players cross machine boundaries cleanly.** Exactly one machine simulates an
entity at any tick, enforced by explicit authority handoff rather than hope.

**Each player receives only what they can see.** Interest management plus
quantised delta compression, so bandwidth tracks what is nearby rather than what
exists.

**Clients get generated, typed SDKs.** Declare a message as a Go struct and the
TypeScript and C# SDKs are generated from it — no hand-written parsers, and no
`.proto` file for gameplay traffic.

**Operators get a real dashboard.** Cells, players, live entity inspection and
runtime tunables, served from the binary with nothing extra to deploy.

## Try it

You need [Go 1.26+](https://go.dev/dl/), [Just](https://github.com/casey/just),
[Bun](https://bun.sh/), Docker, and `tmux`.

```bash
git clone https://github.com/mmokit/mmokit
cd mmokit/examples/simple
just run
```

Open <http://localhost:5174> for the game and <http://localhost:9101/admin/> for
the dashboard (`admin` / `admin` on a fresh database — change it before it leaves
your laptop).

## Three examples, increasing in size

| | What it shows |
| --- | --- |
| [**simple**](examples/simple/) | The smallest thing that runs: one system, one file. Start here. |
| [**4node-basic**](examples/4node-basic/) | A real cluster — four processes, cross-machine handoff, services, hot-swappable WASM systems. |
| [**space**](examples/space/) | A complete game: combat, mining, NPCs, an economy, a world editor, a PixiJS client. The framework's regression bed. |

Each runs with `just run` from its own directory. They share ports, so run one at
a time.

## Status

**v0.1.0, pre-1.0, and honest about it.**

The distributed core is the mature part. It is exercised continuously by the
space example, covered by roughly 1,900 tests, and gated on every merge by a
cross-language wire-parity check. What is *not* settled is the public API
surface — expect it to change.

- **No API compatibility across minor versions.** When an API is wrong, the fix
  is to change it, not to add a second one beside it.
- **Build client and server from the same commit.** Wire identities derive from
  Go type names, so a rename changes the protocol. Version skew is not yet
  detected: a mismatched client silently ignores frames it cannot recognise.
- **2D today.** First-class 3D is designed and sequenced, not built — see the
  [roadmap](docs/roadmap.md).
- **Read [SECURITY.md](SECURITY.md) before exposing anything to a network.** It
  lists the known, unfixed limitations plainly, including an experimental UDP
  transport that is off by default and unauthenticated when enabled.

If you run this somewhere that matters, pin a commit.

## Documentation

- [**pkg/mmokit**](pkg/mmokit/README.md) — the API you actually use, and what
  building on it feels like
- [**Architecture**](docs/architecture.md) — how the pieces fit together today
- [**Roadmap**](docs/roadmap.md) — where it is going, what it will never do, and
  every tracked item
- [**Go reference**](https://pkg.go.dev/github.com/mmokit/mmokit) — generated API
  documentation
- [**AGENTS.md**](AGENTS.md) — the invariants, for human contributors and coding
  agents alike

Contributors: [CONTRIBUTING.md](CONTRIBUTING.md) covers the repository layout,
every `just` recipe, and which checks to run for a given change.

## Contributing

Pull requests are welcome. This is a benevolent-dictator project: one maintainer
has final say, write access is by invitation, and everyone else contributes by
fork and pull request.

Please open an issue before starting anything substantial. There is a sequenced
plan, and unsequenced work is harder to accept than it looks — not a judgement of
the code. [CONTRIBUTING.md](CONTRIBUTING.md) has the workflow, the checks, and an
honest list of what gets declined quickly.

## License

MIT — see [LICENSE](LICENSE). It covers everything here: framework, examples,
tooling and docs.

There are no third-party media assets. The space example's audio is synthesised
by [a committed script](examples/space/web/tools/gen-audio.py) and its art is
drawn procedurally. One piece of third-party code is redistributed in compiled
form — the dashboard bundle, embedded into the binary — and its dependencies are
listed in [ATTRIBUTION.md](ATTRIBUTION.md).

**The framework does not depend on any example.** `pkg/` imports nothing from
`examples/`, and the Go compiler enforces that rather than convention: each
example keeps its game code under its own `internal/`. You can depend on
`pkg/mmokit` without pulling in a line of game code.
