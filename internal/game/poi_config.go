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
			{Archetype: ArchetypeArtillery, Count: 1, SpreadRadius: 40},
			{Archetype: ArchetypeBrawler, Count: 2, SpreadRadius: 25},
			{Archetype: ArchetypeLancer, Count: 3, SpreadRadius: 30},
		},
	},
	{
		Name: "Small Skirmish",
		Members: []RosterMember{
			{Archetype: ArchetypeLancer, Count: 1, SpreadRadius: 25},
			{Archetype: ArchetypeBrawler, Count: 1, SpreadRadius: 20},
		},
	},
	{
		Name: "Medium Warband",
		Members: []RosterMember{
			{Archetype: ArchetypeLancer, Count: 2, SpreadRadius: 30},
			{Archetype: ArchetypeBrawler, Count: 2, SpreadRadius: 25},
			{Archetype: ArchetypeArtillery, Count: 1, SpreadRadius: 40},
		},
	},
	{
		Name: "Disruptor Cell",
		Members: []RosterMember{
			{Archetype: ArchetypeDisruptor, Count: 1, SpreadRadius: 30},
			{Archetype: ArchetypeLancer, Count: 2, SpreadRadius: 30},
			{Archetype: ArchetypeBrawler, Count: 1, SpreadRadius: 25},
		},
	},
	{
		Name: "Heavy Battalion",
		Members: []RosterMember{
			{Archetype: ArchetypeBrawler, Count: 3, SpreadRadius: 30},
			{Archetype: ArchetypeArtillery, Count: 2, SpreadRadius: 40},
			{Archetype: ArchetypeEliteLancer, Count: 2, SpreadRadius: 35},
		},
	},
	{
		Name: "Elite Anchor",
		Members: []RosterMember{
			{Archetype: ArchetypeBossGuardian, Count: 1, SpreadRadius: 0},
			{Archetype: ArchetypeSupport, Count: 1, SpreadRadius: 25},
			{Archetype: ArchetypeEliteBrawler, Count: 2, SpreadRadius: 30},
			{Archetype: ArchetypeEliteArtillery, Count: 2, SpreadRadius: 40},
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

// Roster index constants used by the tier table. Existing rosters slice
// is indexed in declaration order — these constants pin the indices for
// readable tierTable entries.
const (
	StarterArenaIdx   uint16 = 0
	SmallSkirmishIdx  uint16 = 1
	MediumWarbandIdx  uint16 = 2
	DisruptorCellIdx  uint16 = 3
	HeavyBattalionIdx uint16 = 4
	EliteAnchorIdx    uint16 = 5
)

// TierDef describes the difficulty / density / reward shape of a single
// tier. Tiers form concentric radial bands centered on the station.
// Boundary tier is computed per-POI from its world position (not the
// cell), so cells that straddle a boundary naturally produce mixed-tier
// content.
type TierDef struct {
	Tier           uint8
	InnerRadius    float32
	POIsPerCell    [2]int
	StatMultiplier float32
	FluxRewardMul  float32
	Rosters        []uint16
	CooldownSec    int32
}

// tierTable is the single source of truth for tier behavior. Modify
// this table to retune; nothing else should hard-code tier values.
//
// Densities are tuned so a player traveling at MaxSpeed=68u/s encounters
// content roughly every ~20s in T1 (~1500u between POIs in an 8192u
// cell), every ~60s in T2, and roughly per-cell at T3 destination raids.
var tierTable = []TierDef{
	{Tier: 1, InnerRadius: 0, POIsPerCell: [2]int{30, 50}, StatMultiplier: 1.0, FluxRewardMul: 1.0, Rosters: []uint16{SmallSkirmishIdx, StarterArenaIdx}, CooldownSec: 180},
	{Tier: 2, InnerRadius: 16384, POIsPerCell: [2]int{10, 20}, StatMultiplier: 1.5, FluxRewardMul: 2.5, Rosters: []uint16{MediumWarbandIdx, DisruptorCellIdx}, CooldownSec: 300},
	{Tier: 3, InnerRadius: 32768, POIsPerCell: [2]int{1, 5}, StatMultiplier: 2.5, FluxRewardMul: 6.0, Rosters: []uint16{HeavyBattalionIdx, EliteAnchorIdx}, CooldownSec: 900},
}

// tierDef returns the TierDef for a tier (1-indexed). Out-of-range
// returns tier 1 (defensive — should never happen in practice).
func tierDef(tier uint8) TierDef {
	if tier >= 1 && int(tier) <= len(tierTable) {
		return tierTable[tier-1]
	}
	return tierTable[0]
}
