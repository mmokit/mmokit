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
	PreOwnership map[string]string
	Mutation     topologyMutation

	// Split.
	ParentKey        string
	Children         [4]CellID
	ParentCell       *Cell            // resolved local *Cell for the parent (nil when parent lives on a remote host)
	HadParent        bool             // true iff ParentCell was found in c.Cells at snapshot time
	FallbackChildKey string           // MeshCellID(children[0]); used to route sessions whose username isn't in adoptedUsers
	SplitDirectives  []rewireDirective // rewireDirectives computed under c.mu; applied off-lock

	// Merge.
	SurvivorKey string
	DonorIDs    []string
	DonorCells  []*Cell
	Survivor    *Cell

	// Migrate.
	SrcCellKey string
	SrcHost    string
	DestHost   string
	SrcCell    *Cell
}

// ExecuteCommitPlan runs every step in plan.Steps in order, checking
// invariants at entry, between steps, and at exit. Commit log hooks
// are stubbed here and wired in Phase C.
func (c *Process) ExecuteCommitPlan(plan *CommitPlan) error {
	c.CheckInvariants(defaultInvariants,
		fmt.Sprintf("commit %d entry (%s)", plan.ID, plan.Kind))

	for _, step := range plan.Steps {
		start := time.Now()
		err := step.Run(c, plan.Ctx)
		_ = time.Since(start) // commit log Phase C consumes this

		if err != nil {
			return fmt.Errorf("commit %d step %q: %w",
				plan.ID, step.Name, err)
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
	return nil
}
