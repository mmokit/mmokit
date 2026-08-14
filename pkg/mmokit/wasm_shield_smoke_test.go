package mmokit_test

import (
	"context"
	"testing"

	"github.com/mmokit/mmokit/pkg/mmokit/internal/testmods/podcomp"
	"github.com/mmokit/mmokit/pkg/wasmabi"
	"github.com/mmokit/mmokit/pkg/wasmhost"
)

// TestShieldModule_LoadsAndDeclaresQuery builds the real ShieldRegen wasm
// module and confirms it loads as a reactor (init-registered) with the
// expected ABI version and column declaration. Guards against the
// init()-vs-main() and -buildmode=c-shared pitfalls.
func TestShieldModule_LoadsAndDeclaresQuery(t *testing.T) {
	wasmPath := buildWasmModule(t, "internal/testmods/shieldregen")

	ctx := context.Background()
	rt := wasmhost.New(ctx)
	defer rt.Close(ctx)

	wasmBytes := readFile(t, wasmPath)
	m, err := wasmhost.Load(ctx, rt, wasmBytes)
	if err != nil {
		t.Fatalf("load shieldregen module: %v", err)
	}
	defer m.Close(ctx)

	if v := m.ABIVersion(ctx); v != wasmabi.ABIVersion {
		t.Fatalf("ABIVersion=%d want %d", v, wasmabi.ABIVersion)
	}
	id, rw := m.Query(ctx)
	if want := wasmabi.ElemSize[podcomp.Shield](); id != want || !rw {
		t.Fatalf("Query=(%d,%v) want (%d,true)  [Shield elem size, ReadWrite]", id, rw, want)
	}
}
