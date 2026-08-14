package cmdsys

import (
	"testing"
)

type flatArgs struct {
	Name   string  // required by default
	Count  int32   `cmd:"optional"`
	Amount float32 `cmd:"optional"`
	Active bool    `cmd:"optional"`
	Ratio  float64 `cmd:"optional"`
	Big    int64   `cmd:"optional"`
	Tag    string  `cmd:"default=hello,optional"`
	Mode   string  `cmd:"enum=fast|slow,optional"`
}

type nestedArgs struct {
	Label string
	Sub   subArgs
}

type subArgs struct {
	X float32
	Y float32
}

type tooDeep struct {
	Inner deepInner
}

type deepInner struct {
	Deeper deeperInner
}

type deeperInner struct {
	Val string
}

type sliceArgs struct {
	Names  []string
	Scores []int32
}

// intTypesArgs exercises int / uint32 / uint64 field support.
type intTypesArgs struct {
	Plain  int    `cmd:"optional"`
	NetID  uint32 `cmd:"optional"`
	BigInt uint64 `cmd:"optional"`
}

func TestSchemaOf_IntegerKinds(t *testing.T) {
	s, err := SchemaOf(intTypesArgs{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	byName := map[string]FieldSchema{}
	for _, f := range s.Fields {
		byName[f.Name] = f
	}
	if byName["Plain"].Kind != "int" {
		t.Errorf("Plain: kind %q want int", byName["Plain"].Kind)
	}
	if byName["NetID"].Kind != "uint32" {
		t.Errorf("NetID: kind %q want uint32", byName["NetID"].Kind)
	}
	if byName["BigInt"].Kind != "uint64" {
		t.Errorf("BigInt: kind %q want uint64", byName["BigInt"].Kind)
	}
}

func TestSchemaOf_Flat(t *testing.T) {
	s, err := SchemaOf(flatArgs{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.StructName != "flatArgs" {
		t.Errorf("struct name: got %q, want %q", s.StructName, "flatArgs")
	}
	byName := map[string]FieldSchema{}
	for _, f := range s.Fields {
		byName[f.Name] = f
	}

	cases := []struct {
		name     string
		wantKind string
		required bool
		def      string
		enum     []string
	}{
		{"Name", "string", true, "", nil},
		{"Count", "int32", false, "", nil},
		{"Amount", "float32", false, "", nil},
		{"Active", "bool", false, "", nil},
		{"Ratio", "float64", false, "", nil},
		{"Big", "int64", false, "", nil},
		{"Tag", "string", false, "hello", nil},
		{"Mode", "string", false, "", []string{"fast", "slow"}},
	}
	for _, c := range cases {
		f, ok := byName[c.name]
		if !ok {
			t.Errorf("field %s missing from schema", c.name)
			continue
		}
		if f.Kind != c.wantKind {
			t.Errorf("field %s: kind %q want %q", c.name, f.Kind, c.wantKind)
		}
		if f.Required != c.required {
			t.Errorf("field %s: required %v want %v", c.name, f.Required, c.required)
		}
		if f.Default != c.def {
			t.Errorf("field %s: default %q want %q", c.name, f.Default, c.def)
		}
		if len(f.Enum) != len(c.enum) {
			t.Errorf("field %s: enum len %d want %d", c.name, len(f.Enum), len(c.enum))
		}
	}
}

func TestSchemaOf_Nested(t *testing.T) {
	s, err := SchemaOf(nestedArgs{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	byName := map[string]FieldSchema{}
	for _, f := range s.Fields {
		byName[f.Name] = f
	}
	sub, ok := byName["Sub"]
	if !ok {
		t.Fatal("Sub field missing")
	}
	// nested struct kind should start with "{"
	if len(sub.Kind) == 0 || sub.Kind[0] != '{' {
		t.Errorf("Sub kind should be a nested struct kind, got %q", sub.Kind)
	}
}

func TestSchemaOf_TooDeep_Error(t *testing.T) {
	_, err := SchemaOf(tooDeep{})
	if err == nil {
		t.Fatal("expected error for too-deep nesting, got nil")
	}
}

func TestSchemaOf_Slices(t *testing.T) {
	s, err := SchemaOf(sliceArgs{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	byName := map[string]FieldSchema{}
	for _, f := range s.Fields {
		byName[f.Name] = f
	}
	if byName["Names"].Kind != "[]string" {
		t.Errorf("Names kind: got %q want %q", byName["Names"].Kind, "[]string")
	}
	if byName["Scores"].Kind != "[]int32" {
		t.Errorf("Scores kind: got %q want %q", byName["Scores"].Kind, "[]int32")
	}
}

func TestSchemaOf_Pointer(t *testing.T) {
	s, err := SchemaOf(&flatArgs{})
	if err != nil {
		t.Fatalf("pointer to struct: unexpected error: %v", err)
	}
	if s.StructName != "flatArgs" {
		t.Errorf("struct name: got %q", s.StructName)
	}
}

func TestSchemaHashOf_Stability(t *testing.T) {
	h1, err := schemaHashOf(flatArgs{})
	if err != nil {
		t.Fatal(err)
	}
	h2, err := schemaHashOf(flatArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Errorf("hash unstable: %d vs %d", h1, h2)
	}
}

// identicalLayout has the same field layout as flatArgs but a different name.
// The schema hash must be equal (name is excluded from hash).
type identicalLayout struct {
	Name   string
	Count  int32   `cmd:"optional"`
	Amount float32 `cmd:"optional"`
	Active bool    `cmd:"optional"`
	Ratio  float64 `cmd:"optional"`
	Big    int64   `cmd:"optional"`
	Tag    string  `cmd:"default=hello,optional"`
	Mode   string  `cmd:"enum=fast|slow,optional"`
}

func TestSchemaHashOf_EqualAcrossSameLayout(t *testing.T) {
	h1, err := schemaHashOf(flatArgs{})
	if err != nil {
		t.Fatal(err)
	}
	h2, err := schemaHashOf(identicalLayout{})
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Errorf("expected equal hashes for identical field layouts: %d vs %d", h1, h2)
	}
}

type differentLayout struct {
	Name  string
	Extra int32
}

func TestSchemaHashOf_DifferentForDifferentLayouts(t *testing.T) {
	h1, err := schemaHashOf(flatArgs{})
	if err != nil {
		t.Fatal(err)
	}
	h2, err := schemaHashOf(differentLayout{})
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Error("expected different hashes for different field layouts")
	}
}

func TestSchemaOfResult_AllowsDeepNesting(t *testing.T) {
	type leaf struct {
		A int
		B string
	}
	type inner struct {
		L leaf
	}
	type result struct {
		Rows []inner
	}

	if _, err := SchemaOfResult(result{}); err != nil {
		t.Fatalf("deeply nested result should register, got %v", err)
	}
	// SchemaOf (Args-strict) should still reject it.
	if _, err := SchemaOf(result{}); err == nil {
		t.Error("SchemaOf should reject deeply nested struct; no error returned")
	}
}

// ---- optional and named-only tag tests ------------------------------------

type optionalTagArgs struct {
	Explicit string
	Opted    string `cmd:"optional"`
	Plain    string
}

func TestSchemaOf_OptionalTag(t *testing.T) {
	s, err := SchemaOf(optionalTagArgs{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	byName := map[string]FieldSchema{}
	for _, f := range s.Fields {
		byName[f.Name] = f
	}
	if !byName["Explicit"].Required {
		t.Error("Explicit field (no tag) should be Required=true by default")
	}
	if byName["Opted"].Required {
		t.Error("Opted field (cmd:\"optional\") should have Required=false")
	}
	if !byName["Plain"].Required {
		t.Error("Plain field (no tag) should have Required=true by default")
	}
}

type namedOnlyArgs struct {
	A string
	B string `cmd:"named-only"`
	C string
}

func TestSchemaOf_NamedOnlyTag(t *testing.T) {
	s, err := SchemaOf(namedOnlyArgs{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	byName := map[string]FieldSchema{}
	for _, f := range s.Fields {
		byName[f.Name] = f
	}
	if byName["A"].NamedOnly {
		t.Error("A should not be named-only")
	}
	if !byName["B"].NamedOnly {
		t.Error("B should be named-only (cmd:\"named-only\")")
	}
	if byName["C"].NamedOnly {
		t.Error("C should not be named-only")
	}
}

func TestSchemaOf_SecretTagSetsFlag(t *testing.T) {
	type args struct {
		Username string `cmd:"help=name"`
		Password string `cmd:"secret,help=login password"`
	}
	s, err := SchemaOf(args{})
	if err != nil {
		t.Fatalf("SchemaOf: %v", err)
	}
	byName := map[string]FieldSchema{}
	for _, f := range s.Fields {
		byName[f.Name] = f
	}
	if byName["Username"].Secret {
		t.Errorf("Username.Secret = true; want false")
	}
	if !byName["Password"].Secret {
		t.Errorf("Password.Secret = false; want true")
	}
}
