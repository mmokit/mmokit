package main

import (
	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	slitherpb "github.com/zenion/mmoserver/gen/go/slitherpb"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/spatial"
)


// ---------------------------------------------------------------------------
// EntityReplicators
// ---------------------------------------------------------------------------

// snakeReplicator handles hash + serialize for snake entities (player and bot).
type snakeReplicator struct {
	gw *SlitherWorld
}

func (r *snakeReplicator) EntityType() uint8 { return KindPlayerSnake }

func (r *snakeReplicator) Hash(h *mmokit.Hasher, viewer *mmokit.ViewerInfo, entry spatial.Entry) {
	gw := r.gw
	// Read from Position component, not entry.X/Y — the spatial grid may
	// return a body-segment entry (LayerSnakeBody) whose position is a
	// historical body point, not the current head.
	hx, hy := entry.X, entry.Y
	if gw.PositionMap().HasAll(entry.Entity) {
		pos := gw.PositionMap().Get(entry.Entity)
		hx, hy = pos.X, pos.Y
	}
	h.Float32(hx)
	h.Float32(hy)

	if gw.RotationMap.HasAll(entry.Entity) {
		h.Float32(gw.RotationMap.Get(entry.Entity).Angle)
	}
	if gw.SnakeStateMap.HasAll(entry.Entity) {
		state := gw.SnakeStateMap.Get(entry.Entity)
		h.Float32(state.Speed)
		h.Float32(state.Mass)
		h.Uint8(state.SkinID)
		h.Bool(state.Boosting)
	}
	if gw.SnakeBodyMap.HasAll(entry.Entity) {
		body := gw.SnakeBodyMap.Get(entry.Entity)
		// Hash segment count and a sample of segments for change detection.
		h.Uint32(uint32(body.Length))
		step := max(gw.Cfg.SegmentSubsample, 1)
		for i := 0; i < body.Length; i += step {
			seg := body.GetSegment(i)
			h.Float32(seg.X)
			h.Float32(seg.Y)
		}
	}
}

func (r *snakeReplicator) Snapshot(w *quantize.SnapshotWriter, viewer *mmokit.ViewerInfo, entry spatial.Entry) {
	gw := r.gw

	// Read from Position component — see Hash comment above.
	// Position is cell-relative (single-cell nodes); replicas are already translated.
	hx, hy := entry.X, entry.Y
	if gw.PositionMap().HasAll(entry.Entity) {
		pos := gw.PositionMap().Get(entry.Entity)
		hx, hy = pos.X, pos.Y
	}
	w.Float32(hx)
	w.Float32(hy)

	var angle, speed, mass float32
	var skinID uint8
	var boosting bool
	if gw.SnakeStateMap.HasAll(entry.Entity) {
		state := gw.SnakeStateMap.Get(entry.Entity)
		angle = state.TargetAngle
		speed = state.Speed
		mass = state.Mass
		skinID = state.SkinID
		boosting = state.Boosting
	}
	if gw.RotationMap.HasAll(entry.Entity) {
		angle = gw.RotationMap.Get(entry.Entity).Angle
	}

	w.QAngle(angle)
	w.QVel(speed, 500.0)
	w.QVel(mass, 10000.0)
	w.Uint8(skinID)

	var flags uint8
	if boosting {
		flags |= 1
	}
	w.Uint8(flags)

	// Variable-length tail: body segments.
	// DeltaEncoder requires a uint16 BYTE LENGTH prefix (not segment count).
	// Each segment is 8 bytes (2x float32 cell-relative positions).
	if gw.SnakeBodyMap.HasAll(entry.Entity) {
		body := gw.SnakeBodyMap.Get(entry.Entity)
		step := max(gw.Cfg.SegmentSubsample, 1)
		segCount := 0
		for i := 0; i < body.Length; i += step {
			segCount++
		}
		tailByteLen := uint16(segCount * 8) // 8 bytes per segment (2x float32)
		w.Uint16(tailByteLen)
		for i := 0; i < body.Length; i += step {
			seg := body.GetSegment(i)
			w.Float32(seg.X)
			w.Float32(seg.Y)
		}
	} else {
		w.Uint16(0)
	}
}

func (r *snakeReplicator) SnapshotLayout() []int {
	// headX(4) + headY(4) + angle(2) + speed(2) + mass(2) + skinID(1) + flags(1) + variable tail
	return []int{4, 4, 2, 2, 2, 1, 1, -1}
}

func (r *snakeReplicator) InitialData(viewer *mmokit.ViewerInfo, entry spatial.Entry) []byte {
	gw := r.gw
	if gw.SnakeStateMap.HasAll(entry.Entity) {
		name := gw.SnakeStateMap.Get(entry.Entity).Name
		if name != "" {
			nameBytes := []byte(name)
			buf := make([]byte, 1+len(nameBytes))
			buf[0] = byte(len(nameBytes))
			copy(buf[1:], nameBytes)
			return buf
		}
	}
	return nil
}

// foodReplicator handles hash + serialize for food entities (natural and death).
type foodReplicator struct {
	gw *SlitherWorld
}

func (r *foodReplicator) EntityType() uint8 { return KindNaturalFood }

func (r *foodReplicator) Hash(h *mmokit.Hasher, viewer *mmokit.ViewerInfo, entry spatial.Entry) {
	h.Float32(entry.X)
	h.Float32(entry.Y)
	if r.gw.FoodMap.HasAll(entry.Entity) {
		food := r.gw.FoodMap.Get(entry.Entity)
		h.Float32(food.Value)
		h.Uint8(food.ColorIdx)
	}
}

func (r *foodReplicator) Snapshot(w *quantize.SnapshotWriter, viewer *mmokit.ViewerInfo, entry spatial.Entry) {
	w.Float32(entry.X)
	w.Float32(entry.Y)

	var value float32 = 1.0
	var colorIdx uint8
	if r.gw.FoodMap.HasAll(entry.Entity) {
		food := r.gw.FoodMap.Get(entry.Entity)
		value = food.Value
		colorIdx = food.ColorIdx
	}
	w.QNorm(value)
	w.Uint8(colorIdx)
}

func (r *foodReplicator) SnapshotLayout() []int {
	// x(4) + y(4) + value(1) + colorIdx(1)
	return []int{4, 4, 1, 1}
}

func (r *foodReplicator) InitialData(viewer *mmokit.ViewerInfo, entry spatial.Entry) []byte {
	return nil
}

func (r *foodReplicator) ReplicationTier() mmokit.ReplicationTier {
	return mmokit.ReplicationTier{
		Radius:        2000, // food visible at shorter range than snakes
		UpdateDivisor: 3,    // food updates every 3rd tick
		BaseWeight:    0.3,  // low priority
	}
}

// buildLeaderboard encodes a protobuf leaderboard wrapped in a ServerEvent envelope.
func buildLeaderboard(entries []LeaderEntry) []byte {
	msg := &slitherpb.SlitherLeaderboardMsg{}
	for i := range entries {
		e := &entries[i]
		msg.Entries = append(msg.Entries, &slitherpb.SlitherLeaderEntry{
			Name:   e.Name,
			Mass:   e.Mass,
			SkinId: uint32(e.SkinID),
		})
	}
	return mmokit.MakeEvent(uint32(slitherpb.SlitherServerEventCode_SSE_LEADERBOARD), msg)
}

// buildKillFeed encodes a protobuf kill feed wrapped in a ServerEvent envelope.
func buildKillFeed(entries []KillFeedEntry) []byte {
	msg := &slitherpb.SlitherKillFeedMsg{}
	for i := range entries {
		e := &entries[i]
		msg.Entries = append(msg.Entries, &slitherpb.SlitherKillFeedEntry{
			VictimName: e.VictimName,
			KillerName: e.KillerName,
			VictimMass: e.VictimMass,
		})
	}
	return mmokit.MakeEvent(uint32(slitherpb.SlitherServerEventCode_SSE_KILL_FEED), msg)
}

// sendFarewell sends a final world update with all previously visible entities as removed.
func sendFarewell(gw *SlitherWorld, replSys *mmokit.ReplicationSystem, connID uint32, tick uint32) {
	vis := replSys.LastVisible(connID)
	if len(vis) == 0 {
		return
	}
	var removed []uint32
	for id := range vis {
		removed = append(removed, id)
	}
	enc := quantize.NewFrameEncoder(256)
	binData := enc.Encode(tick, 0, nil, nil, removed, nil)
	data := mmokit.MakeEventRaw(uint32(enginepb.ServerEventCode_SE_DELTA_WORLD_UPDATE), binData)
	gw.Engine().ConnMgr.Send(connID, data)
}
