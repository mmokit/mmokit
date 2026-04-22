package component

// Shadow marks a pre-authority entity created from a HandoffPrepare
// payload. The destination cell holds the shadow between Prepare and
// Commit; on HandoffCommit the Shadow component is removed and the
// entity becomes locally authoritative.
//
// Phase I of the Replication Timeline Redesign deletes this component
// entirely — the new hard-cut handoff has no pre-authority phase; the
// existing border Replica serves as the destination-side cache until
// commit-tick, at which point it's promoted in place.
type Shadow struct {
	SourceCellID string
	NetID        uint32
	Epoch        uint32
}
