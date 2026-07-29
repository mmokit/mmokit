package universe

// Event log categories. CommitLog.Append fans out to c.Log via these
// category names so operators can tail topology events live via
// `log events:<kind>`.
const (
	CatEventsSplit       = "events:split"
	CatEventsMerge       = "events:merge"
	CatEventsMigrate     = "events:migrate"
	CatEventsInvariant   = "events:invariant"
	CatEventsHost        = "events:host"
	CatEventsSession     = "events:session"
	CatEventsReplication = "events:replication"
	CatServicesBus       = "services:bus"
	// CatCmdsysAudit carries the cmdsys audit trail: who invoked which verb,
	// over which transport, and whether it succeeded. On a mesh receive path
	// it also carries the authenticated peer that delivered the command,
	// which is the identity that actually authorized it.
	CatCmdsysAudit = "cmdsys:audit"
)

// EventCategories is the full set, for RegisterCategories.
var EventCategories = []string{
	CatEventsSplit, CatEventsMerge, CatEventsMigrate,
	CatEventsInvariant, CatEventsHost, CatEventsSession,
	CatEventsReplication, CatServicesBus, CatCmdsysAudit,
}
