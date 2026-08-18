package universe

import (
	"github.com/mmokit/mmokit/pkg/engine"
)

// NewSchemaStage builds a throwaway Stage carrying this process's realized
// entity-kind definitions, for schema derivation only.
//
// It exists because the protocol schema used to be derived by iterating
// Process.Cells and reading EntityKindDefs off the first cell that had any.
// That made the emitted schema depend on which ROLES the process runs: a
// gateway or a pure coordinator owns no cells, so it reported no entities at
// all, and at runtime every process reported none because the derivation only
// ever ran on the --dump-schema path. A protocol contract that changes shape
// with the deployment topology cannot be fingerprinted — the SDK is generated
// from one process's answer and validated against another's.
//
// The stage is cell-free in the sense that matters: it is constructed here
// rather than found, so a process with zero cells derives the same kinds as a
// process with nine. RealizeKindSpecs replays exactly what createNode replays,
// against a fresh engine whose loop is never started (loops start at
// Cell.Run, not at engine.New), and the stage is discarded by the caller.
//
// The same construction is what examples/space's own test fixture does to
// realize kinds outside a running process.
func (c *Process) NewSchemaStage() *Stage {
	eng := engine.New(engine.Config{TickRate: c.cfg.TickRate}, c.ConnMgr, c.Log)
	stage := NewStage(eng, CellID{}, c.cfg.AoIRadius, nil, c.Wire())
	stage.coord = c
	stage.baseCellSize = c.CellSize()
	c.RealizeKindSpecs(stage)
	return stage
}
