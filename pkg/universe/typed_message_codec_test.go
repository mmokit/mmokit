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
	bytes := pkguniverse.EncodeTypedMessage("damageWire", &src)

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
