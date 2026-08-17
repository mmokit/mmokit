package universe

import (
	"bytes"
	"encoding/binary"
	"iter"
	"math"

	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/quantize"
	"github.com/mmokit/mmokit/pkg/replication"
)

// borderTailUnchanged is the componentCount sentinel value the sender
// writes when an entity's component tail is byte-identical to the last
// tail it sent to this specific neighbor. The receiver detects the
// sentinel in ApplyBorderFrame and skips applyEntityComponents, leaving
// the replica's existing components untouched. 0xFFFF is safe because
// it is a preposterously large count in practice — games that somehow
// legitimately ship more than 65534 components per entity can revisit
// the encoding.
const borderTailUnchanged uint16 = 0xFFFF

// borderLifecycleFullTailFrames is the number of consecutive full component
// tails emitted for a new, re-entered, or authority-epoch-reset entity. Border
// delivery is intentionally lossy, so a single attempted full tail cannot be
// treated as proof the receiver owns a baseline. Three ticks bounds the extra
// bandwidth to 150ms at 20Hz while tolerating two consecutive dropped frames.
const borderLifecycleFullTailFrames uint8 = 3

// borderFullResyncInterval is the number of ticks after which the
// sender forces a full component tail even if the content is unchanged.
// This gives the receiver a recovery window if a frame was dropped at
// the destCell.Inbox select{default} path: at worst the replica is
// stale for borderFullResyncInterval ticks (~1.5s at 20Hz). A fresh
// entity's first frame counts as the initial resync, so this only
// applies to long-lived entities that never change.
const borderFullResyncInterval uint32 = 30

// BorderDispatcher walks local entities near shared cell boundaries and
// builds a replication.Frame per neighbor via the shared pkg/replication
// dispatcher. Tick() runs a spatial query, yields EntityRef candidates
// for each neighbor, and calls replication.Dispatcher.Walk to produce
// frames. CellViewer.Send encodes each non-empty Frame into a
// MsgBorderFrame envelope and writes it to the destination cell's Inbox.
type BorderDispatcher struct {
	base      *Stage
	neighbors map[string]*CellViewer
	disp      *replication.Dispatcher
}

// NewBorderDispatcher creates a dispatcher bound to a Stage and a
// set of neighbor viewers. Both arguments may be nil for unit tests.
func NewBorderDispatcher(base *Stage, neighbors map[string]*CellViewer) *BorderDispatcher {
	return &BorderDispatcher{
		base:      base,
		neighbors: neighbors,
		disp:      replication.NewDispatcher(),
	}
}

// Tick runs one pass of the border dispatcher. For each neighbor it
// builds a Frame via replication.Dispatcher.Walk and hands the frame
// to CellViewer.Send.
func (bd *BorderDispatcher) Tick(currentTick uint64) {
	if len(bd.neighbors) == 0 {
		return
	}
	for _, nv := range bd.neighbors {
		if nv == nil {
			continue
		}
		cands := bd.candidatesFor(nv, currentTick)
		frame := bd.disp.Walk(nv, currentTick, cands)
		nv.Send(frame)
		// Rotate hysteresis state: whatever passed the proximity
		// predicate this tick becomes the "in set" used for exit-margin
		// checks on the next tick, and any entity that dropped out is
		// forgotten. Also forms the authoritative interest-set snapshot
		// the receiver will diff against.
		nv.SwapInSet()
	}
}

// candidatesFor returns an iterator over entities eligible for
// replication to the given neighbor. Uses an ECS Filter4 over
// Position+NetworkID+EntityKind+Collider, excluding Ghost and Dormant entities.
// Only entities within the AoI margin of the shared edge with nv are yielded.
//
// currentTick is threaded through so the per-entity Build closure can
// decide whether to emit a full component tail or the "unchanged since
// baseline" sentinel: every borderFullResyncInterval ticks the tail is
// force-refreshed regardless of equality, so dropped frames heal
// automatically without an explicit ack protocol.
func (bd *BorderDispatcher) candidatesFor(nv *CellViewer, currentTick uint64) iter.Seq[replication.EntityRef] {
	return func(yield func(replication.EntityRef) bool) {
		if bd.base == nil {
			return
		}
		world := bd.base.ECSWorld()
		cellSize := bd.base.CellSize()
		lMinX, lMinY, lMaxX, lMaxY := bd.base.Cell().LocalBounds(cellSize)
		enterMargin := bd.base.GetAoIRadius()
		if enterMargin <= 0 {
			enterMargin = 100
		}
		// Exit margin is 50% wider than enter so an entity already in
		// the push set must travel meaningfully away from the boundary
		// before dropping out. Prevents flicker churn around the edge.
		exitMargin := enterMargin * 1.5

		// World-space origin of the sender's root cell.
		senderCell := bd.base.Cell()
		rootCell := senderCell
		for rootCell.Depth > 0 {
			rootCell = rootCell.Parent()
		}
		cellOriginX := float32(rootCell.X) * cellSize
		cellOriginY := float32(rootCell.Y) * cellSize

		filter := ecs.NewFilter4[component.Position, component.NetworkID, component.EntityKind, component.Collider](world).
			Without(ecs.C[component.Ghost](), ecs.C[component.Replica](), ecs.C[component.Dormant]())
		velMap := ecs.NewMap1[component.Velocity](world)
		rotMap := ecs.NewMap1[component.Rotation](world)

		baselines := nv.Baselines()

		query := filter.Query()
		// defer Close() guards against a panic inside ref.Build (which calls
		// game-specific scanEntityComponents → Scan callbacks) leaking the
		// world's read-lock. See pkg/query/query.go for the full rationale —
		// the failure mode there is identical here: a panic inside yield(ref)
		// propagates up without re-entering Next(), so the inline Close call
		// at line 215 (early-break) is bypassed. Close is idempotent.
		defer query.Close()
		for query.Next() {
			pos, netID, kind, collider := query.Get()
			entity := query.Entity()

			// Hysteresis: entities already in this viewer's push set on the
			// previous tick use the looser exitMargin; new entrants use the
			// tighter enterMargin. Prevents flicker when an entity oscillates
			// across a single margin threshold.
			m := enterMargin
			if nv.WasInSet(netID.ID) {
				m = exitMargin
			}
			if !bd.entityNearNeighborEdge(pos, nv, lMinX, lMinY, lMaxX, lMaxY, m) {
				continue
			}
			// Snapshot values by value so the closure captures stable copies.
			px, py := pos.X, pos.Y
			radius := collider.Radius
			nid := *netID
			ent := entity

			// Read velocity for dead-reckoning (optional — zero if absent).
			var vx, vy float32
			if velMap.HasAll(entity) {
				vel := velMap.Get(entity)
				vx, vy = vel.X, vel.Y
			}

			// Read rotation (optional — zero if absent). Carried in the
			// fixed border header as a 2-byte qangle so neighbor cells
			// can keep their replica entities in sync with the
			// authority's facing direction. Without this, a replica
			// promoted to Live at handoff inherits Rotation=0 and the
			// entity visibly snaps east on every cell crossing.
			var rotAngle float32
			if rotMap.HasAll(entity) {
				rotAngle = rotMap.Get(entity).Angle
			}

			ref := replication.EntityRef{
				NetID: replication.NetID{ID: nid.ID, Epoch: nid.Epoch},
				Kind:  uint16(kind.Type),
				X:     px,
				Y:     py,
				Build: func(dst []byte) []byte {
					// Build runs only after the shared Dispatcher accepts both
					// tier radius and update-rate gates. Record the exact wire
					// membership, rather than the larger pre-dispatch candidate
					// set, so omitted entries cannot retain stale baselines.
					nv.MarkInSet(nid.ID)
					worldX := cellOriginX + px
					worldY := cellOriginY + py
					dst = binary.LittleEndian.AppendUint32(dst, math.Float32bits(worldX))
					dst = binary.LittleEndian.AppendUint32(dst, math.Float32bits(worldY))
					dst = binary.LittleEndian.AppendUint32(dst, math.Float32bits(radius))
					dst = binary.LittleEndian.AppendUint16(dst, uint16(quantizeVelI16(vx, 2000)))
					dst = binary.LittleEndian.AppendUint16(dst, uint16(quantizeVelI16(vy, 2000)))
					dst = binary.LittleEndian.AppendUint16(dst, quantize.Angle(rotAngle))
					// Stamp the authoritative producer's tick-aligned cluster
					// clock so the destination cell caches it on
					// Replica.ProducedAtMs and relays it verbatim through its
					// own outbound replication. Tick-aligned (TickTime vs Now)
					// so cell-tick stagger doesn't bleed into the client's
					// timeline — a border push on source's tick N and a live
					// sample on the new authority's tick N+1 land exactly
					// one tickInterval apart across the seam.
					var producedAtMs uint64
					if bd.base.clusterClock != nil {
						producedAtMs = bd.base.clusterClock.TickTime(bd.base.eng.TickIntervalMs())
					}
					dst = binary.LittleEndian.AppendUint64(dst, producedAtMs)

					// Serialize the per-component tail into scratch so we
					// can compare it against the last-sent tail for this
					// neighbor. If they match AND we haven't hit a forced
					// resync tick, emit the unchanged sentinel (2 bytes)
					// instead of the whole tail.
					nv.tailScratch = nv.tailScratch[:0]
					nv.tailScratch = bd.base.scanEntityComponents(ent, nv.tailScratch)

					bl := baselines.Baseline(nid.ID)
					if bl == nil || !bl.HasAuthorityEpoch || bl.AuthorityEpoch != nid.Epoch {
						// Baselines are scoped to (netID, authority epoch). A
						// handoff can preserve byte-identical component state, but
						// the receiver may be materializing a new entity lifecycle
						// and therefore cannot consume an unchanged sentinel.
						if bl != nil {
							baselines.DropBaseline(nid.ID)
						}
						bl = baselines.GetOrCreateBaseline(nid.ID, 0)
						bl.AuthorityEpoch = nid.Epoch
						bl.HasAuthorityEpoch = true
						bl.FullTailFramesRemaining = borderLifecycleFullTailFrames
					}
					forceResync := bl.Acked == nil ||
						bl.FullTailFramesRemaining > 0 ||
						uint32(currentTick)%borderFullResyncInterval == 0
					if !forceResync && bytes.Equal(bl.Acked, nv.tailScratch) {
						dst = binary.LittleEndian.AppendUint16(dst, borderTailUnchanged)
						return dst
					}

					// Tail changed (or we're forcing a resync) — emit the
					// full tail and update the per-viewer baseline so the
					// next tick's comparison is against what we just sent.
					dst = append(dst, nv.tailScratch...)
					bl.Acked = append(bl.Acked[:0], nv.tailScratch...)
					if bl.FullTailFramesRemaining > 0 {
						bl.FullTailFramesRemaining--
					}
					return dst
				},
			}
			if !yield(ref) {
				return
			}
		}
	}
}

// quantizeVelI16 quantizes a velocity component to int16 [-32767, 32767].
func quantizeVelI16(v, scale float32) int16 {
	if scale <= 0 {
		return 0
	}
	ratio := v / scale
	if ratio < -1 {
		ratio = -1
	} else if ratio > 1 {
		ratio = 1
	}
	return int16(ratio * 32767)
}

// dequantizeVelI16 dequantizes an int16 back to a velocity component.
func dequantizeVelI16(q int16, scale float32) float32 {
	return float32(q) / 32767 * scale
}

// entityNearNeighborEdge reports whether an entity at (pos.X, pos.Y) sits
// within margin of the shared edge with the neighbor at direction (dirDX, dirDY).
func (bd *BorderDispatcher) entityNearNeighborEdge(
	pos *component.Position,
	nv *CellViewer,
	lMinX, lMinY, lMaxX, lMaxY, margin float32,
) bool {
	nearLeft := pos.X < lMinX+margin
	nearRight := pos.X > lMaxX-margin
	nearBottom := pos.Y < lMinY+margin
	nearTop := pos.Y > lMaxY-margin

	switch {
	case nv.dirDX == -1 && nv.dirDY == 0:
		return nearLeft
	case nv.dirDX == 1 && nv.dirDY == 0:
		return nearRight
	case nv.dirDX == 0 && nv.dirDY == -1:
		return nearBottom
	case nv.dirDX == 0 && nv.dirDY == 1:
		return nearTop
	case nv.dirDX == -1 && nv.dirDY == -1:
		return nearLeft && nearBottom
	case nv.dirDX == -1 && nv.dirDY == 1:
		return nearLeft && nearTop
	case nv.dirDX == 1 && nv.dirDY == -1:
		return nearRight && nearBottom
	case nv.dirDX == 1 && nv.dirDY == 1:
		return nearRight && nearTop
	}
	return false
}
