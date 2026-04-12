package component

// Shadow marks a pre-authority entity created from a HandoffPrepare
// payload. The destination cell holds the shadow while the source
// completes the warmup window. On HandoffCommit, the Shadow component
// is removed and the entity becomes a normal local entity.
//
// Game systems exclude shadows via mmokit.Query's default Without
// filter (same pattern as Ghost and Replica). The ReplicationSystem
// DOES iterate shadows so nearby players on the destination see the
// approaching entity before authority commits.
type Shadow struct {
	// SourceCellID is the cell that currently owns the entity.
	SourceCellID string
	// NetID is the entity's network ID (matches NetworkID.ID).
	NetID uint32
	// Epoch is the NEW authority epoch that will apply on commit.
	Epoch uint32
}
