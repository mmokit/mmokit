package universe

import (
	"reflect"
	"strings"
	"testing"

	"github.com/zenion/mmokit/pkg/metrics"
	pkgnet "github.com/zenion/mmokit/pkg/net"
)

// Bounds-regression table for the checked decoder.
//
// Written by CE-002 unit 6 asserting the panic the decoder produced on HEAD, so
// that it was green before the fix and green after, never red in between
// (docs/roadmap.md §6.8.4). Unit 8 flipped the single assertion function below:
// the rows and their data are unchanged, and the `closes` column has stopped
// being documentation and started naming the criterion each row verifies.
type truncatedDecodeCase struct {
	// name identifies the arm of decodeState.value under test.
	name string
	// newPtr returns a fresh destination. One field per case: a shared
	// multi-field struct would let an earlier arm fail first and the row
	// would prove nothing about the arm it names.
	newPtr func() any
	// data is one byte short of, or otherwise inconsistent with, what the arm
	// reads.
	data []byte
	// closes names the CE-002 acceptance criterion this row verifies.
	// See docs/roadmap.md §6.3.
	closes string
}

// One destination struct per decoder arm.
type (
	truncF32   struct{ V float32 }
	truncF64   struct{ V float64 }
	truncU8    struct{ V uint8 }
	truncU16   struct{ V uint16 }
	truncU32   struct{ V uint32 }
	truncU64   struct{ V uint64 }
	truncI8    struct{ V int8 }
	truncI16   struct{ V int16 }
	truncI32   struct{ V int32 }
	truncI64   struct{ V int64 }
	truncBool  struct{ V bool }
	truncStr   struct{ V string }
	truncBytes struct{ V []byte }
	truncSlice struct{ V []uint32 }
	truncCodec struct{ E Entity }
)

// checkedDecode runs the decoder the tolerant public wrappers call, with the
// default limits, and returns the error they swallow. Tests use it rather than
// ReflectUnmarshalStrict so a row asserts the BOUNDS check specifically and not
// the strict trailing-byte rule, which is a different criterion.
func checkedDecode(ptr any, data []byte) error {
	_, err := decodeStruct(nil, data, ptr, pkgnet.DefaultWireLimits(), metrics.SurfaceMesh)
	return err
}

// wantTruncatedRead asserts the contract the checked decoder introduced: the
// arm reports an error rather than reading past the end of the buffer, and the
// destination field is left at its zero value.
//
// This is the single function unit 8 rewrote. Everything else in the file is
// the fixture the rewritten assertion runs against.
func wantTruncatedRead(t *testing.T, tc truncatedDecodeCase) {
	t.Helper()
	ptr := tc.newPtr()
	err := checkedDecode(ptr, tc.data)
	if err == nil {
		t.Fatalf("%s: decoded %d bytes without an error; this row verifies %s",
			tc.name, len(tc.data), tc.closes)
	}
	field := reflect.ValueOf(ptr).Elem().Field(0)
	if !field.IsZero() {
		t.Fatalf("%s: rejected with %v but left the destination at %#v; a refused body "+
			"must not half-populate the struct's failing field", tc.name, err, field.Interface())
	}
}

// TestReflectUnmarshal_Truncated pins the unchecked-read inventory: eleven
// scalar arms, the two length-prefixed arms, and the generic-slice arm. The
// registered-codec delegation moved to TestReflectCodec_ShortBuffer so the unit
// that widened ReflectCodec.Decode owns the assertion it flipped.
func TestReflectUnmarshal_Truncated(t *testing.T) {
	cases := []truncatedDecodeCase{
		// ── The eleven scalar arms ───────────────────────────────────────────
		// Each indexed data[off] or sliced data[off:] and handed the result to
		// encoding/binary without ever comparing off against len(data). Each
		// case supplies one byte fewer than the arm reads.
		{
			name:   "float32/3-of-4-bytes",
			newPtr: func() any { return &truncF32{} },
			data:   []byte{0x00, 0x00, 0x80},
			closes: "criterion 2 (bounds check before every read)",
		},
		{
			name:   "float64/7-of-8-bytes",
			newPtr: func() any { return &truncF64{} },
			data:   []byte{0, 0, 0, 0, 0, 0, 0},
			closes: "criterion 2 (bounds check before every read)",
		},
		{
			name:   "uint8/0-of-1-bytes",
			newPtr: func() any { return &truncU8{} },
			data:   []byte{},
			closes: "criterion 2 (bounds check before every read)",
		},
		{
			name:   "uint16/1-of-2-bytes",
			newPtr: func() any { return &truncU16{} },
			data:   []byte{0x01},
			closes: "criterion 2 (bounds check before every read)",
		},
		{
			name:   "uint32/3-of-4-bytes",
			newPtr: func() any { return &truncU32{} },
			data:   []byte{0x01, 0x02, 0x03},
			closes: "criterion 2 (bounds check before every read)",
		},
		{
			name:   "uint64/7-of-8-bytes",
			newPtr: func() any { return &truncU64{} },
			data:   []byte{1, 2, 3, 4, 5, 6, 7},
			closes: "criterion 2 (bounds check before every read)",
		},
		{
			name:   "int8/0-of-1-bytes",
			newPtr: func() any { return &truncI8{} },
			data:   []byte{},
			closes: "criterion 2 (bounds check before every read)",
		},
		{
			name:   "int16/1-of-2-bytes",
			newPtr: func() any { return &truncI16{} },
			data:   []byte{0xFF},
			closes: "criterion 2 (bounds check before every read)",
		},
		{
			name:   "int32/3-of-4-bytes",
			newPtr: func() any { return &truncI32{} },
			data:   []byte{0xFF, 0xFF, 0xFF},
			closes: "criterion 2 (bounds check before every read)",
		},
		{
			name:   "int64/7-of-8-bytes",
			newPtr: func() any { return &truncI64{} },
			data:   []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
			closes: "criterion 2 (bounds check before every read)",
		},
		{
			name:   "bool/0-of-1-bytes",
			newPtr: func() any { return &truncBool{} },
			data:   []byte{},
			closes: "criterion 2 (bounds check before every read)",
		},

		// ── The length-prefixed arms ─────────────────────────────────────────
		// This is the CE-002 headline defect: the prefix is attacker-supplied
		// and was trusted as both a bound and an allocation size.
		{
			// [u16 len = 16][no bytes] -> data[2:18] on a two-byte buffer.
			name:   "string/length-prefix-past-end",
			newPtr: func() any { return &truncStr{} },
			data:   []byte{0x10, 0x00},
			closes: "criteria 2 and 3 (string length ceiling)",
		},
		{
			// [u32 len = 4096][no bytes].
			name:   "bytes/length-prefix-past-end",
			newPtr: func() any { return &truncBytes{} },
			data:   []byte{0x00, 0x10, 0x00, 0x00},
			closes: "criteria 2 and 3 (aggregate allocation ceiling)",
		},
		{
			// [u32 len = 0xFFFFFFFF][no bytes]. THE row unit 6 could not write:
			// on HEAD this reached make([]byte, 4294967295), and what happens
			// next is the host's decision, not the program's. Measured with the
			// guards removed on a machine with default overcommit, Go took the
			// 4 GiB reservation and the following slice expression panicked;
			// under a cgroup limit or a strict overcommit policy the same line
			// is Go's out-of-memory FATAL error, which no recover anywhere in
			// the process can contain. A decoder whose failure mode depends on
			// the host's memory policy is the defect. It is coverable now for
			// exactly one reason — the length is compared against the limit and
			// the remaining payload BEFORE it is used as an allocation size.
			name:   "bytes/4-gibibyte-length-prefix",
			newPtr: func() any { return &truncBytes{} },
			data:   []byte{0xFF, 0xFF, 0xFF, 0xFF},
			closes: "criterion 3 (allocation charged before it is made)",
		},
		{
			// [u16 len = 65535][no elements]. reflect.MakeSlice reserved all
			// 65535 elements before a single one was read — the ~50000x
			// amplifier CE-002 names, reachable pre-auth through any registered
			// op body carrying a slice.
			name:   "slice/element-count-past-end",
			newPtr: func() any { return &truncSlice{} },
			data:   []byte{0xFF, 0xFF},
			closes: "criteria 2 and 3 (slice element ceiling)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantTruncatedRead(t, tc)
		})
	}
}

// TestReflectUnmarshal_ToleratesMeshTrailing pins the deliberate asymmetry
// between the tolerant wrappers and ReflectUnmarshalStrict: a body LONGER than
// the struct decodes and the surplus is discarded.
//
// This is not an oversight that criterion 4 closes later. Mesh transfer blobs
// and border component blobs are appended to today, and UnmarshalTransferFrame
// does not reject trailing data either; the strict rule belongs to typed client
// bodies, where a surplus means the two ends disagree about the type.
func TestReflectUnmarshal_ToleratesMeshTrailing(t *testing.T) {
	var src truncU32
	src.V = 0xAABBCCDD
	data := append(mustMarshal(t, &src), 0xDE, 0xAD, 0xBE, 0xEF)

	var out truncU32
	if err := ReflectUnmarshal(data, &out); err != nil {
		t.Fatalf("tolerant wrapper rejected trailing bytes: %v", err)
	}
	if out.V != src.V {
		t.Fatalf("V = %#x, want %#x", out.V, src.V)
	}
}

// TestReflectUnmarshalStrict_RejectsTrailing is the other half of the pair: the
// same bytes the tolerant wrapper accepts must be refused by the strict entry
// point, and the error must name the surplus.
func TestReflectUnmarshalStrict_RejectsTrailing(t *testing.T) {
	var src truncU32
	src.V = 0xAABBCCDD
	data := append(mustMarshal(t, &src), 0xDE, 0xAD, 0xBE, 0xEF)

	var out truncU32
	err := ReflectUnmarshalStrict(nil, data, &out, pkgnet.WireLimits{})
	if err == nil {
		t.Fatal("ReflectUnmarshalStrict accepted 4 trailing bytes")
	}
	if !strings.Contains(err.Error(), "consumed 4 of 8 bytes") {
		t.Fatalf("error = %v, want it to name the consumed/total split", err)
	}
	// A zero-value WireLimits must behave as the defaults, not reject
	// everything: flag defaults never reach tests (docs/roadmap.md §6.8.4).
	if err := ReflectUnmarshalStrict(nil, data[:4], &out, pkgnet.WireLimits{}); err != nil {
		t.Fatalf("exact-length body rejected under zero-value limits: %v", err)
	}
	if out.V != src.V {
		t.Fatalf("V = %#x, want %#x", out.V, src.V)
	}
}

// TestReflectUnmarshalStrict_RejectsOversizeFrame covers the other check strict
// decoding adds: a body larger than MaxFrameBytes is refused before a single
// field is decoded.
func TestReflectUnmarshalStrict_RejectsOversizeFrame(t *testing.T) {
	lim := pkgnet.DefaultWireLimits()
	var out truncU32
	err := ReflectUnmarshalStrict(nil, make([]byte, lim.MaxFrameBytes+1), &out, lim)
	if err == nil {
		t.Fatalf("accepted a %d-byte body under a %d-byte frame limit",
			lim.MaxFrameBytes+1, lim.MaxFrameBytes)
	}
	if !strings.Contains(err.Error(), "frame limit") {
		t.Fatalf("error = %v, want it to name the frame limit", err)
	}
}
