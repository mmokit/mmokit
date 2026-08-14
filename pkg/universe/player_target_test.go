package universe

import (
	"testing"

	"github.com/mmokit/mmokit/pkg/cmdsys"
)

func TestResolvePlayerTarget_NotFound(t *testing.T) {
	c := &Process{Cells: map[MeshCellID]*Cell{}}
	env := &cmdsys.Env{Local: &cmdsys.LocalContext{Process: c}}
	target := ResolvePlayerTarget(env, "ghost")
	if target.Online != nil || target.Offline != nil {
		t.Fatalf("expected NotFound, got Online=%v Offline=%v", target.Online, target.Offline)
	}
	if target.Username != "ghost" {
		t.Fatalf("Username = %q, want ghost", target.Username)
	}
}

func TestResolvePlayerTarget_NilProcess(t *testing.T) {
	env := &cmdsys.Env{Local: &cmdsys.LocalContext{}}
	target := ResolvePlayerTarget(env, "alice")
	if target.Online != nil || target.Offline != nil {
		t.Fatalf("nil process should return NotFound")
	}
}

func TestResolvePlayerTarget_DirtyMark_AlwaysNonNil(t *testing.T) {
	// ResolvePlayerTarget must never return a PlayerTarget with a nil
	// DirtyMark — handlers call it unconditionally after Offline writes.
	c := &Process{Cells: map[MeshCellID]*Cell{}}
	env := &cmdsys.Env{Local: &cmdsys.LocalContext{Process: c}}
	target := ResolvePlayerTarget(env, "ghost")
	if target.DirtyMark == nil {
		t.Fatal("DirtyMark must never be nil")
	}
	target.DirtyMark() // must not panic
}
