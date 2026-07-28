package universe

import (
	"encoding/binary"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/zenion/mmoserver/pkg/metrics"
	pkgnet "github.com/zenion/mmoserver/pkg/net"
)

// Configured-ceiling table for the checked decoder — CE-002 criterion 3.
//
// TestReflectUnmarshal_Truncated covers the payload-DERIVED bounds, which
// reject a body that is inconsistent with itself. These rows cover the other
// half: bodies that are perfectly well formed and are refused anyway because a
// limit says so. Every row proves the limit did the rejecting by decoding the
// same bytes cleanly under the defaults first.
type decodeLimitCase struct {
	name string
	// value is marshalled to produce a well-formed body.
	value func() any
	// newPtr returns a fresh destination of the same type.
	newPtr func() any
	// tighten narrows exactly one limit so the row names one control.
	tighten func(l pkgnet.WireLimits) pkgnet.WireLimits
	// want is a substring the rejection must name, so a row cannot pass by
	// tripping a different guard.
	want   string
	closes string
}

type (
	limString struct{ V string }
	limBytes  struct{ V []byte }
	limSlice  struct{ V []uint32 }
	limInner  struct{ C uint32 }
	limMiddle struct{ B limInner }
	limOuter  struct{ A limMiddle }
	limTwoStr struct{ A, B string }
)

func TestReflectUnmarshal_Bounds(t *testing.T) {
	cases := []decodeLimitCase{
		{
			name:    "string/exceeds-MaxStringBytes",
			value:   func() any { return &limString{V: strings.Repeat("x", 64)} },
			newPtr:  func() any { return &limString{} },
			tighten: func(l pkgnet.WireLimits) pkgnet.WireLimits { l.MaxStringBytes = 8; return l },
			want:    "exceeds the 8-byte limit",
			closes:  "criterion 3 (string ceiling)",
		},
		{
			name:    "bytes/exceeds-MaxBytesFieldLen",
			value:   func() any { return &limBytes{V: make([]byte, 64)} },
			newPtr:  func() any { return &limBytes{} },
			tighten: func(l pkgnet.WireLimits) pkgnet.WireLimits { l.MaxBytesFieldLen = 8; return l },
			want:    "byte field of 64 bytes exceeds the 8-byte limit",
			closes:  "criterion 3 (byte-field ceiling)",
		},
		{
			name:    "slice/exceeds-MaxSliceElems",
			value:   func() any { return &limSlice{V: []uint32{1, 2, 3, 4, 5, 6, 7, 8}} },
			newPtr:  func() any { return &limSlice{} },
			tighten: func(l pkgnet.WireLimits) pkgnet.WireLimits { l.MaxSliceElems = 4; return l },
			want:    "slice of 8 elements exceeds the 4-element limit",
			closes:  "criterion 3 (slice ceiling)",
		},
		{
			name:    "struct/exceeds-MaxDepth",
			value:   func() any { return &limOuter{A: limMiddle{B: limInner{C: 7}}} },
			newPtr:  func() any { return &limOuter{} },
			tighten: func(l pkgnet.WireLimits) pkgnet.WireLimits { l.MaxDepth = 2; return l },
			want:    "nesting deeper than the 2-level limit",
			closes:  "criterion 3 (nesting ceiling)",
		},
		{
			name: "aggregate/exceeds-MaxTotalAllocBytes",
			value: func() any {
				return &limTwoStr{A: strings.Repeat("a", 64), B: strings.Repeat("b", 64)}
			},
			newPtr: func() any { return &limTwoStr{} },
			// 100 admits the first 64-byte string and refuses the second:
			// the point of an aggregate budget is that individually-legal
			// fields cannot add up to an illegal total.
			tighten: func(l pkgnet.WireLimits) pkgnet.WireLimits { l.MaxTotalAllocBytes = 100; return l },
			want:    "exceeds the 36 bytes left of the 100-byte decode budget",
			closes:  "criterion 3 (aggregate allocation ceiling)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := mustMarshal(t, tc.value())

			if err := checkedDecode(tc.newPtr(), body); err != nil {
				t.Fatalf("well-formed body rejected under the DEFAULT limits: %v\n"+
					"the row must prove the tightened limit did the rejecting", err)
			}

			lim := tc.tighten(pkgnet.DefaultWireLimits())
			_, err := decodeStruct(nil, body, tc.newPtr(), lim, metrics.SurfaceMesh)
			if err == nil {
				t.Fatalf("accepted %d bytes under the tightened limit; this row verifies %s",
					len(body), tc.closes)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v\nwant it to contain %q", err, tc.want)
			}
		})
	}
}

// bulkSetMembers mirrors chat.ChatBulkSetMembersRequest, the type CE-002 names
// as the client-reachable amplifier: it is registered RouteGatewayLocal, so its
// body is reflect-decoded on the gateway before any handler-side auth check.
type bulkSetMembers struct {
	ChannelID string
	UserIDs   []string
}

// TestReflectUnmarshal_SliceAmplification is the headline CE-002 case: a
// ~20-byte body declaring a 65535-element slice.
//
// On HEAD, reflect.MakeSlice reserved all 65535 elements — 16 bytes each for a
// string header, roughly 1 MiB, a ~50000x amplification — before a single
// element was read. The assertion is therefore not only "returns an error" but
// "does not allocate on the way to returning it".
func TestReflectUnmarshal_SliceAmplification(t *testing.T) {
	body := []byte{0x07, 0x00}
	body = append(body, "general"...) // ChannelID
	body = append(body, 0xFF, 0xFF)   // UserIDs: 65535 elements
	body = append(body, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	if len(body) != 20 {
		t.Fatalf("fixture is %d bytes, want 20", len(body))
	}

	var out bulkSetMembers
	err := checkedDecode(&out, body)
	if err == nil {
		t.Fatal("accepted a 20-byte body declaring 65535 slice elements")
	}
	if out.UserIDs != nil {
		t.Fatalf("UserIDs = %v, want nil; the slice must not be reserved before it is validated", out.UserIDs)
	}

	// Allocation count. The rejection path allocates the wrapped error and
	// the destination's escape; what it must not do is grow with the
	// wire-declared element count.
	allocs := testing.AllocsPerRun(100, func() {
		var v bulkSetMembers
		_ = checkedDecode(&v, body)
	})
	if allocs > 16 {
		t.Fatalf("rejection allocated %.0f times per decode, want <= 16", allocs)
	}

	// Allocation VOLUME, which is what AllocsPerRun cannot see: one
	// reflect.MakeSlice is a single allocation no matter how large it is.
	const runs = 200
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for range runs {
		var v bulkSetMembers
		_ = checkedDecode(&v, body)
	}
	runtime.ReadMemStats(&after)

	perDecode := (after.TotalAlloc - before.TotalAlloc) / runs
	// The old path reserved 65535 * 16 = 1048560 bytes per decode. 4 KiB is
	// three orders of magnitude below that and well above the error-formatting
	// cost, so this pins the amplification without being a timing-sensitive
	// budget on the error path.
	if perDecode > 4096 {
		t.Fatalf("rejection allocated %d bytes per decode from a %d-byte body; "+
			"the wire-declared length is being reserved before it is checked",
			perDecode, len(body))
	}
}

// TestReflectUnmarshal_StringPastEnd covers the arm CE-002 calls the
// client-reachable vector: slen is fully wire-controlled and was used directly
// as a slice bound, so four bytes of payload produced a 65535-byte read past
// the end of the buffer.
func TestReflectUnmarshal_StringPastEnd(t *testing.T) {
	t.Run("declared-length-over-the-ceiling", func(t *testing.T) {
		var out limString
		err := checkedDecode(&out, []byte{0xFF, 0xFF})
		if err == nil {
			t.Fatal("accepted a 2-byte body declaring a 65535-byte string")
		}
		if !strings.Contains(err.Error(), "string of 65535 bytes") {
			t.Fatalf("error = %v, want it to name the declared length", err)
		}
		if out.V != "" {
			t.Fatalf("V = %q, want empty", out.V)
		}
	})

	// Under the configured ceiling but still past the end of the buffer: the
	// payload-derived bound is what has to catch this one, and it is the
	// reason the ceiling alone is not sufficient.
	t.Run("under-the-ceiling-but-past-the-end", func(t *testing.T) {
		var out limString
		err := checkedDecode(&out, []byte{0x64, 0x00, 'a', 'b'}) // declares 100, supplies 2
		if err == nil {
			t.Fatal("accepted a 4-byte body declaring a 100-byte string")
		}
		if !strings.Contains(err.Error(), "read of 100 bytes") {
			t.Fatalf("error = %v, want the payload-derived bound to report it", err)
		}
		if out.V != "" {
			t.Fatalf("V = %q, want empty", out.V)
		}
	})
}

// codecProbe exists only to observe what the delegation site hands a registered
// codec. Its registration is installed by TestReflectCodec_ShortBuffer and is
// harmless to the rest of the binary — no other type is affected.
type codecProbe struct{ Raw uint32 }

// TestReflectCodec_ShortBuffer owns the assertion moved out of
// TestReflectUnmarshal_Truncated when ReflectCodec.Decode gained an error
// return, so one unit owns the flip.
func TestReflectCodec_ShortBuffer(t *testing.T) {
	// The delegation site handed the codec data[off:] and then advanced by
	// codec.Size() without checking either. The Entity codec (entity.go)
	// reads a u32 out of whatever it is given.
	var dst truncCodec
	err := checkedDecode(&dst, []byte{0x01, 0x02, 0x03})
	if err == nil {
		t.Fatal("Entity codec accepted a 3-byte buffer for a 4-byte field")
	}
	if !strings.Contains(err.Error(), "read of 4 bytes") {
		t.Fatalf("error = %v, want the delegation-site bound to report it", err)
	}
	if dst.E.NetID() != 0 {
		t.Fatalf("NetID = %d, want 0", dst.E.NetID())
	}

	// A codec is handed exactly Size() bytes, never the remainder of the
	// body. Size() is documented fixed-width, so a codec that read past it
	// would be reading the following field's bytes.
	var saw int
	RegisterReflectCodec(reflect.TypeFor[codecProbe](), &ReflectCodec{
		Size: func() int { return 4 },
		Encode: func(buf []byte, v reflect.Value) {
			binary.LittleEndian.PutUint32(buf, v.Interface().(codecProbe).Raw)
		},
		Decode: func(_ *Stage, data []byte, v reflect.Value) error {
			saw = len(data)
			if len(data) < 4 {
				return fmt.Errorf("probe codec: need 4 bytes, got %d", len(data))
			}
			v.Set(reflect.ValueOf(codecProbe{Raw: binary.LittleEndian.Uint32(data)}))
			return nil
		},
	})

	type probeHolder struct {
		P    codecProbe
		Tail uint32
	}
	body := mustMarshal(t, &probeHolder{P: codecProbe{Raw: 0xAABBCCDD}, Tail: 0x11223344})

	var holder probeHolder
	if err := checkedDecode(&holder, body); err != nil {
		t.Fatalf("well-formed probe body rejected: %v", err)
	}
	if saw != 4 {
		t.Fatalf("codec was handed %d bytes, want exactly Size() = 4", saw)
	}
	if holder.P.Raw != 0xAABBCCDD || holder.Tail != 0x11223344 {
		t.Fatalf("round-trip = %+v, want {Raw:0xAABBCCDD Tail:0x11223344}", holder)
	}
}

// selfRefNode is the shape a future game author can write and the validators
// could not survive: Go forbids a struct that contains itself by value, but
// `[]Node` inside `Node` is legal and validateType recursed into slice element
// types unconditionally.
type selfRefNode struct {
	Label string
	Kids  []selfRefNode
}

// TestValidateType_SelfReferentialTerminates pins the registration-time cycle
// guard.
//
// This test could NOT have been written before the guard existed. Unbounded
// recursion in validateType is a goroutine stack overflow, which is a Go fatal
// error rather than a recoverable panic, so on HEAD it killed the whole test
// binary instead of failing one case — the same reason unit 6 could not put the
// 4 GiB byte-length row in its table.
func TestValidateType_SelfReferentialTerminates(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("ValidateMessageType accepted a self-referential type")
		}
		if !strings.Contains(fmt.Sprint(r), "self-referential") {
			t.Fatalf("panic = %v, want it to name the likely cause", r)
		}
	}()
	ValidateMessageType(reflect.TypeFor[selfRefNode]())
}

// TestMinWireSize pins the payload-derived slice bound's input. It is the one
// piece of the decoder whose answer is a judgement about the wire format rather
// than a bounds check, so a wrong row here silently weakens the bound instead
// of failing loudly.
func TestMinWireSize(t *testing.T) {
	cases := []struct {
		name string
		t    reflect.Type
		want int
	}{
		{"uint8", reflect.TypeFor[uint8](), 1},
		{"int16", reflect.TypeFor[int16](), 2},
		{"float32", reflect.TypeFor[float32](), 4},
		{"int64", reflect.TypeFor[int64](), 8},
		{"string", reflect.TypeFor[string](), 2},
		{"[]byte", reflect.TypeFor[[]byte](), 4},
		{"[]string", reflect.TypeFor[[]string](), 2},
		{"[3]float32", reflect.TypeFor[[3]float32](), 12},
		{"struct", reflect.TypeFor[limTwoStr](), 4},
		{"nested struct", reflect.TypeFor[limOuter](), 4},
		// The Entity codec's fixed width, not Entity's Go shape.
		{"registered codec", reflect.TypeFor[Entity](), 4},
	}
	for _, tc := range cases {
		if got := minWireSize(tc.t); got != tc.want {
			t.Errorf("minWireSize(%s) = %d, want %d", tc.name, got, tc.want)
		}
	}
}
