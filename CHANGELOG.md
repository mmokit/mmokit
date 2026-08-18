# Changelog

Notable changes per release. This project is pre-1.0: see the Status section
of [`README.md`](README.md#status) for what that means for API and wire compatibility.

## Unreleased

### Breaking

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
