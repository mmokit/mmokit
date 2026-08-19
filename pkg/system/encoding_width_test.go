package system

import (
	"reflect"
	"testing"

	"github.com/mmokit/mmokit/pkg/quantize"
)

func reflectTypeOf(v any) reflect.Type { return reflect.TypeOf(v) }

// Every fixed-width encoding must WRITE the number of bytes it DECLARES.
//
// This is the test that would have caught `net:"pos"`: encodingWireSize said 8
// ("2x float32") while its snapshot writer emitted a single w.Float32 — four.
// Nothing noticed, because the declared width feeds the layout table that
// DeltaEncoder slices with, and a field nobody used never moved the offsets.
// It was a landmine for the first person to reach for the obvious name for a
// Vec2 or Vec3 leaf, which phase 1 makes likely.
//
// The declared width is not cosmetic. It is prefix-summed into the offset
// table at pkg/quantize/delta.go and used for raw byte-range comparison, so a
// width that disagrees with the writer shifts every field after it.
func TestEncodingWireSizeMatchesBytesWritten(t *testing.T) {
	// "string" is variable-length (wireSize -1) and has no fixed-width
	// snapshot writer. Excluded by explicit construction rather than by
	// happening to fall through, so adding another variable-width encoding
	// fails here until it is classified on purpose.
	variable := map[string]bool{"string": true}

	// A value each converter accepts, per encoding.
	sample := map[string]any{
		"f32":    float32(1.5),
		"qvel":   float32(1.5),
		"qangle": float32(1.5),
		"qsize":  float32(1.5),
		"qnorm":  float32(0.5),
		"u8":     uint8(3),
		"u16":    uint16(3),
		"u32":    uint32(3),
		"i16":    int16(-3),
		"bool":   true,
	}

	for encoding, declared := range encodingWireSize {
		if variable[encoding] {
			if declared != -1 {
				t.Errorf("%q is treated as variable-width but declares %d", encoding, declared)
			}
			continue
		}
		val, ok := sample[encoding]
		if !ok {
			t.Errorf("%q has no sample value — add one so its width is checked", encoding)
			continue
		}
		write, err := snapshotWriterFor(fieldMeta{encoding: encoding})
		if err != nil {
			t.Errorf("%q: snapshotWriterFor: %v", encoding, err)
			continue
		}
		w := quantize.NewSnapshotWriter(make([]byte, 64))
		write(val, w)
		if got := len(w.Bytes()); got != declared {
			t.Errorf("%q declares %d bytes in encodingWireSize but its writer emitted %d — "+
				"the declared width is prefix-summed into the delta offset table, so this "+
				"shifts every field after it", encoding, declared, got)
		}
	}
}

// A tagged field the walker cannot encode must be refused where the message can
// name it, not answered with zeros on every tick.
func TestNonScalarTaggedFieldIsRefused(t *testing.T) {
	for _, c := range []struct {
		name     string
		typ      any
		encoding string
	}{
		{"struct tagged u8", struct{ X int }{}, "u8"},
		{"slice tagged f32", []int{}, "f32"},
		{"int tagged string", 0, "string"},
	} {
		if err := checkTaggedFieldKind(reflectTypeOf(c.typ), c.encoding); err == nil {
			t.Errorf("%s: accepted, want refusal", c.name)
		}
	}

	// Named scalar types stay accepted — they are the case the hash walk used
	// to panic on while the snapshot walk encoded them fine.
	type Meters float32
	if err := checkTaggedFieldKind(reflectTypeOf(Meters(0)), "f32"); err != nil {
		t.Errorf("named float32 refused: %v", err)
	}
}
