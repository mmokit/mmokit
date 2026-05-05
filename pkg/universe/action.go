package universe

// ActionType identifies a cross-cell action (game-defined).
type ActionType uint16

// CrossCellAction is a request sent to the authoritative cell for an entity.
// The originating cell sends this when a local entity (e.g. a player) acts on
// a replica — the authoritative cell executes the action.
type CrossCellAction struct {
	Type         ActionType
	TargetNetID  uint32 // the target entity's network ID on the authoritative cell
	SourceNetID  uint32 // the acting entity's network ID
	SourceCellID string // which cell originated this action
	Payload      []byte // game-serialized action data
}
