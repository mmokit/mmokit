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

### 2. Mesh peers authenticate with one shared secret

The server-internal MeshControl and MeshData channels run over TLS and
authenticate peers with a single cluster-wide secret, presented in gRPC call
metadata at stream open and compared with `crypto/subtle`. Certificates are
generated in memory per process and never written to disk; peers dial with
`InsecureSkipVerify`, so the encryption defends against a passive eavesdropper,
not an active on-path attacker.

The secret is set with `--cluster-secret` or `MMO_CLUSTER_SECRET`. A
self-contained role set (`all`, or any coordinator+host combination) generates
one automatically at startup and logs its fingerprint, so single-process
deployments are closed by default with no configuration. **A multi-process
cluster with no secret configured warns loudly and runs unauthenticated** — set
it on every process.

Two limitations are deliberate and worth stating plainly:

- **The secret is shared, not per-peer.** It excludes outsiders and pins each
  stream to one claimed identity for its lifetime, but it does not stop one
  authenticated cluster member from claiming another member's ID. Defending
  against a compromised member requires per-peer credentials, which is out of
  scope. Treat every process holding the secret as equally trusted.
- **No CA, no pinning.** An active MITM on an untrusted network path can
  intercept. The answer is network isolation, not PKI.

**Prefer to keep the mesh ports on a private network regardless.** Do not
expose `--control-listen` (default `:9100`, an all-interfaces bind) or any
MeshData port to the public internet.

Within the cluster, authorization no longer depends on anything a peer asserts
in a message body: control-plane handlers act on the identity the stream
registered with, payload frames are verified per-arm against the identity the
stream presented, and RBAC grants are not carried on the wire at all — a
process executes a routed command because an authenticated coordinator already
authorized it.

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
- Denial of service that requires holding the cluster secret. Per limitation 2
  every process holding it is equally trusted by design; impersonation between
  authenticated members is a known, documented limitation rather than a
  separate finding.
- Anything that requires the attacker to already hold operator credentials.
