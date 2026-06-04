package wasmhost

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// buildModule compiles a Go package at srcDir to a wasip1 .wasm reactor and
// returns its bytes.
//
// -buildmode=c-shared is required: a plain `go build` of a wasip1 package
// emits a *command* module (exports `_start`, no `_initialize`) whose _start
// runs main() then proc_exit, closing the module before any export can be
// called. c-shared makes Go 1.24+ emit a true *reactor* (exports
// `_initialize`, no `_start`), which the host instantiates without
// running-and-exiting, leaving //go:wasmexport functions callable.
func buildModule(t *testing.T, srcDir string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "mod.wasm")
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", out, ".")
	cmd.Dir = srcDir
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", srcDir, err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRuntime_CallsExportedAdd(t *testing.T) {
	wasm := buildModule(t, "internal/testmod")
	rt := New(context.Background())
	defer rt.Close(context.Background())

	mod, err := rt.Instantiate(context.Background(), wasm)
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	defer mod.Close(context.Background())

	res, err := mod.ExportedFunction("add").Call(context.Background(), 2, 3)
	if err != nil {
		t.Fatalf("call add: %v", err)
	}
	if res[0] != 5 {
		t.Fatalf("add(2,3) = %d, want 5", res[0])
	}
}
