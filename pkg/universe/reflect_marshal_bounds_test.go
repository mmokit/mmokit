package universe

import "testing"

// Bounds-regression table for unmarshalValueOnStage.
//
// Every case here asserts what the decoder does TODAY: an unchecked read past
// the end of the buffer, which panics. That is deliberate and it is what makes
// this file safe to land before the checked decoder exists — it is green on
// HEAD and it is green again once the decoder returns errors, never red in
// between (docs/roadmap.md §6.8.4).
//
// The table is the inventory the checked decoder has to satisfy. When
// decodeState lands (§6.8.3 unit 8) the ONLY edits needed are:
//
//   - wantTruncatedRead flips from "must panic" to "must return an error and
//     leave the destination at its zero value";
//   - the flipsTo column stops being documentation and starts being the
//     criterion the case actually verifies.
//
// The rows themselves — the arm inventory and the exact truncations — do not
// change, which is the point of writing them down now.
type truncatedDecodeCase struct {
	// name identifies the arm of unmarshalValueOnStage under test.
	name string
	// newPtr returns a fresh destination. One field per case: a shared
	// multi-field struct would let an earlier arm panic first and the row
	// would prove nothing about the arm it names.
	newPtr func() any
	// data is one byte short of, or otherwise inconsistent with, what the arm
	// reads.
	data []byte
	// flipsTo names the CE-002 acceptance criterion this row closes once the
	// checked decoder lands. See docs/roadmap.md §6.3.
	flipsTo string
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

// wantTruncatedRead asserts the CURRENT contract: the arm reads past the end of
// the buffer and panics with a runtime bounds error.
//
// This is the single function unit 8 rewrites. Everything else in the file is
// the fixture the rewritten assertion runs against.
func wantTruncatedRead(t *testing.T, tc truncatedDecodeCase) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("%s: decoded %d bytes without panicking.\n"+
				"If the checked decoder has landed this is the expected win — flip "+
				"wantTruncatedRead to assert an error return (%s) rather than deleting the row.",
				tc.name, len(tc.data), tc.flipsTo)
		}
	}()
	ReflectUnmarshal(tc.data, tc.newPtr())
}

// TestReflectUnmarshal_Truncated pins the unchecked-read inventory: eleven
// scalar arms, the two length-prefixed arms, the generic-slice arm, and the
// delegation into a registered ReflectCodec.
//
// Named so unit 8 can find it: the checked decoder is not done until every row
// here reports an error instead of a panic.
func TestReflectUnmarshal_Truncated(t *testing.T) {
	cases := []truncatedDecodeCase{
		// ── The eleven scalar arms ───────────────────────────────────────────
		// Each indexes data[off] or slices data[off:] and hands the result to
		// encoding/binary without ever comparing off against len(data). Each
		// case supplies one byte fewer than the arm reads.
		{
			name:    "float32/3-of-4-bytes",
			newPtr:  func() any { return &truncF32{} },
			data:    []byte{0x00, 0x00, 0x80},
			flipsTo: "criterion 2 (bounds check before every read)",
		},
		{
			name:    "float64/7-of-8-bytes",
			newPtr:  func() any { return &truncF64{} },
			data:    []byte{0, 0, 0, 0, 0, 0, 0},
			flipsTo: "criterion 2 (bounds check before every read)",
		},
		{
			name:    "uint8/0-of-1-bytes",
			newPtr:  func() any { return &truncU8{} },
			data:    []byte{},
			flipsTo: "criterion 2 (bounds check before every read)",
		},
		{
			name:    "uint16/1-of-2-bytes",
			newPtr:  func() any { return &truncU16{} },
			data:    []byte{0x01},
			flipsTo: "criterion 2 (bounds check before every read)",
		},
		{
			name:    "uint32/3-of-4-bytes",
			newPtr:  func() any { return &truncU32{} },
			data:    []byte{0x01, 0x02, 0x03},
			flipsTo: "criterion 2 (bounds check before every read)",
		},
		{
			name:    "uint64/7-of-8-bytes",
			newPtr:  func() any { return &truncU64{} },
			data:    []byte{1, 2, 3, 4, 5, 6, 7},
			flipsTo: "criterion 2 (bounds check before every read)",
		},
		{
			name:    "int8/0-of-1-bytes",
			newPtr:  func() any { return &truncI8{} },
			data:    []byte{},
			flipsTo: "criterion 2 (bounds check before every read)",
		},
		{
			name:    "int16/1-of-2-bytes",
			newPtr:  func() any { return &truncI16{} },
			data:    []byte{0xFF},
			flipsTo: "criterion 2 (bounds check before every read)",
		},
		{
			name:    "int32/3-of-4-bytes",
			newPtr:  func() any { return &truncI32{} },
			data:    []byte{0xFF, 0xFF, 0xFF},
			flipsTo: "criterion 2 (bounds check before every read)",
		},
		{
			name:    "int64/7-of-8-bytes",
			newPtr:  func() any { return &truncI64{} },
			data:    []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
			flipsTo: "criterion 2 (bounds check before every read)",
		},
		{
			name:    "bool/0-of-1-bytes",
			newPtr:  func() any { return &truncBool{} },
			data:    []byte{},
			flipsTo: "criterion 2 (bounds check before every read)",
		},

		// ── The length-prefixed arms ─────────────────────────────────────────
		// This is the CE-002 headline defect: the prefix is attacker-supplied
		// and is trusted as both a bound and an allocation size.
		{
			// [u16 len = 16][no bytes] -> data[2:18] on a two-byte buffer.
			name:    "string/length-prefix-past-end",
			newPtr:  func() any { return &truncStr{} },
			data:    []byte{0x10, 0x00},
			flipsTo: "criteria 2 and 3 (string length ceiling)",
		},
		{
			// [u32 len = 4096][no bytes]. The true CE-002 case is
			// n = 0xFFFFFFFF, and it deliberately is NOT a row here: that
			// reaches make([]byte, 4294967295), which is Go's UNRECOVERABLE
			// out-of-memory fatal error rather than a panic. recover() cannot
			// catch it and the whole test binary dies, so the case can only be
			// covered once the decoder charges the allocation before making it
			// (criterion 3). 4096 exercises the same unchecked slice with an
			// allocation the process survives.
			name:    "bytes/length-prefix-past-end",
			newPtr:  func() any { return &truncBytes{} },
			data:    []byte{0x00, 0x10, 0x00, 0x00},
			flipsTo: "criteria 2 and 3 (aggregate allocation ceiling)",
		},
		{
			// [u16 len = 65535][no elements]. reflect.MakeSlice reserves all
			// 65535 elements before a single one is read — the ~50000x
			// amplifier CE-002 names, reachable pre-auth through any registered
			// op body carrying a slice.
			name:    "slice/element-count-past-end",
			newPtr:  func() any { return &truncSlice{} },
			data:    []byte{0xFF, 0xFF},
			flipsTo: "criteria 2 and 3 (slice element ceiling)",
		},

		// ── Delegation into a registered ReflectCodec ────────────────────────
		{
			// unmarshalStructOnStage hands the codec data[off:] and then
			// advances by codec.Size() without checking either. The Entity
			// codec (entity.go) reads a u32 out of whatever it is given.
			name:    "codec/entity-gets-3-of-4-bytes",
			newPtr:  func() any { return &truncCodec{} },
			data:    []byte{0x01, 0x02, 0x03},
			flipsTo: "criterion 2 (bounds check before delegating to a codec)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantTruncatedRead(t, tc)
		})
	}
}

// TestReflectUnmarshal_TrailingBytesAccepted records the other half of
// criterion 4: a body LONGER than the struct decodes without complaint and the
// surplus is silently discarded. Unlike the truncation rows this one is not a
// crash, so it asserts the current (permissive) result directly.
//
// Unit 10 owns the strict-decoding switch that turns this into a rejection;
// when it lands, this test inverts alongside wantTruncatedRead.
func TestReflectUnmarshal_TrailingBytesAccepted(t *testing.T) {
	var src truncU32
	src.V = 0xAABBCCDD
	data := append(mustMarshal(t, &src), 0xDE, 0xAD, 0xBE, 0xEF)

	var out truncU32
	ReflectUnmarshal(data, &out)
	if out.V != src.V {
		t.Fatalf("V = %#x, want %#x", out.V, src.V)
	}
	// No assertion on the trailing bytes: today there is nowhere for the
	// decoder to report them. That absence is criterion 4.
}
