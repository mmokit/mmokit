package universe

import (
	"testing"
)

func TestBorderDispatcher_TickNoCandidatesNoPanic(t *testing.T) {
	d := NewBorderDispatcher(nil, nil)
	d.Tick(0)
}

func TestBorderDispatcher_TickSkipsWithoutNeighbors(t *testing.T) {
	// Even if base is non-nil, an empty neighbor map is a no-op.
	// This test uses a nil base intentionally — the Phase 4 stub
	// must handle nil safely so Phase 5 and 6 integration tests
	// don't have to stand up a full mesh just to exercise tick code.
	d := NewBorderDispatcher(nil, map[string]*NodeViewer{})
	d.Tick(42)
}

func TestBorderDispatcher_TickIgnoresNilNeighbors(t *testing.T) {
	// A nil neighbor entry should be skipped, not panic.
	viewers := map[string]*NodeViewer{
		"node_1_0": nil,
	}
	d := NewBorderDispatcher(nil, viewers)
	d.Tick(1)
}
