package system

import "testing"

type myPhase uint8

const phaseChanneling myPhase = 1

func TestToUint8_NamedType(t *testing.T) {
	if got := toUint8(phaseChanneling); got != 1 {
		t.Fatalf("toUint8 dropped named-uint8 type: got %d, want 1", got)
	}
	if got := toUint8(myPhase(2)); got != 2 {
		t.Fatalf("toUint8 dropped named-uint8 type: got %d, want 2", got)
	}
}

type myU16 uint16

func TestToUint16_NamedType(t *testing.T) {
	if got := toUint16(myU16(300)); got != 300 {
		t.Fatalf("toUint16 dropped named-uint16 type: got %d, want 300", got)
	}
}

type myU32 uint32

func TestToUint32_NamedType(t *testing.T) {
	if got := toUint32(myU32(70000)); got != 70000 {
		t.Fatalf("toUint32 dropped named-uint32 type: got %d, want 70000", got)
	}
}

type myI16 int16

func TestToInt16_NamedType(t *testing.T) {
	if got := toInt16(myI16(-1234)); got != -1234 {
		t.Fatalf("toInt16 dropped named-int16 type: got %d, want -1234", got)
	}
}

type myMul float32

func TestToFloat32_NamedType(t *testing.T) {
	if got := toFloat32(myMul(2.5)); got != 2.5 {
		t.Fatalf("toFloat32 dropped named-float32 type: got %v, want 2.5", got)
	}
}
