package universe

import (
	"context"
	"log"
	"sync"

	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
)

const netIDRangeSize uint32 = 10_000_000

// Coordinator manages multiple Node instances, routes connections, and coordinates transfers.
type Coordinator struct {
	Nodes       map[string]*Node
	SectorOwner map[coords.SectorCoord]string // sector → nodeID
	Topology    Topology

	ConnMgr  *net.ConnManager
	PlayerDB *game.PlayerRepo
	Log      *logger.Logger

	mu         sync.RWMutex
	playerNode map[uint32]string // connID → nodeID
}

// NewCoordinator creates a coordinator with a 3x3 sector grid.
func NewCoordinator(
	platformCfg engine.Config,
	gameCfg game.GameConfig,
	connMgr *net.ConnManager,
	playerDB *game.PlayerRepo,
	gameLog *logger.Logger,
) *Coordinator {
	c := &Coordinator{
		Nodes:       make(map[string]*Node),
		SectorOwner: make(map[coords.SectorCoord]string),
		ConnMgr:     connMgr,
		PlayerDB:    playerDB,
		Log:         gameLog,
		playerNode:  make(map[uint32]string),
	}

	// Create 3x3 grid of sectors: (-1,-1) through (1,1)
	var sectors []coords.SectorCoord
	var nodeIndex uint32
	for sy := int32(-1); sy <= 1; sy++ {
		for sx := int32(-1); sx <= 1; sx++ {
			sector := coords.SectorCoord{SX: sx, SY: sy}
			sectors = append(sectors, sector)

			node := NewNode(sector, platformCfg, gameCfg, connMgr, playerDB, gameLog)
			node.Engine.SetNetIDBase(nodeIndex * netIDRangeSize)

			c.Nodes[node.ID] = node
			c.SectorOwner[sector] = node.ID
			nodeIndex++
		}
	}

	// Compute topology and wire neighbors
	c.Topology = ComputeTopology(sectors)
	for sector, neighborSectors := range c.Topology.Neighbors {
		nodeID := c.SectorOwner[sector]
		node := c.Nodes[nodeID]
		for _, ns := range neighborSectors {
			neighborID := c.SectorOwner[ns]
			node.Neighbors[neighborID] = c.Nodes[neighborID]
		}
	}

	log.Printf("coordinator: created %d nodes, topology computed", len(c.Nodes))
	return c
}

// Start launches all node goroutines and the event routing goroutine.
func (c *Coordinator) Start(ctx context.Context) {
	// Start event routing
	go c.routeEvents(ctx)

	// Start all nodes
	for _, node := range c.Nodes {
		go node.Run(ctx)
	}

	log.Printf("coordinator: all %d nodes started", len(c.Nodes))
}

// Shutdown saves state on all nodes.
func (c *Coordinator) Shutdown() {
	for _, node := range c.Nodes {
		node.Shutdown()
	}
	log.Println("coordinator: all nodes shut down")
}

// routeEvents drains ConnManager.Events() and fans out to per-node Events channels.
func (c *Coordinator) routeEvents(ctx context.Context) {
	events := c.ConnMgr.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case evt := <-events:
			if evt.Connected {
				// New connection — assign to default node (sector 0,0)
				defaultID := SectorID(coords.SectorCoord{SX: 0, SY: 0})
				c.setPlayerNode(evt.ConnID, defaultID)
				c.Nodes[defaultID].Events <- evt
				log.Printf("coordinator: conn %d → %s", evt.ConnID, defaultID)
			} else {
				// Disconnect — route to owning node
				nodeID := c.getPlayerNode(evt.ConnID)
				if nodeID != "" {
					if node, ok := c.Nodes[nodeID]; ok {
						node.Events <- evt
					}
					c.removePlayerNode(evt.ConnID)
				}
			}
		}
	}
}

// NodeForSector returns the node that owns the given sector.
func (c *Coordinator) NodeForSector(sector coords.SectorCoord) *Node {
	nodeID := c.SectorOwner[sector]
	return c.Nodes[nodeID]
}

// DefaultNode returns the node for sector (0,0).
func (c *Coordinator) DefaultNode() *Node {
	return c.NodeForSector(coords.SectorCoord{SX: 0, SY: 0})
}

func (c *Coordinator) getPlayerNode(connID uint32) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.playerNode[connID]
}

func (c *Coordinator) setPlayerNode(connID uint32, nodeID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.playerNode[connID] = nodeID
}

func (c *Coordinator) removePlayerNode(connID uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.playerNode, connID)
}
