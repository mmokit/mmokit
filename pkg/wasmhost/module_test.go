package wasmhost

import (
	"context"
	"encoding/binary"
	"math"
	"testing"

	"github.com/zenion/mmoserver/pkg/wasmabi"
)

func f32sToBytes(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return b
}
func bytesToF32s(b []byte) []float32 {
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return out
}

func TestModule_UpdateBridgesColumn(t *testing.T) {
	ctx := context.Background()
	rt := New(ctx)
	defer rt.Close(ctx)
	m, err := Load(ctx, rt, buildModule(t, "internal/echomod"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(ctx)

	if got := m.ABIVersion(ctx); got != wasmabi.ABIVersion {
		t.Fatalf("ABIVersion=%d want %d", got, wasmabi.ABIVersion)
	}
	id, rw := m.Query(ctx)
	if id != wasmabi.ElemSize[float32]() || !rw {
		t.Fatalf("Query=(%d,%v) want (%d,true)", id, rw, wasmabi.ElemSize[float32]())
	}

	in := f32sToBytes([]float32{1, 2, 3})
	out, err := m.Update(ctx, 3, 0.05, 0, in)
	if err != nil {
		t.Fatal(err)
	}
	got := bytesToF32s(out)
	want := []float32{2, 3, 4}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("col[%d]=%v want %v", i, got[i], want[i])
		}
	}
}

func TestModule_SnapshotRestore(t *testing.T) {
	ctx := context.Background()
	rt := New(ctx)
	defer rt.Close(ctx)
	wasm := buildModule(t, "internal/echomod")

	m1, _ := Load(ctx, rt, wasm)
	in := f32sToBytes([]float32{0})
	m1.Update(ctx, 1, 0.05, 0, in)
	m1.Update(ctx, 1, 0.05, 0, in) // ticks == 2
	state, err := m1.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m1.Close(ctx)

	m2, _ := Load(ctx, rt, wasm)
	defer m2.Close(ctx)
	if err := m2.Restore(ctx, state); err != nil {
		t.Fatal(err)
	}
	s2, _ := m2.Snapshot(ctx) // ticks should still read 2 (4 LE bytes)
	if len(s2) != 4 || s2[0] != 2 {
		t.Fatalf("restored ticks = %v, want first byte 2", s2)
	}
}
