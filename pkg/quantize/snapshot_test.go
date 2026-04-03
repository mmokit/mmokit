package quantize

import (
	"math"
	"testing"
)

func TestSnapshotUint8(t *testing.T) {
	buf := make([]byte, 64)
	w := NewSnapshotWriter(buf)
	w.Uint8(0)
	w.Uint8(127)
	w.Uint8(255)

	r := NewSnapshotReader(w.Bytes())
	if v := r.Uint8(); v != 0 {
		t.Errorf("Uint8: got %d, want 0", v)
	}
	if v := r.Uint8(); v != 127 {
		t.Errorf("Uint8: got %d, want 127", v)
	}
	if v := r.Uint8(); v != 255 {
		t.Errorf("Uint8: got %d, want 255", v)
	}
}

func TestSnapshotUint16(t *testing.T) {
	buf := make([]byte, 64)
	w := NewSnapshotWriter(buf)
	w.Uint16(0)
	w.Uint16(1234)
	w.Uint16(65535)

	r := NewSnapshotReader(w.Bytes())
	if v := r.Uint16(); v != 0 {
		t.Errorf("Uint16: got %d, want 0", v)
	}
	if v := r.Uint16(); v != 1234 {
		t.Errorf("Uint16: got %d, want 1234", v)
	}
	if v := r.Uint16(); v != 65535 {
		t.Errorf("Uint16: got %d, want 65535", v)
	}
}

func TestSnapshotUint32(t *testing.T) {
	buf := make([]byte, 64)
	w := NewSnapshotWriter(buf)
	w.Uint32(0)
	w.Uint32(123456789)

	r := NewSnapshotReader(w.Bytes())
	if v := r.Uint32(); v != 0 {
		t.Errorf("Uint32: got %d, want 0", v)
	}
	if v := r.Uint32(); v != 123456789 {
		t.Errorf("Uint32: got %d, want 123456789", v)
	}
}

func TestSnapshotInt16(t *testing.T) {
	buf := make([]byte, 64)
	w := NewSnapshotWriter(buf)
	w.Int16(0)
	w.Int16(1234)
	w.Int16(-1234)
	w.Int16(32767)
	w.Int16(-32768)

	r := NewSnapshotReader(w.Bytes())
	if v := r.Int16(); v != 0 {
		t.Errorf("Int16: got %d, want 0", v)
	}
	if v := r.Int16(); v != 1234 {
		t.Errorf("Int16: got %d, want 1234", v)
	}
	if v := r.Int16(); v != -1234 {
		t.Errorf("Int16: got %d, want -1234", v)
	}
	if v := r.Int16(); v != 32767 {
		t.Errorf("Int16: got %d, want 32767", v)
	}
	if v := r.Int16(); v != -32768 {
		t.Errorf("Int16: got %d, want -32768", v)
	}
}

func TestSnapshotFloat32(t *testing.T) {
	buf := make([]byte, 64)
	w := NewSnapshotWriter(buf)
	w.Float32(0)
	w.Float32(3.14)
	w.Float32(-1.5)

	r := NewSnapshotReader(w.Bytes())
	if v := r.Float32(); v != 0 {
		t.Errorf("Float32: got %v, want 0", v)
	}
	if v := r.Float32(); v != 3.14 {
		t.Errorf("Float32: got %v, want 3.14", v)
	}
	if v := r.Float32(); v != -1.5 {
		t.Errorf("Float32: got %v, want -1.5", v)
	}
}

func TestSnapshotBool(t *testing.T) {
	buf := make([]byte, 64)
	w := NewSnapshotWriter(buf)
	w.Bool(true)
	w.Bool(false)

	r := NewSnapshotReader(w.Bytes())
	if v := r.Bool(); !v {
		t.Errorf("Bool: got false, want true")
	}
	if v := r.Bool(); v {
		t.Errorf("Bool: got true, want false")
	}
}

func TestSnapshotQPos(t *testing.T) {
	cellSizes := []float32{1024, 4096, 8192, 16384}
	for _, cs := range cellSizes {
		tolerance := cs / 65535
		buf := make([]byte, 64)
		w := NewSnapshotWriter(buf)

		val := cs / 3
		w.QPos(val, cs)

		r := NewSnapshotReader(w.Bytes())
		got := r.UnQPos(cs)
		if abs32(got-val) > tolerance {
			t.Errorf("QPos round-trip %v at cs=%v: got %v, delta %v", val, cs, got, abs32(got-val))
		}
	}
}

func TestSnapshotQAngle(t *testing.T) {
	tolerance := float32(2 * math.Pi / 65535)
	buf := make([]byte, 64)
	w := NewSnapshotWriter(buf)
	w.QAngle(1.5)

	r := NewSnapshotReader(w.Bytes())
	got := r.UnQAngle()
	if abs32(got-1.5) > tolerance {
		t.Errorf("QAngle round-trip 1.5: got %v", got)
	}
}

func TestSnapshotQNorm(t *testing.T) {
	buf := make([]byte, 64)
	w := NewSnapshotWriter(buf)
	w.QNorm(0.75)

	r := NewSnapshotReader(w.Bytes())
	got := r.UnQNorm()
	if abs32(got-0.75) > 1.0/255 {
		t.Errorf("QNorm round-trip 0.75: got %v", got)
	}
}

func TestSnapshotQVel(t *testing.T) {
	scale := float32(2000)
	tolerance := scale / 32767
	buf := make([]byte, 64)
	w := NewSnapshotWriter(buf)
	w.QVel(-500, scale)

	r := NewSnapshotReader(w.Bytes())
	got := r.UnQVel(scale)
	if abs32(got-(-500)) > tolerance {
		t.Errorf("QVel round-trip -500: got %v", got)
	}
}

func TestSnapshotQRel(t *testing.T) {
	halfRange := float32(3000)
	tolerance := halfRange / 32767
	buf := make([]byte, 64)
	w := NewSnapshotWriter(buf)
	w.QRel(1234, halfRange)

	r := NewSnapshotReader(w.Bytes())
	got := r.UnQRel(halfRange)
	if abs32(got-1234) > tolerance {
		t.Errorf("QRel round-trip 1234: got %v", got)
	}
}

func TestSnapshotMixedFields(t *testing.T) {
	buf := make([]byte, 128)
	w := NewSnapshotWriter(buf)

	w.Uint8(42)
	w.Uint16(1000)
	w.Int16(-500)
	w.Uint32(999999)
	w.Float32(2.718)
	w.Bool(true)
	w.QPos(512, 1024)
	w.QAngle(1.0)
	w.QNorm(0.5)
	w.QVel(-300, 1000)
	w.QRel(750, 1500)

	if w.Len() != 1+2+2+4+4+1+2+2+1+2+2 {
		t.Errorf("Len: got %d, want %d", w.Len(), 1+2+2+4+4+1+2+2+1+2+2)
	}

	r := NewSnapshotReader(w.Bytes())

	if v := r.Uint8(); v != 42 {
		t.Errorf("mixed Uint8: got %d", v)
	}
	if v := r.Uint16(); v != 1000 {
		t.Errorf("mixed Uint16: got %d", v)
	}
	if v := r.Int16(); v != -500 {
		t.Errorf("mixed Int16: got %d", v)
	}
	if v := r.Uint32(); v != 999999 {
		t.Errorf("mixed Uint32: got %d", v)
	}
	if v := r.Float32(); v != 2.718 {
		t.Errorf("mixed Float32: got %v", v)
	}
	if v := r.Bool(); !v {
		t.Errorf("mixed Bool: got false")
	}

	posGot := r.UnQPos(1024)
	if abs32(posGot-512) > 1024.0/65535 {
		t.Errorf("mixed QPos: got %v", posGot)
	}

	angleGot := r.UnQAngle()
	if abs32(angleGot-1.0) > float32(2*math.Pi/65535) {
		t.Errorf("mixed QAngle: got %v", angleGot)
	}

	normGot := r.UnQNorm()
	if abs32(normGot-0.5) > 1.0/255 {
		t.Errorf("mixed QNorm: got %v", normGot)
	}

	velGot := r.UnQVel(1000)
	if abs32(velGot-(-300)) > 1000.0/32767 {
		t.Errorf("mixed QVel: got %v", velGot)
	}

	relGot := r.UnQRel(1500)
	if abs32(relGot-750) > 1500.0/32767 {
		t.Errorf("mixed QRel: got %v", relGot)
	}

	if r.Remaining() != 0 {
		t.Errorf("Remaining: got %d, want 0", r.Remaining())
	}
}

func TestSnapshotResetAndReuse(t *testing.T) {
	buf := make([]byte, 64)
	w := NewSnapshotWriter(buf)

	w.Uint16(1111)
	w.Uint16(2222)
	if w.Len() != 4 {
		t.Errorf("Len before reset: got %d, want 4", w.Len())
	}

	w.Reset()
	if w.Len() != 0 {
		t.Errorf("Len after reset: got %d, want 0", w.Len())
	}

	w.Uint16(3333)
	r := NewSnapshotReader(w.Bytes())
	if v := r.Uint16(); v != 3333 {
		t.Errorf("After reset, Uint16: got %d, want 3333", v)
	}
}

func TestSnapshotRemaining(t *testing.T) {
	buf := make([]byte, 10)
	w := NewSnapshotWriter(buf)
	w.Uint8(1)
	w.Uint16(2)
	w.Uint32(3)

	r := NewSnapshotReader(w.Bytes())
	if r.Remaining() != 7 {
		t.Errorf("Remaining initial: got %d, want 7", r.Remaining())
	}
	r.Uint8()
	if r.Remaining() != 6 {
		t.Errorf("Remaining after Uint8: got %d, want 6", r.Remaining())
	}
	r.Uint16()
	if r.Remaining() != 4 {
		t.Errorf("Remaining after Uint16: got %d, want 4", r.Remaining())
	}
	r.Uint32()
	if r.Remaining() != 0 {
		t.Errorf("Remaining after Uint32: got %d, want 0", r.Remaining())
	}
}
