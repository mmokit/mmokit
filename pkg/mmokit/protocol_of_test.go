package mmokit

import "testing"

// TestProtocolOf_NilProcess returns nil rather than panicking. Important
// because WorldBase.SendEvent's init closure calls ProtocolOf(b.Process())
// which can return a typed-nil *Process for WorldBases constructed
// without a coordinator (notably unit tests).
func TestProtocolOf_NilProcess(t *testing.T) {
	if got := ProtocolOf(nil); got != nil {
		t.Errorf("ProtocolOf(nil) = %v, want nil", got)
	}
	if got := ServerEventsOf(nil); got != nil {
		t.Errorf("ServerEventsOf(nil) = %v, want nil", got)
	}
}
