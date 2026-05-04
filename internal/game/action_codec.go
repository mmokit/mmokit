package game

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

// Game-specific cross-cell action types.
const (
	ActionStatusEffect mmokit.ActionType = 2
)

// StatusEffectAction is the payload for ActionStatusEffect. Slot and
// AbilityType ride along so the receiving cell can fire the cast animation
// for viewers near the target, matching same-cell behavior.
type StatusEffectAction struct {
	EffectType  uint8
	Slot        uint8
	AbilityType uint8
	Duration    float32
	Value       float32
}

// MarshalStatusEffectAction serializes a StatusEffectAction to bytes.
func MarshalStatusEffectAction(a *StatusEffectAction) []byte {
	buf := make([]byte, 11)
	buf[0] = a.EffectType
	buf[1] = a.Slot
	buf[2] = a.AbilityType
	binary.LittleEndian.PutUint32(buf[3:], math.Float32bits(a.Duration))
	binary.LittleEndian.PutUint32(buf[7:], math.Float32bits(a.Value))
	return buf
}

// UnmarshalStatusEffectAction deserializes a StatusEffectAction from bytes.
func UnmarshalStatusEffectAction(data []byte) (*StatusEffectAction, error) {
	if len(data) < 11 {
		return nil, fmt.Errorf("status effect action: need 11 bytes, got %d", len(data))
	}
	return &StatusEffectAction{
		EffectType:  data[0],
		Slot:        data[1],
		AbilityType: data[2],
		Duration:    math.Float32frombits(binary.LittleEndian.Uint32(data[3:])),
		Value:       math.Float32frombits(binary.LittleEndian.Uint32(data[7:])),
	}, nil
}

