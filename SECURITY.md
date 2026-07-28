# Security policy

## Reporting a vulnerability

Report privately to **<joshstout@gmail.com>**. Do not open a public issue, and do
not post a proof of concept to a discussion thread, before the report has been
acknowledged.

Useful in a report: the affected file and line, whether the attacker needs to
be authenticated, whether they need to be on the cluster's internal network,
and the smallest input that demonstrates the problem. A failing Go test is the
most useful form a report can take.

Expect an acknowledgement within roughly a week. This is a single-maintainer
project and there is no on-call rotation; treat that number as an intention
rather than a commitment.

## Supported versions

**None, formally.** MMOKIT is pre-1.0, has one git tag, and publishes no
released versions. Fixes land on `main` and there are no maintenance branches
and no backports. If you are running this in an environment where that matters,
pin a commit and carry your own patches.

The project explicitly does not guarantee wire backward compatibility between
commits, so "upgrade to the fixed commit" can mean a lockstep redeploy of every
process in a cluster. That is a documented non-goal, not an oversight — see
[`docs/roadmap.md`](docs/roadmap.md).

## Known limitations

These are known, tracked, and unfixed. They are listed here so that nobody
discovers them by way of an incident, and so that a report describing one of
them is recognisable as a duplicate rather than news.

### 1. The UDP client transport is experimental and unauthenticated

UDP framing is neither authenticated nor encrypted. An on-path attacker can
read and forge client traffic.

It is **off in the shipped binary default**: `--udp-listen` defaults to the
empty string in [`pkg/universe/bootstrap.go`](pkg/universe/bootstrap.go), and
enabling it logs an explicit experimental warning that escalates for a
non-loopback bind.

It is **not off in local development**. Both `just dev` (`justfile:65`) and
`just distributed-space` (`justfile:145`) pass `--udp-listen=:9000`, which is a
**wildcard bind**, not loopback. Every developer process therefore carries the
exposure. This is deliberate — the transport needs exercising — but do not
assume a dev box is closed just because the shipped default is.

Address-identity hardening has landed (CE-005b Tier 1): a UDP token is no
longer a bearer credential, since every data packet must arrive from the
address its session is bound to, and an unauthenticated connection request
allocates nothing until return routability is proven. That closes token replay
from a different address. It does **not** close on-path reading or forgery.

Cryptographic connection identity — an authenticated handshake and AEAD
framing — is CE-005b Tier 2 and is open.

### 2. Mesh gRPC is unauthenticated plaintext

The server-internal MeshControl and MeshData channels use
`insecure.NewCredentials()` at three production sites, verified at this commit:

- [`pkg/universe/host_network.go:233`](pkg/universe/host_network.go)
- [`pkg/universe/mesh_control_client.go:195`](pkg/universe/mesh_control_client.go)
- [`pkg/universe/mesh_gateway_client.go:137`](pkg/universe/mesh_gateway_client.go)

There is no interceptor, no peer-certificate inspection, and no metadata
authentication anywhere in the module. Peer identity is read out of the message
payload and trusted, so any process that can reach a mesh port can act as any
other process: drain a host's cells, take over cell ownership, hijack player
sessions, register fake service instances, inject client input, or execute any
operator command by asserting `*.*` RBAC grants on the wire.

**Treat the mesh ports as a fully trusted internal network.** Do not expose
`--control-listen` (default `:9100`) or any MeshData port to an untrusted
network, and do not run a multi-process cluster across a network segment you do
not control. Note that the single-process `all` preset opens a MeshControl
listener on `:9100` too — the default bind has an empty host, which is all
interfaces, not loopback.

The tracked fix is **CE-006** in [`docs/roadmap.md`](docs/roadmap.md), which
covers both halves: authentication (is this peer in the cluster) via a shared
cluster secret plus in-memory TLS, and authorization (is this peer allowed to
do *this*, as *itself*) via stream-bound identity and locally resolved RBAC.
Authentication alone would still leave every authenticated peer an unrestricted
cluster admin.

The June 2026 TLS work in `pkg/universe/tls_config.go` scopes itself to
client-facing HTTP listeners and does **not** partially close this.

### 3. Development defaults are development defaults

- An empty database seeds an `admin` / `admin` operator with `*.*` grants
  (`seedDefaultOperator` in [`pkg/admin/admin.go`](pkg/admin/admin.go)). It logs
  a change-in-production warning. Change it.
- `--dev-insecure-cookie` disables cookie security attributes so the admin
  dashboard works over plain HTTP locally. It has no production use.
- Self-signed TLS generation is a local-development convenience.

## Out of scope

- Vulnerabilities in the reference space game (`internal/`, `cmd/server`,
  `web-pixi/`). It is not part of the distributed framework — see the License
  section of [`README.md`](README.md) — and it is a test bed, not a product.
- Denial of service that requires access to the mesh ports. Per limitation 2
  those are trusted-network-only by design today; that is the CE-006 item, not
  a separate finding.
- Anything that requires the attacker to already hold operator credentials.
