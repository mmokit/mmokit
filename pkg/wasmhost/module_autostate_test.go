package wasmhost

import (
	"context"
	"testing"
	"unsafe"
)

func i32bytes(c []int32) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(&c[0])), len(c)*4)
}
func i32at(b []byte, i int) int32 {
	return *(*int32)(unsafe.Pointer(&b[i*4]))
}

// A module with NO Stateful impl but an exported untagged field must have that
// field auto-snapshotted and restored across a fresh instance (the hot-swap path).
func TestAutoSnapshotRoundTrip(t *testing.T) {
	ctx := context.Background()
	rt := New(ctx)
	defer rt.Close(ctx)
	wasm := buildModule(t, "internal/statemod")

	m1, err := Load(ctx, rt, wasm)
	if err != nil {
		t.Fatal(err)
	}
	// Tick 3 times → Count == 3 (mirrored into the column).
	in := i32bytes([]int32{0})
	for range 3 {
		in, err = m1.Update(ctx, 1, 0.1, in)
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := i32at(in, 0); got != 3 {
		t.Fatalf("pre-snapshot count = %d, want 3", got)
	}
	snap, err := m1.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap) == 0 {
		t.Fatal("auto-snapshot produced empty bytes (Count was not captured)")
	}
	m1.Close(ctx)

	// Fresh instance (Count starts 0); restore the snapshot; one tick must
	// continue 3 → 4, proving the auto-snapshot carried the state.
	m2, err := Load(ctx, rt, wasm)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close(ctx)
	if err := m2.Restore(ctx, snap); err != nil {
		t.Fatal(err)
	}
	out, err := m2.Update(ctx, 1, 0.1, i32bytes([]int32{0}))
	if err != nil {
		t.Fatal(err)
	}
	if got := i32at(out, 0); got != 4 {
		t.Fatalf("restored count continued to %d, want 4 (auto-snapshot did not carry state)", got)
	}
}
