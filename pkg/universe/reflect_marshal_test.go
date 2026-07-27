package universe

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mlange-42/ark/ecs"

	pkgnet "github.com/zenion/mmoserver/pkg/net"
)

// mustMarshal is the test-side form of ReflectMarshal for values that are
// expected to fit the wire. Encoder-guard rejections are asserted explicitly by
// the oversize tests below.
func mustMarshal(tb testing.TB, ptr any) []byte {
	tb.Helper()
	data, err := ReflectMarshal(ptr)
	if err != nil {
		tb.Fatalf("ReflectMarshal(%T): %v", ptr, err)
	}
	return data
}

// mustUnmarshal is its decode-side mirror, for round-trips where a rejection
// would be the bug under test rather than the behaviour being asserted.
// Decoder-guard rejections are asserted explicitly in
// reflect_marshal_bounds_test.go via checkedDecode.
func mustUnmarshal(tb testing.TB, data []byte, ptr any) {
	tb.Helper()
	if err := ReflectUnmarshal(data, ptr); err != nil {
		tb.Fatalf("ReflectUnmarshal(%T): %v", ptr, err)
	}
}

func TestReflectMarshal_SimpleStruct(t *testing.T) {
	type Health struct {
		Current float32
		Max     float32
	}
	h := Health{Current: 75.5, Max: 100}
	data := mustMarshal(t, &h)
	var out Health
	mustUnmarshal(t, data, &out)
	if out != h {
		t.Fatalf("got %+v, want %+v", out, h)
	}
}

func TestReflectMarshal_NestedStruct(t *testing.T) {
	type Inner struct {
		X float32
		Y float32
	}
	type Outer struct {
		Pos   Inner
		Scale float64
	}
	o := Outer{Pos: Inner{X: 1.5, Y: -3.25}, Scale: 99.99}
	data := mustMarshal(t, &o)
	var out Outer
	mustUnmarshal(t, data, &out)
	if out != o {
		t.Fatalf("got %+v, want %+v", out, o)
	}
}

func TestReflectMarshal_BoolFields(t *testing.T) {
	type Flags struct {
		Active  bool
		Visible bool
		Dead    bool
	}
	f := Flags{Active: true, Visible: false, Dead: true}
	data := mustMarshal(t, &f)
	if len(data) != 3 {
		t.Fatalf("expected 3 bytes, got %d", len(data))
	}
	var out Flags
	mustUnmarshal(t, data, &out)
	if out != f {
		t.Fatalf("got %+v, want %+v", out, f)
	}
}

func TestReflectMarshal_StringFields(t *testing.T) {
	type Named struct {
		Name  string
		Level uint16
		Tag   string
	}
	n := Named{Name: "hello", Level: 42, Tag: "world"}
	data := mustMarshal(t, &n)
	var out Named
	mustUnmarshal(t, data, &out)
	if out != n {
		t.Fatalf("got %+v, want %+v", out, n)
	}
}

func TestReflectMarshal_Uint8Fields(t *testing.T) {
	type Slot struct {
		Index uint8
		Count uint8
	}
	s := Slot{Index: 3, Count: 255}
	data := mustMarshal(t, &s)
	if len(data) != 2 {
		t.Fatalf("expected 2 bytes, got %d", len(data))
	}
	var out Slot
	mustUnmarshal(t, data, &out)
	if out != s {
		t.Fatalf("got %+v, want %+v", out, s)
	}
}

func TestReflectMarshal_FixedArray(t *testing.T) {
	type Color struct {
		RGBA [4]float32
	}
	c := Color{RGBA: [4]float32{0.1, 0.2, 0.3, 1.0}}
	data := mustMarshal(t, &c)
	if len(data) != 16 {
		t.Fatalf("expected 16 bytes, got %d", len(data))
	}
	var out Color
	mustUnmarshal(t, data, &out)
	if out != c {
		t.Fatalf("got %+v, want %+v", out, c)
	}
}

func TestReflectMarshal_EntityFieldSkipped(t *testing.T) {
	type Targeting struct {
		Target ecs.Entity
		Range  float32
	}
	tgt := Targeting{Range: 500.0}
	data := mustMarshal(t, &tgt)
	// Entity field should be skipped, only float32 remains
	if len(data) != 4 {
		t.Fatalf("expected 4 bytes (entity skipped), got %d", len(data))
	}
	var out Targeting
	mustUnmarshal(t, data, &out)
	if out.Range != 500.0 {
		t.Fatalf("Range: got %f, want 500", out.Range)
	}
	// Entity should be zero value
	if out.Target != (ecs.Entity{}) {
		t.Fatalf("Target should be zero entity")
	}
}

func TestReflectMarshal_UnexportedFieldsSkipped(t *testing.T) {
	type Mixed struct {
		Public  float32
		private float32 //nolint:unused
	}
	m := Mixed{Public: 42.0}
	data := mustMarshal(t, &m)
	if len(data) != 4 {
		t.Fatalf("expected 4 bytes (unexported skipped), got %d", len(data))
	}
}

func TestReflectMarshal_IntTypes(t *testing.T) {
	type AllInts struct {
		I8  int8
		I16 int16
		I32 int32
		I64 int64
	}
	a := AllInts{I8: -1, I16: -1000, I32: -100000, I64: -9999999999}
	data := mustMarshal(t, &a)
	var out AllInts
	mustUnmarshal(t, data, &out)
	if out != a {
		t.Fatalf("got %+v, want %+v", out, a)
	}
}

func TestValidateComponentType_RejectsMap(t *testing.T) {
	type Bad struct {
		Data map[string]int
	}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for map field")
		}
	}()
	ValidateComponentType(reflect.TypeFor[Bad]())
}

func TestValidateComponentType_RejectsSlice(t *testing.T) {
	type Bad struct {
		Items []int
	}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for slice field")
		}
	}()
	ValidateComponentType(reflect.TypeFor[Bad]())
}

func TestValidateComponentType_RejectsInt(t *testing.T) {
	type Bad struct {
		Value int
	}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for int field")
		}
	}()
	ValidateComponentType(reflect.TypeFor[Bad]())
}

func TestValidateComponentType_RejectsUint(t *testing.T) {
	type Bad struct {
		Value uint
	}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for uint field")
		}
	}()
	ValidateComponentType(reflect.TypeFor[Bad]())
}

func TestValidateComponentType_AcceptsEntityField(t *testing.T) {
	type WithEntity struct {
		Target ecs.Entity
		Range  float32
	}
	// Should not panic — entity fields are skipped, not rejected
	ValidateComponentType(reflect.TypeFor[WithEntity]())
}

func TestReflectMarshal_SliceOfStructs(t *testing.T) {
	type Item struct {
		ID   uint32
		Name string
	}
	type Bag struct {
		Owner string
		Items []Item
	}
	b := Bag{
		Owner: "alice",
		Items: []Item{
			{ID: 1, Name: "potion"},
			{ID: 7, Name: "sword"},
		},
	}
	data := mustMarshal(t, &b)
	var out Bag
	mustUnmarshal(t, data, &out)
	if out.Owner != b.Owner || len(out.Items) != len(b.Items) {
		t.Fatalf("got %+v, want %+v", out, b)
	}
	for i := range b.Items {
		if out.Items[i] != b.Items[i] {
			t.Fatalf("item %d: got %+v, want %+v", i, out.Items[i], b.Items[i])
		}
	}
}

func TestReflectMarshal_SliceOfPrimitives(t *testing.T) {
	type Bag struct {
		IDs []uint32
	}
	b := Bag{IDs: []uint32{42, 7, 1024}}
	data := mustMarshal(t, &b)
	var out Bag
	mustUnmarshal(t, data, &out)
	if len(out.IDs) != len(b.IDs) {
		t.Fatalf("len: got %d, want %d", len(out.IDs), len(b.IDs))
	}
	for i := range b.IDs {
		if out.IDs[i] != b.IDs[i] {
			t.Fatalf("IDs[%d]: got %d, want %d", i, out.IDs[i], b.IDs[i])
		}
	}
}

func TestReflectMarshal_EmptySlice(t *testing.T) {
	type Bag struct {
		Items []uint32
	}
	b := Bag{Items: nil}
	data := mustMarshal(t, &b)
	if len(data) != 2 { // just the u16 zero length
		t.Fatalf("expected 2 bytes (u16 length=0), got %d", len(data))
	}
	var out Bag
	mustUnmarshal(t, data, &out)
	if len(out.Items) != 0 {
		t.Fatalf("expected empty, got %v", out.Items)
	}
}

func TestValidateMessageType_AcceptsSlice(t *testing.T) {
	type Msg struct {
		Items []uint32
	}
	// Should not panic for typed-event message types.
	ValidateMessageType(reflect.TypeFor[Msg]())
}

// TestReflectMarshal_BytesFastPath verifies the []byte fast path:
// encoded as [u32 len][N bytes] without per-element iteration.
// Used by mmokit.WorldDelta — the per-tick delta frame body.
func TestReflectMarshal_BytesFastPath(t *testing.T) {
	type Frame struct {
		Body []byte
	}
	body := []byte{0x01, 0x02, 0x03, 0x04, 0xFF, 0xAB, 0xCD}
	in := Frame{Body: body}
	data := mustMarshal(t, &in)
	// Wire: [u32 len=7][7 bytes]
	if len(data) != 4+len(body) {
		t.Fatalf("expected %d bytes (u32 len + N bytes), got %d", 4+len(body), len(data))
	}
	got32 := uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16 | uint32(data[3])<<24
	if int(got32) != len(body) {
		t.Fatalf("u32 length prefix = %d, want %d", got32, len(body))
	}
	for i, b := range body {
		if data[4+i] != b {
			t.Fatalf("body[%d] = 0x%02x, want 0x%02x", i, data[4+i], b)
		}
	}
	var out Frame
	mustUnmarshal(t, data, &out)
	if len(out.Body) != len(body) {
		t.Fatalf("decoded body len = %d, want %d", len(out.Body), len(body))
	}
	for i, b := range body {
		if out.Body[i] != b {
			t.Fatalf("decoded body[%d] = 0x%02x, want 0x%02x", i, out.Body[i], b)
		}
	}
}

// TestReflectMarshal_BytesFastPath_Empty verifies a nil/empty []byte
// round-trips as just a u32 zero-length prefix.
func TestReflectMarshal_BytesFastPath_Empty(t *testing.T) {
	type Frame struct {
		Body []byte
	}
	in := Frame{Body: nil}
	data := mustMarshal(t, &in)
	if len(data) != 4 {
		t.Fatalf("expected 4 bytes (u32 length=0), got %d", len(data))
	}
	var out Frame
	mustUnmarshal(t, data, &out)
	if len(out.Body) != 0 {
		t.Fatalf("expected empty, got %v", out.Body)
	}
}

// TestReflectMarshal_BytesFastPath_LargePayload verifies bodies above the
// u16 cap (65535 bytes) round-trip correctly — the whole reason for the
// u32 length prefix.
func TestReflectMarshal_BytesFastPath_LargePayload(t *testing.T) {
	type Frame struct {
		Body []byte
	}
	body := make([]byte, 100_000)
	for i := range body {
		body[i] = byte(i & 0xff)
	}
	in := Frame{Body: body}
	data := mustMarshal(t, &in)
	if len(data) != 4+len(body) {
		t.Fatalf("expected %d bytes, got %d", 4+len(body), len(data))
	}
	var out Frame
	mustUnmarshal(t, data, &out)
	if len(out.Body) != len(body) {
		t.Fatalf("decoded body len = %d, want %d", len(out.Body), len(body))
	}
	for i := range body {
		if out.Body[i] != body[i] {
			t.Fatalf("byte %d: got 0x%02x want 0x%02x", i, out.Body[i], body[i])
		}
	}
}

// TestReflectMarshal_OversizedStringRejected pins the CE-002 encoder guard.
// Before it, marshalValue wrote uint16(len(s)) unchecked: a 70000-byte string
// emitted a frame whose u16 prefix read 4464 (70000 mod 65536) followed by
// 70000 bytes of payload, so every decoder in the cluster — Go, TypeScript and
// C# alike — resynchronized on the wrong offset. The server produced that
// frame itself, which is why the fix belongs on the encode side.
func TestReflectMarshal_OversizedStringRejected(t *testing.T) {
	type Named struct {
		Name string
	}
	const oversize = 70_000
	in := Named{Name: string(make([]byte, oversize))}

	data, err := ReflectMarshal(&in)
	if err == nil {
		t.Fatalf("ReflectMarshal accepted a %d-byte string; u16 prefix would read %d",
			oversize, oversize%(maxWireStringLen+1))
	}
	if data != nil {
		t.Fatalf("ReflectMarshal returned %d bytes alongside an error", len(data))
	}
	// The message must name the offending field, or an operator seeing this in
	// a log has no way to find it.
	if !strings.Contains(err.Error(), "Named.Name") {
		t.Errorf("error %q does not name the field", err)
	}
	if !strings.Contains(err.Error(), "70000") {
		t.Errorf("error %q does not report the offending length", err)
	}
}

// TestReflectMarshal_MaxLengthStringAccepted pins the boundary: exactly
// maxWireStringLen bytes still fits the u16 prefix and must encode.
//
// It also records the deliberate split between the two ceilings, which is the
// one place they visibly disagree. maxWireStringLen is a WIRE-FORMAT limit —
// the widest value a u16 prefix can describe, frozen across three languages.
// WireLimits.MaxStringBytes is POLICY on a body someone else supplied, and it
// is set two orders of magnitude lower because no production string field comes
// close. A value the encoder will emit is therefore not automatically a value
// the decoder will accept under the default limits.
func TestReflectMarshal_MaxLengthStringAccepted(t *testing.T) {
	type Named struct {
		Name string
	}
	in := Named{Name: string(make([]byte, maxWireStringLen))}
	data := mustMarshal(t, &in)
	if len(data) != 2+maxWireStringLen {
		t.Fatalf("encoded %d bytes, want %d", len(data), 2+maxWireStringLen)
	}

	lim := pkgnet.DefaultWireLimits()
	if err := checkedDecode(&Named{}, data); err == nil {
		t.Fatalf("default limits accepted a %d-byte string; MaxStringBytes (%d) is not being applied",
			maxWireStringLen, lim.MaxStringBytes)
	}

	lim.MaxStringBytes = maxWireStringLen
	var out Named
	if _, err := decodeStruct(nil, data, &out, lim); err != nil {
		t.Fatalf("decode under a wire-width string limit: %v", err)
	}
	if len(out.Name) != maxWireStringLen {
		t.Fatalf("decoded string of %d bytes, want %d", len(out.Name), maxWireStringLen)
	}
}

// TestReflectMarshal_OversizedSliceRejected is the slice-arm counterpart:
// the generic slice arm is [u16 len][elem0]..., so a 70000-element slice
// truncated its own element count exactly the same way.
func TestReflectMarshal_OversizedSliceRejected(t *testing.T) {
	type Bag struct {
		IDs []uint32
	}
	const oversize = 70_000
	in := Bag{IDs: make([]uint32, oversize)}

	data, err := ReflectMarshal(&in)
	if err == nil {
		t.Fatalf("ReflectMarshal accepted a %d-element slice; u16 prefix would read %d",
			oversize, oversize%(maxWireSliceElems+1))
	}
	if data != nil {
		t.Fatalf("ReflectMarshal returned %d bytes alongside an error", len(data))
	}
	if !strings.Contains(err.Error(), "Bag.IDs") {
		t.Errorf("error %q does not name the field", err)
	}
}

// TestReflectMarshal_OversizedNestedStringRejected verifies the guard travels
// out of a nested value rather than only firing on top-level fields — the
// slice-of-structs shape is what chat and inventory payloads actually use.
func TestReflectMarshal_OversizedNestedStringRejected(t *testing.T) {
	type Item struct {
		Label string
	}
	type Bag struct {
		Items []Item
	}
	in := Bag{Items: []Item{{Label: "ok"}, {Label: string(make([]byte, 70_000))}}}

	if _, err := ReflectMarshal(&in); err == nil {
		t.Fatal("ReflectMarshal accepted an oversized string nested in a slice element")
	} else if !strings.Contains(err.Error(), "Item.Label") {
		t.Errorf("error %q does not name the nested field", err)
	}
}

// TestValueSizeAndMarshalValueAgree is the load-bearing consistency check.
// If the size walk accepted a length the write walk rejects the frame is
// short; if the write walk accepted one the size walk rejects, marshalStruct
// writes past the end of a buffer sized by structSize. Drive both walks
// directly over the same value at the boundary and one element past it.
func TestValueSizeAndMarshalValueAgree(t *testing.T) {
	cases := []struct {
		name string
		val  reflect.Value
		ok   bool
	}{
		{"string at cap", reflect.ValueOf(string(make([]byte, maxWireStringLen))), true},
		{"string over cap", reflect.ValueOf(string(make([]byte, maxWireStringLen+1))), false},
		{"slice at cap", reflect.ValueOf(make([]uint32, maxWireSliceElems)), true},
		{"slice over cap", reflect.ValueOf(make([]uint32, maxWireSliceElems+1)), false},
	}
	for _, tc := range cases {
		size, sizeErr := valueSize(tc.val)
		if (sizeErr == nil) != tc.ok {
			t.Fatalf("%s: valueSize err = %v, want ok=%v", tc.name, sizeErr, tc.ok)
		}
		if sizeErr != nil {
			// Mirrored guard: the write walk must reject it too.
			if _, wErr := marshalValue(make([]byte, 8), 0, tc.val); wErr == nil {
				t.Fatalf("%s: valueSize rejected but marshalValue accepted", tc.name)
			}
			continue
		}
		off, wErr := marshalValue(make([]byte, size), 0, tc.val)
		if wErr != nil {
			t.Fatalf("%s: valueSize accepted (%d bytes) but marshalValue rejected: %v",
				tc.name, size, wErr)
		}
		if off != size {
			t.Fatalf("%s: marshalValue wrote %d bytes, valueSize reserved %d", tc.name, off, size)
		}
	}
}

// TestEncodeTypedMessage_OversizedTypeNameRejected covers the other u16 prefix
// this package writes by hand. SplitTypedMessage on the destination cell reads
// the same field, so a truncated name resolves to a different type or to none.
func TestEncodeTypedMessage_OversizedTypeNameRejected(t *testing.T) {
	type Damage struct{ Amount float32 }
	if _, err := EncodeTypedMessage(string(make([]byte, 70_000)), &Damage{Amount: 1}); err == nil {
		t.Fatal("EncodeTypedMessage accepted a type name longer than its u16 prefix")
	}
}

// TestReflectMarshalOrDrop_SkipsComponentAndCounts pins the ComponentReplicator
// Scan contract: an encoder-guard rejection omits that one component from the
// frame (nil, the same value Scan returns for "entity lacks this component")
// rather than failing the whole entity, and the drop is counted — never silent.
// See the roadmap's "corrupt component blob must skip that component, not the
// entity" trap.
func TestReflectMarshalOrDrop_SkipsComponentAndCounts(t *testing.T) {
	type Label struct {
		Text string
	}
	before := MarshalDrops()

	if got := reflectMarshalOrDrop(reflect.TypeFor[Label](),
		&Label{Text: string(make([]byte, 70_000))}); got != nil {
		t.Fatalf("reflectMarshalOrDrop returned %d bytes, want nil", len(got))
	}
	if after := MarshalDrops(); after != before+1 {
		t.Fatalf("MarshalDrops = %d, want %d", after, before+1)
	}

	// A component that fits still encodes normally.
	if got := reflectMarshalOrDrop(reflect.TypeFor[Label](), &Label{Text: "ok"}); got == nil {
		t.Fatal("reflectMarshalOrDrop dropped a component that fits the wire")
	}
}
