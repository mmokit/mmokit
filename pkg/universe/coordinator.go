package universe

import (
	"context"
	"log"
	"sync"

	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
)

const netIDRangeSize uint32 = 10_000_000

// NodeFactory creates a GameWorld and GameLoop for a single sector node.
// The engine and logger are provided; the factory returns the game world implementation
// and the configured game loop.
type NodeFactory func(sector coords.SectorCoord, eng *engine.Engine, events chan net.PlayerEvent, log *logger.Logger) (GameWorld, *engine.GameLoop)

// GridConfig defines the sector grid boundaries.
type GridConfig struct {
	MinSX, MaxSX int32
	MinSY, MaxSY int32
}

// Coordinator manages multiple Node instances, routes connections, and coordinates transfers.
type Coordinator struct {
	Nodes       map[string]*Node
	SectorOwner map[coords.SectorCoord]string // sector -> nodeID
	Topology    Topology

	ConnMgr *net.ConnManager
	Log     *logger.Logger

	mu         sync.RWMutex
	playerNode map[uint32]string // connID -> nodeID
}

// NewCoordinator creates a coordinator with a sector grid defined by GridConfig.
func NewCoordinator(
	grid GridConfig,
	platformCfg engine.Config,
	connMgr *net.ConnManager,
	gameLog *logger.Logger,
	factory NodeFactory,
) *Coordinator {
	c := &Coordinator{
		Nodes:       make(map[string]*Node),
		SectorOwner: make(map[coords.SectorCoord]string),
		ConnMgr:     connMgr,
		Log:         gameLog,
		playerNode:  make(map[uint32]string),
	}

	// Create grid of sectors
	var sectors []coords.SectorCoord
	var nodeIndex uint32
	for sy := grid.MinSY; sy <= grid.MaxSY; sy++ {
		for sx := grid.MinSX; sx <= grid.MaxSX; sx++ {
			sector := coords.SectorCoord{SX: sx, SY: sy}
			sectors = append(sectors, sector)

			id := SectorID(sector)
			eng := engine.New(platformCfg, connMgr, gameLog)
			eng.SetNetIDBase(nodeIndex * netIDRangeSize)

			events := make(chan net.PlayerEvent, 64)
			world, gameLoop := factory(sector, eng, events, gameLog)
			gameLoop.SetEventsCh(events)

			node := &Node{
				ID:        id,
				Sector:    sector,
				Engine:    eng,
				World:     world,
				Loop:      gameLoop,
				Inbox:     make(chan NodeMessage, 256),
				Events:    events,
				Neighbors: make(map[string]*Node),
				Log:       gameLog,
			}

			c.Nodes[id] = node
			c.SectorOwner[sector] = id
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

	// Wire node bridges
	for _, node := range c.Nodes {
		n := node // capture for closures
		bridge := &nodeBridge{node: n, coord: c}
		n.Bridge = bridge
	}

	log.Printf("coordinator: created %d nodes, topology computed", len(c.Nodes))
	return c
}

// Start launches all node goroutines and the event routing goroutine.
func (c *Coordinator) Start(ctx context.Context) {
	go c.routeEvents(ctx)

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
				defaultID := SectorID(coords.SectorCoord{SX: 0, SY: 0})
				c.setPlayerNode(evt.ConnID, defaultID)
				c.Nodes[defaultID].Events <- evt
				log.Printf("coordinator: conn %d -> %s", evt.ConnID, defaultID)
			} else {
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
