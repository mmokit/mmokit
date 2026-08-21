package main

import (
	"strings"
	"testing"
)

func mustPanic(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("%s did not panic on an unknown encoding", what)
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "sdkgen:") {
			t.Errorf("%s panicked with %v, want an sdkgen-prefixed message", what, r)
		}
	}()
	fn()
}

// TestUnknownEncodingsArePanicsNotFallbacks covers four sites that used to
// degrade silently, and each one degraded differently badly:
//
//   - encodingToTSType returned "number" and encodingToCSharpType returned
//     "float", so a new encoding shipped as a plain scalar in every generated
//     client while the server emitted something else.
//   - Both initial-field decoders assigned a default WITHOUT advancing the
//     offset, which desynchronizes every initial field after it — a
//     corruption that reads as plausible values rather than as an error.
//
// The decode-line emitters already panicked; the asymmetry was the point.
func TestUnknownEncodingsArePanicsNotFallbacks(t *testing.T) {
	mustPanic(t, "encodingToTSType", func() { encodingToTSType("qsomethingnew") })
	mustPanic(t, "encodingToCSharpType", func() { encodingToCSharpType("qsomethingnew") })

	var b strings.Builder
	mustPanic(t, "writeInitialFieldDecoder", func() {
		writeInitialFieldDecoder(&b, BindingSchemaField{Name: "f", Encoding: "qsomethingnew", Initial: true}, "")
	})

	var sb strings.Builder
	mustPanic(t, "writeCsInitialFieldDecode", func() {
		writeCsInitialFieldDecode(&sb, "e.f", "existing?.f", BindingSchemaField{Name: "f", Encoding: "qsomethingnew", Initial: true})
	})
}

// TestQuatEncodingMapsToAnObjectType pins the one-name-non-scalar decision:
// qquat stays a single schema field and gains an object type, rather than
// being split into four synthesized scalars the schema does not carry.
func TestQuatEncodingMapsToAnObjectType(t *testing.T) {
	if got := encodingToTSType("qquat"); got != "Quat" {
		t.Errorf("encodingToTSType(qquat) = %q, want Quat", got)
	}
	if got := encodingToCSharpType("qquat"); got != "Quat" {
		t.Errorf("encodingToCSharpType(qquat) = %q, want Quat", got)
	}
}

// TestSchemaUsesQuatGatesTheImports is the mechanism that keeps a 2D SDK
// byte-identical: the quat imports are emitted only when a schema actually
// carries the encoding.
func TestSchemaUsesQuatGatesTheImports(t *testing.T) {
	twoD := ProtocolSchema{Entities: []EntitySchema{{
		Bindings: []BindingSchema{{Fields: []BindingSchemaField{
			{Name: "worldX", Encoding: "f32"}, {Name: "angle", Encoding: "qangle"},
		}}},
	}}}
	if schemaUsesQuat(twoD) {
		t.Error("a 2D schema reports using qquat")
	}

	threeD := ProtocolSchema{Entities: []EntitySchema{{
		Bindings: []BindingSchema{{Fields: []BindingSchemaField{
			{Name: "worldX", Encoding: "f32"}, {Name: "rot", Encoding: "qquat"},
		}}},
	}}}
	if !schemaUsesQuat(threeD) {
		t.Error("a 3D schema does not report using qquat")
	}

	// A var-tail item field must count too, or an SDK whose only quaternion
	// lives in a repeated field would emit a decode line with no import.
	tail := ProtocolSchema{Entities: []EntitySchema{{
		VarTail: &VarTailSchema{ItemFields: []BindingSchemaField{{Name: "rot", Encoding: "qquat"}}},
	}}}
	if !schemaUsesQuat(tail) {
		t.Error("a var-tail qquat field is not detected")
	}
}
