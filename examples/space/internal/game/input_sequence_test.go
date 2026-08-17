package game

import (
	"math"
	"reflect"
	"testing"

	"github.com/mmokit/mmokit"
)

func TestInputSequenceAfterWrapSafe(t *testing.T) {
	tests := []struct {
		name      string
		candidate uint32
		current   uint32
		wantAfter bool
	}{
		{"newer", 11, 10, true},
		{"duplicate", 10, 10, false},
		{"older", 9, 10, false},
		{"wrap", 1, math.MaxUint32 - 1, true},
		{"stale across wrap", math.MaxUint32 - 1, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inputSequenceAfter(tt.candidate, tt.current); got != tt.wantAfter {
				t.Fatalf("inputSequenceAfter(%d, %d) = %v, want %v", tt.candidate, tt.current, got, tt.wantAfter)
			}
		})
	}
}

func TestConsumeMoveTargetInputSequencesAndValidates(t *testing.T) {
	mt := &mmokit.MoveTarget{}
	if got := consumeMoveTargetInput(mt, &SetMoveTarget{Sequence: 5, Active: true, X: 100, Y: 200}, true, 1000, 1000, mmokit.DefaultCellSize); got != moveTargetApplied {
		t.Fatalf("first input outcome = %v, want moveTargetApplied", got)
	}
	if mt.Sequence != 5 || !mt.Active {
		t.Fatalf("first input: %+v", mt)
	}

	// Stale input neither rewinds the ack nor mutates the applied target.
	if got := consumeMoveTargetInput(mt, &SetMoveTarget{Sequence: 4, Active: true, X: 300, Y: 400}, true, 1000, 1000, mmokit.DefaultCellSize); got != moveTargetStale {
		t.Fatalf("stale input outcome = %v, want moveTargetStale", got)
	}
	if mt.Sequence != 5 || mt.LocalX != 100 || mt.LocalY != 200 {
		t.Fatalf("stale input mutated target: %+v", mt)
	}

	// Invalid coordinates are consumed/acked, but cannot poison movement.
	if got := consumeMoveTargetInput(mt, &SetMoveTarget{Sequence: 6, Active: true, X: float32(math.NaN()), Y: 1}, true, 1000, 1000, mmokit.DefaultCellSize); got != moveTargetRejected {
		t.Fatalf("invalid input outcome = %v, want moveTargetRejected (consumed, not applied)", got)
	}
	if mt.Sequence != 6 || mt.LocalX != 100 || mt.LocalY != 200 {
		t.Fatalf("invalid input changed target: %+v", mt)
	}

	// Docking consumes the newest sequence while leaving movement unchanged.
	if got := consumeMoveTargetInput(mt, &SetMoveTarget{Sequence: 7, Active: false}, false, 1000, 1000, mmokit.DefaultCellSize); got != moveTargetBlocked {
		t.Fatalf("docking input outcome = %v, want moveTargetBlocked", got)
	}
	if mt.Sequence != 7 || !mt.Active {
		t.Fatalf("docking input changed target: %+v", mt)
	}
}

func TestConsumeMoveTargetInputAcknowledgesLifecycleRejectedCommand(t *testing.T) {
	mt := &mmokit.MoveTarget{
		Active:   true,
		LocalX:   25,
		LocalY:   50,
		Sequence: 7,
	}

	got := consumeMoveTargetInput(mt, &SetMoveTarget{
		Sequence: 8,
		Active:   true,
		X:        800,
		Y:        900,
	}, false, 1000, 1000, mmokit.DefaultCellSize)
	if got != moveTargetBlocked {
		t.Fatalf("lifecycle-rejected command outcome = %v, want moveTargetBlocked", got)
	}
	if !got.consumed() {
		t.Fatal("lifecycle-rejected movement command was not consumed")
	}
	if mt.Sequence != 8 {
		t.Fatalf("sequence = %d, want rejected command acknowledged at 8", mt.Sequence)
	}
	if !mt.Active || mt.LocalX != 25 || mt.LocalY != 50 {
		t.Fatalf("rejected command mutated target: %+v", mt)
	}
}

func TestMovementInputPolicyMatchesReplicationViewerStates(t *testing.T) {
	tests := []struct {
		name    string
		state   mmokit.PlayerState
		consume bool
		canMove bool
	}{
		{name: "pending", state: mmokit.StatePending},
		{name: "active", state: mmokit.StateActive, consume: true, canMove: true},
		{name: "transferring", state: mmokit.StateTransferring},
		{name: "dead", state: StateDead, consume: true},
		{name: "docking", state: StateDocking, consume: true},
		{name: "docked", state: StateDocked, consume: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			consume, canMove := movementInputPolicy(tt.state)
			if consume != tt.consume || canMove != tt.canMove {
				t.Fatalf("movementInputPolicy(%d) = (%v,%v), want (%v,%v)",
					tt.state, consume, canMove, tt.consume, tt.canMove)
			}
		})
	}
}

// TestMoveTargetSequenceIsTransferred pins that ShipBundle serializes
// MoveTarget across a cell handoff.
//
// MoveTarget.Sequence IS the input-acknowledgement frontier: the server
// reflects it back on every replication frame, and the client retires
// buffered inputs against it. Tagging the field `mmokit:"local"` — an easy,
// plausible-looking change, since three of its siblings carry that tag —
// would reset the frontier to zero at every handoff. The client would then
// replay its entire pending input buffer against a stale seed, i.e. visible
// rubber-banding at every cell line, with nothing failing anywhere.
func TestMoveTargetSequenceIsTransferred(t *testing.T) {
	field, ok := reflect.TypeFor[ShipBundle]().FieldByName("MoveTarget")
	if !ok {
		t.Fatal("ShipBundle has no MoveTarget field")
	}
	if tag, tagged := field.Tag.Lookup("mmokit"); tagged {
		t.Fatalf("ShipBundle.MoveTarget is tagged mmokit:%q — it MUST be serialized across "+
			"cell transfers, because MoveTarget.Sequence is the input-ack frontier the "+
			"client retires predicted inputs against", tag)
	}

	// Guard the premise: the tag really is what excludes a field, and other
	// fields in this same bundle really do use it.
	local, ok := reflect.TypeFor[ShipBundle]().FieldByName("PlayerInput")
	if !ok {
		t.Fatal("ShipBundle has no PlayerInput field")
	}
	if got := local.Tag.Get("mmokit"); got != "local" {
		t.Fatalf(`ShipBundle.PlayerInput mmokit tag = %q, want "local" — `+
			"the exclusion mechanism this test depends on has changed", got)
	}
}
