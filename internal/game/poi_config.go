package game

// RosterMember describes one component of a POI's enemy roster.
type RosterMember struct {
	Archetype uint8
	Count     int
	// SpreadRadius is the local-coord radius around the POI center
	// inside which members of this group are randomly placed.
	SpreadRadius float32
}

// RosterDef is the full roster blueprint for a POI. Looked up by
// POI.RosterDefIdx.
type RosterDef struct {
	Name    string
	Members []RosterMember
}

// rosters is the v1 roster table. Index 0 = "Starter Arena" — used by
// every combat POI in v1. Future POI difficulty tiers add more entries.
var rosters = []RosterDef{
	{
		Name: "Starter Arena",
		Members: []RosterMember{
			{Archetype: ArchetypeBrawler, Count: 2, SpreadRadius: 25},
			{Archetype: ArchetypeSniper, Count: 1, SpreadRadius: 40},
			{Archetype: ArchetypeSwarmer, Count: 3, SpreadRadius: 30},
		},
	},
}

// rosterForIdx returns the roster def for an index, or the default (0) on out-of-range.
func rosterForIdx(idx uint16) RosterDef {
	if int(idx) < len(rosters) {
		return rosters[idx]
	}
	return rosters[0]
}

// RosterForIdx is the exported accessor used by cmdsys command handlers
// in internal/game/commands. v1 ships with a single roster ("Starter
// Arena") — future POI difficulty tiers extend the table.
func RosterForIdx(idx uint16) RosterDef { return rosterForIdx(idx) }
