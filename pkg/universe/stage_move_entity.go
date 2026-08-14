package universe

import (
	"fmt"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmokit/pkg/component"
	"github.com/zenion/mmokit/pkg/coords"
)

// MoveOpt configures a MoveEntityTo call.
type MoveOpt func(*moveOpts)

type moveOpts struct {
	bypassCooldown bool
	connID         uint32
	username       string
}

// MoveBypassCooldown skips HandoffCooldownTicks for an explicit teleport.
// Boundary-crossing events (which never set this flag) keep the default
// anti-thrash cooldown.
func MoveBypassCooldown() MoveOpt {
	return func(o *moveOpts) { o.bypassCooldown = true }
}

// MoveAsPlayer attaches a player session's ConnID and Username to the
// emitted CrossingEvent so the destination cell can re-register the
// session on commit. Required for player-entity moves; non-player
// entities omit this opt.
func MoveAsPlayer(connID uint32, username string) MoveOpt {
	return func(o *moveOpts) {
		o.connID = connID
		o.username = username
	}
}

// MoveEntityTo moves the named entity to absolute world coordinates.
// Same-cell → direct Position/Velocity/MoveTarget update. Different cell →
// pre-stamps Position+CellCoord to destination-frame coords and enqueues a
// crossing event for HandoffDriver to convert into a hard-cut handoff at
// currentClusterTick + HandoffLeadTicks. Cross-host moves are routed by
// the bridge wired into HandoffDriver — same code path as natural
// boundary crossings.
//
// MUST be called on the cell's loop goroutine. Command handlers running
// via mmokit.OnLoop satisfy this; off-loop callers must wrap in
// engine.RunOnLoop.
//
// Returns an error if the entity is not alive or has no Position.
func (s *Stage) MoveEntityTo(e ecs.Entity, worldX, worldY float32, opts ...MoveOpt) error {
	cfg := moveOpts{}
	for _, o := range opts {
		o(&cfg)
	}
	if !s.ECSWorld().Alive(e) {
		return fmt.Errorf("MoveEntityTo: entity not alive")
	}
	if !s.posMap.HasAll(e) {
		return fmt.Errorf("MoveEntityTo: entity has no Position")
	}

	cellSize := coords.CellSize
	destCellX := int32(worldX / cellSize)
	destCellY := int32(worldY / cellSize)
	// Negative-coords correction: int32 truncation is toward zero, but cell
	// coords floor toward -infinity. Correct when the raw world coord is
	// negative and not exactly on the cell boundary.
	if worldX < 0 && worldX != float32(destCellX)*cellSize {
		destCellX--
	}
	if worldY < 0 && worldY != float32(destCellY)*cellSize {
		destCellY--
	}

	// Resolve destCellID via the topology — this returns the canonical
	// MeshCellID including depth info for sub-cells produced by splits.
	// Hand-rolling fmt.Sprintf("cell_%d_%d", ...) would break TPs into a
	// post-split cell whose actual ID is e.g. cell_d1_2_0.
	destCellID := MeshCellID(s.Bridge().CellOwnerAtPos(worldX, worldY))
	if destCellID == "" {
		return fmt.Errorf("MoveEntityTo: no cell owns world position (%g,%g)", worldX, worldY)
	}

	// Entity Position + CellCoord stays in the base-cell coordinate system
	// regardless of split depth (per CLAUDE.md: "Entities always keep
	// base-cell coordinates"). Compute local coords against the base cell.
	localX := worldX - float32(destCellX)*cellSize
	localY := worldY - float32(destCellY)*cellSize

	// Pre-stamp the entity with destination-frame coords so the serialized
	// TransferFrame carries the correct post-TP position whether same-cell
	// or cross-cell.
	pos := s.posMap.Get(e)
	pos.X, pos.Y = localX, localY

	if s.velMap.HasAll(e) {
		vel := s.velMap.Get(e)
		vel.X, vel.Y = 0, 0
	}
	if s.cellMap.HasAll(e) {
		cc := s.cellMap.Get(e)
		cc.CellX, cc.CellY = destCellX, destCellY
	}

	// Reset MoveTarget if present (engine-generic component; map built inline).
	mtMap := ecs.NewMap1[component.MoveTarget](s.ECSWorld())
	if mtMap.HasAll(e) {
		mt := mtMap.Get(e)
		mt.Active = false
	}

	// Same-cell fast path: in-place mutation done; no handoff machinery needed.
	if destCellID == s.cellID {
		return nil
	}

	// Cross-cell branch: enqueue a crossing for HandoffDriver.
	netID := uint32(0)
	if s.netIDMap.HasAll(e) {
		netID = s.netIDMap.Get(e).ID
	}

	s.QueueCrossing(CrossingEvent{
		Entity:         e,
		NetID:          netID,
		ConnID:         cfg.connID,
		Username:       cfg.username,
		DestCellID:     destCellID,
		BypassCooldown: cfg.bypassCooldown,
	})
	return nil
}
