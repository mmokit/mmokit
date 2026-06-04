package wasmabi

import "testing"

func TestEncodeQuery_RoundTrips(t *testing.T) {
	q := EncodeQuery(7, true)
	id, rw := DecodeQuery(q)
	if id != 7 || !rw {
		t.Fatalf("got id=%d rw=%v, want 7,true", id, rw)
	}
	id, rw = DecodeQuery(EncodeQuery(3, false))
	if id != 3 || rw {
		t.Fatalf("got id=%d rw=%v, want 3,false", id, rw)
	}
}

func TestHeaderSize_Aligned(t *testing.T) {
	if HeaderSize%8 != 0 {
		t.Fatalf("HeaderSize=%d must be 8-aligned", HeaderSize)
	}
}

func TestSchemaRoundTrip(t *testing.T) {
	in := []ParamField{
		{Name: "Amplitude", Kind: 2, Default: 220, Min: 60, Max: 420, Step: 10, BoundsMask: BoundDefault | BoundMin | BoundMax | BoundStep},
		{Name: "Enabled", Kind: 3, Default: 1, BoundsMask: BoundDefault},
	}
	out, err := DecodeSchema(EncodeSchema(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Name != "Amplitude" || out[0].Max != 420 || out[1].Kind != 3 {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

func TestBlockRoundTrip(t *testing.T) {
	in := []float64{1, 2.5, 0}
	out, err := DecodeBlock(EncodeBlock(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 || out[1] != 2.5 {
		t.Fatalf("block round-trip mismatch: %+v", out)
	}
}

func TestDecodeBlockRejectsMisaligned(t *testing.T) {
	if _, err := DecodeBlock([]byte{1, 2, 3}); err == nil {
		t.Fatal("non-multiple-of-8 block should error")
	}
}

func TestDecodeSchemaRejectsTruncated(t *testing.T) {
	full := EncodeSchema([]ParamField{{Name: "X", Kind: 2, BoundsMask: BoundMin}})
	if _, err := DecodeSchema(full[:len(full)-3]); err == nil {
		t.Fatal("truncated schema should error")
	}
	if _, err := DecodeSchema(nil); err == nil {
		t.Fatal("empty buffer (missing count) should error")
	}
}
