package mmokit

import "testing"

// TestMmokitFacade_ExportsMoveEntityAPI is a compile-time pin: every
// symbol that game-side handlers reach for through the mmokit facade
// must resolve here. Failures look like "undefined: X" — telling the
// reader exactly which export went missing during a refactor.
func TestMmokitFacade_ExportsMoveEntityAPI(t *testing.T) {
	var _ MoveOpt = MoveBypassCooldown()
	var _ MoveOpt = MoveAsPlayer(0, "")
	var _ PlayerTarget = PlayerTarget{}
	_ = ResolvePlayerTarget
	_ = RoutePlayerHomeOrOwner
	// PlayerDataAccessor + PlayerDataLocator are interfaces — referenced
	// by name to ensure the alias compiles.
	var _ PlayerDataAccessor
	var _ PlayerDataLocator
}
