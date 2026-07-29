package universe

import (
	"context"
	"flag"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
)

// TestNew_ClusterSecretFallsBackWithoutBindFlags pins the same §6.8.4 trap
// TestNew_WireLimitsFallBackWithoutBindFlags pins, for the cluster secret.
//
// New's `if !flag.Parsed()` guard is always false under `go test`, so BindFlags
// never runs and its MMO_CLUSTER_SECRET read never fires. If the env read lived
// only in BindFlags, every test fixture and every game that calls flag.Parse
// itself would silently come up with no secret.
func TestNew_ClusterSecretFallsBackWithoutBindFlags(t *testing.T) {
	if !flag.Parsed() {
		t.Fatal("flag.Parsed() is false under go test; the premise of this test " +
			"(and of New's guard) has changed")
	}

	t.Setenv(clusterSecretEnvVar, "from-env")

	c := New(Config{Mode: "all"})
	if got := c.cfg.ClusterSecret; got != "from-env" {
		t.Fatalf("ClusterSecret = %q, want %q from the env fallback in New", got, "from-env")
	}
}

// TestNew_ClusterSecretFieldBeatsEnv pins the precedence order's lower half.
// The flag > env half is exercised by BindFlags in a real process; here the
// preset field must win over the environment because a caller that set the
// field in Go was explicit about it.
func TestNew_ClusterSecretFieldBeatsEnv(t *testing.T) {
	t.Setenv(clusterSecretEnvVar, "from-env")

	c := New(Config{Mode: "all", ClusterSecret: "from-field"})
	if got := c.cfg.ClusterSecret; got != "from-field" {
		t.Fatalf("ClusterSecret = %q, want the preset field to beat the env", got)
	}
}

// TestClusterSecretFingerprint_NeverContainsTheSecret is the guard against
// someone "improving" the log line by including the value. A fingerprint that
// leaks its input is worse than no log line at all: remote hosts forward their
// logs over the very channel the secret protects.
func TestClusterSecretFingerprint_NeverContainsTheSecret(t *testing.T) {
	const secret = "super-secret-value"
	fp := clusterSecretFingerprint(secret)

	if strings.Contains(fp, secret) {
		t.Fatalf("fingerprint %q contains the secret", fp)
	}
	if len(fp) != 8 {
		t.Fatalf("fingerprint %q has length %d, want 8 hex chars (4 bytes)", fp, len(fp))
	}
	if fp == clusterSecretFingerprint(secret+"x") {
		t.Fatal("fingerprint does not distinguish different secrets")
	}
	if fp != clusterSecretFingerprint(secret) {
		t.Fatal("fingerprint is not stable across calls")
	}
}

// fakeServerStream is the minimum grpc.ServerStream the interceptor touches.
type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f fakeServerStream) Context() context.Context { return f.ctx }

func incomingCtx(pairs ...string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(pairs...))
}

// TestMeshAuth_InterceptorRejectsWrongAndAbsentSecret pins criteria 2 and 4:
// an unauthenticated stream is refused with codes.Unauthenticated, and a wrong
// secret is refused exactly as firmly as an absent one.
func TestMeshAuth_InterceptorRejectsWrongAndAbsentSecret(t *testing.T) {
	const want = "correct-horse-battery-staple"

	cases := []struct {
		name    string
		ctx     context.Context
		wantErr bool
	}{
		{"correct secret", incomingCtx(clusterSecretMDKey, want), false},
		{"wrong secret", incomingCtx(clusterSecretMDKey, "nope"), true},
		{"empty secret value", incomingCtx(clusterSecretMDKey, ""), true},
		{"key absent", incomingCtx("unrelated", "x"), true},
		{"no metadata at all", context.Background(), true},
		// A prefix of the right secret must not pass: ConstantTimeCompare
		// returns 0 on a length mismatch, but assert it rather than trust it.
		{"prefix of the secret", incomingCtx(clusterSecretMDKey, want[:5]), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			handler := func(any, grpc.ServerStream) error { called = true; return nil }

			err := clusterSecretStreamInterceptor(want)(nil, fakeServerStream{ctx: tc.ctx}, nil, handler)

			if tc.wantErr {
				if err == nil {
					t.Fatal("expected the stream to be rejected")
				}
				if got := status.Code(err); got != codes.Unauthenticated {
					t.Fatalf("status code = %v, want %v", got, codes.Unauthenticated)
				}
				if called {
					t.Fatal("handler ran despite rejection — the frame reached the service")
				}
				return
			}
			if err != nil {
				t.Fatalf("expected acceptance, got %v", err)
			}
			if !called {
				t.Fatal("handler did not run on an authenticated stream")
			}
		})
	}
}

// TestMeshAuth_EmptyServerSecretAcceptsEveryone pins criterion 7: a role set
// with no configured secret warns and continues rather than failing closed.
// This is what keeps every distributed test fixture and both dev recipes
// working without a secret.
func TestMeshAuth_EmptyServerSecretAcceptsEveryone(t *testing.T) {
	called := false
	handler := func(any, grpc.ServerStream) error { called = true; return nil }

	err := clusterSecretStreamInterceptor("")(nil, fakeServerStream{ctx: context.Background()}, nil, handler)
	if err != nil {
		t.Fatalf("unenforced interceptor rejected a stream: %v", err)
	}
	if !called {
		t.Fatal("handler did not run under the unenforced posture")
	}
}

// TestMeshAuth_PeerIDRoundTrip pins that the ID a dialer attaches is the ID the
// server reads back, and that an absent ID is "" rather than a wildcard.
func TestMeshAuth_PeerIDRoundTrip(t *testing.T) {
	ctx := outgoingMeshMD(context.Background(), "s3cret", "host-a")
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		t.Fatal("no outgoing metadata attached")
	}
	if got := md.Get(peerIDMDKey); len(got) != 1 || got[0] != "host-a" {
		t.Fatalf("peer id metadata = %v, want [host-a]", got)
	}

	if got := peerIDFromContext(incomingCtx(peerIDMDKey, "host-a")); got != "host-a" {
		t.Fatalf("peerIDFromContext = %q, want host-a", got)
	}
	if got := peerIDFromContext(context.Background()); got != "" {
		t.Fatalf("peerIDFromContext with no metadata = %q, want empty", got)
	}
}

// TestMeshAuth_NoSecretStillAttachesPeerID pins the split between the two
// metadata keys, which is subtle enough to have already caused one outage in
// development: the secret decides whether a stream may speak at all, the peer
// ID decides which payload identities its frames may claim.
//
// Withholding the ID when no secret is configured makes every bound arm in
// routeInboundFrame drop, because an empty stream identity is treated as
// unattributable. That silently killed the service event bus in every
// secret-less fixture.
func TestMeshAuth_NoSecretStillAttachesPeerID(t *testing.T) {
	ctx := outgoingMeshMD(context.Background(), "", "host-a")

	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		t.Fatal("no metadata attached at all under the unauthenticated posture")
	}
	if len(md.Get(clusterSecretMDKey)) > 0 {
		t.Fatal("attached a secret despite none being configured")
	}
	if got := md.Get(peerIDMDKey); len(got) != 1 || got[0] != "host-a" {
		t.Fatalf("peer id = %v, want [host-a] even with no secret", got)
	}
}

// meshAuthPair stands up two real HostNetworks on ":0" with the given secrets
// and returns them plus B's destination cell inbox.
//
// Deliberately raw NewHostNetwork rather than the "all" preset: the all preset
// never assigns Host.Network, so newBridgeForCell hands back a plain cellBridge
// and routeInboundFrame is structurally unreachable. A criterion-3 test written
// against "all" is a false pass.
func meshAuthPair(t *testing.T, secretA, secretB string) (*HostNetwork, *HostNetwork, chan CellMessage) {
	t.Helper()
	hostA := NewHost("auth-a")
	hostB := NewHost("auth-b")

	netA, err := NewHostNetwork(hostA, ":0", testHostNetworkLogger(t), 50*time.Millisecond, testMeshTLS(t), secretA)
	if err != nil {
		t.Fatalf("NewHostNetwork A: %v", err)
	}
	netB, err := NewHostNetwork(hostB, ":0", testHostNetworkLogger(t), 50*time.Millisecond, testMeshTLS(t), secretB)
	if err != nil {
		t.Fatalf("NewHostNetwork B: %v", err)
	}
	t.Cleanup(func() {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = netA.Shutdown() }()
		go func() { defer wg.Done(); _ = netB.Shutdown() }()
		wg.Wait()
	})

	cellB := NewCell("cell_0_0", CellID{X: 0, Y: 0})
	cellB.Inbox = make(chan CellMessage, 16)
	hostB.AddCell(CellID{X: 0, Y: 0}, cellB)

	return netA, netB, cellB.Inbox
}

func meshAuthBorderFrame(t *testing.T) *meshpb.MeshFrame {
	t.Helper()
	frame, err := encodeCellMessage(CellMessage{
		Type:        MsgBorderFrame,
		FromCellID:  "cell_1_0",
		BorderFrame: []byte{0x01, 0x02, 0x03},
	}, "cell_0_0")
	if err != nil {
		t.Fatalf("encodeCellMessage: %v", err)
	}
	return frame
}

// TestMeshAuth_MatchingSecretDelivers is the positive control for the two tests
// below: with a shared secret the payload plane behaves exactly as before.
func TestMeshAuth_MatchingSecretDelivers(t *testing.T) {
	const secret = "shared-cluster-secret"
	netA, netB, inbox := meshAuthPair(t, secret, secret)

	if err := netA.ConnectPeer("auth-b", netB.Addr(), peerKindNode); err != nil {
		t.Fatalf("ConnectPeer: %v", err)
	}
	if err := netA.WaitPeersReady([]string{"auth-b"}, 2*time.Second); err != nil {
		t.Fatalf("WaitPeersReady: %v", err)
	}
	if err := netA.SendReliable("auth-b", meshAuthBorderFrame(t)); err != nil {
		t.Fatalf("SendReliable: %v", err)
	}

	select {
	case msg := <-inbox:
		if msg.Type != MsgBorderFrame {
			t.Fatalf("inbox got %v, want MsgBorderFrame", msg.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("authenticated frame never arrived")
	}
}

// TestMeshAuth_WrongSecretDeliversNothing pins criterion 3 on the payload
// plane: a MeshData stream that cannot authenticate delivers no frame to
// routeInboundFrame. The assertion is on delivery rather than on ConnectPeer's
// return value because gRPC surfaces a stream-interceptor rejection on the
// first Send/Recv, not necessarily at stream open.
func TestMeshAuth_WrongSecretDeliversNothing(t *testing.T) {
	netA, netB, inbox := meshAuthPair(t, "attacker-guess", "real-cluster-secret")

	_ = netA.ConnectPeer("auth-b", netB.Addr(), peerKindNode)
	_ = netA.SendReliable("auth-b", meshAuthBorderFrame(t))

	select {
	case msg := <-inbox:
		t.Fatalf("an unauthenticated frame reached the cell inbox: %v", msg.Type)
	case <-time.After(750 * time.Millisecond):
	}
}

// TestMeshAuth_NoSecretDeliversNothingToEnforcingPeer covers the outsider case
// the wrong-secret test does not: a peer that presents no credential at all.
func TestMeshAuth_NoSecretDeliversNothingToEnforcingPeer(t *testing.T) {
	netA, netB, inbox := meshAuthPair(t, "", "real-cluster-secret")

	_ = netA.ConnectPeer("auth-b", netB.Addr(), peerKindNode)
	_ = netA.SendReliable("auth-b", meshAuthBorderFrame(t))

	select {
	case msg := <-inbox:
		t.Fatalf("an unauthenticated frame reached the cell inbox: %v", msg.Type)
	case <-time.After(750 * time.Millisecond):
	}
}

// TestBuild_AutoGeneratesSecretForSelfContainedRoles pins criterion 6. The
// "all" preset must be closed by default — and this must NOT be gated on a
// loopback check, because --control-listen defaults to ":9100" and
// isLoopbackBind treats an empty host as all-interfaces, so the heuristic
// would never fire for the bind every dev recipe actually uses.
func TestBuild_AutoGeneratesSecretForSelfContainedRoles(t *testing.T) {
	for _, mode := range []string{"all", "coordinator,host", "coordinator,host,gateway"} {
		t.Run(mode, func(t *testing.T) {
			c := New(Config{Mode: mode, ControlListen: "", AdminListen: ""})
			c.Build()

			if c.cfg.ClusterSecret == "" {
				t.Fatalf("mode %q left the mesh unauthenticated; criterion 6 requires an auto-generated secret", mode)
			}
			if len(c.cfg.ClusterSecret) < 32 {
				t.Fatalf("generated secret is only %d chars; want 32 bytes of crypto/rand hex", len(c.cfg.ClusterSecret))
			}
		})
	}
}

// TestBuild_GeneratedSecretsAreUnique guards against a constant or a
// process-global sneaking in. A package-level sync.Once value would make every
// in-process fixture agree and look correct here, while breaking every real
// multi-process deployment — five OS processes would each draw their own.
func TestBuild_GeneratedSecretsAreUnique(t *testing.T) {
	a := New(Config{Mode: "all"})
	a.Build()
	b := New(Config{Mode: "all"})
	b.Build()

	if a.cfg.ClusterSecret == b.cfg.ClusterSecret {
		t.Fatal("two processes generated an identical secret; the value is not per-process crypto/rand")
	}
}

// TestClusterSecretPosture_ByRoleSet is the full 6/7 reconciliation matrix.
//
// It drives resolveClusterSecretPosture directly rather than Build: the
// coordinator-bearing modes would each bind a real MeshControl listener on
// :9100 and collide with one another, and the posture decision is a pure
// function of the role set anyway. TestBuild_AutoGeneratesSecretForSelfContainedRoles
// covers the wiring from Build.
func TestClusterSecretPosture_ByRoleSet(t *testing.T) {
	cases := []struct {
		mode         string
		wantGenerate bool
	}{
		// Self-contained: a whole game in one process, which can never dial
		// out, so generating cannot split anything.
		{"all", true},
		{"coordinator,host", true},
		{"coordinator,host,gateway", true},
		// Cluster members: generating here would give each OS process a
		// different secret and no host could ever join.
		{"coordinator", false},
		{"coordinator,gateway", false},
		{"host", false},
		{"gateway", false},
		{"service", false},
		{"gateway,service", false},
	}

	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			roles, err := ParseRoles(tc.mode)
			if err != nil {
				t.Fatalf("ParseRoles(%q): %v", tc.mode, err)
			}
			c := &Process{Log: testHostNetworkLogger(t)}
			cfg := Config{Mode: tc.mode}

			c.resolveClusterSecretPosture(&cfg, roles)

			if got := cfg.ClusterSecret != ""; got != tc.wantGenerate {
				t.Fatalf("mode %q generated=%v, want %v", tc.mode, got, tc.wantGenerate)
			}
		})
	}
}

// TestClusterSecretPosture_ConfiguredSecretIsNeverOverwritten pins that an
// operator-supplied secret survives on every role set.
func TestClusterSecretPosture_ConfiguredSecretIsNeverOverwritten(t *testing.T) {
	for _, mode := range []string{"all", "coordinator", "host"} {
		t.Run(mode, func(t *testing.T) {
			roles, err := ParseRoles(mode)
			if err != nil {
				t.Fatalf("ParseRoles(%q): %v", mode, err)
			}
			c := &Process{Log: testHostNetworkLogger(t)}
			cfg := Config{Mode: mode, ClusterSecret: "operator-supplied"}

			c.resolveClusterSecretPosture(&cfg, roles)

			if cfg.ClusterSecret != "operator-supplied" {
				t.Fatalf("mode %q overwrote the configured secret with %q", mode, cfg.ClusterSecret)
			}
		})
	}
}

// TestMeshAuth_UnauthenticatedRegisterHostIsRejected pins criterion 2 on the
// control plane: a host presenting no cluster secret is refused, and — the
// half that actually matters — the coordinator's registry is left untouched,
// so nothing downstream can be fooled into assigning it cells.
func TestMeshAuth_UnauthenticatedRegisterHostIsRejected(t *testing.T) {
	coord := New(Config{
		CellsX: 1, CellsY: 1,
		Mode:                "coordinator",
		ControlListen:       "127.0.0.1:0",
		ClusterSecret:       "coord-cluster-secret",
		Headless:            true,
		SettleWindow:        50 * time.Millisecond,
		ShutdownGracePeriod: 50 * time.Millisecond,
	})
	coord.Build()
	t.Cleanup(coord.Shutdown)

	coordAddr := coord.controlListener.Addr().String()

	// A host with NO secret against a coordinator that requires one.
	node := New(Config{
		CellsX: 1, CellsY: 1,
		Mode:                "host",
		CoordinatorAddr:     coordAddr,
		HostID:              "intruder",
		Headless:            true,
		ShutdownGracePeriod: 50 * time.Millisecond,
	})
	node.Build()
	t.Cleanup(node.Shutdown)

	// The client retries with backoff, so poll rather than sleeping once:
	// the assertion is that it never lands, not that it has not landed yet.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h := coord.hostRegistry.Get("intruder"); h != nil {
			t.Fatalf("an unauthenticated host registered: state=%v addr=%q", h.State, h.GrpcAddr)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestMeshAuth_AuthenticatedRegisterHostSucceeds is the positive control for
// the test above: the same topology with a matching secret must register, or
// the negative result proves nothing about authentication.
func TestMeshAuth_AuthenticatedRegisterHostSucceeds(t *testing.T) {
	const secret = "coord-cluster-secret"

	coord := New(Config{
		CellsX: 1, CellsY: 1,
		Mode:                "coordinator",
		ControlListen:       "127.0.0.1:0",
		ClusterSecret:       secret,
		Headless:            true,
		SettleWindow:        50 * time.Millisecond,
		ShutdownGracePeriod: 50 * time.Millisecond,
	})
	coord.Build()
	t.Cleanup(coord.Shutdown)

	node := New(Config{
		CellsX: 1, CellsY: 1,
		Mode:                "host",
		CoordinatorAddr:     coord.controlListener.Addr().String(),
		HostID:              "member",
		ClusterSecret:       secret,
		Headless:            true,
		ShutdownGracePeriod: 50 * time.Millisecond,
	})
	node.Build()
	t.Cleanup(node.Shutdown)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if coord.hostRegistry.Get("member") != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("an authenticated host never registered; the secret path is broken")
}

// TestMeshAuth_ListenersAreNonPlaintext pins criterion 5 by dialing each mesh
// listener with insecure credentials and asserting the RPC cannot complete.
func TestMeshAuth_ListenersAreNonPlaintext(t *testing.T) {
	coord := New(Config{
		CellsX: 1, CellsY: 1,
		Mode:                "coordinator",
		ControlListen:       "127.0.0.1:0",
		Headless:            true,
		SettleWindow:        50 * time.Millisecond,
		ShutdownGracePeriod: 50 * time.Millisecond,
	})
	coord.Build()
	t.Cleanup(coord.Shutdown)

	_, netB, _ := meshAuthPair(t, "", "")

	// Each listener is probed with its OWN service client. Probing MeshControl
	// with a MeshData client would fail as Unimplemented even over a working
	// TLS channel, and the test would pass for the wrong reason.
	cases := []struct {
		name string
		addr string
		open func(*grpc.ClientConn, context.Context) error
	}{
		{
			name: "MeshControl",
			addr: coord.controlListener.Addr().String(),
			open: func(conn *grpc.ClientConn, ctx context.Context) error {
				s, err := meshpb.NewMeshControlClient(conn).Control(ctx)
				if err != nil {
					return err
				}
				_, err = s.Recv()
				return err
			},
		},
		{
			name: "MeshData",
			addr: netB.Addr(),
			open: func(conn *grpc.ClientConn, ctx context.Context) error {
				s, err := meshpb.NewMeshDataClient(conn).Data(ctx)
				if err != nil {
					return err
				}
				_, err = s.Recv()
				return err
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := grpc.NewClient(tc.addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				t.Fatalf("grpc.NewClient: %v", err)
			}
			defer conn.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			// Opening the stream may succeed lazily; the handshake failure
			// surfaces on the first Recv.
			err = tc.open(conn, ctx)
			if err == nil {
				t.Fatalf("%s accepted a plaintext client; the listener is not TLS", tc.name)
			}
			if got := status.Code(err); got == codes.Unimplemented {
				t.Fatalf("%s rejected for the wrong reason (%v) — the probe used the wrong service", tc.name, got)
			}
		})
	}
}
