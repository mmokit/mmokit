# Changelog

Notable changes per release. This project is pre-1.0: see the Status section
of [`README.md`](README.md#status) for what that means for API and wire compatibility.

## Unreleased

### Added

- **The 3D dimension profile is implemented.** `Config.Dimension:
  Dimension3D` selects a process that replicates three world coordinates,
  three velocity axes, collider depth and full quaternion orientation, and
  integrates and transfers all of it. It was previously declared and
  panicked at construction. See `examples/cube3d` for the smallest working
  3D process, and `docs/roadmap.md` §7.5.6 for what it does and does not
  cover.

  A **2D game is unaffected and pays no additional wire bytes**, on the
  client wire or the mesh: the 2D engine binding set and the 2D border
  frame header are byte-identical, which the committed schema goldens
  pin.

  Notable pieces: a 7-byte smallest-three quaternion encoding in
  `pkg/quantize`; `component.Motion` with `MoveFly` / `MoveWalk` /
  `MoveBallistic` plus `Config.Gravity`; and `component.Rotation` gaining
  `RotationFromAxisAngle`, `Mul` and `RotateAxis`, without which no game
  could express pitch or roll.

  Not yet covered: no client can render it — quaternion decode and slerp
  in TypeScript and C# are the next phase — and `pkg/spatial` is still 2D,
  so a 3D game collides as though flattened.

### Fixed

- **Entity height survived neither a cell boundary nor a reconnect.** The
  mesh transfer frame reserved `PosZ`/`VelZ` and round-tripped them in its
  codec, but nothing ever wrote or read them, so Z was dropped at both
  ends of every split, merge and migrate. `engine.players` likewise had no
  `pos_z`. Both are fixed; migration 002 adds the column with a default of
  0, so a 2D deployment needs no action.

### Breaking

- **Three engine API signatures changed with CE-008** (tick timing and
  loop-job lifecycle). No wire format moves and no client is affected; these
  are compile errors for a game embedding the framework.

  - `mmokit.Hooks.PreFlush` is now `func(dt float32)` rather than `func()`.
    It receives the loop's own timestep, which is what stops a system and an
    `mmokit.OnWorldTick` callback from integrating different values of `dt`
    inside the same tick.
  - `Engine.SubmitLoopJob` returns `error` rather than `bool`: `nil`,
    `ErrLoopQueueFull`, or `ErrLoopStopped`. The old `true` on a stopped loop
    was indistinguishable from `true` on a live one, so a caller owing a
    client a response left it pending forever.
  - `mmokit.NewCellMetrics` takes the tick budget as a `time.Duration` rather
    than a tick rate as an `int`. Note that an untyped constant call site
    (`NewCellMetrics("x", 20, ...)`) still compiles and now means 20
    nanoseconds — check yours.

  Behaviour worth knowing even though it is not a signature change: a tick
  rate that does not divide 1000 now rounds to the nearer whole-millisecond
  period instead of truncating, so a configured 60 Hz runs at 58.8 Hz rather
  than 62.5 Hz. `RunOnLoop` on a loop that has already exited returns
  `ErrLoopStopped` immediately instead of blocking, and a job whose caller's
  context expires while it is still queued is now discarded rather than
  applied on a later tick.

- **The UDP wire format is now version 2, and UDP clients must authenticate
  over HTTPS before they connect.** Every UDP packet changes shape, so a v1
  client cannot talk to a v2 server or the reverse: **redeploy both halves
  together.** WebSocket clients are entirely unaffected.

  What changed on the wire:

  - Data packets carry an explicit AEAD counter and a ChaCha20-Poly1305 sealed
    body. Cleartext headers are authenticated as additional data. ACKs and
    disconnects are sealed too — in v1 anyone who learned a token could retire
    a frame the peer never received, or tear the session down.
  - A version byte follows the type byte on **every** packet, not just the
    handshake ([CE-009](docs/roadmap.md)). It versions the packet envelope: a
    peer running a different packet layout is rejected at the first datagram.
    It does **not** version the sealed payload's shape — that needs CE-009's
    schema fingerprint, which is not in this release.
  - A new `ConnConfirm` (`0x06`) step carries the key ID and echoes a stateless
    HMAC cookie. The server now retains nothing for a peer that has not proven
    return routability, which removes the pending-handshake table and the
    spoofed-address denial window it exposed.
  - The 32-bit token stops being a credential and becomes a session index. The
    AEAD tag is the credential.

  What changed for clients:

  - Clients authenticate over HTTPS and draw a short-lived session key from the
    new `POST /auth/udp-key` before opening a socket. `UdpTransport.Connect` and
    `udpclient.Dial` therefore take a key ID and key; there is no
    unauthenticated connect path.
  - **Op-channel authentication is retired in the C# client.** The generated
    client gained `ConnectAsync(baseUrl, host, port, username, password)`, which
    performs the HTTPS auth, draws the key, and completes the handshake. The
    server binds the player from that key, so a connected session is already an
    authenticated one and the old post-connect `AuthLogin`/`AuthRegister` round
    trip is gone. Browser clients already authenticated over HTTPS and are
    unchanged.

  **Client wire type IDs are unchanged.** UDP framing is not the reflection
  codec; verified by byte-diffing the `--dump-schema` output of all three
  examples across the change.

- **The facade moved to the module root.** Games now import
  `github.com/mmokit/mmokit` instead of `github.com/mmokit/mmokit/pkg/mmokit`.
  Update the import path; no other change is required, because the package is
  still named `mmokit` and every exported symbol kept its name.

  **The wire format is unchanged.** Client wire IDs hash
  `reflect.Type.String()`, which qualifies by package *name*, not import path —
  so moving the package between directories is not a protocol change. Verified
  by byte-diffing the `--dump-schema` output of all three examples before and
  after: identical. A client built against v0.1.0 still talks to a server built
  from this commit, subject to the same-commit rule in
  [`README.md`](README.md#status).

- **`mmokit.SyncCellTunables` was removed.** It had no callers and was only
  ever reached through the `universe.OnCellSystemsReady` hook — internal wiring
  that had leaked into the exported surface. It now lives at
  `tunectl.SyncCellTunables`, inside `internal/`.

### Changed

- `SystemBase`, `WireSystem` and `Any` / `FindOne` / `ForEach{1,2,3}` are now
  implemented in `pkg/universe` and re-exported from the facade — `SystemBase`
  as a type alias, the rest as forwarders. Game code sees no difference.
- The admin topic bus moved to `pkg/admin` (`admin.BusFor`,
  `admin.PublishTopic`). `mmokit.PublishAdminTopic` is unchanged for callers.
  The new `admin.ForgetBus` releases a process's cached bus, which previously
  pinned every `*universe.Process` for the life of the program.
- New `universe.Process.CellsMatching(node, cell)` implements the `--node` /
  `--cell` operator filters that the built-in `wasm.*` and `tune.*` verbs share.

## v0.1.0 — first public release

The first tagged, published version. Everything below already existed; this
entry describes what the release contains rather than what changed inside it.

### Framework

- Server-authoritative simulation with an Ark-based ECS and a fixed-timestep
  loop per cell, 20 Hz by default.
- Dynamic spatial partitioning: cells split under load, merge when quiet, and
  migrate between hosts. Entity handoff is epoch-gated, so exactly one host has
  authority at any tick.
- Interest-managed delta replication with quantised snapshots, per-connection
  baselines, and border replicas for cross-cell visibility.
- Four composable process roles — coordinator, host, gateway, service — running
  in one process or many. The coordinator is a control plane; payloads travel
  directly between gateways, hosts, and services.
- Typed binary client events, input, and request/response operations over
  WebSocket, with an experimental UDP transport that is off by default.
- TypeScript and C# client SDKs generated from registered Go types.
- Hot-swappable Go/WASM game systems.
- PostgreSQL-backed engine and game persistence, pluggable auth and chat
  services, and typed operator commands shared by the console and the Svelte
  admin dashboard.

### Examples

- `examples/simple` — the smallest runnable game.
- `examples/4node-basic` — distributed roles, services, WASM systems, generated
  SDK.
- `examples/space` — the reference space game: combat, mining, NPCs, an
  economy, a world editor, and a PixiJS client. The framework's regression bed.

### Known limitations

Documented rather than discovered: see the Known limitations section of
[`SECURITY.md`](SECURITY.md) for the UDP transport's lack of authentication,
the shared-secret mesh model, and the development defaults. `docs/roadmap.md`
tracks everything else, including the 2D/3D program this release predates.
