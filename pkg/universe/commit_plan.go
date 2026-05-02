package universe

import (
	"fmt"
	"time"
)

// CommitKind identifies the shape of a commit (which plan was built).
type CommitKind uint8

const (
	CommitKindSplit CommitKind = iota
	CommitKindMerge
	CommitKindMigrate
)

func (k CommitKind) String() string {
	switch k {
	case CommitKindSplit:
		return "Split"
	case CommitKindMerge:
		return "Merge"
	case CommitKindMigrate:
		return "Migrate"
	default:
		return fmt.Sprintf("CommitKind(%d)", uint8(k))
	}
}

// CommitPlan describes a commit as a sequence of PlanSteps interpreted
// by ExecuteCommitPlan. It replaces the imperative
// applySplitCommit/applyMergeCommit/applyMigrateCommit functions with
// a value: the shape of a commit becomes inspectable, testable, and
// instrumentable without changing control flow.
type CommitPlan struct {
	ID    uint64
	Kind  CommitKind
	Req   *CellTransferRequest
	Steps []PlanStep
	Ctx   *CommitContext
}

// PlanStep is one mutation within a commit. Run performs the mutation;
// the executor runs CheckInvariants(step.Invariants or defaultInvariants)
// after each step and before proceeding to the next.
type PlanStep struct {
	Name       string
	Run        func(c *Process, ctx *CommitContext) error
	Invariants []Invariant // empty = defaultInvariants
}

// CommitContext carries state shared across steps within one commit.
// Replaces the bag of local variables each applyXxxCommit threads
// manually today. Only a subset of fields are relevant to any given
// commit kind — the unused fields stay zero.
type CommitContext struct {
	// Common.
	Req          *CellTransferRequest // underlying request (adoptedUsers, commands, mutation, etc.)
	PreOwnership map[MeshCellID]string
	Mutation     topologyMutation

	// Split.
	ParentKey        MeshCellID
	Children         [4]CellID
	ParentCell       *Cell             // resolved local *Cell for the parent (nil when parent lives on a remote host)
	HadParent        bool              // true iff ParentCell was found in c.Cells at snapshot time
	FallbackChildKey MeshCellID        // children[0].MeshID(); used to route sessions whose username isn't in adoptedUsers
	SplitDirectives  []rewireDirective // rewireDirectives computed under c.mu; applied off-lock

	// Merge.
	SurvivorKey       MeshCellID
	DonorIDs          []MeshCellID
	DonorCells        []*Cell
	Survivor          *Cell
	SurvivorCellID    CellID            // resolved sibling CellID whose key equals SurvivorKey
	SurvivorIsSibling bool              // true iff SurvivorKey matched a sibling (vs. the parent itself)
	MergeDirectives   []rewireDirective // rewireDirectives computed under c.mu; applied off-lock

	// Migrate.
	SrcCellKey MeshCellID
	SrcHost    string
	DestHost   string
	SrcCell    *Cell
}

// ExecuteCommitPlan runs every step in plan.Steps in order, checking
// invariants at entry, between steps, and at exit. Each step emits a
// CommitEvent to c.commitLog; begin/end markers bracket the sequence.
func (c *Process) ExecuteCommitPlan(plan *CommitPlan) error {
	eventKind := commitKindToEvent(plan.Kind)

	c.commitLog.Append(CommitEvent{
		CommitID: plan.ID, Kind: eventKind, Scenario: plan.Kind,
		StepIndex: -1, Step: "begin", Success: true,
	})
	c.CheckInvariants(defaultInvariants,
		fmt.Sprintf("commit %d entry (%s)", plan.ID, plan.Kind))

	for i, step := range plan.Steps {
		start := time.Now()
		err := step.Run(c, plan.Ctx)
		dur := time.Since(start)

		c.commitLog.Append(CommitEvent{
			CommitID:   plan.ID,
			Kind:       eventKind,
			Scenario:   plan.Kind,
			StepIndex:  i,
			Step:       step.Name,
			Success:    err == nil,
			DurationMs: dur.Milliseconds(),
			Error:      errString(err),
		})

		if err != nil {
			return fmt.Errorf("commit %d step %q: %w", plan.ID, step.Name, err)
		}

		invs := step.Invariants
		if len(invs) == 0 {
			invs = defaultInvariants
		}
		c.CheckInvariants(invs,
			fmt.Sprintf("commit %d after %s", plan.ID, step.Name))
	}

	c.CheckInvariants(defaultInvariants,
		fmt.Sprintf("commit %d exit (%s)", plan.ID, plan.Kind))
	c.commitLog.Append(CommitEvent{
		CommitID: plan.ID, Kind: eventKind, Scenario: plan.Kind,
		StepIndex: -1, Step: "end", Success: true,
	})
	return nil
}

func commitKindToEvent(k CommitKind) EventKind {
	switch k {
	case CommitKindSplit:
		return EventCommitSplit
	case CommitKindMerge:
		return EventCommitMerge
	case CommitKindMigrate:
		return EventCommitMigrate
	default:
		return EventCommitSplit
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
