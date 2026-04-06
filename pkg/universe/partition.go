package universe

import (
	"context"
	"fmt"
	"sync"
	"time"

	"slices"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/metrics"

	"github.com/mlange-42/ark/ecs"
)

// PartitionConfig configures dynamic cell partitioning behavior.
// Use DefaultPartitionConfig() for sensible defaults.
type PartitionConfig struct {
	// MinCellSize is the smallest allowed cell size. Cells at this size cannot
	// split further. Zero means BaseCellSize / 4 (resolved during Build).
	MinCellSize float32

	// SplitThreshold is the load fraction (0.0–1.0+) above which a cell
	// should split. Default: 0.75.
	SplitThreshold float64

	// MergeThreshold is the load fraction below which cells should merge.
	// All 4 siblings must be below this threshold. Default: 0.20.
	MergeThreshold float64

	// SplitSustain is how long the split threshold must be continuously
	// exceeded before a split is triggered. Default: 30s.
	SplitSustain time.Duration

	// MergeSustain is how long all 4 siblings must be below the merge
	// threshold before a merge is triggered. Default: 60s.
	MergeSustain time.Duration

	// Cooldown is the minimum time between automatic split/merge operations
	// on the same cells. Console commands bypass this. Default: 60s.
	Cooldown time.Duration

	// EvalInterval is how often the partition monitor checks load metrics.
	// Default: 5s.
	EvalInterval time.Duration

	// AutoSplitEnabled controls whether the partition monitor automatically
	// splits cells when load exceeds SplitThreshold. Default: true.
	// Manual splits via console are always allowed regardless of this setting.
	AutoSplitEnabled bool

	// AutoMergeEnabled controls whether the partition monitor automatically
	// merges cells when load drops below MergeThreshold. Default: true.
	// Manual merges via console are always allowed regardless of this setting.
	AutoMergeEnabled bool

	// MetricFunc computes a load score (0.0 = idle, 1.0 = full budget) from
	// a node's load snapshot. Nil uses the default (tick budget usage).
	MetricFunc func(snap metrics.LoadSnapshot) float64

	// OnTopologyChanged is called after each split or merge completes.
	OnTopologyChanged func()
}

// DefaultPartitionConfig returns a PartitionConfig with sensible defaults.
// MinCellSize defaults to 0, which Build() resolves to BaseCellSize / 4.
func DefaultPartitionConfig() *PartitionConfig {
	return &PartitionConfig{
		AutoSplitEnabled: true,
		AutoMergeEnabled: true,
		SplitThreshold:   0.75,
		MergeThreshold:   0.20,
		SplitSustain:     30 * time.Second,
		MergeSustain:     60 * time.Second,
		Cooldown:         60 * time.Second,
		EvalInterval:     5 * time.Second,
	}
}

// partitionState tracks per-cell cooldowns for the partition system.
type partitionState struct {
	mu        sync.Mutex
	cooldowns map[CellID]time.Time // cell -> earliest next auto split/merge
}

func newPartitionState() *partitionState {
	return &partitionState{
		cooldowns: make(map[CellID]time.Time),
	}
}

func (ps *partitionState) onCooldown(cell CellID) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return time.Now().Before(ps.cooldowns[cell])
}

func (ps *partitionState) setCooldown(cell CellID, d time.Duration) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.cooldowns[cell] = time.Now().Add(d)
}

func (ps *partitionState) clearCooldown(cell CellID) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	delete(ps.cooldowns, cell)
}

// SplitCell splits a cell into 4 quadrant sub-cells. The original node keeps
// the heaviest quadrant (most entities). 3 new nodes are created for the others.
// Returns an error if the split is not valid.
//
// If bypassCooldown is true, cooldown checks are skipped (for console commands).
func (c *Coordinator) SplitCell(cell CellID, bypassCooldown bool) error {
	pc := c.cfg.DynamicPartitioning
	if pc == nil {
		return fmt.Errorf("dynamic partitioning is not enabled")
	}

	// Step 1: Validate (read lock for safety)
	c.mu.RLock()
	nodeID, ok := c.NodeOwner[cell]
	var oldNode *Node
	if ok {
		oldNode = c.Nodes[nodeID]
	}
	c.mu.RUnlock()

	if !ok || oldNode == nil {
		return fmt.Errorf("cell %s does not exist", cell)
	}

	cellSize := cell.Size(coords.CellSize)
	if cellSize/2 < pc.MinCellSize {
		return fmt.Errorf("cell %s (size %.0f) cannot split: would be below min size %.0f", cell, cellSize, pc.MinCellSize)
	}

	if !bypassCooldown && c.partState != nil && c.partState.onCooldown(cell) {
		return fmt.Errorf("cell %s is on cooldown", cell)
	}

	children := cell.Children()
	c.Log.Log(CatMeshNode, "coordinator: splitting cell %s into 4 sub-cells", cell)

	// Step 2: On the old node's game loop, serialize all entities and migrate sessions.
	// This runs synchronously on the game loop — no shared state races.
	type entityTransfer struct {
		data       []byte
		netID      uint32
		connID     uint32
		destNodeID string
	}

	transfersCh := make(chan []entityTransfer, 1)
	oldNode.Engine.PendingAdminCmds <- func() {
		posMap := ecs.NewMap1[component.Position](oldNode.Engine.ECS)
		netIDMap := ecs.NewMap1[component.NetworkID](oldNode.Engine.ECS)
		playerMap := ecs.NewMap1[component.PlayerConn](oldNode.Engine.ECS)

		filter := ecs.NewFilter1[component.Position](oldNode.Engine.ECS).
			Without(ecs.C[component.Ghost](), ecs.C[component.Replica](), ecs.C[component.Proxy]())

		var transfers []entityTransfer
		query := filter.Query()
		for query.Next() {
			entity := query.Entity()
			pos := posMap.Get(entity)

			xi := int32(0)
			if pos.X >= cellSize/2 {
				xi = 1
			}
			yi := int32(0)
			if pos.Y >= cellSize/2 {
				yi = 1
			}
			destChild := CellID{X: cell.X*2 + xi, Y: cell.Y*2 + yi, Depth: cell.Depth + 1}

			data, err := oldNode.World.SerializeEntity(entity)
			if err != nil {
				continue
			}

			var netID uint32
			if netIDMap.HasAll(entity) {
				netID = netIDMap.Get(entity).ID
			}
			var connID uint32
			if playerMap.HasAll(entity) {
				connID = playerMap.Get(entity).ConnID
			}

			transfers = append(transfers, entityTransfer{
				data: data, netID: netID, connID: connID,
				destNodeID: MeshNodeID(destChild),
			})

			c.Log.Log(CatMeshNode, "  serialize: netID=%d pos=(%.0f,%.0f) -> %s",
				netID, pos.X, pos.Y, MeshNodeID(destChild))
		}

		// Migrate player sessions on source node
		for _, t := range transfers {
			if t.connID != 0 {
				if sess := oldNode.Engine.Players.ByConnID(t.connID); sess != nil {
					oldNode.Engine.Players.Transition(sess, engine.StateTransferring)
					oldNode.Engine.Players.Remove(sess)
				}
			}
		}

		transfersCh <- transfers
	}

	var transfers []entityTransfer
	select {
	case transfers = <-transfersCh:
	case <-time.After(5 * time.Second):
		return fmt.Errorf("timeout serializing entities on %s during split", nodeID)
	}

	// Step 3: Modify shared state under write lock.
	// All game loops that read Nodes/NodeOwner via the bridge will block on
	// RLock until we're done. This is the atomic topology update.
	c.mu.Lock()

	spatialBucketSize := c.cfg.SpatialBucketSize
	if spatialBucketSize <= 0 {
		spatialBucketSize = coords.CellSize / 10
	}

	// Create 4 new nodes
	for _, child := range children {
		newNode := c.createNode(child, spatialBucketSize)
		c.Log.Log(CatMeshNode, "coordinator: created node %s for sub-cell %s", newNode.ID, child)
	}

	// Remove old cell, update topology
	delete(c.NodeOwner, cell)
	delete(c.Nodes, nodeID)
	c.Topology.UpdateAfterSplit(cell, children, coords.CellSize)
	c.rewireNeighbors()

	// Update player routing
	for _, t := range transfers {
		if t.connID != 0 {
			c.playerNode[t.connID] = t.destNodeID
		}
	}

	// Call Init() on newly created worlds now that bridges and neighbors are wired.
	for _, child := range children {
		childNode := c.Nodes[MeshNodeID(child)]
		childNode.World.Init()
	}

	c.mu.Unlock()

	// Step 4: Start new nodes and send transfers (safe — nodes are in the maps now)
	for _, child := range children {
		childNode := c.Nodes[MeshNodeID(child)]
		go childNode.Run(context.Background())
	}

	// Send serialized entities to new nodes' inboxes
	for _, t := range transfers {
		if dest, ok := c.Nodes[t.destNodeID]; ok {
			dest.Inbox <- NodeMessage{
				Type:          MsgTransfer,
				FromNodeID:    nodeID,
				TransferNetID: t.netID,
				Transfer:      t.data,
			}
		}
	}

	// Step 5: Shut down old node
	oldNode.Shutdown()
	c.netIDAlloc.Release(oldNode.Engine.NetIDBase())

	if c.partState != nil {
		for _, child := range children {
			c.partState.setCooldown(child, pc.Cooldown)
		}
	}

	c.Log.Log(CatMeshNode, "coordinator: split complete — cell %s -> %v", cell, children)

	if pc.OnTopologyChanged != nil {
		pc.OnTopologyChanged()
	}

	return nil
}

// MergeCell merges a cell and its 3 siblings back into their parent cell.
// The sibling with the most entities becomes the survivor; the other 3 drain
// and shut down.
//
// If bypassCooldown is true, cooldown checks are skipped (for console commands).
func (c *Coordinator) MergeCell(cell CellID, bypassCooldown bool) error {
	pc := c.cfg.DynamicPartitioning
	if pc == nil {
		return fmt.Errorf("dynamic partitioning is not enabled")
	}

	if cell.Depth == 0 {
		return fmt.Errorf("cannot merge depth-0 cells")
	}

	siblings := cell.Siblings()

	// Validate under read lock
	c.mu.RLock()
	for _, s := range siblings {
		if _, ok := c.NodeOwner[s]; !ok {
			c.mu.RUnlock()
			return fmt.Errorf("sibling cell %s does not exist — cannot merge partial split", s)
		}
	}
	c.mu.RUnlock()

	if !bypassCooldown && c.partState != nil {
		for _, s := range siblings {
			if c.partState.onCooldown(s) {
				return fmt.Errorf("cell %s is on cooldown", s)
			}
		}
	}

	parent := cell.Parent()
	c.Log.Log(CatMeshNode, "coordinator: merging cells %v into parent %s", siblings, parent)

	// Find survivor (most entities)
	survivorIdx := 0
	maxEntities := 0
	for i, s := range siblings {
		nID := c.getNodeOwner(s)
		if snap, ok := c.NodeLoad(nID); ok {
			total := snap.Entities.Real + snap.Entities.Players
			if total > maxEntities {
				maxEntities = total
				survivorIdx = i
			}
		}
	}

	// Step 2: Drain entities from non-survivor nodes.
	// Run serialization closures on each non-survivor's game loop.
	type entityTransfer struct {
		data   []byte
		netID  uint32
		connID uint32
	}

	nonSurvivorIDs := make([]string, 0, 3)
	for i, s := range siblings {
		if i == survivorIdx {
			continue
		}
		nonSurvivorIDs = append(nonSurvivorIDs, c.getNodeOwner(s))
	}

	allTransfers := make([]entityTransfer, 0)

	for _, nID := range nonSurvivorIDs {
		node := c.Nodes[nID]
		if node == nil {
			continue
		}

		transfersCh := make(chan []entityTransfer, 1)
		node.Engine.PendingAdminCmds <- func() {
			netIDMap := ecs.NewMap1[component.NetworkID](node.Engine.ECS)
			playerMap := ecs.NewMap1[component.PlayerConn](node.Engine.ECS)

			filter := ecs.NewFilter1[component.Position](node.Engine.ECS).
				Without(ecs.C[component.Ghost](), ecs.C[component.Replica](), ecs.C[component.Proxy]())

			var transfers []entityTransfer
			query := filter.Query()
			for query.Next() {
				entity := query.Entity()

				data, err := node.World.SerializeEntity(entity)
				if err != nil {
					continue
				}

				var netID uint32
				if netIDMap.HasAll(entity) {
					netID = netIDMap.Get(entity).ID
				}
				var connID uint32
				if playerMap.HasAll(entity) {
					connID = playerMap.Get(entity).ConnID
				}

				transfers = append(transfers, entityTransfer{
					data: data, netID: netID, connID: connID,
				})

				c.Log.Log(CatMeshNode, "  merge drain: netID=%d from %s", netID, nID)
			}

			// Migrate player sessions on source node
			for _, t := range transfers {
				if t.connID != 0 {
					if sess := node.Engine.Players.ByConnID(t.connID); sess != nil {
						node.Engine.Players.Transition(sess, engine.StateTransferring)
						node.Engine.Players.Remove(sess)
					}
				}
			}

			transfersCh <- transfers
		}

		select {
		case transfers := <-transfersCh:
			allTransfers = append(allTransfers, transfers...)
		case <-time.After(5 * time.Second):
			c.Log.Log(CatMeshNode, "coordinator: timeout draining entities from %s during merge", nID)
		}
	}

	// Step 3: Update topology and routing under write lock.
	c.mu.Lock()

	survivor := c.Nodes[c.NodeOwner[siblings[survivorIdx]]]
	oldSurvivorID := survivor.ID
	newSurvivorID := MeshNodeID(parent)

	// Rename survivor to parent
	delete(c.Nodes, oldSurvivorID)
	delete(c.NodeOwner, siblings[survivorIdx])
	survivor.ID = newSurvivorID
	survivor.Cell = parent
	c.Nodes[newSurvivorID] = survivor
	c.NodeOwner[parent] = newSurvivorID

	if survivor.Metrics != nil {
		survivor.Metrics.SetNodeID(newSurvivorID)
	}

	// Collect non-survivor nodes and remove from maps
	nonSurvivorNodes := make([]*Node, 0, 3)
	for i, s := range siblings {
		if i == survivorIdx {
			continue
		}
		nID := c.NodeOwner[s]
		if node, ok := c.Nodes[nID]; ok {
			nonSurvivorNodes = append(nonSurvivorNodes, node)
			delete(c.Nodes, nID)
		}
		delete(c.NodeOwner, s)
	}

	c.Topology.UpdateAfterMerge(siblings, parent, coords.CellSize)
	c.rewireNeighbors()

	// Remap player routing — survivor's old ID AND all non-survivor players
	for connID, nID := range c.playerNode {
		if nID == oldSurvivorID {
			c.playerNode[connID] = newSurvivorID
			continue
		}
		if slices.Contains(nonSurvivorIDs, nID) {
			c.playerNode[connID] = newSurvivorID
		}
	}

	c.mu.Unlock()

	// Step 4: Update survivor's WorldBase cell identity on its game loop.
	doneCh := make(chan struct{}, 1)
	survivor.Engine.PendingAdminCmds <- func() {
		survivor.World.UpdateCellBounds(parent, coords.CellSize)
		doneCh <- struct{}{}
	}

	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		c.Log.Log(CatMeshNode, "coordinator: timeout updating cell bounds on survivor %s", newSurvivorID)
	}

	// Step 5: Deliver drained entities to survivor's inbox.
	for _, t := range allTransfers {
		survivor.Inbox <- NodeMessage{
			Type:          MsgTransfer,
			FromNodeID:    "merge",
			TransferNetID: t.netID,
			Transfer:      t.data,
		}
	}

	// Step 6: Shut down non-survivor nodes and release resources.
	for _, node := range nonSurvivorNodes {
		node.Shutdown()
		c.netIDAlloc.Release(node.Engine.NetIDBase())
		c.Log.Log(CatMeshNode, "coordinator: shut down merged node %s", node.ID)
	}

	if c.partState != nil {
		c.partState.setCooldown(parent, pc.Cooldown)
		for _, s := range siblings {
			c.partState.clearCooldown(s)
		}
	}

	c.Log.Log(CatMeshNode, "coordinator: merge complete — %v -> %s (transferred %d entities)", siblings, parent, len(allTransfers))

	if pc.OnTopologyChanged != nil {
		pc.OnTopologyChanged()
	}

	return nil
}

// rewireNeighbors rebuilds all Node.Neighbors maps from the current topology.
// Caller must hold c.mu write lock.
func (c *Coordinator) rewireNeighbors() {
	// Clear all neighbor maps
	for _, node := range c.Nodes {
		for k := range node.Neighbors {
			delete(node.Neighbors, k)
		}
	}

	// Rebuild from topology
	for cell, neighborCells := range c.Topology.Neighbors {
		nodeID := c.NodeOwner[cell]
		node := c.Nodes[nodeID]
		if node == nil {
			continue
		}
		for _, nc := range neighborCells {
			neighborID := c.NodeOwner[nc]
			if neighbor, ok := c.Nodes[neighborID]; ok {
				node.Neighbors[neighborID] = neighbor
			}
		}
	}
}
