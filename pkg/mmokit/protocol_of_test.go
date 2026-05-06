package mmokit

import "testing"

// TestProtocolOf_NilProcess returns nil rather than panicking. Important
// because callers may invoke ProtocolOf(b.Process()) where b is constructed
// without a coordinator (notably unit tests).
func TestProtocolOf_NilProcess(t *testing.T) {
	if got := ProtocolOf(nil); got != nil {
		t.Errorf("ProtocolOf(nil) = %v, want nil", got)
	}
}
