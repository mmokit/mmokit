package system

import (
	"github.com/mlange-42/ark/ecs"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/component"
)

// DebugInfoWriter walks every entity with *component.DebugInfo each
// tick and writes Presence (LOCAL/REPLICA/GHOST), OwnerHost (uint8
// host index), and AoIRadius. Game code must not write to DebugInfo
// directly — the writer overwrites every tick.
//
// The writer is engine-owned. The wiring layer (pkg/universe) lifts
// it into the per-cell tick loop via a SystemDef that's auto-prepended
// to Process.systemDefs in Build().
type DebugInfoWriter struct {
	debugFilter *ecs.Filter1[component.DebugInfo]
	ghostMap    *ecs.Map1[component.Ghost]
	replicaMap  *ecs.Map1[component.Replica]

	// localHost is the host-index for the stage hosting this writer.
	// Captured at construction; updated only on host migration via
	// SetLocalHost (rare — most stages keep the same host for life).
	localHost uint8

	// hostByCellID resolves a source-cell string ID (e.g. "cell_3_4")
	// to a uint8 host index. Used only for REPLICA entities, whose
	// authoritative cell lives on a (possibly remote) host.
	hostByCellID func(cellID string) uint8

	// aoiRadius reads the live AoI radius from Process.Cfg() / Stage.
	aoiRadius func() float32
}

// NewDebugInfoWriter constructs a writer with closures for the bits
// that live outside the cell's ECS world. localHost is captured at
// construction; hostByCellID and aoiRadius are called per-tick.
func NewDebugInfoWriter(
	w *ecs.World,
	localHost uint8,
	hostByCellID func(cellID string) uint8,
	aoiRadius func() float32,
) *DebugInfoWriter {
	return &DebugInfoWriter{
		debugFilter:  ecs.NewFilter1[component.DebugInfo](w),
		ghostMap:     ecs.NewMap1[component.Ghost](w),
		replicaMap:   ecs.NewMap1[component.Replica](w),
		localHost:    localHost,
		hostByCellID: hostByCellID,
		aoiRadius:    aoiRadius,
	}
}

// SetLocalHost updates the writer's notion of which host owns the
// stage. Call from the wiring layer when a cell migrates to a new
// host. Most stages keep the same host for their lifetime, so this
// is rarely invoked.
func (w *DebugInfoWriter) SetLocalHost(idx uint8) { w.localHost = idx }

// Update runs the writer once. Plug into the engine's per-cell tick
// before the network/replication system so emitted snapshots see the
// fresh DebugInfo bytes.
func (w *DebugInfoWriter) Update(_ float32) {
	radius := w.aoiRadius()
	q := w.debugFilter.Query()
	defer q.Close()
	for q.Next() {
		e := q.Entity()
		di := q.Get()

		switch {
		case w.ghostMap.HasAll(e):
			di.Presence = uint8(enginepb.EntityMeshState_EMS_GHOST)
			di.OwnerHost = w.localHost
		case w.replicaMap.HasAll(e):
			di.Presence = uint8(enginepb.EntityMeshState_EMS_REPLICA)
			di.OwnerHost = w.hostByCellID(w.replicaMap.Get(e).SourceCellID)
		default:
			di.Presence = uint8(enginepb.EntityMeshState_EMS_LOCAL)
			di.OwnerHost = w.localHost
		}
		di.AoIRadius = radius
	}
}
