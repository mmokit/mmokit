package game

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

// Game-specific cross-cell action types.
const (
	ActionDamage       mmokit.ActionType = 1
	ActionStatusEffect mmokit.ActionType = 2
	ActionMining       mmokit.ActionType = 3
)

// DamageAction is the payload for ActionDamage.
type DamageAction struct {
	Damage      float32
	BonusDamage float32 // extra damage if target shield is depleted (server decides)
	Slot        uint8   // ability slot (for VFX on result)
	AbilityType uint8   // ability type enum (for VFX on result)
}

// MarshalDamageAction serializes a DamageAction to bytes.
func MarshalDamageAction(a *DamageAction) []byte {
	buf := make([]byte, 10)
	binary.LittleEndian.PutUint32(buf[0:], math.Float32bits(a.Damage))
	binary.LittleEndian.PutUint32(buf[4:], math.Float32bits(a.BonusDamage))
	buf[8] = a.Slot
	buf[9] = a.AbilityType
	return buf
}

// UnmarshalDamageAction deserializes a DamageAction from bytes.
func UnmarshalDamageAction(data []byte) (*DamageAction, error) {
	if len(data) < 10 {
		return nil, fmt.Errorf("damage action: need 10 bytes, got %d", len(data))
	}
	return &DamageAction{
		Damage:      math.Float32frombits(binary.LittleEndian.Uint32(data[0:])),
		BonusDamage: math.Float32frombits(binary.LittleEndian.Uint32(data[4:])),
		Slot:        data[8],
		AbilityType: data[9],
	}, nil
}

// DamageResult is the payload for an ActionDamage result.
type DamageResult struct {
	DamageDealt float32
	TargetDead  bool
	Slot        uint8 // echoed back from DamageAction
	AbilityType uint8 // echoed back from DamageAction
}

// MarshalDamageResult serializes a DamageResult to bytes.
func MarshalDamageResult(r *DamageResult) []byte {
	buf := make([]byte, 7)
	binary.LittleEndian.PutUint32(buf[0:], math.Float32bits(r.DamageDealt))
	if r.TargetDead {
		buf[4] = 1
	}
	buf[5] = r.Slot
	buf[6] = r.AbilityType
	return buf
}

// UnmarshalDamageResult deserializes a DamageResult from bytes.
func UnmarshalDamageResult(data []byte) (*DamageResult, error) {
	if len(data) < 7 {
		return nil, fmt.Errorf("damage result: need 7 bytes, got %d", len(data))
	}
	return &DamageResult{
		DamageDealt: math.Float32frombits(binary.LittleEndian.Uint32(data[0:])),
		TargetDead:  data[4] != 0,
		Slot:        data[5],
		AbilityType: data[6],
	}, nil
}

// StatusEffectAction is the payload for ActionStatusEffect.
type StatusEffectAction struct {
	EffectType uint8
	Duration   float32
	Value      float32
}

// MarshalStatusEffectAction serializes a StatusEffectAction to bytes.
func MarshalStatusEffectAction(a *StatusEffectAction) []byte {
	buf := make([]byte, 9)
	buf[0] = a.EffectType
	binary.LittleEndian.PutUint32(buf[1:], math.Float32bits(a.Duration))
	binary.LittleEndian.PutUint32(buf[5:], math.Float32bits(a.Value))
	return buf
}

// UnmarshalStatusEffectAction deserializes a StatusEffectAction from bytes.
func UnmarshalStatusEffectAction(data []byte) (*StatusEffectAction, error) {
	if len(data) < 9 {
		return nil, fmt.Errorf("status effect action: need 9 bytes, got %d", len(data))
	}
	return &StatusEffectAction{
		EffectType: data[0],
		Duration:   math.Float32frombits(binary.LittleEndian.Uint32(data[1:])),
		Value:      math.Float32frombits(binary.LittleEndian.Uint32(data[5:])),
	}, nil
}

// MiningAction is the payload for ActionMining.
type MiningAction struct {
	Amount float32 // resources to extract
}

// MarshalMiningAction serializes a MiningAction to bytes.
func MarshalMiningAction(a *MiningAction) []byte {
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf[0:], math.Float32bits(a.Amount))
	return buf
}

// UnmarshalMiningAction deserializes a MiningAction from bytes.
func UnmarshalMiningAction(data []byte) (*MiningAction, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("mining action: need 4 bytes, got %d", len(data))
	}
	return &MiningAction{
		Amount: math.Float32frombits(binary.LittleEndian.Uint32(data[0:])),
	}, nil
}

// MiningResult is the payload for an ActionMining result.
type MiningResult struct {
	Extracted float32
	Depleted  bool
}

// MarshalMiningResult serializes a MiningResult to bytes.
func MarshalMiningResult(r *MiningResult) []byte {
	buf := make([]byte, 5)
	binary.LittleEndian.PutUint32(buf[0:], math.Float32bits(r.Extracted))
	if r.Depleted {
		buf[4] = 1
	}
	return buf
}

// UnmarshalMiningResult deserializes a MiningResult from bytes.
func UnmarshalMiningResult(data []byte) (*MiningResult, error) {
	if len(data) < 5 {
		return nil, fmt.Errorf("mining result: need 5 bytes, got %d", len(data))
	}
	return &MiningResult{
		Extracted: math.Float32frombits(binary.LittleEndian.Uint32(data[0:])),
		Depleted:  data[4] != 0,
	}, nil
}
