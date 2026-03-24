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

// NodeFactory creates a GameWorld and a list of game systems for a single
// sector node. The Coordinator provides a pre-configured WorldBase; the
// factory returns the GameWorld (typically embedding the WorldBase) and any
// custom systems. The Coordinator handles Engine creation, GameLoop setup,
// hook wiring, and the built-in BoundarySystem.
type NodeFactory func(base *WorldBase) (GameWorld, []engine.System)

// GridConfig defines the sector grid boundaries.
type GridConfig struct {
	MinSX, MaxSX int32
	MinSY, MaxSY int32
}

// CoordinatorOption configures optional Coordinator settings.
type CoordinatorOption func(*coordOpts)

type coordOpts struct {
	connMgr   *net.ConnManager
	log       *logger.Logger
	aoiRadius float32
}

// WithConnManager sets a custom connection manager.
func WithConnManager(cm *net.ConnManager) CoordinatorOption {
	return func(o *coordOpts) { o.connMgr = cm }
}

// WithLogger sets a custom debug logger.
func WithLogger(l *logger.Logger) CoordinatorOption {
	return func(o *coordOpts) { o.log = l }
}

// WithAoIRadius sets the area-of-interest radius used for replication margins.
func WithAoIRadius(r float32) CoordinatorOption {
	return func(o *coordOpts) { o.aoiRadius = r }
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
// The factory is called once per sector to create the game world and systems.
// ConnManager and Logger are created with defaults if not provided via options.
func NewCoordinator(
	grid GridConfig,
	platformCfg engine.Config,
	factory NodeFactory,
	opts ...CoordinatorOption,
) *Coordinator {
	o := coordOpts{
		aoiRadius: 500,
	}
	for _, opt := range opts {
		opt(&o)
	}
	if o.connMgr == nil {
		o.connMgr = net.NewConnManager()
	}
	if o.log == nil {
		o.log = logger.New()
	}

	c := &Coordinator{
		Nodes:       make(map[string]*Node),
		SectorOwner: make(map[coords.SectorCoord]string),
		ConnMgr:     o.connMgr,
		Log:         o.log,
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
			eng := engine.New(platformCfg, o.connMgr, o.log)
			eng.SetNetIDBase(nodeIndex * netIDRangeSize)

			events := make(chan net.PlayerEvent, 64)

			// Create WorldBase for this node
			base := NewWorldBase(eng, sector, o.aoiRadius, nil)

			// Call factory to get GameWorld and custom systems
			world, gameSystems := factory(&base)

			// Append built-in BoundarySystem after user systems
			if bw, ok := world.(BoundaryWorld); ok {
				gameSystems = append(gameSystems, NewBoundarySystem(eng.ECS, bw))
			}

			// Build the node first so hook closures can capture its Bridge
			// field (which is wired after all nodes are created).
			node := &Node{
				ID:        id,
				Sector:    sector,
				Engine:    eng,
				World:     world,
				Inbox:     make(chan NodeMessage, 256),
				Events:    events,
				Neighbors: make(map[string]*Node),
				Log:       o.log,
			}

			gameHooks := world.Hooks()
			mergedHooks := engine.Hooks{
				OnConnect:     gameHooks.OnConnect,
				OnDisconnect:  gameHooks.OnDisconnect,
				ProcessLogins: gameHooks.ProcessLogins,
				PreFlush:      gameHooks.PreFlush,
				PostFlush:     gameHooks.PostFlush,
				ClearTickState: func() {
					if gameHooks.ClearTickState != nil {
						gameHooks.ClearTickState()
					}
					node.Bridge.PreTick()
				},
				PostTick: func() {
					node.Bridge.PostSystems()
					if gameHooks.PostTick != nil {
						gameHooks.PostTick()
					}
				},
			}

			gameLoop := engine.NewGameLoop(eng, gameSystems, mergedHooks)
			gameLoop.SetEventsCh(events)
			node.Loop = gameLoop

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
		n := node
		bridge := &nodeBridge{node: n, coord: c}
		n.Bridge = bridge
		n.World.SetBridge(bridge)
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
