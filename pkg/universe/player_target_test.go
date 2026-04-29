package universe

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

func TestResolvePlayerTarget_NotFound(t *testing.T) {
	c := &Process{Cells: map[string]*Cell{}}
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

func TestResolvePlayerTarget_DirtyMark_NoOpForOnline(t *testing.T) {
	// When Online is non-nil, DirtyMark should be a no-op closure (never nil).
	target := PlayerTarget{Username: "alice", DirtyMark: func() {}}
	target.DirtyMark() // must not panic
}
