package replication

import "testing"

func TestDispatcher_WalkSendsInRangeEntities(t *testing.T) {
	viewer := &fakeViewer{id: 1, x: 0, y: 0}
	cands := []EntityRef{
		{NetID: NetID{ID: 1}, Kind: 1, X: 50, Y: 0, Build: func(dst []byte) []byte { return append(dst, 0xDE) }},
		{NetID: NetID{ID: 2}, Kind: 1, X: 200, Y: 0, Build: func(dst []byte) []byte { return append(dst, 0xAD) }}, // outside radius 100
	}

	d := NewDispatcher()
	frame := d.Walk(viewer, 0, iterRefs(cands))

	if len(frame.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(frame.Entries))
	}
	if frame.Entries[0].NetID.ID != 1 {
		t.Fatalf("wrong entity included: %d", frame.Entries[0].NetID.ID)
	}
	if len(frame.Entries[0].DeltaBuf) != 1 || frame.Entries[0].DeltaBuf[0] != 0xDE {
		t.Fatalf("wrong delta buf: %x", frame.Entries[0].DeltaBuf)
	}
	if frame.ViewerID != 1 {
		t.Fatalf("frame ViewerID: got %d want 1", frame.ViewerID)
	}
	if frame.Tick != 0 {
		t.Fatalf("frame Tick: got %d want 0", frame.Tick)
	}
}

func TestDispatcher_WalkRespectsUpdateDivisor(t *testing.T) {
	// Viewer returns a tier with UpdateDivisor=3 for kind 1.
	viewer := &tierViewer{
		id: 1, x: 0, y: 0,
		tiers: map[uint16]ReplicationTier{
			1: {Radius: 100, UpdateDivisor: 3},
		},
	}
	cand := []EntityRef{
		{NetID: NetID{ID: 1}, Kind: 1, X: 10, Y: 0, Build: func(dst []byte) []byte { return append(dst, 0xDE) }},
	}

	d := NewDispatcher()

	// Tick 0 is divisible by 3, entity is sent.
	f0 := d.Walk(viewer, 0, iterRefs(cand))
	if len(f0.Entries) != 1 {
		t.Fatalf("tick 0: got %d entries want 1", len(f0.Entries))
	}
	// Tick 1 is skipped.
	f1 := d.Walk(viewer, 1, iterRefs(cand))
	if len(f1.Entries) != 0 {
		t.Fatalf("tick 1: got %d entries want 0", len(f1.Entries))
	}
	// Tick 2 is skipped.
	f2 := d.Walk(viewer, 2, iterRefs(cand))
	if len(f2.Entries) != 0 {
		t.Fatalf("tick 2: got %d entries want 0", len(f2.Entries))
	}
	// Tick 3 is divisible by 3.
	f3 := d.Walk(viewer, 3, iterRefs(cand))
	if len(f3.Entries) != 1 {
		t.Fatalf("tick 3: got %d entries want 1", len(f3.Entries))
	}
}

func TestDispatcher_WalkDropsEmptyDelta(t *testing.T) {
	// An entity whose Build() returns an empty slice is silently dropped.
	// This lets the caller short-circuit a "no changes" decision inside
	// Build without an extra flag.
	viewer := &fakeViewer{id: 1, x: 0, y: 0}
	cand := []EntityRef{
		{NetID: NetID{ID: 1}, Kind: 1, X: 10, Y: 0, Build: func(dst []byte) []byte { return nil }},
		{NetID: NetID{ID: 2}, Kind: 1, X: 20, Y: 0, Build: func(dst []byte) []byte { return dst }},
		{NetID: NetID{ID: 3}, Kind: 1, X: 30, Y: 0, Build: func(dst []byte) []byte { return append(dst, 0xFF) }},
	}

	d := NewDispatcher()
	frame := d.Walk(viewer, 0, iterRefs(cand))

	if len(frame.Entries) != 1 {
		t.Fatalf("expected 1 non-empty entry, got %d", len(frame.Entries))
	}
	if frame.Entries[0].NetID.ID != 3 {
		t.Fatalf("wrong entity kept: %d", frame.Entries[0].NetID.ID)
	}
}

func TestDispatcher_WalkReusesScratchBuffer(t *testing.T) {
	// Build is called with a zero-length slice whose capacity is the
	// dispatcher's persistent scratch buffer. This test verifies:
	//   1. The dispatcher passes in an empty slice (len == 0)
	//   2. A capacity grown by one Build is retained into the next Build
	//   3. Each frame entry owns a distinct DeltaBuf (not aliased to scratch)
	viewer := &fakeViewer{id: 1, x: 0, y: 0}

	var observedLens []int
	var observedCaps []int
	build := func(dst []byte) []byte {
		observedLens = append(observedLens, len(dst))
		observedCaps = append(observedCaps, cap(dst))
		return append(dst, 0xAA, 0xBB, 0xCC)
	}

	cand := []EntityRef{
		{NetID: NetID{ID: 1}, Kind: 1, X: 10, Y: 0, Build: build},
		{NetID: NetID{ID: 2}, Kind: 1, X: 20, Y: 0, Build: build},
	}

	d := NewDispatcher()
	frame := d.Walk(viewer, 0, iterRefs(cand))

	if len(frame.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(frame.Entries))
	}
	// Each call received a zero-length slice.
	for i, l := range observedLens {
		if l != 0 {
			t.Fatalf("call %d: expected empty scratch (len 0), got len %d", i, l)
		}
	}
	// Frame entries own independent byte slices — mutating one must
	// not affect the other.
	frame.Entries[0].DeltaBuf[0] = 0xFF
	if frame.Entries[1].DeltaBuf[0] != 0xAA {
		t.Fatalf("frame entries alias each other: entry[1]=%x", frame.Entries[1].DeltaBuf)
	}
}

func TestDispatcher_WalkBuildsEmptyFrameWhenNoCandidates(t *testing.T) {
	viewer := &fakeViewer{id: 1, x: 0, y: 0}
	d := NewDispatcher()
	frame := d.Walk(viewer, 5, iterRefs(nil))

	if len(frame.Entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(frame.Entries))
	}
	if frame.ViewerID != 1 {
		t.Fatalf("ViewerID lost: %d", frame.ViewerID)
	}
	if frame.Tick != 5 {
		t.Fatalf("Tick lost: %d", frame.Tick)
	}
}

// Test-only helper that wraps a slice in an iter.Seq[EntityRef].
func iterRefs(refs []EntityRef) func(yield func(EntityRef) bool) {
	return func(yield func(EntityRef) bool) {
		for _, r := range refs {
			if !yield(r) {
				return
			}
		}
	}
}

// tierViewer is a test viewer that returns a configured tier per kind.
type tierViewer struct {
	id    uint64
	x, y  float32
	tiers map[uint16]ReplicationTier
	sent  []Frame
}

func (v *tierViewer) ID() uint64                   { return v.id }
func (v *tierViewer) Position() (float32, float32) { return v.x, v.y }
func (v *tierViewer) Tier(kind uint16) ReplicationTier {
	if t, ok := v.tiers[kind]; ok {
		return t
	}
	return ReplicationTier{Radius: 100}
}
func (v *tierViewer) Baselines() *BaselineStore { return nil }
func (v *tierViewer) Send(frame Frame)          { v.sent = append(v.sent, frame) }
