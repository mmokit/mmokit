package game

import (
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// FinishTransferSpawn applies config-dependent overrides after transfer.
// Most components are handled by EnsureEntityKindComponents from the
// bundle structs registered via mmokit.RegisterKind[T] (including
// `mmokit:"local"`-tagged local-only fields). Only config-dependent
// values that can't be expressed as zero defaults remain here.
func (gw *GameWorld) FinishTransferSpawn(handle mmokit.EntityHandle, frame *mmokit.TransferFrame) {
	entity := mmokit.EntityFromECS(gw.stage, handle)
	switch frame.EntityType {
	case gamecomp.KindShip:
		// Override collider to match game config
		if col := mmokit.Get[mmokit.Collider](entity); col != nil {
			col.Radius = boundingRadius(gw.Config.ShipWidth, gw.Config.ShipHeight)
			col.Width = gw.Config.ShipWidth
			col.Height = gw.Config.ShipHeight
			col.Layer = gamecomp.LayerPlayer
			col.Shape = mmokit.ShapeRect
		}
		gw.ApplyEquipmentStats(entity)

	case gamecomp.KindNPC:
		if col := mmokit.Get[mmokit.Collider](entity); col != nil {
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
// Does NOT call reconnectPlayer — that sends PlayerEntityAssigned which clears client entities
// and causes a visual blink. The adapter sends a CellChange event instead.
func (gw *GameWorld) WireTransferPlayer(entity mmokit.EntityHandle, s *mmokit.PlayerSession) {
	s.Entity = entity
	gw.updatePlayerCompletions()
}

