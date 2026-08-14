package universe

import (
	pkgnet "github.com/zenion/mmokit/pkg/net"
)

// The reflection decoder runs on two surfaces with genuinely different threat
// models, so it runs under two limit profiles rather than one number for both.
//
//   - The CLIENT profile bounds bodies an unauthenticated remote peer supplies.
//     It is derived from Config.WireLimits, so an operator can tighten it, and
//     it is applied through ReflectUnmarshalStrict — a body that is not exactly
//     the size of the type it claims to be is a disagreement about the type,
//     not a producer appending something.
//
//   - The MESH profile bounds bodies produced by this cluster's own encoder:
//     border component blobs, transfer frames, service-event payloads, and the
//     client-side decode in the reference game's bot client. It is fixed rather than configurable
//     because it is not a security control — the peer is already trusted at the
//     transport layer (CE-006 owns making that true) — and it exists only so
//     the decoder cannot be tripped by the codec's own legitimate output.
//
// Both profiles are values. They are passed down the decode recursion by value
// inside decodeState and are never stored in a package-level variable: CE-010
// counts coords.CellSize plus four package-global wire registries as the debt
// gating the whole 2D/3D program, and this unit is not adding a fifth.

// clientProfile derives the ingress limit profile from an operator-supplied
// base (Config.WireLimits, which may be the zero value).
//
// It normalizes the base and then applies the one rule that is a property of
// the surface rather than of the deployment: no []byte field is decodable from
// a client. See WireLimits.RejectByteFields for the evidence that no
// client-reachable registered type has one.
func clientProfile(base pkgnet.WireLimits) pkgnet.WireLimits {
	l := base.Normalized()
	l.RejectByteFields = true
	return l
}

// meshProfile returns the limit profile the tolerant wrappers
// (ReflectUnmarshalOnStage / ReflectUnmarshal) decode under.
//
// It is sized against meshMaxMsgBytes, the gRPC send/recv cap every mesh
// stream is already configured with, so the decoder can never be the thing
// that refuses a message the transport was willing to carry. That matters
// concretely: DefaultWireLimits caps aggregate allocation at 1 MiB and a
// single []byte field at 1 MiB, but valueSize records that WorldDelta bodies
// "routinely exceed 65535 bytes for stress-test densities" and a merge-drain
// transfer frame is the whole donor cell. The string and slice ceilings are
// raised to the wire widths for the same reason — an op RESPONSE decoded on
// the console path or a component blob carrying a long name is the encoder's
// own output, and truncating it would be a bug, not a defence.
//
// MaxDepth is NOT raised. Nesting depth is fixed by the static Go type graph
// (see decodeState.enter), so widening it would buy nothing and would only
// weaken the one arm that is cheap to keep tight.
func meshProfile() pkgnet.WireLimits {
	l := pkgnet.DefaultWireLimits()
	l.MaxFrameBytes = meshMaxMsgBytes
	l.MaxBytesFieldLen = meshMaxMsgBytes
	l.MaxTotalAllocBytes = meshMaxMsgBytes
	l.MaxStringBytes = maxWireStringLen
	l.MaxSliceElems = maxWireSliceElems
	return l
}

// clientWireLimits returns the frozen client ingress profile for this process.
// Resolved once in New() from Config.WireLimits; never re-read from cfg, which
// Build() reassigns wholesale in three places.
func (c *Process) clientWireLimits() pkgnet.WireLimits {
	if c == nil {
		return clientProfile(pkgnet.WireLimits{})
	}
	return c.wireLimits
}

// clientWireLimits returns the owning Process's client ingress profile, or the
// defaults for a Stage built without one. Fixtures across pkg/universe and
// pkg/mmokit construct a bare Stage, and a zero WireLimits would otherwise
// reject every body.
func (b *Stage) clientWireLimits() pkgnet.WireLimits {
	if b == nil || b.coord == nil {
		return clientProfile(pkgnet.WireLimits{})
	}
	return b.coord.clientWireLimits()
}
