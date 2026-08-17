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

### 1. The UDP client transport is off by default and needs an HTTPS origin

**This limitation has been substantially closed.** CE-005b Tier 2 landed
cryptographic connection identity: every UDP packet is sealed with
ChaCha20-Poly1305 under per-direction keys, the handshake is stateless and
authenticated, and a session is bound to a user before it carries a byte. On-path
reading and forgery are closed. What remains here is deployment guidance.

It is **off in the shipped binary default**: `--udp-listen` defaults to the empty
string in [`pkg/universe/bootstrap.go`](pkg/universe/bootstrap.go). This is now
an opt-in choice about exposing a second port, not a warning about an unsafe
transport.

**The UDP transport's security depends on the HTTPS listener that issues its
keys.** Clients draw a session key from `POST /auth/udp-key`, so that endpoint
is the root of the transport's trust. It refuses to serve over a plaintext
listener unless `--dev-insecure-cookie` is set, precisely because the response
body is a bearer secret. **Do not run `--dev-insecure-cookie` outside local
development**, and terminate TLS in front of the gateway in any deployment where
UDP is enabled — a plaintext key handout gives an on-path attacker everything the
AEAD was protecting.

Both `just dev` and `just distributed` in the example justfiles pass
`--udp-listen=:9000` **and** `--dev-insecure-cookie`, on a wildcard bind. That is
a deliberate local-development posture, not a template for deployment.

Residual, unclosed: keys are multi-use for their five-minute TTL, so an attacker
who obtains one can open a session as that user until it expires. Source-address
binding from Tier 1 remains as defence in depth but is not a substitute for
protecting the key in transit.

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
- The multi-process dev recipes default `MMO_CLUSTER_SECRET` to the literal
  string `dev-cluster-secret` (`examples/space/justfile`,
  `examples/4node-basic/justfile`). Any process holding it is fully trusted per
  limitation 2, so treat a cluster started from a dev recipe as open to anyone
  who has read this repository. Set a real secret for anything else.

## Out of scope

- Game logic in the examples (`examples/simple`, `examples/4node-basic`,
  `examples/space`). They are test beds and demonstrations, not products, and
  they are not hardened: expect missing rate limits, trusting handlers, and
  debug affordances. A vulnerability in `pkg/` that an example merely happens
  to expose IS in scope — report it against the framework.
- Denial of service that requires holding the cluster secret. Per limitation 2
  every process holding it is equally trusted by design; impersonation between
  authenticated members is a known, documented limitation rather than a
  separate finding.
- Anything that requires the attacker to already hold operator credentials.
