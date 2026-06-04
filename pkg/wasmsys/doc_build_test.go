package wasmsys

import "testing"

// The native build only compiles sdk.go (exports.go is wasip1-tagged). This
// test exists so `go test ./pkg/wasmsys/` exercises the package on the host
// toolchain; the real wasip1 compile is validated by the fixture builds in
// later tasks.
func TestPackageCompiles(t *testing.T) {
	_ = ReadWrite[int32]()
}
