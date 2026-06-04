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
