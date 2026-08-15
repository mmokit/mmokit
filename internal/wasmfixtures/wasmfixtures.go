// Package wasmfixtures holds the WebAssembly guest fixtures the framework's
// own tests compile and load, together with the helper that builds them.
//
// The guest modules (shieldregen, wavetune) are wasip1 `package main` programs
// behind a `//go:build wasip1` tag, so they are invisible to an ordinary host
// build. podcomp is untagged: it declares plain POD components shared by a
// guest and by the host-side tests that assert against it.
package wasmfixtures

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Build compiles the Go package at dir to a wasip1 reactor .wasm and returns
// the output path, which lives under the test's temp dir and is cleaned up
// with it.
//
// dir is resolved against the MODULE ROOT, not the calling test's working
// directory. That distinction is the whole point of this helper: these
// fixtures are built from tests in several different packages, and Go runs
// each test with its own package directory as the working directory, so a
// package-relative path silently breaks the moment a test file moves to
// another package. Module-root-relative paths survive that.
//
// The -buildmode=c-shared flag is REQUIRED on this Go toolchain to emit a
// reactor whose //go:wasmexport functions are reachable.
func Build(t testing.TB, dir string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), filepath.Base(dir)+".wasm")
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", out, ".")
	cmd.Dir = filepath.Join(ModuleRoot(t), dir)
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", dir, err, b)
	}
	return out
}

// ModuleRoot returns the absolute path of the directory holding go.mod.
// `go env GOMOD` answers from any directory inside the module, which is what
// makes Build independent of the caller's package.
func ModuleRoot(t testing.TB) string {
	t.Helper()
	b, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	gomod := strings.TrimSpace(string(b))
	if gomod == "" || gomod == os.DevNull {
		t.Fatal("wasmfixtures: not inside a Go module")
	}
	return filepath.Dir(gomod)
}
