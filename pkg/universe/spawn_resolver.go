package universe

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/coords"
)

// SpawnResolver resolves a username to an absolute world-space Location.
// Called once per login on the process that owns playerDB (typically the
// coordinator). Returns ok=false when the user has no saved location —
// the gateway then falls back to Config.DefaultSpawn.
//
// The resolver is topology-blind: it returns world-space coords only.
// The gateway calls CellAtPosition(loc.X, loc.Y) at dispatch time to find
// the current owning cell, so split/merge between the resolver call and
// dispatch is handled naturally.
type SpawnResolver func(username string) (coords.Location, bool)

// SetSpawnResolver registers the spawn resolver on the coordinator.
// Called from game setup code (typically inside the needsGameState block).
// Must be called before Start().
func (c *Process) SetSpawnResolver(r SpawnResolver) {
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
		minX, minY, maxX, maxY := cell.WorldBounds(coords.CellSize)
		if worldX >= minX && worldX < maxX && worldY >= minY && worldY < maxY {
			return cellID
		}
	}
	return ""
}

// resolveSpawn returns the world-space Location for username.
//
//  1. Embedded coordinator with resolver → call inline (zero RPC overhead).
//  2. Standalone gateway → send ResolveSpawn RPC with 2s deadline.
//  3. Resolver absent, returns ok=false, or RPC fails → use DefaultSpawn.
func (g *Gateway) resolveSpawn(ctx context.Context, username string) coords.Location {
	if g.coord != nil {
		g.coord.mu.RLock()
		resolver := g.coord.spawnResolver
		defaultSpawn := g.coord.cfg.DefaultSpawn
		g.coord.mu.RUnlock()
		if resolver != nil {
			if loc, ok := resolver(username); ok {
				return loc
			}
		}
		return defaultSpawn
	}

	if g.controlClient != nil {
		rpcCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		resp, err := g.spawnOrch.send(rpcCtx, g.controlClient, g.id, username)
		if err == nil && resp != nil && resp.Ok {
			// Facing/Tag not yet carried on the RPC — teleport work will extend it.
			return coords.Location{X: resp.WorldX, Y: resp.WorldY}
		}
		if err != nil {
			g.log.Log(CatNetConn, "gateway: resolveSpawn RPC failed for %s: %v — using DefaultSpawn", username, err)
		}
	}
	return g.defaultSpawn
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
// timeout or if no stream is available.
func (o *spawnOrchestrator) send(ctx context.Context, client *meshGatewayClient, gatewayID string, username string) (*meshpb.SpawnResolved, error) {
	if client == nil {
		return nil, fmt.Errorf("resolveSpawn: no control client")
	}
	id, ch := o.alloc()

	msg := &meshpb.HostMessage{
		Msg: &meshpb.HostMessage_ResolveSpawn{
			ResolveSpawn: &meshpb.ResolveSpawn{
				RequestId: id,
				GatewayId: gatewayID,
				Username:  username,
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
