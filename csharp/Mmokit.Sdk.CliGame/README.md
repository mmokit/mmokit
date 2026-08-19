# mmokit-cli — a terminal client for `examples/4node-basic`

A small, playable client over the **UDP transport**. Connect, walk around, watch
the authoritative world stream arrive.

```
cd examples/4node-basic && just dev     # one pane: server, UDP on :9000
just csharp-cli                          # another pane: play
```

```
┌────────────────────────────────────────┐
│              o                         │
│                     @        A         │
│         o                              │
└────────────────────────────────────────┘
 runner-a1b2c  pos (   2010,   1700)  tick 4923  20Hz  rtt  6ms
 players 2   bots 12  deltas 468    [WASD] move  [space] stop  [q] quit
```

`@` is you, letters are other players (first initial, in their tint), `o` is a
bot. The view is centred on your entity, so what you see is what your own area
of interest covers.

## Why it exists

`Mmokit.Sdk.SmokeBot` next door proves the transport works and exits. This is
driven by a person, so it exercises what a scripted round-trip never does:
sustained input, entities crossing in and out of AoI, and what movement actually
*feels* like when the server is the only authority on it.

It is also the smallest complete example of the C# SDK. Unity is the SDK's real
target and needs Unity to run; this needs a terminal.

## Two things it demonstrates deliberately

**No client-side prediction.** Every position drawn came from a decoded
`WorldDelta`. A terminal client that predicted movement would hide the thing
this is useful for showing — what the authoritative stream looks like on its
own, including the latency you are actually paying.

**Move targets, not velocities.** A keypress sends `MoveTargetMsg` with an
absolute world coordinate; the server's `ClickToMoveSystem` walks the entity
toward it. A dropped input costs a little distance, never a desynced position —
which is what makes this safe to send over an unreliable channel. Walk east past
x=2000 and you cross a cell boundary with nothing in the client aware of it.

## Requirements

`just csharp-cli` generates its own SDK into a temp dir, so it needs no
`UNITY_SDK_DIR` — but it does need PostgreSQL for the schema dump (`just db-up`)
and a running server.

The server's UDP listener is **off by default**. `just dev` and
`just distributed` both pass `--udp-listen=:9000` and `--dev-insecure-cookie`
(the latter lets `/auth/udp-key` issue a key over plain HTTP); a hand-launched
binary does neither. In distributed mode only the **gateway** binds UDP.

`just csharp-cli-build` is the compile gate and needs no running server.

## Failure modes it explains rather than just reporting

- **409** — the SDK was generated from a different build than the server runs.
  Regenerate: `just csharp-sdk`.
- **403** — the server refuses to put a UDP key on a plaintext listener. Pass
  `--dev-insecure-cookie`, or use an `https://` base URL.
- **404** — this process has no auth service, so it cannot issue UDP keys. In
  distributed mode auth runs on the gateway.
- **Handshake timeout** — HTTP worked, UDP did not. Check for
  `udp: listening on :9000` in the server log. On WSL2→Windows, pass the WSL IP
  (`hostname -I`) rather than `127.0.0.1`.
