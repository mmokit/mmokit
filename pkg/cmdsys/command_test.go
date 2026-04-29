package cmdsys

import "testing"

func TestRouteKindString_PlayerHomeOrOwner(t *testing.T) {
	if got := RoutePlayerHomeOrOwner.String(); got != "player_home_or_owner" {
		t.Fatalf("RoutePlayerHomeOrOwner.String() = %q, want %q", got, "player_home_or_owner")
	}
}
