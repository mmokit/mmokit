package mmokit_test

import (
	"context"
	"testing"

	"github.com/zenion/mmoserver/pkg/wasmhost"
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

	if v := m.ABIVersion(ctx); v != 1 {
		t.Fatalf("ABIVersion=%d want 1", v)
	}
	id, rw := m.Query(ctx)
	if id != 1 || !rw {
		t.Fatalf("Query=(%d,%v) want (1,true)  [Shield typeID=1, ReadWrite]", id, rw)
	}
}
