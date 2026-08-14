package universe

import (
	"flag"
	"testing"

	pkgnet "github.com/zenion/mmokit/pkg/net"
)

// TestNew_WireLimitsFallBackWithoutBindFlags pins §6.8.4's "flag defaults never
// reach tests" trap on the limit set specifically.
//
// New's `if !flag.Parsed()` guard is always false under `go test` — testing.M
// parses the command line before any test runs — so BindFlags never executes
// here and --wire-max-* never contributes a thing. Every default therefore has
// to arrive through WireLimits.Normalized inside New. This fixture deliberately
// never calls BindFlags, which is the whole point: if the defaults were only
// wired through the flag layer, c.wireLimits would come out zeroed and the
// decoder would refuse every client body in every test and every embedding that
// builds its Config in Go.
func TestNew_WireLimitsFallBackWithoutBindFlags(t *testing.T) {
	if !flag.Parsed() {
		t.Fatal("flag.Parsed() is false under go test; the premise of this test " +
			"(and of New's guard) has changed")
	}

	c := New(Config{Mode: "all"})
	got := c.clientWireLimits()
	want := pkgnet.DefaultWireLimits()

	if got.MaxFrameBytes != want.MaxFrameBytes {
		t.Errorf("MaxFrameBytes = %d, want the default %d", got.MaxFrameBytes, want.MaxFrameBytes)
	}
	if got.MaxStringBytes != want.MaxStringBytes {
		t.Errorf("MaxStringBytes = %d, want the default %d", got.MaxStringBytes, want.MaxStringBytes)
	}
	if got.MaxSliceElems != want.MaxSliceElems {
		t.Errorf("MaxSliceElems = %d, want the default %d", got.MaxSliceElems, want.MaxSliceElems)
	}
	if got.MaxDepth != want.MaxDepth {
		t.Errorf("MaxDepth = %d, want the default %d", got.MaxDepth, want.MaxDepth)
	}
	if got.MaxTotalAllocBytes != want.MaxTotalAllocBytes {
		t.Errorf("MaxTotalAllocBytes = %d, want the default %d",
			got.MaxTotalAllocBytes, want.MaxTotalAllocBytes)
	}
	if !got.RejectByteFields {
		t.Error("RejectByteFields = false on the client profile; the []byte arm is " +
			"the only place the codec reads a u32 length and no client-reachable " +
			"registered type has such a field")
	}
}

// TestNew_WireLimitsHonoursConfig is criterion 3's configurable half: a value
// set on Config must survive into the frozen profile rather than being
// overwritten by the default, and the fields left zero must still fall back.
func TestNew_WireLimitsHonoursConfig(t *testing.T) {
	c := New(Config{
		Mode: "all",
		WireLimits: pkgnet.WireLimits{
			MaxStringBytes: 64,
			MaxSliceElems:  8,
		},
	})
	got := c.clientWireLimits()

	if got.MaxStringBytes != 64 {
		t.Errorf("MaxStringBytes = %d, want the configured 64", got.MaxStringBytes)
	}
	if got.MaxSliceElems != 8 {
		t.Errorf("MaxSliceElems = %d, want the configured 8", got.MaxSliceElems)
	}
	if want := pkgnet.DefaultWireLimits().MaxDepth; got.MaxDepth != want {
		t.Errorf("MaxDepth = %d, want the default %d for an unset field", got.MaxDepth, want)
	}
}

// TestMeshProfileIsWiderThanTheClientProfile pins the asymmetry the two
// profiles exist for.
//
// The mesh profile bounds bodies this cluster's own encoder produced, and the
// defaults are demonstrably too tight for them: valueSize records that
// WorldDelta bodies "routinely exceed 65535 bytes for stress-test densities"
// while DefaultWireLimits caps aggregate allocation at 1 MiB, and MaxSliceElems
// 4096 applies to op RESPONSE bodies whose row count is a query result rather
// than an attacker's choice. Every widened field here is a body the decoder
// must not be the thing that refuses.
func TestMeshProfileIsWiderThanTheClientProfile(t *testing.T) {
	mesh := meshProfile()
	client := clientProfile(pkgnet.WireLimits{})

	if mesh.MaxTotalAllocBytes != meshMaxMsgBytes {
		t.Errorf("mesh MaxTotalAllocBytes = %d, want meshMaxMsgBytes %d — the gRPC "+
			"streams already carry messages that large",
			mesh.MaxTotalAllocBytes, meshMaxMsgBytes)
	}
	if mesh.MaxBytesFieldLen != meshMaxMsgBytes {
		t.Errorf("mesh MaxBytesFieldLen = %d, want meshMaxMsgBytes %d",
			mesh.MaxBytesFieldLen, meshMaxMsgBytes)
	}
	if mesh.MaxStringBytes != maxWireStringLen {
		t.Errorf("mesh MaxStringBytes = %d, want the wire width %d",
			mesh.MaxStringBytes, maxWireStringLen)
	}
	if mesh.MaxSliceElems != maxWireSliceElems {
		t.Errorf("mesh MaxSliceElems = %d, want the wire width %d",
			mesh.MaxSliceElems, maxWireSliceElems)
	}
	if mesh.RejectByteFields {
		t.Error("mesh profile rejects []byte fields; mmokit.WorldDelta.body is one " +
			"and internal/bot decodes it")
	}
	// MaxDepth deliberately NOT widened: nesting is fixed by the static Go type
	// graph, so raising it buys nothing.
	if mesh.MaxDepth != client.MaxDepth {
		t.Errorf("mesh MaxDepth = %d, client %d — depth is not a mesh-vs-client "+
			"distinction and the two must not drift", mesh.MaxDepth, client.MaxDepth)
	}
}

// TestStageClientWireLimits_NilProcessFallsBackToDefaults covers the fixtures.
// Both pkg/universe and pkg/mmokit build bare Stages with no owning Process,
// and a zero WireLimits reaching decodeState would reject every body — the
// failure would look like a codec bug rather than a wiring one.
func TestStageClientWireLimits_NilProcessFallsBackToDefaults(t *testing.T) {
	var s *Stage
	if got := s.clientWireLimits(); got.MaxFrameBytes != pkgnet.DefaultMaxFrameBytes {
		t.Errorf("nil Stage: MaxFrameBytes = %d, want %d", got.MaxFrameBytes, pkgnet.DefaultMaxFrameBytes)
	}
	bare := &Stage{}
	if got := bare.clientWireLimits(); got.MaxFrameBytes != pkgnet.DefaultMaxFrameBytes {
		t.Errorf("coord-less Stage: MaxFrameBytes = %d, want %d", got.MaxFrameBytes, pkgnet.DefaultMaxFrameBytes)
	}
	var p *Process
	if got := p.clientWireLimits(); got.MaxFrameBytes != pkgnet.DefaultMaxFrameBytes {
		t.Errorf("nil Process: MaxFrameBytes = %d, want %d", got.MaxFrameBytes, pkgnet.DefaultMaxFrameBytes)
	}
}
