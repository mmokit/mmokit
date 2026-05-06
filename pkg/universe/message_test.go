package universe

import "testing"

// TestMsgTypes_Distinct asserts all message type constants are pairwise distinct.
// Guards against accidental renumber or duplicate.
func TestMsgTypes_Distinct(t *testing.T) {
	all := []struct {
		mt   MsgType
		name string
	}{
		{MsgSpawnTransfer, "MsgSpawnTransfer"},
		{MsgCrossCellAction, "MsgCrossCellAction"},
		{MsgPlayerAssignment, "MsgPlayerAssignment"},
		{MsgSessionTransfer, "MsgSessionTransfer"},
		{MsgBorderFrame, "MsgBorderFrame"},
		{MsgHandoff, "MsgHandoff"},
		{MsgForwardInput, "MsgForwardInput"},
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
