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
)

// EventCategories is the full set, for RegisterCategories.
var EventCategories = []string{
	CatEventsSplit, CatEventsMerge, CatEventsMigrate,
	CatEventsInvariant, CatEventsHost, CatEventsSession,
	CatEventsReplication,
}
