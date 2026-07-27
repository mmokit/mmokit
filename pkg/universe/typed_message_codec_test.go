package universe_test

import (
	"reflect"
	"testing"

	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

type damageWire struct {
	Amount float32
	Dealt  float32
}

func TestEncodeDecodeTypedMessage(t *testing.T) {
	src := damageWire{Amount: 25, Dealt: 0}
	bytes, err := pkguniverse.EncodeTypedMessage("damageWire", &src)
	if err != nil {
		t.Fatalf("EncodeTypedMessage: %v", err)
	}

	typeName, payload := pkguniverse.SplitTypedMessage(bytes)
	if typeName != "damageWire" {
		t.Fatalf("type name = %q, want damageWire", typeName)
	}
	out := damageWire{}
	pkguniverse.DecodeTypedMessage(payload, &out)
	if !reflect.DeepEqual(src, out) {
		t.Fatalf("roundtrip:\n got %+v\nwant %+v", out, src)
	}
}

// TestTypeKeyIsPackageQualified pins the wire-key choice — Type.String()
// produces "package.Name" so two unrelated types named the same in
// different packages don't collide on the wire. Regressing to Type.Name()
// would silently break cross-process dispatch for any game that defines
// e.g. combat.Damage and effects.Damage.
func TestTypeKeyIsPackageQualified(t *testing.T) {
	got := reflect.TypeFor[damageWire]().String()
	if got == "damageWire" || got == "" {
		t.Fatalf("Type.String() = %q; expected package-qualified form like \"universe_test.damageWire\"", got)
	}
}
