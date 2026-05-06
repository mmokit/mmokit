package universe

import (
	"reflect"
	"testing"

	"github.com/mlange-42/ark/ecs"
)

func TestReflectMarshal_SimpleStruct(t *testing.T) {
	type Health struct {
		Current float32
		Max     float32
	}
	h := Health{Current: 75.5, Max: 100}
	data := ReflectMarshal(&h)
	var out Health
	ReflectUnmarshal(data, &out)
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
	data := ReflectMarshal(&o)
	var out Outer
	ReflectUnmarshal(data, &out)
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
	data := ReflectMarshal(&f)
	if len(data) != 3 {
		t.Fatalf("expected 3 bytes, got %d", len(data))
	}
	var out Flags
	ReflectUnmarshal(data, &out)
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
	data := ReflectMarshal(&n)
	var out Named
	ReflectUnmarshal(data, &out)
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
	data := ReflectMarshal(&s)
	if len(data) != 2 {
		t.Fatalf("expected 2 bytes, got %d", len(data))
	}
	var out Slot
	ReflectUnmarshal(data, &out)
	if out != s {
		t.Fatalf("got %+v, want %+v", out, s)
	}
}

func TestReflectMarshal_FixedArray(t *testing.T) {
	type Color struct {
		RGBA [4]float32
	}
	c := Color{RGBA: [4]float32{0.1, 0.2, 0.3, 1.0}}
	data := ReflectMarshal(&c)
	if len(data) != 16 {
		t.Fatalf("expected 16 bytes, got %d", len(data))
	}
	var out Color
	ReflectUnmarshal(data, &out)
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
	data := ReflectMarshal(&tgt)
	// Entity field should be skipped, only float32 remains
	if len(data) != 4 {
		t.Fatalf("expected 4 bytes (entity skipped), got %d", len(data))
	}
	var out Targeting
	ReflectUnmarshal(data, &out)
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
	data := ReflectMarshal(&m)
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
	data := ReflectMarshal(&a)
	var out AllInts
	ReflectUnmarshal(data, &out)
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
	data := ReflectMarshal(&b)
	var out Bag
	ReflectUnmarshal(data, &out)
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
	data := ReflectMarshal(&b)
	var out Bag
	ReflectUnmarshal(data, &out)
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
	data := ReflectMarshal(&b)
	if len(data) != 2 { // just the u16 zero length
		t.Fatalf("expected 2 bytes (u16 length=0), got %d", len(data))
	}
	var out Bag
	ReflectUnmarshal(data, &out)
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
	data := ReflectMarshal(&in)
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
	ReflectUnmarshal(data, &out)
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
	data := ReflectMarshal(&in)
	if len(data) != 4 {
		t.Fatalf("expected 4 bytes (u32 length=0), got %d", len(data))
	}
	var out Frame
	ReflectUnmarshal(data, &out)
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
	data := ReflectMarshal(&in)
	if len(data) != 4+len(body) {
		t.Fatalf("expected %d bytes, got %d", 4+len(body), len(data))
	}
	var out Frame
	ReflectUnmarshal(data, &out)
	if len(out.Body) != len(body) {
		t.Fatalf("decoded body len = %d, want %d", len(out.Body), len(body))
	}
	for i := range body {
		if out.Body[i] != body[i] {
			t.Fatalf("byte %d: got 0x%02x want 0x%02x", i, out.Body[i], body[i])
		}
	}
}
