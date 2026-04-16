package universe

import "testing"

// TestMsgTypes_Distinct asserts all message type constants are pairwise distinct.
// Guards against accidental renumber or duplicate.
func TestMsgTypes_Distinct(t *testing.T) {
	all := []struct {
		mt   MsgType
		name string
	}{
		{MsgChat, "MsgChat"},
		{MsgSpawnTransfer, "MsgSpawnTransfer"},
		{MsgCrossCellAction, "MsgCrossCellAction"},
		{MsgActionResult, "MsgActionResult"},
		{MsgPlayerAssignment, "MsgPlayerAssignment"},
		{MsgSessionTransfer, "MsgSessionTransfer"},
		{MsgBorderFrame, "MsgBorderFrame"},
		{MsgHandoffPrepare, "MsgHandoffPrepare"},
		{MsgHandoffCommit, "MsgHandoffCommit"},
		{MsgForwardInput, "MsgForwardInput"},
		{MsgHandoffCancel, "MsgHandoffCancel"},
		{MsgPlayerDisconnected, "MsgPlayerDisconnected"},
	}

	seen := make(map[MsgType]string, len(all))
	for _, c := range all {
		if existing, dup := seen[c.mt]; dup {
			t.Fatalf("%s (%d) collides with %s", c.name, c.mt, existing)
		}
		seen[c.mt] = c.name
	}
}
