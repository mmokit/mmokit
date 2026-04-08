package game

import (
	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// FinishTransferSpawn applies config-dependent overrides after transfer.
// Most components are handled by EnsureEntityKindComponents (KindComponent
// and KindComponentLocalOnly registrations). Only config-dependent values
// that can't be expressed as zero defaults remain here.
func (gw *GameWorld) FinishTransferSpawn(entity ecs.Entity, frame *mmokit.TransferFrame) {
	switch frame.EntityType {
	case gamecomp.TypeShip:
		// Override collider to match game config
		if gw.C.Collider.HasAll(entity) {
			col := gw.C.Collider.Get(entity)
			col.Radius = boundingRadius(gw.Config.ShipWidth, gw.Config.ShipHeight)
			col.Width = gw.Config.ShipWidth
			col.Height = gw.Config.ShipHeight
			col.Layer = gamecomp.LayerPlayer
			col.Shape = mmokit.ShapeRect
		}
		gw.ApplyEquipmentStats(entity)

	case gamecomp.TypeNPC:
		if gw.C.Collider.HasAll(entity) {
			col := gw.C.Collider.Get(entity)
			col.Radius = boundingRadius(gw.Config.NpcWidth, gw.Config.NpcHeight)
			col.Width = gw.Config.NpcWidth
			col.Height = gw.Config.NpcHeight
			col.Layer = gamecomp.LayerPlayer
			col.Shape = mmokit.ShapeRect
		}
	}
}

// WireTransferPlayer handles post-transfer player session wiring.
// Called from the universe adapter after SpawnFromTransferCore + FinishTransferSpawn.
// Does NOT call reconnectPlayer — that sends SE_PLAYER_SPAWNED which clears client entities
// and causes a visual blink. The adapter sends SE_CELL_CHANGE instead.
func (gw *GameWorld) WireTransferPlayer(entity ecs.Entity, s *mmokit.PlayerSession) {
	s.Entity = entity
	gw.updatePlayerCompletions()
}

