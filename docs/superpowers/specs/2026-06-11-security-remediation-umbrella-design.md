# Security Remediation — Umbrella Design

**Date:** 2026-06-11
**Status:** Approved (umbrella); per-unit specs follow
**Scope:** Remediate the findings from the 2026-06-11 security review of the
authentication system and network transport.

This document is the **index + shared decisions**. Each unit below gets its own
detailed spec and implementation plan. This spec does not itself prescribe
implementation detail beyond the cross-cutting decisions that bind the units
together.

---

## Background — what the review found

The account-credential layer (`pkg/services/auth`) is strong: argon2id
(OWASP-2024 params), 256-bit `crypto/rand` opaque session tokens (only the
SHA-256 hash persisted), per-account + per-IP lockout, `HttpOnly`/`Secure`/
`SameSite=Strict` cookies, and correct enforcement (a connection spawns no
player until `onAuthSuccess` fires). The exposure is almost entirely at the
**transport layer**, where an attacker on the network path can sidestep the good
auth entirely.

Confirmed findings (with the load-bearing ones verified directly against code):

1. **UDP player spoofing (critical).** The UDP connection is identified solely by
   a 32-bit token computed as an XOR-fold of two salts sent in cleartext during
   the handshake (`pkg/net/udpproto/proto.go::MakeToken`). Data packets are
   routed **by token only** — the source address recorded at handshake is never
   re-checked (`pkg/net/udp_server.go::dispatch`). An attacker who observes one
   handshake (or brute-forces 2^32) can inject packets from any spoofed source
   IP, attributed to the victim's authenticated player.
2. **No UDP replay protection (critical).** `handleReliable` calls
   `routePayload` for every packet including duplicates/old sequences
   (`pkg/net/udp_transport.go`); `recvSeq`/`recvBits` exist only to generate
   ACKs, not to drop replays. Unreliable packets have no sequence at all.
3. **No transport TLS (critical).** WS/HTTP is served via `ListenAndServe`
   (plain `ws://`) with `InsecureSkipVerify: true` on accept
   (`pkg/net/server.go`, `pkg/universe/bootstrap.go`). The session cookie rides
   this in cleartext → trivial session hijack on an observed network. This is the
   single cheapest exploit to mount.
4. **Unauthenticated, unencrypted mesh (high).** Coordinator/host/gateway gRPC
   uses `insecure.NewCredentials()` and `RegisterHost` carries no secret/cert
   (`pkg/universe/mesh_control_client.go`, `host_network.go`, `proto/meshpb`).
   Anyone who can reach `--control-listen` (`:9100`) can register as a host,
   receive cell assignments, intercept handoffs, and inject `CellTransfer`
   commands.
5. **App-layer hardening gaps (medium/low).** Default `admin/admin` operator with
   `*.*` grants; no CSRF tokens on admin mutations; session token not rotated on
   password change; `AnonymousAuth` escape hatch with no production guard;
   per-IP lockout doesn't honor `X-Forwarded-For` on all paths.

---

## Units & sequencing

Four independent units, ordered by dependency then severity. Each gets its own
detailed spec (`docs/superpowers/specs/`) and implementation plan.

| # | Unit | Depends on | Detailed spec |
|---|------|------------|---------------|
| 1 | Client transport TLS | — | `2026-06-11-transport-tls-design.md` |
| 2 | UDP secure framing | Unit 1 | (to be written) |
| 3 | Mesh security (shared-secret auth + ephemeral TLS) | — | (to be written) |
| 4 | App-layer auth hardening | — | (to be written) |

**Why Unit 1 is first:** it closes the cheapest real-world exploit (cleartext
cookie), and Unit 2's UDP session key is handed to the client over the
authenticated WS channel — so UDP security genuinely depends on that channel
being encrypted first.

Units 3 and 4 are independent and can land in any order relative to 1/2.

### Unit 1 — Client transport TLS
Covered in detail in `2026-06-11-transport-tls-design.md`. Summary: in-process
TLS configurable (static cert/key files for prod; opt-in in-memory self-signed
for local TLS testing; plaintext default for localhost dev / proxy-terminated
deployments). Remove `InsecureSkipVerify` and add a WS origin allowlist
(CSWSH fix). Closes finding #3.

### Unit 2 — UDP secure framing
Closes findings #1 and #2. Direction (detail deferred to its own spec):
- Replace the 32-bit XOR token with a **per-session key** minted server-side and
  delivered to the client over an **authenticated HTTPS endpoint** (NOT the WS
  channel and NOT the UDP op-channel). Decision (2026-06-12): all clients —
  web and Unity/C# alike — authenticate over HTTPS and receive a session token
  + per-session UDP key, then open the UDP connection keyed by it. Rationale:
  HTTPS is the only channel that can host future OIDC/OAuth2 social-auth
  redirect flows (impossible over a UDP datagram), and it unifies the web and
  UDP-only Unity clients on one auth path. This makes Unit 2 depend on Unit 1
  (HTTPS must be available) and supersedes any earlier "key handoff over the WS
  channel" phrasing.
- **Per-packet AEAD** using ChaCha20-Poly1305 (recommended: fast pure-software on
  every platform incl. Unity/mobile; available in Go `golang.org/x/crypto/
  chacha20poly1305` and .NET `System.Security.Cryptography.ChaCha20Poly1305`),
  with a counter-based nonce derived from a monotonic per-session sequence.
- **Replay window**: the existing `recvBits` bitmap becomes an enforcement
  mechanism that *drops* replayed/too-old sequences instead of merely ACKing.
- Must be mirrored across three codebases + golden harness: Go server
  (`pkg/net/udpproto`, `udp_server.go`, `udp_transport.go`), Go client
  (`pkg/net/udpclient`), C# SDK (`csharp/Mmokit.Sdk.Core/UdpProto.cs`,
  `UdpTransport*.cs`), and `cmd/csharp-golden`.

### Unit 3 — Mesh security
Closes finding #4. Decided model (detail deferred to its own spec):
- **Shared-secret join auth** carried in gRPC call metadata, checked by the
  coordinator on `RegisterHost`/`RegisterGateway` and on every MeshControl/
  MeshData stream open.
- **Ephemeral in-memory self-signed TLS** for the gRPC channel: the coordinator
  generates a self-signed cert in memory at startup (never written to disk),
  clients dial with `InsecureSkipVerify` and authenticate via the shared secret.
  Gives confidentiality against passive eavesdropping with **zero files and zero
  cert management**, identical local and remote.
- **Zero-config:** single `--cluster-secret` (env-overridable, e.g.
  `MMO_CLUSTER_SECRET`). Not required in single-process `all` mode (no network
  mesh). On a localhost dev bind the coordinator may auto-generate and log a
  secret. Remote hosts/gateways pass the same secret string — one env var, no
  files.
- **Documented residual:** the secret is an authentication control, not a
  substitute for network isolation. An active MITM on an untrusted path could
  intercept (InsecureSkipVerify). Mitigation: run the mesh over a private
  network/VPN. This is within the single-operator trust boundary by assumption.

### Unit 4 — App-layer auth hardening
Closes finding #5. A cluster of smaller, independent fixes (detail deferred):
- **CSRF** protection on admin state-changing routes (the admin SPA + cmdsys
  POST surface). `SameSite=Strict` mitigates but does not eliminate.
- **Session-token rotation on password change** — currently only *other*
  sessions are revoked (`handlers.go`); a compromised current token survives a
  reset. Rotate the active token too.
- **`AnonymousAuth` production guard** — the dev escape hatch
  (`gateway.go::onWSUpgrade`) should be impossible to enable on a non-loopback /
  production bind.
- **Proxy-header trust** for lockout IP attribution — honor `X-Forwarded-For`
  (when a trusted-proxy flag is set) consistently across the game-auth and admin
  lockout paths.
- **Default `admin/admin`** — keep first-run seeding for zero-config dev, but
  force a password change on first production login (or refuse the default
  credential on a non-loopback bind). Exact mechanism in the unit spec.

---

## Cross-cutting decisions (locked)

These were settled during brainstorming and bind all units:

1. **Client TLS termination is configurable** — in-process TLS is a first-class
   built-in option, and running behind a TLS-terminating edge proxy is supported.
2. **Client cert source: static files (prod) + opt-in dev self-signed.** No ACME/
   Let's Encrypt (cut as YAGNI for now).
3. **Mesh security: shared-secret auth + ephemeral in-memory TLS.** No certificate
   files, no CA, no PKI to manage — explicitly chosen over an mTLS-with-CA model
   because the operator must never have to think about cert/file management, and
   it must be zero-config for remote servers. The mesh is treated as a single
   trust domain (single-operator cluster).
4. **Secure-by-default where it matters, frictionless where it doesn't.** Plaintext
   is acceptable on loopback (no network path); a non-loopback plaintext bind
   emits a prominent startup warning and continues (does not hard-fail).

---

## Non-goals

- ACME / automatic public-CA certificate provisioning.
- mTLS-with-CA or a CSR-signing bootstrap protocol for the mesh.
- MFA / OIDC (schema seams exist in `auth.users` but are out of scope here).
- Gateway-crash transparent session recovery (tracked separately).
- Defending the mesh against an active MITM on an untrusted network path (answer
  is network isolation/VPN, by trust-boundary assumption).

---

## Verification strategy (per unit)

Each unit's plan must demonstrate the gap is closed, not just that code compiles:
- **Unit 1:** a test/observation that the WS listener serves TLS when configured,
  rejects disallowed origins, and that the dev self-signed path produces a usable
  cert; manual confirmation the cookie no longer transits in cleartext.
- **Unit 2:** tests that a replayed/forged UDP packet (wrong key, reused/old
  sequence) is dropped and never reaches `routePayload`; Go↔Go and Go↔C# golden
  interop over the new framing.
- **Unit 3:** a test that `RegisterHost` without the shared secret is rejected,
  and that the channel is encrypted (non-plaintext on the wire).
- **Unit 4:** tests for token rotation on password change, CSRF rejection of a
  forged admin mutation, and `AnonymousAuth` refusal on a non-loopback bind.
