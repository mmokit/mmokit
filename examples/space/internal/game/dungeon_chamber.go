package game

import "github.com/mmokit/mmokit/pkg/spatial"

// ChamberRole describes a chamber's content tier.
type ChamberRole uint8

const (
	ChamberMobPack  ChamberRole = 0
	ChamberSideBoss ChamberRole = 1
	ChamberTerminal ChamberRole = 2
)

// ChamberStatus is the lifecycle state of a single chamber.
type ChamberStatus uint8

const (
	ChamberActive   ChamberStatus = 0
	ChamberCleared  ChamberStatus = 1
	ChamberCooldown ChamberStatus = 2
)

// ChamberState is the server-side tracking record for one chamber.
type ChamberState struct {
	ID             uint16
	Center         spatial.Vec2
	Radius         float32
	Role           ChamberRole
	Status         ChamberStatus
	RosterDefIdx   uint16
	RespawnCount   uint32
	ClearedAt      int64 // unix nanos; 0 if not yet cleared this cycle
	AliveNetIDs    []uint32
	LastChestNetID uint32
	LastKillPos    spatial.Vec2 // chest spawn position (boss death loc or chamber center)
}
