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
