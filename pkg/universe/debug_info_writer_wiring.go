package universe

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/system"
)

// stageProvider is the minimal contract the debug-info wiring needs
// from the per-cell game world. Both bare *Stage and game-side typed
// wrappers that embed *Stage satisfy it.
type stageProvider interface {
	CellID() string
	GetAoIRadius() float32
}

// debugInfoWriterShim wraps a system.DebugInfoWriter as an
// engine.System. Its Factory closure (returned by
// debugInfoWriterSystemDef) captures the *Process; SetDeps stores
// the typed game world (a stageProvider) and constructs the writer
// with closures that lazily resolve localHost / hostByCellID /
// aoiRadius. Lazy resolution matters for two reasons:
//
//  1. SetDeps is invoked from cellTransferExecutor.Receive while it
//     holds Process.mu. HostIndex / HostForCellID would re-enter
//     that same lock and deadlock.
//  2. Cell→host ownership can change at runtime via migration; lazy
//     resolution keeps the OwnerHost overlay accurate without
//     re-wiring the system.
//
// The shim is per-cell — the coordinator's instantiation loop calls
// the SystemDef's Factory once per cell, so each cell gets a fresh
// writer.
type debugInfoWriterShim struct {
	coord  *Process
	sp     stageProvider // nil if the game world doesn't satisfy stageProvider
	writer *system.DebugInfoWriter
}

// SetDeps is invoked by the per-cell setup loop in Build() and by
// cellTransferExecutor.Receive (under Process.mu). gw is either a
// bare *Stage (default world factory) or a typed wrapper that
// embeds *Stage; the stageProvider interface handles both without
// forcing a concrete type assertion. Falls back to defaults
// (localHost = 0, AoIRadius from Config) if gw doesn't satisfy
// stageProvider.
func (s *debugInfoWriterShim) SetDeps(w *ecs.World, _ *engine.Engine, gw any) {
	s.sp, _ = gw.(stageProvider)

	hostByCellID := func(id string) uint8 {
		host := s.coord.HostForCellID(id)
		if host == "" {
			return 0
		}
		return s.coord.HostIndex(host)
	}

	aoiRadius := func() float32 {
		if s.sp != nil {
			return s.sp.GetAoIRadius()
		}
		return s.coord.cfg.AoIRadius
	}

	// localHost starts at 0; refreshLocalHost() at the top of every
	// Update() resolves the current owner. SetDeps must NOT call
	// HostIndex/HostForCellID itself — see type doc for the deadlock.
	s.writer = system.NewDebugInfoWriter(w, 0, hostByCellID, aoiRadius)
}

// Init is a no-op — all setup happens in SetDeps.
func (s *debugInfoWriterShim) Init() {}

// Update refreshes localHost from the current cell→host ownership
// snapshot, then delegates to the underlying writer. The refresh is
// O(1) (two hash-table reads under short-lived locks) and tracks
// post-construction migrations.
func (s *debugInfoWriterShim) Update(dt float32) {
	if s.writer == nil {
		return
	}
	if s.sp != nil {
		host := s.coord.HostForCellID(s.sp.CellID())
		if host == "" {
			s.writer.SetLocalHost(0)
		} else {
			s.writer.SetLocalHost(s.coord.HostIndex(host))
		}
	}
	s.writer.Update(dt)
}

// debugInfoWriterSystemDef returns the SystemDef the coordinator
// prepends to c.systemDefs in Build() so every cell receives the
// writer. Factory is called per-cell to create a fresh shim.
func debugInfoWriterSystemDef(c *Process) engine.SystemDef {
	return engine.SystemDef{
		Name: "DebugInfoWriter",
		Factory: func() engine.System {
			return &debugInfoWriterShim{coord: c}
		},
	}
}
