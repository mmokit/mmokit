package cmdsys

import "testing"

func TestRouteKindString_PlayerHomeOrOwner(t *testing.T) {
	if got := RoutePlayerHomeOrOwner.String(); got != "player_home_or_owner" {
		t.Fatalf("RoutePlayerHomeOrOwner.String() = %q, want %q", got, "player_home_or_owner")
	}
}

type fakeProcess struct{}

func (fakeProcess) isLocalProcess() {}

func TestLocalContext_AcceptsLocalProcess(t *testing.T) {
	var lp LocalProcess = fakeProcess{}
	lc := LocalContext{Process: lp}
	if lc.Process == nil {
		t.Fatal("LocalContext.Process should retain the assigned LocalProcess")
	}
}
