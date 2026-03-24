package game

import (
	"google.golang.org/protobuf/proto"

	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// MarshalTransferPayload encodes a TransferPayload to protobuf bytes.
func MarshalTransferPayload(p *TransferPayload) ([]byte, error) {
	pb := &gamepb.TransferPayloadPB{
		NetworkId:      p.NetworkID,
		EntityType:     uint32(p.EntityType),
		ConnId:         p.ConnID,
		Username:       p.Username,
		SourceTick:     p.SourceTick,
		PosX:           p.Position.X,
		PosY:           p.Position.Y,
		SectorSx:       p.Sector.SX,
		SectorSy:       p.Sector.SY,
		VelX:           p.Velocity.X,
		VelY:           p.Velocity.Y,
		Rotation:       p.Rotation.Angle,
		ColliderRadius: p.Collider.Radius,
		ColliderWidth:  p.Collider.Width,
		ColliderHeight: p.Collider.Height,
		ColliderLayer:  uint32(p.Collider.Layer),
		ColliderShape:  uint32(p.Collider.Shape),
		MaxCargo:       p.MaxCargo,
	}

	// Optional components (nil proto message = not present)
	if p.Health != nil {
		pb.Health = &gamepb.HealthPB{Current: p.Health.Current, Max: p.Health.Max}
	}
	if p.Shield != nil {
		pb.Shield = &gamepb.ShieldPB{
			Current:       p.Shield.Current,
			Max:           p.Shield.Max,
			RegenRate:     p.Shield.RegenRate,
			RegenDelay:    p.Shield.RegenDelay,
			DamageCooldown: p.Shield.DamageCooldown,
		}
	}
	if p.ShipControl != nil {
		pb.ShipControl = &gamepb.ShipControlPB{
			Thrust:  p.ShipControl.Thrust,
			TurnRate: p.ShipControl.TurnRate,
			MaxSpeed: p.ShipControl.MaxSpeed,
		}
	}
	if p.Equipment != nil {
		pb.Equipment = &gamepb.EquipmentPB{
			Weapon1:  p.Equipment.Weapon1,
			Weapon2:  p.Equipment.Weapon2,
			Shield:   p.Equipment.Shield,
			Thruster: p.Equipment.Thruster,
		}
	}
	if p.MoveTarget != nil {
		pb.MoveTarget = &gamepb.MoveTargetPB{
			X:      p.MoveTarget.X,
			Y:      p.MoveTarget.Y,
			Sx:     p.MoveTarget.SX,
			Sy:     p.MoveTarget.SY,
			Active: p.MoveTarget.Active,
		}
	}
	if p.AbilitySet != nil {
		pb.AbilitySet = &gamepb.AbilitySetPB{
			Cooldowns: p.AbilitySet.Cooldowns[:],
		}
	}
	if p.Minable != nil {
		pb.Minable = &gamepb.MinablePB{
			ResourceType: uint32(p.Minable.ResourceType),
			Remaining:    p.Minable.Remaining,
		}
	}
	if p.Lifetime != nil {
		pb.Lifetime = &gamepb.LifetimePB{
			Remaining: p.Lifetime.Remaining,
		}
	}
	if p.StatusEffects != nil {
		pbSE := &gamepb.StatusEffectsPB{}
		for i := uint8(0); i < p.StatusEffects.Count; i++ {
			e := p.StatusEffects.Effects[i]
			pbSE.Effects = append(pbSE.Effects, &gamepb.StatusEffectTransferPB{
				Type:     uint32(e.Type),
				Duration: e.Duration,
				Value:    e.Value,
			})
		}
		pb.StatusEffects = pbSE
	}

	// Cargo map → repeated CargoEntry
	for id, qty := range p.CargoItems {
		pb.CargoItems = append(pb.CargoItems, &gamepb.CargoEntry{ItemId: id, Quantity: qty})
	}

	return proto.Marshal(pb)
}

// UnmarshalTransferPayload decodes a TransferPayload from protobuf bytes.
func UnmarshalTransferPayload(data []byte) (*TransferPayload, error) {
	pb := &gamepb.TransferPayloadPB{}
	if err := proto.Unmarshal(data, pb); err != nil {
		return nil, err
	}

	p := &TransferPayload{
		NetworkID:  pb.NetworkId,
		EntityType: uint8(pb.EntityType),
		ConnID:     pb.ConnId,
		Username:   pb.Username,
		SourceTick: pb.SourceTick,
		Position:   mmokit.Position{X: pb.PosX, Y: pb.PosY},
		Sector:     mmokit.SectorCoord{SX: pb.SectorSx, SY: pb.SectorSy},
		Velocity:   mmokit.Velocity{X: pb.VelX, Y: pb.VelY},
		Rotation:   mmokit.Rotation{Angle: pb.Rotation},
		Collider: mmokit.Collider{
			Radius: pb.ColliderRadius,
			Width:  pb.ColliderWidth,
			Height: pb.ColliderHeight,
			Layer:  uint8(pb.ColliderLayer),
			Shape:  uint8(pb.ColliderShape),
		},
		MaxCargo: pb.MaxCargo,
	}

	// Optional components
	if pb.Health != nil {
		p.Health = &mmokit.Health{Current: pb.Health.Current, Max: pb.Health.Max}
	}
	if pb.Shield != nil {
		p.Shield = &mmokit.Shield{
			Current:        pb.Shield.Current,
			Max:            pb.Shield.Max,
			RegenRate:      pb.Shield.RegenRate,
			RegenDelay:     pb.Shield.RegenDelay,
			DamageCooldown: pb.Shield.DamageCooldown,
		}
	}
	if pb.ShipControl != nil {
		p.ShipControl = &gamecomp.ShipControl{
			Thrust:   pb.ShipControl.Thrust,
			TurnRate: pb.ShipControl.TurnRate,
			MaxSpeed: pb.ShipControl.MaxSpeed,
		}
	}
	if pb.Equipment != nil {
		p.Equipment = &gamecomp.Equipment{
			Weapon1:  pb.Equipment.Weapon1,
			Weapon2:  pb.Equipment.Weapon2,
			Shield:   pb.Equipment.Shield,
			Thruster: pb.Equipment.Thruster,
		}
	}
	if pb.MoveTarget != nil {
		p.MoveTarget = &mmokit.MoveTarget{
			X:      pb.MoveTarget.X,
			Y:      pb.MoveTarget.Y,
			SX:     pb.MoveTarget.Sx,
			SY:     pb.MoveTarget.Sy,
			Active: pb.MoveTarget.Active,
		}
	}
	if pb.AbilitySet != nil {
		as := &gamecomp.AbilitySet{}
		for i := 0; i < len(pb.AbilitySet.Cooldowns) && i < len(as.Cooldowns); i++ {
			as.Cooldowns[i] = pb.AbilitySet.Cooldowns[i]
		}
		p.AbilitySet = as
	}
	if pb.Minable != nil {
		p.Minable = &gamecomp.Minable{
			ResourceType: uint8(pb.Minable.ResourceType),
			Remaining:    pb.Minable.Remaining,
		}
	}
	if pb.Lifetime != nil {
		p.Lifetime = &mmokit.Lifetime{Remaining: pb.Lifetime.Remaining}
	}
	if pb.StatusEffects != nil {
		se := &gamecomp.StatusEffects{}
		for i, e := range pb.StatusEffects.Effects {
			if i >= gamecomp.MaxStatusEffects {
				break
			}
			se.Effects[i] = gamecomp.StatusEffect{
				Type:     gamecomp.StatusType(e.Type),
				Duration: e.Duration,
				Value:    e.Value,
			}
			se.Count++
		}
		p.StatusEffects = se
	}

	// Cargo entries → map
	if len(pb.CargoItems) > 0 {
		p.CargoItems = make(map[uint32]int32, len(pb.CargoItems))
		for _, entry := range pb.CargoItems {
			p.CargoItems[entry.ItemId] = entry.Quantity
		}
	}

	return p, nil
}

