package universe

import (
	"testing"
)

// These tests pin the step ordering of the three data-driven commit plans
// (split / merge / migrate). Reordering or renaming steps without updating
// these tests will fail — the order is load-bearing for commit semantics
// (e.g. coord mutation before session remap, rewire before release, etc.).

func TestBuildSplitPlan_StepOrdering(t *testing.T) {
	req := &CellTransferRequest{
		ID:       17,
		Kind:     CellTransferSplit,
		SrcCell:  CellID{X: 0, Y: 0},
		mutation: topologyMutation{add: map[MeshCellID]string{}, remove: []MeshCellID{}},
	}
	plan := buildSplitPlan(nil, req)

	want := []string{
		"apply-coord-mutation",
		"apply-rewire-directives",
		"remap-sessions",
		"apply-registry-delta",
		"release-parent-host",
		"prime-cooldowns",
		"broadcast-peer-list",
	}
	if len(plan.Steps) != len(want) {
		t.Fatalf("step count = %d, want %d", len(plan.Steps), len(want))
	}
	for i, name := range want {
		if plan.Steps[i].Name != name {
			t.Errorf("Steps[%d] = %q, want %q", i, plan.Steps[i].Name, name)
		}
	}
	if plan.ID != 17 {
		t.Errorf("plan.ID = %d, want 17", plan.ID)
	}
	if plan.Kind != CommitKindSplit {
		t.Errorf("plan.Kind = %v, want CommitKindSplit", plan.Kind)
	}
	if plan.Req != req {
		t.Errorf("plan.Req = %p, want %p", plan.Req, req)
	}
	if plan.Ctx == nil {
		t.Error("plan.Ctx is nil, want non-nil")
	}
}

func TestBuildMergePlan_StepOrdering(t *testing.T) {
	req := &CellTransferRequest{
		ID:       17,
		Kind:     CellTransferMerge,
		SrcCell:  CellID{X: 0, Y: 0},
		mutation: topologyMutation{add: map[MeshCellID]string{}, remove: []MeshCellID{}},
	}
	plan := buildMergePlan(nil, req)

	want := []string{
		"apply-coord-mutation",
		"rename-survivor-host",
		"apply-rewire-directives",
		"remap-sessions",
		"apply-registry-delta",
		"drain-donor-residuals",
		"release-donors",
		"release-donor-netids",
		"prime-cooldowns",
		"broadcast-peer-list",
	}
	if len(plan.Steps) != len(want) {
		t.Fatalf("step count = %d, want %d", len(plan.Steps), len(want))
	}
	for i, name := range want {
		if plan.Steps[i].Name != name {
			t.Errorf("Steps[%d] = %q, want %q", i, plan.Steps[i].Name, name)
		}
	}
	if plan.ID != 17 {
		t.Errorf("plan.ID = %d, want 17", plan.ID)
	}
	if plan.Kind != CommitKindMerge {
		t.Errorf("plan.Kind = %v, want CommitKindMerge", plan.Kind)
	}
	if plan.Req != req {
		t.Errorf("plan.Req = %p, want %p", plan.Req, req)
	}
	if plan.Ctx == nil {
		t.Error("plan.Ctx is nil, want non-nil")
	}
}

func TestBuildMigratePlan_StepOrdering(t *testing.T) {
	req := &CellTransferRequest{
		ID:       17,
		Kind:     CellTransferMigrate,
		SrcCell:  CellID{X: 0, Y: 0},
		mutation: topologyMutation{add: map[MeshCellID]string{}, remove: []MeshCellID{}},
	}
	plan := buildMigratePlan(nil, req)

	want := []string{
		"apply-coord-mutation",
		"remap-sessions",
		"apply-registry-delta",
		"release-src-host",
		"release-src-netid",
		"broadcast-peer-list",
	}
	if len(plan.Steps) != len(want) {
		t.Fatalf("step count = %d, want %d", len(plan.Steps), len(want))
	}
	for i, name := range want {
		if plan.Steps[i].Name != name {
			t.Errorf("Steps[%d] = %q, want %q", i, plan.Steps[i].Name, name)
		}
	}
	if plan.ID != 17 {
		t.Errorf("plan.ID = %d, want 17", plan.ID)
	}
	if plan.Kind != CommitKindMigrate {
		t.Errorf("plan.Kind = %v, want CommitKindMigrate", plan.Kind)
	}
	if plan.Req != req {
		t.Errorf("plan.Req = %p, want %p", plan.Req, req)
	}
	if plan.Ctx == nil {
		t.Error("plan.Ctx is nil, want non-nil")
	}
}
