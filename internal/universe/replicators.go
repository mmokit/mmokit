package universe

import (
	"encoding/binary"
	"math"

	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	comp "github.com/zenion/mmoserver/pkg/component"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// Game-specific component IDs for replication.
// ComponentID 0 is reserved for collider (always included in frames).
const (
	ReplVelocity pkguniverse.ComponentID = 1
	ReplRotation pkguniverse.ComponentID = 2
	ReplHealth   pkguniverse.ComponentID = 3
	ReplShield   pkguniverse.ComponentID = 4
	ReplMinable  pkguniverse.ComponentID = 5
)

// buildReplicationRegistry creates a ReplicationRegistry with all game-specific
// component replicators. The closures capture the typed ECS mappers so the
// generic engine can serialize/deserialize components without knowing their types.
func buildReplicationRegistry(gw *game.GameWorld) *pkguniverse.ReplicationRegistry {
	reg := pkguniverse.NewReplicationRegistry()

	reg.Register(velocityReplicator(gw.C.Velocity))
	reg.Register(rotationReplicator(gw.C.Rotation))
	reg.Register(healthReplicator(gw.C.Health))
	reg.Register(shieldReplicator(gw.C.Shield))
	reg.Register(minableReplicator(gw.C.Minable))

	return reg
}

func velocityReplicator(m *ecs.Map1[comp.Velocity]) pkguniverse.ComponentReplicator {
	return pkguniverse.ComponentReplicator{
		ID: ReplVelocity,
		Scan: func(entity ecs.Entity) []byte {
			if !m.HasAll(entity) {
				return nil
			}
			v := m.Get(entity)
			buf := make([]byte, 8)
			putF32(buf[0:], v.X)
			putF32(buf[4:], v.Y)
			return buf
		},
		Apply: func(entity ecs.Entity, data []byte) {
			if len(data) < 8 || !m.HasAll(entity) {
				return
			}
			v := m.Get(entity)
			v.X = getF32(data[0:])
			v.Y = getF32(data[4:])
		},
	}
}

func rotationReplicator(m *ecs.Map1[comp.Rotation]) pkguniverse.ComponentReplicator {
	return pkguniverse.ComponentReplicator{
		ID: ReplRotation,
		Scan: func(entity ecs.Entity) []byte {
			if !m.HasAll(entity) {
				return nil
			}
			r := m.Get(entity)
			buf := make([]byte, 4)
			putF32(buf, r.Angle)
			return buf
		},
		Apply: func(entity ecs.Entity, data []byte) {
			if len(data) < 4 || !m.HasAll(entity) {
				return
			}
			r := m.Get(entity)
			r.Angle = getF32(data)
		},
	}
}

func healthReplicator(m *ecs.Map1[comp.Health]) pkguniverse.ComponentReplicator {
	return pkguniverse.ComponentReplicator{
		ID: ReplHealth,
		Scan: func(entity ecs.Entity) []byte {
			if !m.HasAll(entity) {
				return nil
			}
			h := m.Get(entity)
			buf := make([]byte, 8)
			putF32(buf[0:], h.Current)
			putF32(buf[4:], h.Max)
			return buf
		},
		Apply: func(entity ecs.Entity, data []byte) {
			if len(data) < 8 || !m.HasAll(entity) {
				return
			}
			h := m.Get(entity)
			h.Current = getF32(data[0:])
			h.Max = getF32(data[4:])
		},
		Add: func(entity ecs.Entity, data []byte) {
			if len(data) < 8 {
				return
			}
			m.Add(entity, &comp.Health{
				Current: getF32(data[0:]),
				Max:     getF32(data[4:]),
			})
		},
	}
}

func shieldReplicator(m *ecs.Map1[comp.Shield]) pkguniverse.ComponentReplicator {
	return pkguniverse.ComponentReplicator{
		ID: ReplShield,
		Scan: func(entity ecs.Entity) []byte {
			if !m.HasAll(entity) {
				return nil
			}
			s := m.Get(entity)
			buf := make([]byte, 20)
			putF32(buf[0:], s.Current)
			putF32(buf[4:], s.Max)
			putF32(buf[8:], s.RegenRate)
			putF32(buf[12:], s.RegenDelay)
			putF32(buf[16:], s.DamageCooldown)
			return buf
		},
		Apply: func(entity ecs.Entity, data []byte) {
			if len(data) < 20 || !m.HasAll(entity) {
				return
			}
			s := m.Get(entity)
			s.Current = getF32(data[0:])
			s.Max = getF32(data[4:])
			s.RegenRate = getF32(data[8:])
			s.RegenDelay = getF32(data[12:])
			s.DamageCooldown = getF32(data[16:])
		},
		Add: func(entity ecs.Entity, data []byte) {
			if len(data) < 20 {
				return
			}
			m.Add(entity, &comp.Shield{
				Current:        getF32(data[0:]),
				Max:            getF32(data[4:]),
				RegenRate:      getF32(data[8:]),
				RegenDelay:     getF32(data[12:]),
				DamageCooldown: getF32(data[16:]),
			})
		},
	}
}

func minableReplicator(m *ecs.Map1[gamecomp.Minable]) pkguniverse.ComponentReplicator {
	return pkguniverse.ComponentReplicator{
		ID: ReplMinable,
		Scan: func(entity ecs.Entity) []byte {
			if !m.HasAll(entity) {
				return nil
			}
			min := m.Get(entity)
			buf := make([]byte, 5)
			buf[0] = min.ResourceType
			putF32(buf[1:], min.Remaining)
			return buf
		},
		Apply: func(entity ecs.Entity, data []byte) {
			if len(data) < 5 || !m.HasAll(entity) {
				return
			}
			min := m.Get(entity)
			min.ResourceType = data[0]
			min.Remaining = getF32(data[1:])
		},
		Add: func(entity ecs.Entity, data []byte) {
			if len(data) < 5 {
				return
			}
			m.Add(entity, &gamecomp.Minable{
				ResourceType: data[0],
				Remaining:    getF32(data[1:]),
			})
		},
	}
}

func putF32(buf []byte, v float32) {
	binary.LittleEndian.PutUint32(buf, math.Float32bits(v))
}

func getF32(buf []byte) float32 {
	return math.Float32frombits(binary.LittleEndian.Uint32(buf))
}
