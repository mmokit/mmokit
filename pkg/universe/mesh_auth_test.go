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

// TestMeshAuth_NoSecretAttachesNoMetadata pins that the unauthenticated posture
// sends nothing rather than an empty credential, so a server that IS enforcing
// rejects it on the absent-key path.
func TestMeshAuth_NoSecretAttachesNoMetadata(t *testing.T) {
	ctx := outgoingMeshMD(context.Background(), "", "host-a")
	if md, ok := metadata.FromOutgoingContext(ctx); ok && len(md.Get(clusterSecretMDKey)) > 0 {
		t.Fatal("attached a secret despite none being configured")
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
