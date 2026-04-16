package cmdsys

import (
	"strings"
	"testing"
)

type parserArgs struct {
	Name   string
	Count  int32  `cmd:"optional"`
	Active bool   `cmd:"optional"`
	Tag    string `cmd:"default=world,optional"`
	Mode   string `cmd:"enum=fast|slow,optional"`
}

func bindHelper(t *testing.T, raw string) (map[string]any, error) {
	t.Helper()
	schema, err := SchemaOf(parserArgs{})
	if err != nil {
		t.Fatalf("SchemaOf: %v", err)
	}
	var p Parser
	return p.Bind(raw, schema)
}

func TestParser_Positional(t *testing.T) {
	m, err := bindHelper(t, "alice 42 true")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["Name"] != "alice" {
		t.Errorf("Name: got %v want alice", m["Name"])
	}
	if m["Count"] != int32(42) {
		t.Errorf("Count: got %v want 42", m["Count"])
	}
	if m["Active"] != true {
		t.Errorf("Active: got %v want true", m["Active"])
	}
}

func TestParser_Named(t *testing.T) {
	m, err := bindHelper(t, "--Name=bob --Count=7 --Active=false")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["Name"] != "bob" {
		t.Errorf("Name: got %v", m["Name"])
	}
	if m["Count"] != int32(7) {
		t.Errorf("Count: got %v", m["Count"])
	}
}

func TestParser_NamedSpaceSeparated(t *testing.T) {
	m, err := bindHelper(t, "--Name carol --Count 3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["Name"] != "carol" {
		t.Errorf("Name: got %v", m["Name"])
	}
	if m["Count"] != int32(3) {
		t.Errorf("Count: got %v", m["Count"])
	}
}

func TestParser_QuotedString(t *testing.T) {
	m, err := bindHelper(t, `"hello world" 1`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["Name"] != "hello world" {
		t.Errorf("Name: got %v", m["Name"])
	}
}

func TestParser_QuotedEscapes(t *testing.T) {
	m, err := bindHelper(t, `"say \"hi\\" 0`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `say "hi\`
	if m["Name"] != want {
		t.Errorf("Name: got %q want %q", m["Name"], want)
	}
}

func TestParser_Default(t *testing.T) {
	// Only supply required Name; Tag should get default "world"
	m, err := bindHelper(t, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["Tag"] != "world" {
		t.Errorf("Tag default: got %v want world", m["Tag"])
	}
}

func TestParser_MissingRequired(t *testing.T) {
	_, err := bindHelper(t, "")
	if err == nil {
		t.Fatal("expected error for missing required field")
	}
}

func TestParser_UnknownNamedArg(t *testing.T) {
	_, err := bindHelper(t, "--Name=x --Unknown=y")
	if err == nil {
		t.Fatal("expected error for unknown named arg")
	}
}

func TestParser_EnumValid(t *testing.T) {
	schema, _ := SchemaOf(parserArgs{})
	var p Parser
	// supply Name + Mode
	m, err := p.Bind("alice 0 false x fast", schema)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["Mode"] != "fast" {
		t.Errorf("Mode: got %v", m["Mode"])
	}
}

func TestParser_EnumInvalid(t *testing.T) {
	schema, _ := SchemaOf(parserArgs{})
	var p Parser
	_, err := p.Bind("alice 0 false x turbo", schema)
	if err == nil {
		t.Fatal("expected error for invalid enum value")
	}
}

func TestParser_UnterminatedQuote(t *testing.T) {
	_, err := bindHelper(t, `"unclosed`)
	if err == nil {
		t.Fatal("expected error for unterminated quote")
	}
}

// intKindsArgs exercises int / uint32 / uint64 text binding + ApplyMap round-trip.
type intKindsArgs struct {
	Plain  int
	NetID  uint32
	BigInt uint64
}

func TestParser_BindIntKinds(t *testing.T) {
	schema, err := SchemaOf(intKindsArgs{})
	if err != nil {
		t.Fatalf("SchemaOf: %v", err)
	}
	var p Parser
	m, err := p.Bind("42 4294967295 18446744073709551610", schema)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if m["Plain"] != 42 {
		t.Errorf("Plain: got %v want 42", m["Plain"])
	}
	if m["NetID"] != uint32(4294967295) {
		t.Errorf("NetID: got %v want 4294967295", m["NetID"])
	}
	if m["BigInt"] != uint64(18446744073709551610) {
		t.Errorf("BigInt: got %v", m["BigInt"])
	}
	var dst intKindsArgs
	if err := ApplyMap(&dst, m); err != nil {
		t.Fatalf("ApplyMap: %v", err)
	}
	if dst.Plain != 42 || dst.NetID != 4294967295 || dst.BigInt != 18446744073709551610 {
		t.Errorf("ApplyMap round-trip: got %+v", dst)
	}
}

func TestTokenize_Basic(t *testing.T) {
	toks, err := tokenize(`foo bar "baz qux"`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"foo", "bar", "baz qux"}
	if len(toks) != len(want) {
		t.Fatalf("token count: got %d want %d: %v", len(toks), len(want), toks)
	}
	for i, w := range want {
		if toks[i] != w {
			t.Errorf("tok[%d]: got %q want %q", i, toks[i], w)
		}
	}
}

// ---- named-only tag parser tests ------------------------------------------

// namedOnlyBindArgs has B as named-only so positional tokens skip over it.
// B is optional so the skip-positional test can omit it without a parse error.
type namedOnlyBindArgs struct {
	A string
	B string `cmd:"named-only,optional"`
	C string
}

func TestParser_NamedOnlySkippedPositionally(t *testing.T) {
	schema, err := SchemaOf(namedOnlyBindArgs{})
	if err != nil {
		t.Fatalf("SchemaOf: %v", err)
	}
	var p Parser
	// "foo bar" should bind A=foo, C=bar (B is named-only, skipped positionally).
	m, err := p.Bind("foo bar", schema)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["A"] != "foo" {
		t.Errorf("A: got %v want foo", m["A"])
	}
	if _, ok := m["B"]; ok {
		t.Errorf("B should not be set from positional args, got %v", m["B"])
	}
	if m["C"] != "bar" {
		t.Errorf("C: got %v want bar", m["C"])
	}
}

// sliceFieldArgs has a []string field; the parser must reject it with a clear
// error rather than silently binding the raw string.
type sliceFieldArgs struct {
	Names []string
}

func TestParser_SliceFieldReturnsError(t *testing.T) {
	schema, err := SchemaOf(sliceFieldArgs{})
	if err != nil {
		t.Fatalf("SchemaOf: %v", err)
	}
	var p Parser
	_, err = p.Bind("alice bob", schema)
	if err == nil {
		t.Fatal("expected error binding slice field from positional args, got nil")
	}
	if !strings.Contains(err.Error(), "cannot be bound from positional args") {
		t.Errorf("error message should mention 'cannot be bound from positional args', got: %v", err)
	}
}

func TestParser_NamedOnlySettableByName(t *testing.T) {
	schema, err := SchemaOf(namedOnlyBindArgs{})
	if err != nil {
		t.Fatalf("SchemaOf: %v", err)
	}
	var p Parser
	// B is settable via --B=value even though it's named-only.
	m, err := p.Bind("foo --B=explicit bar", schema)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["A"] != "foo" {
		t.Errorf("A: got %v want foo", m["A"])
	}
	if m["B"] != "explicit" {
		t.Errorf("B: got %v want explicit", m["B"])
	}
	if m["C"] != "bar" {
		t.Errorf("C: got %v want bar", m["C"])
	}
}
