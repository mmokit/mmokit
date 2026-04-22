package component

// Shadow marks a pre-authority entity created from a HandoffPrepare
// payload. The destination cell holds the shadow while the source
// completes the warmup window. On HandoffCommit, the Shadow component
// is removed and the entity becomes a normal local entity.
//
// Phase I of the Replication Timeline Redesign deletes this component
// entirely in favor of the existing Replica component as the sole
// destination-side pre-authority representation.
type Shadow struct {
	SourceCellID string
	NetID        uint32
	Epoch        uint32
}
