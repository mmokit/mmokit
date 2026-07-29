# Mesh Authentication and Authorization (CE-006) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close every mesh channel to processes outside the cluster, encrypt both channels in transit, and stop the receiving side from believing identity and authority claims made in message bodies — with zero configuration for the single-process preset and no certificate management.

**Architecture:** A single `universe.Config.ClusterSecret` (flag > `MMO_CLUSTER_SECRET` > preset field) travels in gRPC call metadata under the key `mmokit-cluster-secret`, attached with `metadata.AppendToOutgoingContext` at the three stream-open sites and checked by a `grpc.ChainStreamInterceptor` on both servers with `subtle.ConstantTimeCompare`. Both servers get `grpc.Creds` from a per-process in-memory self-signed cert produced by the existing `generateDevCert`; all three dials use `credentials.NewTLS(&tls.Config{InsecureSkipVerify: true})`. Auto-generation of the secret is gated on the role set (`RoleCoordinator && RoleHost`), never on a loopback check. Control-plane handlers stop reading identity from message bodies and use the ID captured at registration; a registration-admission predicate rejects an ID still being *defended* by an incumbent. `Grant` is deleted from `meshpb.Caller` outright and mesh-delivered commands execute on the authenticated-coordinator relation. `routeInboundFrame` takes a `senderID string` parameter captured once per stream, verified per-arm.

**Tech Stack:** Go stdlib (`crypto/tls`, `crypto/rand`, `crypto/sha256`, `crypto/subtle`), `google.golang.org/grpc` v1.80.0 (`credentials`, `credentials/insecure` removal, `metadata`, `status`, `codes`), Buf remote plugins for `just proto`. No new dependencies.

**Spec:** [`docs/superpowers/specs/2026-07-28-mesh-authentication-design.md`](../specs/2026-07-28-mesh-authentication-design.md)

**Conventions to honor (project memory + AGENTS.md):**
- Never `go build ./...`; `go vet ./...` is the compile check, `just build-go` the DB-free build.
- Never hand-edit `gen/go/meshpb/`. Change `proto/meshpb/mesh.proto` and run `just proto`.
- Proto field removal renumbers rather than reserving — here `id=1`/`source=2` already survive contiguously.
- No backward-compat shims or re-exports; update callers directly.
- Manual smoke instructions are delivered inline in chat, never as a `*_SMOKE.md` file.
- Commits are Conventional Commits with package-list scopes and a `Co-Authored-By: Claude Opus 5 (1M context)` trailer.

---

## File Structure

- **Create** `pkg/universe/mesh_auth.go` — the metadata key constant, `clusterSecretFingerprint`, the stream interceptor, `peerIDFromContext`. One responsibility: deciding whether a stream may speak and who it claims to be.
- **Create** `pkg/universe/mesh_auth_test.go` — interceptor accept/reject, constant-time comparison, fingerprint stability.
- **Create** `pkg/universe/mesh_tls.go` — `Process.meshTLSConfig()` (server) and `meshClientTLSConfig()` (dial), memoized separately from `httpTLSConfig`.
- **Create** `pkg/universe/registration_admission.go` — `killClosed`, `admitHostRegistration`, `admitGatewayRegistration`.
- **Create** `pkg/universe/registration_admission_test.go` — the seven admission scenarios.
- **Modify** `pkg/universe/coordinator.go` — `Config.ClusterSecret`; `Process.meshTLSOnce`/`meshTLSCert`/`meshWarnOnce`; the role-gated secret block in `Build()`; `grpc.Creds` + interceptor on the MeshControl server; `NoopAuditSink` replacement.
- **Modify** `pkg/universe/bootstrap.go` — `MMO_CLUSTER_SECRET` read then `stringFlag("cluster-secret", …)`.
- **Modify** `pkg/universe/host_network.go` — `NewHostNetwork` signature; `grpc.Creds` + interceptor on the MeshData server; TLS on `ConnectPeer`; delete `TODO(S4)`; `routeInboundFrame(senderID, frame)` and the per-arm checks; the three epoch-hole fixes.
- **Modify** `pkg/universe/mesh_control_client.go`, `mesh_gateway_client.go` — TLS creds, outgoing metadata, delete `TODO(mTLS)`.
- **Modify** `pkg/universe/mesh_data_server.go` — capture the peer ID once before the `Recv` loop.
- **Modify** `pkg/universe/mesh_control_server.go` — twelve stream-bound identity reads; admission calls; `cancelGatewayStream`; delete the two `HostMessage_CommandRequest` receive paths; clamp the request deadline.
- **Modify** `pkg/universe/coord_assignment.go` — `cancelStream` on `MarkDead`.
- **Modify** `pkg/universe/cmdsys_transport.go` — drop `Grants` from both conversions; `executeCommandRequest` takes a peer authority.
- **Modify** `pkg/cmdsys/dispatcher.go` — `InvokeAsPeer`, audit emission.
- **Modify** `pkg/universe/grpc_bridge.go` — pass `b.host.ID` on the self-route.
- **Modify** `pkg/universe/virtual_conn_manager.go`, `replication_receipt.go` — receipt oneof arm; delete the marker helpers.
- **Modify** `pkg/metrics/ingress.go` — one new `IngressReason` before `numIngressReasons`.
- **Modify** `proto/meshpb/mesh.proto` — delete `Grant` and `Caller.grants`; add `ClientFrame.source_host_id`/`receipt_token`; add `ReplicationReceipt` + oneof arm.
- **Generated** `gen/go/meshpb/` — `just proto` only, never hand-edited.
- **Modify** `docs/roadmap.md`, `docs/architecture.md`, `SECURITY.md`, `cmd/server/README.md` — criterion 11's stale-claim sweep.

---

## Phase A — Authenticate and encrypt both channels (criteria 1–7)

### Task A1: Config surface and precedence

**Files:**
- Modify: `pkg/universe/coordinator.go` (`Config`, after `TLSMode` ~line 171; `New` fallback ~line 810)
- Modify: `pkg/universe/bootstrap.go` (flag section, after the `tls-mode` block)
- Create: `pkg/universe/mesh_auth_test.go`

- [ ] **Step 1: Add the Config field**

In `pkg/universe/coordinator.go`, immediately after the `TLSMode string` field, add:

```go
	// ClusterSecret authenticates mesh peers to each other. It is sent in
	// gRPC call metadata at stream open on both mesh channels and compared
	// with crypto/subtle. Precedence is --cluster-secret > MMO_CLUSTER_SECRET
	// > this field.
	//
	// Empty means: auto-generated for a self-contained role set (coordinator
	// + host, which includes the "all" preset), and unauthenticated with a
	// one-time warning for every other role set. See Build().
	//
	// NEVER include this in any dump, snapshot, admin response or log line.
	// Process.Config() hands out a mutable *Config; log the fingerprint from
	// clusterSecretFingerprint instead.
	ClusterSecret string
```

- [ ] **Step 2: Wire the flag with env precedence**

In `pkg/universe/bootstrap.go`, after the `stringFlag("tls-mode", …)` block, add:

```go
	// Read the env var BEFORE stringFlag so it becomes the flag's default:
	// stringFlag only applies its engineDefault when the field is still zero,
	// so an explicit --cluster-secret beats the env, which beats the preset
	// field. No flag.Visit needed (it is used nowhere in this module).
	if v := os.Getenv("MMO_CLUSTER_SECRET"); v != "" {
		c.ClusterSecret = v
	}
	stringFlag("cluster-secret",
		"shared secret authenticating mesh peers (env: MMO_CLUSTER_SECRET); "+
			"auto-generated for single-process role sets, required for every peer in a distributed cluster",
		"", &c.ClusterSecret)
```

Add `"os"` to the import block if absent.

- [ ] **Step 3: Add the zero-value fallback in `New`**

In `pkg/universe/coordinator.go`, next to the `cfg.WireLimits = cfg.WireLimits.Normalized()` line, add:

```go
	// Same reason as WireLimits above: flag defaults never reach a Config
	// built by a test fixture, and BindFlags is also skipped for any game
	// that calls flag.Parse() itself. Read the env on both paths.
	if cfg.ClusterSecret == "" {
		cfg.ClusterSecret = os.Getenv("MMO_CLUSTER_SECRET")
	}
```

- [ ] **Step 4: Test the fallback**

Create `pkg/universe/mesh_auth_test.go` with a test mirroring `wire_limits_test.go:21-25`:

```go
func TestNew_ClusterSecretFallsBackWithoutBindFlags(t *testing.T) {
	t.Setenv("MMO_CLUSTER_SECRET", "from-env")
	p := New(Config{})
	if got := p.cfg.ClusterSecret; got != "from-env" {
		t.Fatalf("ClusterSecret = %q, want %q (BindFlags is skipped under go test)", got, "from-env")
	}
}

func TestNew_ClusterSecretFieldBeatsEnv(t *testing.T) {
	t.Setenv("MMO_CLUSTER_SECRET", "from-env")
	p := New(Config{ClusterSecret: "from-field"})
	if got := p.cfg.ClusterSecret; got != "from-field" {
		t.Fatalf("ClusterSecret = %q, want the preset field to win", got)
	}
}
```

Run: `go test ./pkg/universe/ -run TestNew_ClusterSecret -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/coordinator.go pkg/universe/bootstrap.go pkg/universe/mesh_auth_test.go
git commit -m "feat(universe): add the cluster-secret configuration surface"
```

No enforcement yet — the tree stays green everywhere.

---

### Task A2: Mesh TLS on both channels (lockstep)

**Files:**
- Create: `pkg/universe/mesh_tls.go`
- Modify: `pkg/universe/coordinator.go` (`Process` fields ~line 647; MeshControl server ~line 2219; call sites `:1838, 2021, 2078, 2112`)
- Modify: `pkg/universe/host_network.go` (`NewHostNetwork` ~line 126; server ~line 148; `ConnectPeer` ~line 229)
- Modify: `pkg/universe/mesh_control_client.go` (~line 191), `pkg/universe/mesh_gateway_client.go` (~line 136)
- Modify: `pkg/universe/host_network_test.go` (`:97, :101, :560, :564`), `pkg/universe/cell_transfer_executor_test.go` (`:1121`)

This is one commit. A `grpc.Creds` server cannot accept a plaintext client and vice-versa; there is no negotiation. Every multi-process launch in this repo builds one binary and runs N copies of it, so there is no version-skew path and no transition period is warranted.

- [ ] **Step 1: Add the mesh TLS helpers**

Create `pkg/universe/mesh_tls.go`. `generateDevCert` is reused **unchanged** — do not touch its SANs or `SerialNumber`; peers dial `InsecureSkipVerify`, so neither is consulted, and `tls_config_test.go:140,155` assert them for the HTTP path.

```go
package universe

import "crypto/tls"

// meshTLSConfig returns the server-side TLS config for this process's mesh
// listeners, generating one in-memory self-signed certificate per process
// lifetime. It is deliberately NOT httpTLSConfig: that one memoizes the
// client-facing posture and returns nil in the shipped default, while the
// mesh needs a certificate even when client TLS is plaintext.
//
// The certificate's SANs and validity are irrelevant by design — peers dial
// with InsecureSkipVerify and authenticate with the cluster secret, so this
// buys confidentiality against a passive eavesdropper and nothing more.
func (p *Process) meshTLSConfig() (*tls.Config, error) {
	var err error
	p.meshTLSOnce.Do(func() {
		p.meshTLSCert, err = generateDevCert()
	})
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{p.meshTLSCert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// meshClientTLSConfig is the dial-side counterpart. InsecureSkipVerify is
// intentional and load-bearing: there is no CA and no cert distribution, so
// verification would have nothing to verify against.
func meshClientTLSConfig() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}
}
```

Add to `Process` beside `tlsOnce`/`tlsConfig`/`tlsSelfSigned`:

```go
	meshTLSOnce sync.Once
	meshTLSCert tls.Certificate
	meshWarnOnce sync.Once
```

- [ ] **Step 2: Widen `NewHostNetwork`**

The signature change is **mandatory**, not stylistic. `NewHostNetwork` builds the server and calls `go n.server.Serve(listener)` inside the constructor, before any setter could run — and reading `host.coord.cfg` instead would nil-deref: in `buildRemoteHost`, `NewHostNetwork` is at `coordinator.go:2021` but `host.coord = c` is not set until `:2052`.

```go
func NewHostNetwork(host *Host, grpcAddr string, log *logger.Logger, gracePeriod time.Duration, tlsCfg *tls.Config, secret string) (*HostNetwork, error)
```

Store `secret` on `HostNetwork` (needed again in A3). Add to the `grpc.NewServer` option list:

```go
		grpc.Creds(credentials.NewTLS(tlsCfg)),
```

- [ ] **Step 3: TLS on all three dials, and delete both TODOs**

At `host_network.go:229-233`, delete the `TODO(S4)` comment block (whose claim that "S3 only runs in loopback" is false against four `":0"` binds) and replace:

```go
		grpc.WithTransportCredentials(insecure.NewCredentials()),
```
with:
```go
		grpc.WithTransportCredentials(credentials.NewTLS(meshClientTLSConfig())),
```

Do the same at `mesh_control_client.go:192-195` (deleting the `TODO(mTLS)` comment) and `mesh_gateway_client.go:137`. Drop the now-unused `credentials/insecure` imports from all three files.

- [ ] **Step 4: `grpc.Creds` on the MeshControl server**

At `coordinator.go:2219`, resolve the config before `grpc.NewServer` and add `grpc.Creds(credentials.NewTLS(meshCfg))` to the option list. Fail the listener on a cert error rather than degrading to plaintext — unlike the HTTP path, there is no plaintext posture to fall back to.

- [ ] **Step 5: Update the four production and five test call sites**

Production: `coordinator.go:1838, 2021, 2078, 2112`. Test: `host_network_test.go:97, 101, 560, 564` and `cell_transfer_executor_test.go:1121`.

Leave `host_network_test.go:71`'s `grpc.NewClient("localhost:1", insecure…)` **alone** and add a one-line comment saying why (it is a never-connecting sentinel), or a reviewer will "fix" it.

- [ ] **Step 6: Verify**

Run: `go vet ./...`
Expected: no errors. This is what catches all nine call sites.

Run: `go test ./pkg/universe/ -count=1 -p 1 -timeout 900s`
Expected: PASS.

Run: `go test ./examples/4node-basic/ -count=1 -timeout 900s`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/universe/mesh_tls.go pkg/universe/coordinator.go pkg/universe/host_network.go \
        pkg/universe/mesh_control_client.go pkg/universe/mesh_gateway_client.go \
        pkg/universe/host_network_test.go pkg/universe/cell_transfer_executor_test.go
git commit -m "feat(universe): serve both mesh channels over ephemeral in-memory TLS"
```

---

### Task A3: Cluster-secret join authentication

**Files:**
- Create: `pkg/universe/mesh_auth.go`
- Modify: `pkg/universe/host_network.go` (server options; `ConnectPeer` stream open ~line 250)
- Modify: `pkg/universe/coordinator.go` (MeshControl server options)
- Modify: `pkg/universe/mesh_control_client.go` (~line 212), `pkg/universe/mesh_gateway_client.go` (~line 154)
- Modify: `pkg/metrics/ingress.go`
- Modify: `pkg/universe/mesh_auth_test.go`

- [ ] **Step 1: Write the failing interceptor tests**

Add to `pkg/universe/mesh_auth_test.go`: an accepted stream with the right secret, `codes.Unauthenticated` with a wrong secret, `codes.Unauthenticated` with no metadata at all, and acceptance when the server secret is empty (criterion 7).

Run: `go test ./pkg/universe/ -run TestMeshAuth -count=1`
Expected: FAIL — `undefined: clusterSecretStreamInterceptor`.

- [ ] **Step 2: Implement `mesh_auth.go`**

```go
package universe

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// clusterSecretMDKey carries the shared cluster secret in gRPC call metadata
// at stream open. Lowercase ASCII is required: gRPC lowercases metadata keys,
// and a "-bin" suffix would force base64 encoding.
const clusterSecretMDKey = "mmokit-cluster-secret"

// peerIDMDKey carries the sending process's own ID at stream open. Under a
// shared cluster secret this is an assertion, not a proof — see the design
// spec's criterion 12. It buys outsider exclusion and per-stream identity
// consistency, not defence against an authenticated peer claiming another's
// ID.
const peerIDMDKey = "mmokit-peer-id"

// clusterSecretFingerprint is the first 4 bytes of SHA-256, hex-encoded. Log
// this, never the secret: remote hosts install a MeshControl log forwarder,
// so a logged secret ships over the channel it protects.
func clusterSecretFingerprint(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:4])
}

// clusterSecretStreamInterceptor rejects any stream that does not present a
// matching secret. A stream interceptor is sufficient and correct: both mesh
// services are bidi-stream-only, so a UnaryInterceptor would be dead code
// that a later reader mistakes for coverage.
//
// An empty want disables enforcement entirely (criterion 7: a distributed
// deployment with no configured secret warns once and continues).
func clusterSecretStreamInterceptor(want string) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if want == "" {
			return handler(srv, ss)
		}
		if !clusterSecretOK(ss.Context(), want) {
			return status.Error(codes.Unauthenticated, "mesh: cluster secret missing or incorrect")
		}
		return handler(srv, ss)
	}
}

func clusterSecretOK(ctx context.Context, want string) bool {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return false
	}
	// Compare unconditionally against the first value (or "") so an absent
	// secret costs the same as a wrong one.
	var got string
	if vs := md.Get(clusterSecretMDKey); len(vs) > 0 {
		got = vs[0]
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// peerIDFromContext returns the peer ID a stream claimed at open, or "".
func peerIDFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	if vs := md.Get(peerIDMDKey); len(vs) > 0 {
		return vs[0]
	}
	return ""
}
```

- [ ] **Step 3: Install the interceptor on both servers**

Add `grpc.ChainStreamInterceptor(clusterSecretStreamInterceptor(secret))` to the option list at `host_network.go:148` and at `coordinator.go:2219`.

- [ ] **Step 4: Attach metadata at all three stream-open sites**

At `host_network.go:250`, `mesh_control_client.go:212` and `mesh_gateway_client.go:154`, wrap the stream context:

```go
	if secret != "" {
		streamCtx = metadata.AppendToOutgoingContext(streamCtx,
			clusterSecretMDKey, secret,
			peerIDMDKey, ownProcessID)
	}
```

Use `metadata.AppendToOutgoingContext`, **not** `PerRPCCredentials`. `grpc@v1.80.0/clientconn.go:494-499` rejects insecure transport combined with a per-RPC credential demanding security; metadata sidesteps that entirely and costs zero per-frame bytes on a long-lived bidi stream.

- [ ] **Step 5: Add the rejection metric**

In `pkg/metrics/ingress.go`, add `ReasonUnauthenticatedPeer` **immediately before** `numIngressReasons`, and its `ingressReasonNames` entry in the same edit. `metrics.SurfaceMesh` already exists and `routeInboundFrame` already calls `RecordRejected`.

- [ ] **Step 6: Verify**

Run: `go test ./pkg/universe/ -run TestMeshAuth -count=1 -v`
Expected: PASS.

Run: `go test ./pkg/universe/ ./pkg/metrics/ -count=1 -p 1 -timeout 900s`
Expected: PASS — nothing sets `ClusterSecret` yet, so every fixture takes the `want == ""` path.

- [ ] **Step 7: Commit**

```bash
git add pkg/universe/mesh_auth.go pkg/universe/mesh_auth_test.go pkg/universe/host_network.go \
        pkg/universe/coordinator.go pkg/universe/mesh_control_client.go \
        pkg/universe/mesh_gateway_client.go pkg/metrics/ingress.go
git commit -m "feat(universe): authenticate mesh streams with a shared cluster secret"
```

---

### Task A4: Zero-config posture

**Files:**
- Modify: `pkg/universe/coordinator.go` (`Build()`, between `c.roles = roles` ~`:1606` and `startControlPlane()` ~`:1762`)
- Modify: `pkg/universe/mesh_auth_test.go`

- [ ] **Step 1: Add the role-gated block**

It must sit **after** the log-category enable at `:1715-1718` (`Logger.Log` early-returns on a disabled category) and **before** `startControlPlane()`. It cannot live in `New()` — `ParseRoles` runs once, at `:1602`. `Build()` works on the local copy `cfg := c.cfg`, so write back with `c.cfg = cfg`.

```go
	// Secret posture is decided from the role set, not from a loopback check
	// and not from CoordinatorAddr. isLoopbackBind returns false for an empty
	// host, so ":9100" — the default every dev recipe uses — never looks like
	// loopback; and the distributed coordinator has no CoordinatorAddr either.
	//
	// A coordinator+host process is definitionally a whole game in one
	// process and can never dial out (IsRemoteHost requires a lone host role;
	// isStandaloneGateway requires no coordinator role), so auto-generating
	// for it cannot split a cluster. Every other role set is a cluster member
	// that must be told the secret.
	selfContained := roles.Has(RoleCoordinator) && roles.Has(RoleHost)
	switch {
	case cfg.ClusterSecret != "":
		// Configured: enforce.
	case selfContained:
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return fmt.Errorf("generate cluster secret: %w", err)
		}
		cfg.ClusterSecret = hex.EncodeToString(buf)
		c.meshWarnOnce.Do(func() {
			c.Log.Log(CatMeshCell,
				"cluster: no --cluster-secret set; generated an ephemeral secret for this process "+
					"(fingerprint %s). Remote hosts and gateways must pass a matching --cluster-secret to join.",
				clusterSecretFingerprint(cfg.ClusterSecret))
		})
	default:
		c.meshWarnOnce.Do(func() {
			c.Log.Log(CatMeshCell,
				"cluster: WARNING no --cluster-secret set on a multi-process role set (%s); "+
					"mesh peers are UNAUTHENTICATED. Set --cluster-secret or MMO_CLUSTER_SECRET on every process.",
				cfg.Mode)
		})
	}
	c.cfg = cfg
```

Both log lines are behind `meshWarnOnce`. Without it, 37+ test functions reaching `newDistributedFixture` add roughly 100 lines to `go test ./pkg/universe` output — `CatMeshCell` is force-enabled via `StartupCategories`.

Both go through `c.Log.Log` (→ `log.Printf` → **stderr**), never `fmt.Println`: `just client-sdk` / `space-sdk` / `csharp-sdk` pipe server stdout into `cmd/sdkgen`.

- [ ] **Step 2: Test both branches**

```go
func TestBuild_AutoGeneratesSecretForSelfContainedRoles(t *testing.T) // Mode:"all" → non-empty, and a second Build gives a different value
func TestBuild_LeavesSecretEmptyForClusterRoles(t *testing.T)         // Mode:"coordinator" and Mode:"host" → still empty
func TestClusterSecretFingerprint_NeverContainsTheSecret(t *testing.T)
```

Run: `go test ./pkg/universe/ -run 'TestBuild_.*Secret|TestClusterSecretFingerprint' -count=1 -v`
Expected: PASS.

- [ ] **Step 3: Full-suite check**

Run: `go test ./... -count=1 -p 1 -timeout 900s`
Expected: PASS. The 18 mesh-exercising fixture `Config` sites all resolve to `coordinator`/`coordinator,gateway`/`host` role sets, so every one takes the criterion-7 path and **none needs a secret**. Do not edit them.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/coordinator.go pkg/universe/mesh_auth_test.go
git commit -m "feat(universe): auto-generate a cluster secret for self-contained role sets"
```

---

### Task A5: Phase A acceptance tests

**Files:**
- Modify: `pkg/universe/mesh_auth_test.go`

- [ ] **Step 1: Write the criterion tests against the right fixtures**

Criterion 3 is a **no-op on the `all` preset** — it has no `MeshData` listener at all, and `routeInboundFrame` is structurally unreachable there (`coordinator.go:1870-1979` never assigns `h.Network`). A criterion-3 test written against `all` is a false pass. Use `newDistributedFixture` (`cluster_fixture_distributed_test.go:24`) or a raw `NewHostNetwork` pair.

- Criterion 2: `RegisterHost` without the secret → `codes.Unauthenticated`, and `hostRegistry.Get(id)` stays nil.
- Criterion 3: a `MeshData` `Data` stream without the secret → rejected, and no frame reaches `routeInboundFrame` (assert via a counter or a spy).
- Criterion 4: a wrong secret is rejected identically to an absent one.
- Criterion 5: both listeners refuse a plaintext dial.
- Criterion 6: on `Mode:"all"`, `p.cfg.ClusterSecret != ""` after `Build()`.
- Criterion 7: on `Mode:"coordinator"` + `Mode:"host"` with no secret, a cluster still forms.

Run: `go test ./pkg/universe/ -run 'TestMeshAuth|TestClusterSecret' -count=1 -v`
Expected: PASS.

- [ ] **Step 2: Commit**

```bash
git add pkg/universe/mesh_auth_test.go
git commit -m "test(universe): pin CE-006 phase A criteria 2-7"
```

---

## Phase B — Control-plane authorization (criteria 8–11)

Highest-risk phase. It touches crash-reconnect, graceful drain and the operator command path.

### Task B1: Stream-bound identity at all twelve sites

**Files:**
- Modify: `pkg/universe/mesh_control_server.go` (`:364, 373, 383, 391, 394, 421, 427, 451, 480, 719, 776, 810`)

- [ ] **Step 1: Thread the registration-captured ID**

`:174` already captures `hostID := reg.HostId` at registration. For each of the twelve sites, compare the body-supplied ID against the stream-bound one and, on mismatch, **log and drop** — never rewrite. Do not "fix" a mismatch by substituting the correct value; that hides a misbehaving or hostile peer.

The twelve verified at HEAD: `CellRelease` dispatch (`:364`), `AssignCell` (`:373`), `ReleaseCell` (`:383`), `Touch` (`:391`), `applyRemoteCellMetrics` (`:394`), `notifyPlayerMigrated` (`:421`), `orchestrator.OnReady` (`:427`), `GracefulLeave` (`:451`), service announce (`:480`), `SessionKey` construction (`:719`), `registerAuthenticatedSession` (`:776`), the second service announce (`:810`).

- [ ] **Step 2: Tests**

One table test per site family: a matching ID takes effect, a mismatched ID is a no-op and increments the rejection metric.

Run: `go test ./pkg/universe/ -run TestMeshControl -count=1 -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/mesh_control_server.go pkg/universe/mesh_control_server_test.go
git commit -m "fix(universe): authorize MeshControl handlers against the stream-bound peer ID"
```

---

### Task B2: Registration admission

**Files:**
- Create: `pkg/universe/registration_admission.go`, `registration_admission_test.go`
- Modify: `pkg/universe/mesh_control_server.go` (before `registerHostStream` ~`:176`, before the gateway twin ~`:596`; add `cancelGatewayStream`)
- Modify: `pkg/universe/coord_assignment.go` (`checkLiveness` ~`:118-120`)

- [ ] **Step 1: Write the admission tests first**

Seven scenarios, each closing a verified break:

1. `host.kill` → readmitted within ~200 ms (the `killClosed` term).
2. Live incumbent heartbeating at 1 s → second `RegisterHost` **rejected** `codes.AlreadyExists`, `GrpcAddr` unchanged.
3. Incumbent silent >3 s → **admitted**, and `GrpcAddr` **does** change to the new `":0"` port. Assert the change, not its absence — this is where the criterion as originally worded is wrong.
4. `State == RemoteHostRegistered` with `LastHeartbeat` aged past 3 s → **admitted**.
5. `RegisterHost("local")` and `RegisterGateway("inproc")` → **rejected**, at both 0 s and 60 s of process uptime.
6. `RegisterHost` for an ID held by a live gateway stream → **rejected**.
7. `mesh_control_stream_test.go:24-116` still passes **unmodified**.

Run: `go test ./pkg/universe/ -run TestRegistrationAdmission -count=1`
Expected: FAIL — `undefined: admitHostRegistration`.

- [ ] **Step 2: Implement the predicate**

It must run **before** `registerHostStream`, which unconditionally swaps the map and closes the predecessor's kill channel.

```go
// killClosed reports whether someone has already ordered cs down — host.kill
// via cancelStream, or an eviction in flight. cancelStream closes the kill
// channel but does NOT delete the map entry; the delete happens later in the
// handler's defer. Without this term, host.kill becomes a 3s lockout that
// contradicts the verb's own documented contract.
func killClosed(cs *controlStream) bool {
	select {
	case <-cs.kill:
		return true
	default:
		return false
	}
}

func (s *meshControlServer) admitHostRegistration(hostID string) error {
	h := s.registry.Get(hostID)

	// (a) A Local entry can never be replaced by a remote stream, and this
	//     must be unconditional. checkLiveness skips Local entries BEFORE
	//     the staleness test and Touch is unreachable without a control
	//     stream, so a local host's LastHeartbeat is frozen at RegisterLocal
	//     time and is stale forever. A staleness-only rule would hand the
	//     well-known IDs "local" and "inproc" to any caller.
	if h != nil && h.Local {
		return status.Errorf(codes.AlreadyExists,
			"host id %q is the coordinator's in-process host", hostID)
	}

	s.mu.RLock()
	cs, gwCS := s.streams[hostID], s.gatewayStreams[hostID]
	s.mu.RUnlock()

	// (b) Cross-map collision: sendCoordMessageToHost falls back to
	//     gatewayStreams on an unenforced assumption that host and gateway
	//     IDs never collide, so a guard consulting only its own map is
	//     bypassable.
	if gwCS != nil && !killClosed(gwCS) {
		return status.Errorf(codes.AlreadyExists,
			"id %q is held by a live gateway control stream", hostID)
	}

	// (c) Still defending: a stream record exists, nobody has ordered it
	//     down, and the registry says it is heartbeating. State != Dead is
	//     sufficient but not necessary — a host wedged in Registered is
	//     never marked Dead, so the staleness term is what admits it.
	if cs != nil && !killClosed(cs) &&
		h != nil && h.State != RemoteHostDead &&
		time.Since(h.LastHeartbeat) <= deadThreshold {
		return status.Errorf(codes.AlreadyExists,
			"host %q already registered and heartbeating %s ago",
			hostID, time.Since(h.LastHeartbeat).Round(time.Millisecond))
	}
	return nil
}
```

Gateway twin: same shape against `gatewayStreams`/`gatewayRegistry`, substituting `gatewayDeadThreshold` so the guard and `checkGatewayLiveness` agree, and cross-checking `s.streams`.

Rejection must **return the status error and close the stream**, so `runConnection` sees an error and backs off. Do not silently decline to register — the client would heartbeat into a void for the life of the process.

- [ ] **Step 3: Land the two fixes that keep this from being a regression**

Both are required, not optional:

1. **Close the zombie.** Re-registration is the sole exit from `RemoteHostDead` (`Touch` has no `Dead→Live` arm; `Remove` is graceful-only) and a partition-healed host never re-registers, so a host marked Dead whose stream then recovers heartbeats forever with `State` stuck at Dead, excluded from `reassignOrphanedCells`' `liveIDs`. In `checkLiveness`, alongside `MarkDead`, call `s.controlServer.cancelStream(host.ID)`. That converts the zombie into a clean reconnect and makes the guard's stream term honest.
2. **Add `cancelGatewayStream`.** `cancelStream` consults only `s.streams`, so there is no operator escape hatch for a wedged gateway ID — and a locked-out gateway breaks client reconnect outright (Task B4).

- [ ] **Step 4: Verify**

Run: `go test ./pkg/universe/ -run 'TestRegistrationAdmission|TestControlStream' -count=1 -v`
Expected: PASS, with `mesh_control_stream_test.go` unmodified.

Run: `go test ./pkg/universe/ -count=1 -p 1 -race -timeout 900s`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/registration_admission.go pkg/universe/registration_admission_test.go \
        pkg/universe/mesh_control_server.go pkg/universe/coord_assignment.go
git commit -m "fix(universe): reject re-registration of an ID a live peer is still defending"
```

State the residual in the commit body: a genuine abrupt-loss restart inside the first 3 s (5 s for gateways) is rejected once and retried by the client's 200 ms-floor backoff. Bounded and self-healing.

---

### Task B3: Grants off the wire

**Files:**
- Modify: `proto/meshpb/mesh.proto` (`message Caller` ~`:387`, `message Grant` ~`:393`)
- Generated: `gen/go/meshpb/`
- Modify: `pkg/universe/cmdsys_transport.go` (`:204-208, 265-282, 284-299, 339`)
- Modify: `pkg/cmdsys/dispatcher.go`
- Modify: `pkg/universe/mesh_control_server.go` (`:463-469, 858-864, 930-945, 951-965, 968-974`)

- [ ] **Step 1: Delete the proto fields**

Remove `repeated Grant grants = 3;` from `message Caller` and delete `message Grant` entirely. `id = 1` and `source = 2` survive contiguously, so the repo's renumber-on-removal rule is already satisfied and nothing else moves.

Run: `just proto`
Then: `just fuzz-corpus`
Expected: `gen/go/meshpb/` regenerates; commit both together. `just fuzz-corpus` matters because `TestFuzzSeedCorpus` runs under the **required** CI `go test` job, not the nightly.

- [ ] **Step 2: Drop the Go conversions**

Delete `grantsToProto`; drop `Grants:` from `callerToProto` and from `callerFromProto`, which becomes:

```go
	return cmdsys.Caller{ID: pb.Id, Source: cmdsys.CallerSource(pb.Source)}
```

Fix `sendLocal`'s round-trip literal at `:204-208`, but keep it on a separate local-only constructor so it cannot be confused with the wire path — `Invoke`'s already-checked caller must flow straight through, ungutted.

- [ ] **Step 3: Make the trust relation explicit and greppable**

Pass peer authority as a parameter rather than inferring it:

```go
func executeCommandRequest(ctx context.Context, d *cmdsys.Dispatcher, hostID string, peer peerAuthority, req *meshpb.CommandRequest) *meshpb.CommandResponse
```

Pass `peerCoordinator` from `mesh_control_client.go:979` and `mesh_gateway_client.go:448` — both read from the stream the process itself dialed to `cfg.CoordinatorAddr`, so "this came from the coordinator" is structurally proven once Phase A authenticates the channel.

Inside, build `caller := cmdsys.Caller{ID: req.Caller.GetId(), Source: cmdsys.SourceMeshControl}` — extending the existing overwrite at `:339` from `Source` to the whole identity — and route through a new `Dispatcher.InvokeAsPeer(ctx, caller, verb, argsJSON)` that skips `Check` and emits audit.

**Do not synthesize `*.*` grants locally.** That recreates the hole in a different shape.

- [ ] **Step 4: Delete the two attacker-only receive paths**

Remove `case *meshpb.HostMessage_CommandRequest:` at `:463-469` and `:858-864`, plus `handleInboundCommandRequest` (`:930-945`) and `handleInboundCommandRequestFromGateway` (`:951-965`).

**Re-run this grep at implementation time — the absence of a producer is the entire justification:**

```bash
rg -n 'HostMessage_CommandRequest' --type go
```
Expected: only the two `case` labels being deleted. `meshControlTransport.Send` is the sole producer of `CoordMessage_CommandRequest` and errors when `controlServer == nil`, which is assigned only inside `startControlPlane` under the coordinator-role gate — so no non-coordinator can originate one.

This deletion is what closes the `admin.operator.create` → `*.*` escalation, more completely than a route check would: `InvokeLocal` ignores `cmd.Route` entirely, and `admin.operator.create` defaults grants to `["*.*"]` when its argument is empty.

- [ ] **Step 5: Clamp the attacker-controlled deadline**

`timeFromUnixNanos(req.DeadlineUnixNanos)` (`:968-974`) only defaults when `nanos <= 0`, so a far-future value yields an unbounded goroutine per request. Cap at a server-side maximum.

- [ ] **Step 6: Add the audit emission criterion 10 presupposes**

`InvokeLocal`/`InvokeAsPeer` emit nothing today and the production sink is `cmdsys.NoopAuditSink{}` (`coordinator.go:913`). Emit an `AuditRecord` carrying `Caller.ID`, the authenticated peer ID, the verb and the outcome, and replace `NoopAuditSink{}` with a real sink. Otherwise "preserved for audit" is satisfied by a field nobody reads.

- [ ] **Step 7: Verify**

Run: `go test ./pkg/universe -run TestCmdsys -count=1 -v`
Expected: PASS with only `TestCmdsys_SchemaVersionMismatch` edited, for its direct `executeCommandRequest` call at `cmdsys_meshcontrol_test.go:260`. No fixture needs a seeded grant store — a decisive advantage over resolving grants locally.

Run: `go test ./... -count=1 -p 1 -timeout 900s` and `./scripts/no_ark_in_game.sh`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add proto/meshpb/mesh.proto gen/go/meshpb/ pkg/universe/cmdsys_transport.go \
        pkg/cmdsys/dispatcher.go pkg/universe/mesh_control_server.go \
        pkg/universe/cmdsys_meshcontrol_test.go pkg/universe/testdata/
git commit -m "fix(universe,cmdsys,meshpb): stop taking RBAC grants off the mesh wire"
```

---

### Task B4: The two memory invariants, pinned

**Files:**
- Create/Modify: a distributed-fixture regression test

- [ ] **Step 1: Pin both**

Kill a host mid-drain and restart with the same `--host-id`, then assert:

1. `PlayerMigrated` still reaches the coordinator (`grpc_bridge.go:211`). A locked-out host keeps running its cells — nothing tears them down on a registration error — so every boundary handoff on that host would otherwise silently lose routing.
2. A reconnecting client on a standalone gateway still gets `IsReconnect=true` rather than `defaultSpawnLocation()`. A locked-out gateway sends `resolveSpawn` down its 2 s-deadline fallback, turning every reconnect into a fresh spawn at the centre of cell (0,0) — the duplicate-entity / clone-and-loot class, produced by the security fix.

Run: `go test ./pkg/universe/ -run TestAdmission_ -count=1 -timeout 900s`
Expected: PASS.

- [ ] **Step 2: Commit**

```bash
git add pkg/universe/registration_admission_test.go
git commit -m "test(universe): pin handoff notify and standalone-gateway reconnect against admission"
```

---

## Phase C — Payload-plane binding (criterion 12)

### Task C1: Thread the stream-captured peer ID

**Files:**
- Modify: `pkg/universe/host_network.go` (`routeInboundFrame` `:742`; `runPeerReceiver` `:327`)
- Modify: `pkg/universe/mesh_data_server.go`, `pkg/universe/grpc_bridge.go` (`:102`)
- Modify: 13 test call sites

- [ ] **Step 1: Change the signature**

```go
func (n *HostNetwork) routeInboundFrame(senderID string, frame *meshpb.MeshFrame) (err error)
```

Sixteen call sites, all in `pkg/universe`, all compiler-checked. Three production:

- `mesh_data_server.go:33` — capture **once before** the `for` loop via `peerIDFromContext(stream.Context())`. `MeshData_DataServer` embeds `grpc.ServerStream`, so no interceptor is needed, and Phase A already reads that context.
- `host_network.go:327` (`runPeerReceiver`) — pass `p.hostID`. Add a comment that in production the server end never `Send`s, so this path is frame-free today; pass the correct value anyway rather than `""`.
- `grpc_bridge.go:102` (always-proxy self-route) — pass `b.host.ID`. This bypasses gRPC entirely and is production-reachable via `--gateway-mode=always-proxy`.

That third site is why this is a parameter and not a proto field: the compiler demands a value there, whereas a field silently yields `""` and forces a choice between a silent drop and a silent bypass.

- [ ] **Step 2: Do not bulk-edit the test sites**

`virtual_conn_manager_test.go:310/341/348` call `routeInboundFrame(newReplicationReceiptFrame(hn.hostID, "gw-1", …))`. The marker host is `hn.hostID` — the *receiver*, by design — while the **sender** is the gateway. Pass `"gw-1"`. `:369` (`wrongHost`) exists specifically to prove the `:769` mismatch drop; preserve that distinction.

- [ ] **Step 3: Verify**

Run: `go vet ./...` then `go test ./pkg/universe/ -count=1 -p 1 -timeout 900s`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/host_network.go pkg/universe/mesh_data_server.go pkg/universe/grpc_bridge.go \
        pkg/universe/host_network_test.go pkg/universe/virtual_conn_manager_test.go \
        pkg/universe/cell_transfer_executor_test.go
git commit -m "refactor(universe): thread the stream-captured peer ID into routeInboundFrame"
```

---

### Task C2: Per-arm binding rules

**Files:**
- Modify: `pkg/universe/host_network.go` (`routeInboundFrame` arms)

- [ ] **Step 1: Apply the table, not a blanket check**

A blanket "payload identity == senderID" rule drops the hottest arm in the mesh. `ClientFrame.GatewayId` names the **receiving** gateway and both producers are hosts — one frame per player per tick at `TickRate = 20`.

| Arm | Rule |
| --- | --- |
| `ClientInput.GatewayId` | `== senderID` |
| `ClientDisconnect.GatewayId` | `== senderID` |
| `PlayerAssignment.GatewayId` | `== senderID` |
| `ServiceEvent.SourceProcessId` | `== senderID` |
| `ClientFrame.GatewayId` | `== n.hostID` — the receiver's own ID. This is the currently-missing untracked-path check |
| receipt marker host | `== n.hostID` — already checked at `:769`; keep |
| `CellTransferReady.HostId` | `== senderID`, or delete the arm (`:926-931`) — zero production producers |
| `CellTransferAbort` | delete the arm (`:934-946`) — zero constructions anywhere |
| `ForwardInput.GatewayId` | **no check** — legitimately names a third-party gateway on a host→host frame |
| `FromCellId` / `SourceCellId` | **no check** — needs the eventually-consistent `cellToHost` snapshot, stale by construction during commit windows |

Every mismatch logs and drops, and records `ReasonUnauthenticatedPeer` on `SurfaceMesh`.

- [ ] **Step 2: Close the three epoch bypasses in the same unit**

`host_network.go:817-822` reads `if !tracked && cf.Epoch > 0 { if sess := …; sess != nil && cf.Epoch < sess.epoch { drop } }` — three bypasses (`tracked`, `Epoch==0`, `sess==nil`), not the one the roadmap names. Reject `cf.Epoch == 0` and `sess == nil` on the untracked `ClientFrame` path, and reject `epoch == 0` in `InjectChannelInputWithEpoch`.

- [ ] **Step 3: Tests, against the right fixture**

Criterion 12 **cannot** be tested against the `all` preset — `routeInboundFrame` is structurally unreachable there. Use `newDistributedFixture` or a raw `NewHostNetwork` pair.

Run: `go test ./pkg/universe/ -run 'TestRouteInboundFrame|TestPayloadBinding' -count=1 -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/host_network.go pkg/universe/host_network_test.go pkg/universe/virtual_conn_manager.go
git commit -m "fix(universe): verify payload identities against the stream sender per arm"
```

---

### Task C3: Retire the replication-receipt `DestCellId` overload

**Files:**
- Modify: `proto/meshpb/mesh.proto`
- Generated: `gen/go/meshpb/`
- Modify: `pkg/universe/replication_receipt.go`, `virtual_conn_manager.go`, `host_network.go`

- [ ] **Step 1: Add both legs**

The overload is two-directional; land both or half survives.

- Outbound tracked leg: add `source_host_id` and `receipt_token` to `ClientFrame` (next free field 5), replacing `virtual_conn_manager.go:299`.
- Return leg: a new `ReplicationReceipt` message and a `MeshFrame` oneof arm (next free arm 14), replacing `replication_receipt.go:82/88/89`.

Then delete `replicationReceiptNamespacePrefix`, `replicationReceiptMarkerPrefix`, `replicationReceiptChannel` and the four parse helpers.

Run: `just proto` then `just fuzz-corpus`
Expected: regenerated `gen/go/meshpb/`; seed corpus updated. This arm **does** change bytes, and `TestFuzzSeedCorpus` runs under the required CI job.

- [ ] **Step 2: Verify**

Run: `go test ./pkg/universe/ -run 'TestReplicationReceipt|TestFuzzSeedCorpus' -count=1 -v`
Expected: PASS.

Run: `go test ./... -count=1 -p 1 -timeout 900s`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add proto/meshpb/mesh.proto gen/go/meshpb/ pkg/universe/replication_receipt.go \
        pkg/universe/virtual_conn_manager.go pkg/universe/host_network.go pkg/universe/testdata/
git commit -m "refactor(universe,meshpb): give replication receipts their own oneof arm"
```

---

## Phase D — Close-out

### Task D1: Recipes and documentation

**Files:**
- Modify: `justfile` (`:116, 120, 125, 129, 144`), `examples/4node-basic/justfile` (`:121, 128, 133, 138`)
- Modify: `cmd/server/README.md`, `docs/architecture.md` (`:169, :173, :195`), `SECURITY.md` (`:62-93`, `:108-110`)

- [ ] **Step 1: Recipe plumbing (hardening, not correctness)**

Under the role-gated design both distributed recipes work with no secret via the criterion-7 path. Add one anyway so the shipped recipes demonstrate the authenticated posture. Pass `--cluster-secret=` explicitly on each command string, or use `tmux new-session -e` / `split-window -e` — a bare `export` does **not** reliably reach panes when a tmux server is already running.

- [ ] **Step 2: Document the one regression**

Attaching `--mode=host --coordinator-addr=localhost:9100` to a `just dev` server now fails `codes.Unauthenticated`. That workflow is blessed by the in-source comment at `coordinator.go:1750-1759`. The fix is `--cluster-secret=<anything>` on both; it must appear in the flag help and in `cmd/server/README.md`'s `--control-listen` row.

- [ ] **Step 3: Criterion 11's stale-claim sweep**

The criterion as written omits two files that go stale the moment CE-006 lands:

- `SECURITY.md:62-93` carries a full mesh-insecurity disclosure with three file:line citations, and `:108-110` excuses mesh DoS reports on trusted-network grounds.
- `docs/architecture.md:169, :173, :195`.

Both `TODO(mTLS)`/`TODO(S4)` comments are already deleted in Task A2.

- [ ] **Step 4: Commit**

```bash
git add justfile examples/4node-basic/justfile cmd/server/README.md docs/architecture.md SECURITY.md
git commit -m "docs+build(ce-006): document the cluster secret and sweep stale mesh-insecurity claims"
```

---

### Task D2: Roadmap write-back and corrections

**Files:**
- Modify: `docs/roadmap.md`

- [ ] **Step 1: Correct the verified errors**

These are wrong at HEAD and must not be copied forward:

- `:194` — `host_network.go:232` → `:233` (`:230-232` is the TODO comment). `SECURITY.md:66` already has it right, so the roadmap is the stale copy.
- `:207` — `:769` → `:790`, `:802` → `:823`, `:796` → `:817`; and record **three** stale-epoch bypasses (`tracked`, `Epoch==0`, `sess==nil`), plus the fact that `cf.GatewayId` is never consulted on the untracked path before the socket write.
- `:225` — replace *"The mechanism is a real `sender_id` on `MeshFrame`"* with the metadata-plus-parameter mechanism. A server-populated proto3 field is never serialized, so it is a function parameter with a lockstep redeploy attached, and the paragraph's own "interned numeric peer ID" fallback argues about the width of a field never on the wire.
- `:227` — `gateway.go:1127`/`:1151` → `:1161`/`:1185`; **delete** *"There is no legitimate case where a frame's claimed identity differs from its stream sender"* and replace it with the three verified counterexamples; correct the `newReplicationReceiptFrame` description — it **echoes** `cf.GatewayId`, and self-equality comes from `gateway.go:754`.
- `:417` — replace the `RequireTransportSecurity()` reasoning, which states the mechanism backwards, and correct "both client-side tasks" to three dials.
- `:441` — add `just fuzz-corpus`.

- [ ] **Step 2: Re-scope criteria 9, 10 and 12**

Per the spec's §4.2, §4.4 and §4.3. Criterion 12's residual — that a shared secret leaves the stream-bound ID forgeable *between* cluster members — goes in the CE-006 close-out's **Residual, deliberately accepted** list, shaped like CE-002's at `:132-139`.

- [ ] **Step 3: Add the traps to §6.8.4**

- Criteria 3 and 12 cannot be tested against the `all` preset; `routeInboundFrame` is structurally unreachable there. Use `newDistributedFixture`.
- Do not stamp a per-frame identity in the send path: `service_event_dispatch.go` builds one frame and fans it to N peers, so a write there races in-flight marshaling.

- [ ] **Step 4: Flip the status**

§6.8.3 units 12–15 `open` → ``**done** `<sha>` ``; §6.3's heading fraction `**Open (0/12)**` → `**Done (12/12)**`. Changing the heading breaks two in-document anchors — update both referrers.

- [ ] **Step 5: Commit**

```bash
git add docs/roadmap.md
git commit -m "docs(roadmap): record CE-006 as done and correct six verified claims"
```

---

### Task D3: Final verification

- [ ] **Step 1: Full suite**

Run: `go vet ./...`
Expected: no errors. (Do **not** use `go build ./...` — per CLAUDE.md it drops binaries in package dirs.)

Run: `go test ./... -count=1 -p 1 -timeout 900s`
Expected: PASS.

Run: `go test ./pkg/universe/ -count=1 -p 1 -race -timeout 900s`
Expected: PASS.

Run: `./scripts/no_ark_in_game.sh` and `just build`
Expected: clean.

- [ ] **Step 2: Manual smoke — deliver inline in chat, never as a SMOKE.md**

Zero-config single process:
```bash
just dev
```
Expected: exactly one new log line, `cluster: no --cluster-secret set; generated an ephemeral secret for this process (fingerprint …)`. The game works unchanged.

Distributed, authenticated:
```bash
just distributed-space
```
Expected: cluster forms; no `WARNING … UNAUTHENTICATED` line.

Distributed, mismatched secret: start one host with a different `--cluster-secret`.
Expected: that host fails to register with `codes.Unauthenticated` and backs off; the rest of the cluster is unaffected.

---

## Self-Review

**Criteria coverage** (the twelve live at `docs/roadmap.md:210-222`; not restated here, per AGENTS.md's one-owning-document rule):

| Criterion | Task |
| --- | --- |
| 1 | A1 |
| 2, 4 | A3, A5 |
| 3 | A3, A5 (distributed fixture only) |
| 5 | A2 |
| 6, 7 | A4, A5 |
| 8 | B1 |
| 9 (re-scoped) | B2, B4 |
| 10 (re-scoped) | B3 |
| 11 | D1, D2 |
| 12 (re-scoped) | C1, C2 |

**Cross-task ordering.** A2 must precede A3 (the interceptor needs a channel to run on, and both flip together). A4 must follow A1. B2's zombie fix and gateway `cancelStream` must land **in the same commit** as the admission predicate or the guard is a net regression. C1 must precede C2. C3 is independent of C1/C2 and could land first.

**Scope additions beyond the roadmap's unit list, each with a reason:**
- *Required (prevents a regression this phase would otherwise introduce):* the `cancelStream`-on-`MarkDead` zombie fix and `cancelGatewayStream` (B2); the two invariant regression tests (B4).
- *Required (the criterion presupposes it and it does not exist):* audit emission and a real sink (B3).
- *Recommended (same files, already open, cheap):* the three epoch bypasses (C2); the deadline clamp and the two dead-arm deletions (B3, C2).

**Out of scope (per spec §6):** CA-based mTLS, certificate pinning, per-peer credentials and therefore insider peer impersonation, active-MITM defence, and the pre-existing coordinator-console chat moderation break — which must **not** be "fixed" by preserving `Source`.

**Risk note.** Phase B is the high-risk half. It touches crash-reconnect, graceful drain and the operator command path, and two of its failure modes are silent (routing loss on a locked-out host; duplicate entities on a locked-out gateway). B4 exists specifically to make those loud.
