package game

// DungeonRosterDef is the roster template for one chamber.
type DungeonRosterDef struct {
	Name    string
	Role    ChamberRole
	Members []DungeonRosterMember
}

// DungeonRosterMember is one entry in a roster template.
type DungeonRosterMember struct {
	Archetype    uint8
	Count        int
	SpreadRadius float32
	Elite        bool // for ChamberSideBoss: marks the boss vs escorts
	Main         bool // for ChamberTerminal: marks the BossGuardian
}

// dungeonRosters is the v1 roster table. Indices are stable.
var dungeonRosters = []DungeonRosterDef{
	// 0: mob pack — brawlers
	{
		Name: "Brawler Pack", Role: ChamberMobPack,
		Members: []DungeonRosterMember{
			{Archetype: ArchetypeBrawler, Count: 3, SpreadRadius: 80},
		},
	},
	// 1: mob pack — mixed
	{
		Name: "Mixed Pack", Role: ChamberMobPack,
		Members: []DungeonRosterMember{
			{Archetype: ArchetypeBrawler, Count: 2, SpreadRadius: 80},
			{Archetype: ArchetypeLancer, Count: 1, SpreadRadius: 60},
		},
	},
	// 2: side boss — elite brawler + escorts
	{
		Name: "Brawler Champion", Role: ChamberSideBoss,
		Members: []DungeonRosterMember{
			{Archetype: ArchetypeBrawler, Count: 1, SpreadRadius: 0, Elite: true},
			{Archetype: ArchetypeLancer, Count: 2, SpreadRadius: 60},
		},
	},
	// 3: side boss — elite artillery + escorts
	{
		Name: "Artillery Champion", Role: ChamberSideBoss,
		Members: []DungeonRosterMember{
			{Archetype: ArchetypeArtillery, Count: 1, SpreadRadius: 0, Elite: true},
			{Archetype: ArchetypeBrawler, Count: 2, SpreadRadius: 60},
		},
	},
	// 4: terminal — BossGuardian + escorts
	{
		Name: "The Guardian", Role: ChamberTerminal,
		Members: []DungeonRosterMember{
			{Archetype: ArchetypeBossGuardian, Count: 1, SpreadRadius: 0, Main: true},
			{Archetype: ArchetypeLancer, Count: 3, SpreadRadius: 100},
		},
	},
}

// dungeonNames is the procgen name pool, hash-selected on dungeon seed.
var dungeonNames = []string{
	"Stillveil Hollow",
	"Ashbore Cradle",
	"Verdigris Maw",
	"Roteye Reach",
	"Brackwhisper Pit",
	"Nethermerge Hollow",
	"Bonelight Drift",
	"Quartzgrave Bound",
}

// dungeonNameForSeed returns a stable name choice from the pool.
func dungeonNameForSeed(seed uint64) string {
	return dungeonNames[int(seed%uint64(len(dungeonNames)))]
}

// rosterIdxForRole picks a roster index for the given chamber role from
// a stable RNG-on-seed source. Deterministic per chamber.
func rosterIdxForRole(role ChamberRole, rngU64 uint64) uint16 {
	var candidates []int
	for i, r := range dungeonRosters {
		if r.Role == role {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		return 0
	}
	return uint16(candidates[int(rngU64%uint64(len(candidates)))])
}

