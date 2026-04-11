package universe

import "testing"

// TestMsgTypes_NewConstantsDistinct asserts the Phase 4+ message type
// constants are pairwise distinct from each other and from the legacy
// constants. Guards against an accidental renumber or duplicate.
func TestMsgTypes_NewConstantsDistinct(t *testing.T) {
	seen := map[MsgType]string{
		MsgTransfer:         "MsgTransfer",
		MsgArrivalConfirm:   "MsgArrivalConfirm",
		MsgReplica:          "MsgReplica",
		MsgChat:             "MsgChat",
		MsgSpawnTransfer:    "MsgSpawnTransfer",
		MsgCrossNodeAction:  "MsgCrossNodeAction",
		MsgActionResult:     "MsgActionResult",
		MsgPlayerAssignment: "MsgPlayerAssignment",
		MsgProxySummary:     "MsgProxySummary",
		MsgDetailRequest:    "MsgDetailRequest",
		MsgDetailResponse:   "MsgDetailResponse",
		MsgSessionTransfer:  "MsgSessionTransfer",
	}

	newConstants := []struct {
		mt   MsgType
		name string
	}{
		{MsgBorderFrame, "MsgBorderFrame"},
		{MsgHandoffPrepare, "MsgHandoffPrepare"},
		{MsgHandoffCommit, "MsgHandoffCommit"},
		{MsgForwardInput, "MsgForwardInput"},
	}

	for _, c := range newConstants {
		if existing, dup := seen[c.mt]; dup {
			t.Fatalf("%s (%d) collides with %s", c.name, c.mt, existing)
		}
		seen[c.mt] = c.name
	}
}
