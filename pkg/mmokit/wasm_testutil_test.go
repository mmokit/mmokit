package mmokit_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// buildWasmModule compiles a Go package at srcDir to a wasip1 reactor .wasm and
// returns the output path. The -buildmode=c-shared flag is REQUIRED on this Go
// toolchain to emit a reactor whose //go:wasmexport functions are reachable.
func buildWasmModule(t *testing.T, srcDir string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "mod.wasm")
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", out, ".")
	cmd.Dir = srcDir
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", srcDir, err, b)
	}
	return out
}
