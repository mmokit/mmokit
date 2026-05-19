package world

// Repository is the read/write surface for the hand-authored world manifest.
// jsonrepo is the only impl in v1; tests can substitute a fake.
type Repository interface {
	LoadAll() (*Snapshot, error)

	SaveStations(*Stations) error
	SavePOIs(*POIs) error
	SaveDungeons(*Dungeons) error
	SaveBelts(*Belts) error
	SaveDecorations(*Decorations) error
	SaveRegions(*Regions) error

	AddStation(Station) error
	UpdateStation(id string, mut func(*Station)) error
	DeleteStation(id string) error

	AddPOI(POI) error
	UpdatePOI(id string, mut func(*POI)) error
	DeletePOI(id string) error

	AddDungeon(Dungeon) error
	UpdateDungeon(id string, mut func(*Dungeon)) error
	DeleteDungeon(id string) error

	AddBelt(Belt) error
	UpdateBelt(id string, mut func(*Belt)) error
	DeleteBelt(id string) error

	AddDecoration(Decoration) error
	UpdateDecoration(id string, mut func(*Decoration)) error
	DeleteDecoration(id string) error

	AddRegion(Region) error
	UpdateRegion(id string, mut func(*Region)) error
	DeleteRegion(id string) error
}
