package universe

import "testing"

func TestHandoffState_FreshKeyIsUnseen(t *testing.T) {
	sm := NewHandoffStateMachine()
	key := HandoffKey{EntityNetID: 1, NeighborID: "cell_1_0"}
	if sm.State(key) != HandoffUnseen {
		t.Fatalf("fresh key should be Unseen, got %v", sm.State(key))
	}
}

func TestHandoffState_SetStatePersists(t *testing.T) {
	sm := NewHandoffStateMachine()
	key := HandoffKey{EntityNetID: 1, NeighborID: "cell_1_0"}

	sm.SetState(key, HandoffUnseen)
	if sm.State(key) != HandoffUnseen {
		t.Fatalf("state did not persist, got %v", sm.State(key))
	}
}

func TestHandoffState_Forget(t *testing.T) {
	sm := NewHandoffStateMachine()
	key := HandoffKey{EntityNetID: 1, NeighborID: "cell_1_0"}

	sm.SetState(key, HandoffUnseen)
	sm.Forget(key)

	// After Forget, state reverts to the zero default (HandoffUnseen)
	// and the internal entry is released.
	if sm.State(key) != HandoffUnseen {
		t.Fatalf("after Forget, state should be Unseen, got %v", sm.State(key))
	}
}
