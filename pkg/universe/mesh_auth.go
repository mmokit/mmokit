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
// at stream open. Lowercase ASCII is required — gRPC lowercases metadata keys,
// so a mixed-case constant would silently never match on the server side — and
// a "-bin" suffix is deliberately avoided because it would force base64.
const clusterSecretMDKey = "mmokit-cluster-secret"

// peerIDMDKey carries the sending process's own ID at stream open.
//
// Under a single shared cluster secret this is an assertion, not a proof: any
// peer holding the secret could claim any ID. It buys outsider exclusion and
// per-stream identity consistency — one claimed ID for a stream's lifetime
// instead of a per-frame free-for-all — which is what kills the "one stream,
// many identities" attack and makes logs correlatable. Defending against an
// authenticated peer impersonating another requires per-peer credentials and
// is out of scope for CE-006.
const peerIDMDKey = "mmokit-peer-id"

// outgoingMeshMD attaches the join credentials to a stream context. Callers
// pass their own process ID; there is no path on which a process legitimately
// claims someone else's.
//
// Metadata rather than PerRPCCredentials on purpose: both mesh services are
// bidi-stream-only and each peer opens exactly one long-lived stream, so this
// costs zero per-frame bytes, and it sidesteps grpc-go's insecure-transport
// validation of credentials that require transport security.
func outgoingMeshMD(ctx context.Context, secret, selfID string) context.Context {
	if secret == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx,
		clusterSecretMDKey, secret,
		peerIDMDKey, selfID)
}

// clusterSecretStreamInterceptor rejects any stream that does not present a
// matching secret.
//
// A stream interceptor alone is sufficient and correct: MeshControl.Control
// and MeshData.Data are the only RPCs on either service and both are bidi
// streaming, so a UnaryInterceptor would be dead code that a later reader
// mistakes for coverage.
//
// An empty want disables enforcement (CE-006 criterion 7: a role set that is
// not self-contained and has no configured secret warns once and continues).
func clusterSecretStreamInterceptor(want string) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if want == "" {
			return handler(srv, ss)
		}
		if !clusterSecretOK(ss.Context(), want) {
			return status.Error(codes.Unauthenticated, "mesh: cluster secret missing or incorrect")
		}
		return handler(srv, ss)
	}
}

// clusterSecretOK compares the presented secret against want in constant time.
// An absent secret takes the same path as a wrong one — the comparison always
// runs — so the two are indistinguishable to a caller measuring latency.
func clusterSecretOK(ctx context.Context, want string) bool {
	var got string
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vs := md.Get(clusterSecretMDKey); len(vs) > 0 {
			got = vs[0]
		}
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// peerIDFromContext returns the peer ID a stream claimed at open, or "" when
// the stream predates the field or presented none. Callers must treat "" as
// unattributable rather than as a wildcard.
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

// clusterSecretEnvVar is the environment fallback for --cluster-secret. It is
// read in two places on purpose — BindFlags (so it becomes the flag default,
// giving flag > env > preset field) and New (because BindFlags is skipped
// under go test and for any game that calls flag.Parse itself).
const clusterSecretEnvVar = "MMO_CLUSTER_SECRET"

// clusterSecretFingerprint is the first 4 bytes of SHA-256, hex-encoded. Log
// this, never the secret itself: remote hosts install a MeshControl log
// forwarder, so a logged secret would ship over the very channel it protects.
//
// It is a fingerprint, not a credential — two processes showing the same value
// agree on the secret, which is the only operational question a log line needs
// to answer.
func clusterSecretFingerprint(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:4])
}
