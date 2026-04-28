package system

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/component"
)

// debugInfoWriterFixture wires up the smallest harness that can run
// DebugInfoWriter.Update once: a world, entity-typed maps, and the
// closures the writer reads. Tests mutate the fixture's localHost /
// hostByCellID / aoiRadius fields after construction; the constructor
// closes over the fixture so the writer reads live values.
type debugInfoWriterFixture struct {
	w            *ecs.World
	debugMap     *ecs.Map1[component.DebugInfo]
	ghostMap     *ecs.Map1[component.Ghost]
	replicaMap   *ecs.Map1[component.Replica]
	localHost    uint8
	hostByCellID func(cellID string) uint8
	aoiRadius    float32
}

func newDebugInfoWriterFixture() *debugInfoWriterFixture {
	w := ecs.NewWorld(1024)
	return &debugInfoWriterFixture{
		w:            w,
		debugMap:     ecs.NewMap1[component.DebugInfo](w),
		ghostMap:     ecs.NewMap1[component.Ghost](w),
		replicaMap:   ecs.NewMap1[component.Replica](w),
		localHost:    0,
		hostByCellID: func(string) uint8 { return 0 },
		aoiRadius:    500,
	}
}

// debugInfoWriterUnderTest wraps DebugInfoWriter with a one-shot Update
// helper for terser test bodies.
type debugInfoWriterUnderTest struct{ *DebugInfoWriter }

func (t *debugInfoWriterUnderTest) UpdateOnce() { t.Update(0) }

// newDebugInfoWriterForTest constructs a writer whose closures read
// from the fixture so post-construction tweaks (e.g. setting
// f.aoiRadius = 1234) take effect on the next UpdateOnce().
func newDebugInfoWriterForTest(f *debugInfoWriterFixture) *debugInfoWriterUnderTest {
	w := NewDebugInfoWriter(
		f.w,
		f.localHost,
		func(id string) uint8 { return f.hostByCellID(id) },
		func() float32 { return f.aoiRadius },
	)
	// localHost is captured at construction. Tests that need a different
	// localHost should pass it via fixture.localHost before calling this
	// constructor, or call SetLocalHost on the returned writer between
	// UpdateOnce() calls.
	return &debugInfoWriterUnderTest{DebugInfoWriter: w}
}

func TestDebugInfoWriter_PresenceLocal(t *testing.T) {
	f := newDebugInfoWriterFixture()
	e := f.debugMap.NewEntity(&component.DebugInfo{})

	wr := newDebugInfoWriterForTest(f)
	wr.UpdateOnce()

	got := f.debugMap.Get(e).Presence
	if got != uint8(enginepb.EntityMeshState_EMS_LOCAL) {
		t.Errorf("Presence: got %d, want EMS_LOCAL (%d)", got, enginepb.EntityMeshState_EMS_LOCAL)
	}
}

func TestDebugInfoWriter_PresenceGhost(t *testing.T) {
	f := newDebugInfoWriterFixture()
	e := f.debugMap.NewEntity(&component.DebugInfo{})
	f.ghostMap.Add(e, &component.Ghost{})

	wr := newDebugInfoWriterForTest(f)
	wr.UpdateOnce()

	got := f.debugMap.Get(e).Presence
	if got != uint8(enginepb.EntityMeshState_EMS_GHOST) {
		t.Errorf("Presence: got %d, want EMS_GHOST", got)
	}
}

func TestDebugInfoWriter_PresenceReplica(t *testing.T) {
	f := newDebugInfoWriterFixture()
	e := f.debugMap.NewEntity(&component.DebugInfo{})
	f.replicaMap.Add(e, &component.Replica{SourceCellID: "cell_3_4"})

	wr := newDebugInfoWriterForTest(f)
	wr.UpdateOnce()

	got := f.debugMap.Get(e).Presence
	if got != uint8(enginepb.EntityMeshState_EMS_REPLICA) {
		t.Errorf("Presence: got %d, want EMS_REPLICA", got)
	}
}

func TestDebugInfoWriter_AoIRadiusFromConfig(t *testing.T) {
	f := newDebugInfoWriterFixture()
	f.aoiRadius = 1234
	e := f.debugMap.NewEntity(&component.DebugInfo{})

	wr := newDebugInfoWriterForTest(f)
	wr.UpdateOnce()

	if got := f.debugMap.Get(e).AoIRadius; got != 1234 {
		t.Errorf("AoIRadius: got %v, want 1234", got)
	}
}

func TestDebugInfoWriter_OwnerHostFromResolver(t *testing.T) {
	f := newDebugInfoWriterFixture()
	f.hostByCellID = func(id string) uint8 {
		if id == "cell_3_4" {
			return 7
		}
		return 0
	}
	e := f.debugMap.NewEntity(&component.DebugInfo{})
	f.replicaMap.Add(e, &component.Replica{SourceCellID: "cell_3_4"})

	wr := newDebugInfoWriterForTest(f)
	wr.UpdateOnce()

	if got := f.debugMap.Get(e).OwnerHost; got != 7 {
		t.Errorf("OwnerHost: got %d, want 7", got)
	}
}

func TestDebugInfoWriter_OwnerHostLocal(t *testing.T) {
	f := newDebugInfoWriterFixture()
	f.localHost = 9
	e := f.debugMap.NewEntity(&component.DebugInfo{})
	// no Replica/Ghost — entity is LOCAL

	// Construct the writer with the fixture's localHost = 9 already set.
	wr := newDebugInfoWriterForTest(f)
	wr.UpdateOnce()

	if got := f.debugMap.Get(e).OwnerHost; got != 9 {
		t.Errorf("OwnerHost: got %d, want 9 (localHost)", got)
	}
}

func TestDebugInfoWriter_NoComponentNoCrash(t *testing.T) {
	f := newDebugInfoWriterFixture()
	// Entity without DebugInfo is excluded by the filter; the test only
	// verifies no panic on archetype-mismatch.
	f.ghostMap.NewEntity(&component.Ghost{})

	wr := newDebugInfoWriterForTest(f)
	wr.UpdateOnce() // must not panic
}

// TestDebugInfoWriter_SetLocalHostBetweenTicks verifies that the shim's
// per-tick SetLocalHost call (used to keep DebugInfo.OwnerHost current
// across cell migrations) is honored on the next Update — i.e. the
// writer reads the field, not a captured constructor value.
func TestDebugInfoWriter_SetLocalHostBetweenTicks(t *testing.T) {
	f := newDebugInfoWriterFixture() // localHost: 0
	e := f.debugMap.NewEntity(&component.DebugInfo{})

	wr := newDebugInfoWriterForTest(f)
	wr.UpdateOnce()
	if got := f.debugMap.Get(e).OwnerHost; got != 0 {
		t.Fatalf("initial OwnerHost: got %d, want 0", got)
	}

	wr.SetLocalHost(11)
	wr.UpdateOnce()
	if got := f.debugMap.Get(e).OwnerHost; got != 11 {
		t.Errorf("after SetLocalHost(11): got %d, want 11", got)
	}
}

// TestDebugInfoWriter_GhostBeatsReplica pins the precedence in the
// switch statement: an entity carrying both Ghost and Replica markers
// (which can briefly happen during handoff teardown) is reported as
// GHOST, matching the deleted meshStateBinding's resolution order.
func TestDebugInfoWriter_GhostBeatsReplica(t *testing.T) {
	f := newDebugInfoWriterFixture()
	e := f.debugMap.NewEntity(&component.DebugInfo{})
	f.ghostMap.Add(e, &component.Ghost{})
	f.replicaMap.Add(e, &component.Replica{SourceCellID: "cell_3_4"})

	wr := newDebugInfoWriterForTest(f)
	wr.UpdateOnce()

	if got := f.debugMap.Get(e).Presence; got != uint8(enginepb.EntityMeshState_EMS_GHOST) {
		t.Errorf("Presence: got %d, want EMS_GHOST (Ghost should win over Replica)", got)
	}
}
