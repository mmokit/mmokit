package universe

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	meshpb "github.com/mmokit/mmokit/gen/go/meshpb"
	"github.com/mmokit/mmokit/pkg/coords"
	"github.com/mmokit/mmokit/pkg/engine"
)

// SpawnResolver decides the world-space spawn location for a player session
// at login (or post-death respawn). Called once per login on the process that
// owns the coordinator. The returned Location is fed into CellAtPosition so
// the gateway can route the PlayerAssignment to the owning cell.
//
// The resolver is topology-blind: it returns world-space coords only. The
// gateway calls CellAtPosition(loc.X, loc.Y) at dispatch time to pick the
// current owning cell, so split/merge between resolver call and dispatch is
// handled naturally.
//
// Games own the entire decision: DB lookup, faction-based zones, group
// respawn — whatever logic the game needs lives inside this callback. If no
// resolver is registered, the engine defaults to the center of cell (0,0)
// derived from Config.CellSize.
type SpawnResolver func(session *engine.PlayerSession) coords.Location

// OnResolveSpawn registers the spawn resolver on the coordinator. Must be
// called before Start(). At most one resolver is registered; later calls
// overwrite earlier ones.
func (c *Process) OnResolveSpawn(r SpawnResolver) {
	c.mu.Lock()
	c.spawnResolver = r
	c.mu.Unlock()
}

// CellAtPosition returns the cell ID currently owning world position (worldX, worldY).
// Handles any split depth by walking CellOwner and checking WorldBounds.
// Returns "" if no cell owns the point (shouldn't happen in normal topology).
func (c *Process) CellAtPosition(worldX, worldY float32) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for cell, cellID := range c.CellOwner {
		minX, minY, maxX, maxY := cell.WorldBounds(c.CellSize())
		if worldX >= minX && worldX < maxX && worldY >= minY && worldY < maxY {
			return string(cellID)
		}
	}
	return ""
}

// spawnResolution carries the result of resolveSpawn — both the world-space
// spawn location AND any reconnect-routing override discovered on the
// coordinator (standalone gateway only; the embedded path runs reconnect
// detection inline at dispatchPlayerAssignment).
//
// Rejected=true means an Active session already exists for this user_id; the
// caller MUST close the new WebSocket without dispatching a PlayerAssignment
// (see Gateway.dispatchPostAuthAssignment).
type spawnResolution struct {
	Location     coords.Location
	IsReconnect  bool
	TargetHostID string
	TargetCellID string
	Rejected     bool
}

// resolveSpawn returns the spawn location for the (userID, username) pair and
// (in standalone mode) any reconnect-routing override discovered via the
// coordinator's activeUsers index.
//
//  1. Embedded coordinator with resolver → call inline (zero RPC overhead).
//     Reconnect detection happens later in dispatchPlayerAssignment.
//  2. Standalone gateway → send ResolveSpawn RPC with 2s deadline. The
//     coordinator runs the resolver and may piggyback reconnect info.
//  3. No resolver registered or RPC fails → fall back to the engine default
//     (center of cell (0,0), computed from cfg.CellSize).
func (g *Gateway) resolveSpawn(ctx context.Context, userID uuid.UUID, username string) spawnResolution {
	if g.coord != nil {
		g.coord.mu.RLock()
		resolver := g.coord.spawnResolver
		g.coord.mu.RUnlock()
		if resolver != nil {
			session := &engine.PlayerSession{UserID: userID, Username: username}
			return spawnResolution{Location: resolver(session)}
		}
		return spawnResolution{Location: defaultSpawnLocation(g.topology.baseCellSize)}
	}

	if g.controlClient != nil {
		rpcCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		resp, err := g.spawnOrch.send(rpcCtx, g.controlClient, g.id, userID, username)
		if err == nil && resp != nil {
			return spawnResolution{
				Location:     coords.Location{X: resp.WorldX, Y: resp.WorldY},
				IsReconnect:  resp.IsReconnect,
				TargetHostID: resp.TargetHostId,
				TargetCellID: resp.TargetCellId,
				Rejected:     resp.Rejected,
			}
		}
		if err != nil {
			g.log.Log(CatNetConn, "gateway: resolveSpawn RPC failed for %s: %v — using engine default", username, err)
		}
	}
	return spawnResolution{Location: defaultSpawnLocation(g.topology.baseCellSize)}
}

// defaultSpawnLocation returns the center of cell (0,0) using the current
// package-level world cell size. Used when no SpawnResolver is registered
// (or the standalone RPC fails). Topology-blind — the gateway still routes
// via CellAtPosition.
func defaultSpawnLocation(baseCellSize float32) coords.Location {
	return coords.Location{X: baseCellSize / 2, Y: baseCellSize / 2}
}

// ── spawnOrchestrator ─────────────────────────────────────────────────────────

// spawnOrchestrator tracks in-flight ResolveSpawn requests. Mirrors
// commandOrchestrator in shape and thread-safety.
type spawnOrchestrator struct {
	mu       sync.Mutex
	nextID   atomic.Uint64
	inflight map[uint64]*inflightSpawn
}

type inflightSpawn struct {
	done chan *meshpb.SpawnResolved
}

func newSpawnOrchestrator() *spawnOrchestrator {
	return &spawnOrchestrator{
		inflight: make(map[uint64]*inflightSpawn),
	}
}

// alloc reserves a request slot and returns its ID + response channel.
func (o *spawnOrchestrator) alloc() (uint64, chan *meshpb.SpawnResolved) {
	id := o.nextID.Add(1)
	ch := make(chan *meshpb.SpawnResolved, 1)
	o.mu.Lock()
	o.inflight[id] = &inflightSpawn{done: ch}
	o.mu.Unlock()
	return id, ch
}

// OnResponse delivers a SpawnResolved to the waiting caller. No-op if the
// request ID is unknown (already timed out).
func (o *spawnOrchestrator) OnResponse(resp *meshpb.SpawnResolved) {
	o.mu.Lock()
	inf, ok := o.inflight[resp.RequestId]
	if ok {
		delete(o.inflight, resp.RequestId)
	}
	o.mu.Unlock()
	if !ok {
		return
	}
	select {
	case inf.done <- resp:
	default:
	}
}

// remove cleans up an in-flight slot without signalling. Used on timeout.
func (o *spawnOrchestrator) remove(id uint64) {
	o.mu.Lock()
	delete(o.inflight, id)
	o.mu.Unlock()
}

// send sends a ResolveSpawn on the gateway client stream, waits for the
// coordinator's reply, and returns the response. Returns an error on
// timeout or if no stream is available. userID is forwarded so the
// coordinator can run reconnect detection against activeUsers.
func (o *spawnOrchestrator) send(ctx context.Context, client *meshGatewayClient, gatewayID string, userID uuid.UUID, username string) (*meshpb.SpawnResolved, error) {
	if client == nil {
		return nil, fmt.Errorf("resolveSpawn: no control client")
	}
	id, ch := o.alloc()

	var userIDStr string
	if userID != uuid.Nil {
		userIDStr = userID.String()
	}
	msg := &meshpb.HostMessage{
		Msg: &meshpb.HostMessage_ResolveSpawn{
			ResolveSpawn: &meshpb.ResolveSpawn{
				RequestId: id,
				GatewayId: gatewayID,
				Username:  username,
				UserId:    userIDStr,
			},
		},
	}
	if err := client.send(msg); err != nil {
		o.remove(id)
		return nil, fmt.Errorf("resolveSpawn: send: %w", err)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		o.remove(id)
		return nil, ctx.Err()
	}
}
