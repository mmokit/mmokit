# 4-Node Basic Example Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create a minimal 2×2 server mesh example where players connect as colored circles, click-to-move, and a plain HTML Canvas2D client renders rich debug overlays showing cell boundaries, AoI, replicas, ghosts, transfers, and per-node stats.

**Architecture:** Server uses mmokit Coordinator with a 2×2 grid (cell size 2000, AoI 1500). A custom NetworkSystem bypasses the ReplicationSystem and sends full entity state every tick as flat binary. The web client is a single HTML file with inline JS, zero dependencies, hand-coded protobuf encode/decode.

**Tech Stack:** Go (mmokit), Protobuf (buf generate), Plain HTML/JS Canvas2D

**Spec:** `docs/superpowers/specs/2026-03-28-4node-basic-example-design.md`

---

## File Map

```
examples/4node-basic/
├── main.go              # Coordinator, HTTP server, signal handling
├── world.go             # BasicWorld, login handler, spawn, hooks
├── components.go        # PlayerInput + PlayerName components
├── system_input.go      # InputRouter for movement
├── system_movement.go   # Directional movement + friction
├── system_spatial.go    # Spatial grid updates
├── system_network.go    # Custom binary world update sender
├── config.go            # Constants
└── web/
    └── index.html       # Single-file client (Canvas2D + debug overlays)

proto/basicpb/
└── basic.proto          # Game-specific messages

gen/go/basicpb/          # Generated (buf generate)
```

---

### Task 1: Proto File + Codegen

**Files:**
- Create: `proto/basicpb/basic.proto`
- Modify: generated output in `gen/go/basicpb/`

- [ ] **Step 1: Create proto file**

```protobuf
syntax = "proto3";
package basicpb;
option go_package = "github.com/mmokit/mmokit/gen/go/basicpb";

// Client → Server
enum BasicClientEventCode {
    BCE_UNKNOWN = 0;
    BCE_PLAYER_INPUT = 100;
    BCE_LOGIN = 101;
}

message BasicLoginMsg {
    string name = 1;
}

message BasicInputMsg {
    float dir_x = 1;
    float dir_y = 2;
    bool moving = 3;
    uint32 sequence = 4;
}

// Payload for SE_PLAYER_SPAWNED (enginepb code 1)
message BasicSpawnedMsg {
    uint32 entity_net_id = 1;
    int32 cell_x = 2;
    int32 cell_y = 3;
    float cell_size = 4;
    int32 grid_w = 5;
    int32 grid_h = 6;
    float aoi_radius = 7;
}
```

- [ ] **Step 2: Generate Go code**

Run: `make proto`
Expected: `gen/go/basicpb/basic.pb.go` is generated

- [ ] **Step 3: Commit**

```bash
git add proto/basicpb/basic.proto gen/go/basicpb/
git commit -m "feat(4node-basic): add proto definitions"
```

---

### Task 2: Config + Components

**Files:**
- Create: `examples/4node-basic/config.go`
- Create: `examples/4node-basic/components.go`

- [ ] **Step 1: Create config.go**

```go
package main

const (
	GridCellSize  float32 = 2000.0
	AoIRadius     float32 = 1500.0
	TickRate      int     = 20
	MoveSpeed     float32 = 300.0
	Friction      float32 = 0.92 // velocity decay per tick when not moving
	PlayerRadius  float32 = 20.0
	GridMinSX     int32   = 0
	GridMaxSX     int32   = 1
	GridMinSY     int32   = 0
	GridMaxSY     int32   = 1
	SpatialCellSz float32 = 200.0
	KindPlayer    uint8   = 1
)
```

- [ ] **Step 2: Create components.go**

```go
package main

// PlayerInput stores the current directional input for a player entity.
type PlayerInput struct {
	DirX, DirY float32
	Moving     bool
	Sequence   uint32
}

// PlayerName stores a player's display name (replicated to other nodes).
type PlayerName struct {
	Name string
}
```

- [ ] **Step 3: Commit**

```bash
git add examples/4node-basic/config.go examples/4node-basic/components.go
git commit -m "feat(4node-basic): add config and components"
```

---

### Task 3: World Setup

**Files:**
- Create: `examples/4node-basic/world.go`

This is the core file: BasicWorld struct, player spawn, login handler, state callbacks, transfer hooks, replication registry.

- [ ] **Step 1: Create world.go**

```go
package main

import (
	"fmt"
	"math/rand"
	"strings"

	"github.com/mlange-42/ark/ecs"
	enginepb "github.com/mmokit/mmokit/gen/go/enginepb"
	basicpb "github.com/mmokit/mmokit/gen/go/basicpb"
	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/mmokit"
	"github.com/mmokit/mmokit/pkg/universe"
	"google.golang.org/protobuf/proto"
)

// BasicWorld is the game world for a single node in the 4-node basic example.
type BasicWorld struct {
	mmokit.WorldBase

	Grid     *mmokit.Grid
	InputMap *ecs.Map1[PlayerInput]
	ConnMap  *ecs.Map1[mmokit.PlayerConn]
	NameMap  *ecs.Map1[PlayerName]
}

// NewBasicWorld creates a BasicWorld for a node.
func NewBasicWorld(base *mmokit.WorldBase) *BasicWorld {
	w := base.ECSWorld()

	gw := &BasicWorld{
		WorldBase: *base,
		Grid:      mmokit.NewGrid(SpatialCellSz),
		InputMap:  ecs.NewMap1[PlayerInput](w),
		ConnMap:   ecs.NewMap1[mmokit.PlayerConn](w),
		NameMap:   ecs.NewMap1[PlayerName](w),
	}

	base.Engine().OnEntityRemoved = func(e mmokit.Entity) {
		gw.Grid.Deregister(e)
	}

	// --- Replication registry (PlayerName only) ---
	reg := mmokit.NewReplicationRegistry()
	reg.Register(mmokit.ComponentReplicator{
		ID: 1,
		Scan: func(e ecs.Entity) []byte {
			if !gw.NameMap.HasAll(e) {
				return nil
			}
			name := gw.NameMap.Get(e).Name
			b := []byte(name)
			if len(b) > 255 {
				b = b[:255]
			}
			buf := make([]byte, 1+len(b))
			buf[0] = byte(len(b))
			copy(buf[1:], b)
			return buf
		},
		Apply: func(e ecs.Entity, data []byte) {
			if len(data) < 1 {
				return
			}
			n := int(data[0])
			if 1+n > len(data) {
				return
			}
			if gw.NameMap.HasAll(e) {
				gw.NameMap.Get(e).Name = string(data[1 : 1+n])
			}
		},
		Add: func(e ecs.Entity, data []byte) {
			if len(data) < 1 {
				return
			}
			n := int(data[0])
			if 1+n > len(data) {
				return
			}
			gw.NameMap.Add(e, &PlayerName{Name: string(data[1 : 1+n])})
		},
	})
	gw.SetReplicationRegistry(reg)

	// --- Login handler ---
	pm := base.Engine().Players
	pm.SetLoginHandler(func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) error {
		msgs := pm.Engine().ConnMgr.DrainInput(s.ConnID)
		for _, data := range msgs {
			var evt enginepb.ClientEvent
			if err := proto.Unmarshal(data, &evt); err != nil {
				continue
			}
			if evt.Code == uint32(basicpb.BasicClientEventCode_BCE_LOGIN) {
				var login basicpb.BasicLoginMsg
				if err := proto.Unmarshal(evt.Data, &login); err != nil {
					continue
				}
				name := strings.ToLower(strings.TrimSpace(login.Name))
				if name == "" || len(name) > 20 {
					continue
				}
				s.Username = name
				return nil
			}
		}
		return mmokit.ErrLoginPending
	})

	// --- State callbacks ---
	pm.OnState(mmokit.StateActive, mmokit.StateCallbacks{
		OnEnter: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
			s.Entity = gw.spawnPlayer(s.ConnID, s.Username)
			gw.sendSpawnedMsg(s.ConnID, s.Entity)
		},
		OnExit: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
			if s.Entity != (ecs.Entity{}) && gw.ECSWorld().Alive(s.Entity) {
				gw.MarkForRemoval(s.Entity)
				s.Entity = ecs.Entity{}
			}
		},
	})

	// --- Transfer hooks ---
	gw.SetOnTransferReceived(func(entity ecs.Entity, frame *mmokit.TransferFrame) {
		if !gw.InputMap.HasAll(entity) {
			gw.InputMap.Add(entity, &PlayerInput{})
		}
		if frame.ConnID != 0 && !gw.ConnMap.HasAll(entity) {
			gw.ConnMap.Add(entity, &mmokit.PlayerConn{ConnID: frame.ConnID})
		}
	})

	gw.SetOnPlayerTransferReceived(func(entity ecs.Entity, frame *mmokit.TransferFrame) {
		if s := gw.Engine().Players.ByConnID(frame.ConnID); s != nil {
			s.Entity = entity
		}
		gw.sendSpawnedMsg(frame.ConnID, entity)
	})

	return gw
}

func (gw *BasicWorld) Hooks() engine.Hooks {
	return engine.Hooks{}
}

// spawnPlayer creates a player circle entity at a random position within the cell.
func (gw *BasicWorld) spawnPlayer(connID uint32, username string) ecs.Entity {
	cellSize := mmokit.CellSize()
	x := cellSize*0.2 + rand.Float32()*cellSize*0.6
	y := cellSize*0.2 + rand.Float32()*cellSize*0.6

	entity := gw.SpawnEntity(
		mmokit.Position{X: x, Y: y},
		mmokit.WithCollider(PlayerRadius),
		mmokit.WithEntityKind(KindPlayer),
	)

	gw.ConnMap.Add(entity, &mmokit.PlayerConn{ConnID: connID})
	gw.InputMap.Add(entity, &PlayerInput{})
	gw.NameMap.Add(entity, &PlayerName{Name: username})

	gw.Grid.Register(mmokit.SpatialEntry{
		Entity: entity,
		X:      x,
		Y:      y,
		Radius: PlayerRadius,
	})

	return entity
}

// sendSpawnedMsg tells the client its entity ID and grid metadata.
func (gw *BasicWorld) sendSpawnedMsg(connID uint32, entity ecs.Entity) {
	cell := gw.Cell()
	netID := uint32(0)
	if gw.NetworkIDMap().HasAll(entity) {
		netID = gw.NetworkIDMap().Get(entity).ID
	}
	msg := &basicpb.BasicSpawnedMsg{
		EntityNetId: netID,
		CellX:       int32(cell.SX),
		CellY:       int32(cell.SY),
		CellSize:    mmokit.CellSize(),
		GridW:       GridMaxSX - GridMinSX + 1,
		GridH:       GridMaxSY - GridMinSY + 1,
		AoiRadius:   AoIRadius,
	}
	frame := mmokit.MakeEvent(uint32(enginepb.ServerEventCode_SE_PLAYER_SPAWNED), msg)
	gw.Engine().ConnMgr.Send(connID, frame)
}

// nodeIndexFromCell maps cell coordinates to a 0-based node index.
func nodeIndexFromCell(sx, sy int32) uint8 {
	gridW := int(GridMaxSX - GridMinSX + 1)
	return uint8(int(sy-GridMinSY)*gridW + int(sx-GridMinSX))
}

// parseNodeIndex extracts cell coords from a nodeID like "node_1_0" and returns the index.
func parseNodeIndex(nodeID string) uint8 {
	var sx, sy int32
	fmt.Sscanf(nodeID, "node_%d_%d", &sx, &sy)
	return nodeIndexFromCell(sx, sy)
}
```

- [ ] **Step 2: Verify compilation**

Run: `go vet ./examples/4node-basic/...`
Expected: may fail because main.go doesn't exist yet — that's expected

- [ ] **Step 3: Commit**

```bash
git add examples/4node-basic/world.go
git commit -m "feat(4node-basic): add BasicWorld with login, spawn, transfer hooks"
```

---

### Task 4: Input System

**Files:**
- Create: `examples/4node-basic/system_input.go`

- [ ] **Step 1: Create system_input.go**

```go
package main

import (
	basicpb "github.com/mmokit/mmokit/gen/go/basicpb"
	"github.com/mmokit/mmokit/pkg/mmokit"
)

func registerInputHandlers(eng *mmokit.Engine, gw *BasicWorld) *mmokit.InputRouter {
	router := mmokit.NewInputRouter(eng, mmokit.ProtoEnvelopeParser)

	router.StateFilter(mmokit.StateActive, func(ctx *mmokit.InputContext) bool {
		return gw.ECSWorld().Alive(ctx.Entity)
	})

	mmokit.HandleProto[basicpb.BasicInputMsg](router,
		uint32(basicpb.BasicClientEventCode_BCE_PLAYER_INPUT),
		mmokit.States(mmokit.StateActive),
		func(ctx *mmokit.InputContext, msg *basicpb.BasicInputMsg) {
			if !gw.InputMap.HasAll(ctx.Entity) {
				return
			}
			input := gw.InputMap.Get(ctx.Entity)
			input.DirX = msg.DirX
			input.DirY = msg.DirY
			input.Moving = msg.Moving
			input.Sequence = msg.Sequence
		})

	return router
}
```

- [ ] **Step 2: Commit**

```bash
git add examples/4node-basic/system_input.go
git commit -m "feat(4node-basic): add input handler for player movement"
```

---

### Task 5: Movement System

**Files:**
- Create: `examples/4node-basic/system_movement.go`

- [ ] **Step 1: Create system_movement.go**

```go
package main

import (
	"math"

	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/mmokit"
)

type MovementSystem struct {
	filter *ecs.Filter3[mmokit.Position, mmokit.Velocity, PlayerInput]
}

func NewMovementSystem(w *ecs.World) *MovementSystem {
	return &MovementSystem{
		filter: ecs.NewFilter3[mmokit.Position, mmokit.Velocity, PlayerInput](w).
			Without(ecs.C[mmokit.Ghost](), ecs.C[mmokit.Replica]()),
	}
}

func (s *MovementSystem) Name() string { return "Movement" }

func (s *MovementSystem) Update(dt float32) {
	query := s.filter.Query()
	for query.Next() {
		_, vel, input := query.Get()
		if input.Moving {
			vel.X = input.DirX * MoveSpeed
			vel.Y = input.DirY * MoveSpeed
		} else {
			vel.X *= Friction
			vel.Y *= Friction
			if math.Abs(float64(vel.X)) < 0.5 {
				vel.X = 0
			}
			if math.Abs(float64(vel.Y)) < 0.5 {
				vel.Y = 0
			}
		}
	}
}
```

- [ ] **Step 2: Commit**

```bash
git add examples/4node-basic/system_movement.go
git commit -m "feat(4node-basic): add movement system with friction"
```

---

### Task 6: Spatial System

**Files:**
- Create: `examples/4node-basic/system_spatial.go`

- [ ] **Step 1: Create system_spatial.go**

Registers/updates all entities (including replicas and ghosts) in the spatial grid for AoI queries.

```go
package main

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/mmokit"
)

type SpatialSystem struct {
	gw     *BasicWorld
	filter *ecs.Filter3[mmokit.Position, mmokit.Collider, mmokit.NetworkID]
}

func NewSpatialSystem(gw *BasicWorld) *SpatialSystem {
	return &SpatialSystem{
		gw:     gw,
		filter: ecs.NewFilter3[mmokit.Position, mmokit.Collider, mmokit.NetworkID](gw.ECSWorld()),
	}
}

func (s *SpatialSystem) Name() string { return "Spatial" }

func (s *SpatialSystem) Update(dt float32) {
	query := s.filter.Query()
	for query.Next() {
		pos, col, _ := query.Get()
		entity := query.Entity()
		entry := mmokit.SpatialEntry{
			Entity: entity,
			X:      pos.X,
			Y:      pos.Y,
			Radius: col.Radius,
		}
		if s.gw.Grid.IsRegistered(entity) {
			s.gw.Grid.Update(entry)
		} else {
			s.gw.Grid.Register(entry)
		}
	}
}
```

- [ ] **Step 2: Commit**

```bash
git add examples/4node-basic/system_spatial.go
git commit -m "feat(4node-basic): add spatial system for AoI queries"
```

---

### Task 7: Network System

**Files:**
- Create: `examples/4node-basic/system_network.go`

Custom NetworkSystem that sends full entity state as flat binary every tick. No delta encoding — the simplest possible approach.

- [ ] **Step 1: Create system_network.go**

```go
package main

import (
	"encoding/binary"
	"math"

	"github.com/mlange-42/ark/ecs"
	enginepb "github.com/mmokit/mmokit/gen/go/enginepb"
	"github.com/mmokit/mmokit/pkg/mmokit"
)

// Binary frame layout:
//
//   Header (20 bytes, big-endian):
//     [4] tick           uint32
//     [4] viewerX        float32  (world-absolute)
//     [4] viewerY        float32  (world-absolute)
//     [2] entityCount    uint16
//     [2] removedCount   uint16
//     [1] nodeIndex      uint8    (viewer's node 0-3)
//     [1] nodeEntities   uint8
//     [1] nodePlayers    uint8
//     [1] _pad           uint8
//
//   Per entity (28 + nameLen):
//     [4] netID          uint32
//     [1] entityType     uint8
//     [1] flags          uint8  (bit0=replica, bit1=ghost)
//     [4] x              float32
//     [4] y              float32
//     [4] vx             float32
//     [4] vy             float32
//     [4] radius         float32
//     [1] ownerNodeIdx   uint8
//     [1] nameLen        uint8
//     [N] name           bytes
//
//   Removed IDs: [4]*removedCount uint32

type NetworkSystem struct {
	gw         *BasicWorld
	ghostMap   *ecs.Map1[mmokit.Ghost]
	replicaMap *ecs.Map1[mmokit.Replica]
	cellMap    *ecs.Map1[mmokit.CellCoord]
	lastSeen   map[uint32]map[uint32]bool // connID -> set of visible netIDs
	results    []mmokit.SpatialEntry
	buf        []byte
}

func NewNetworkSystem(gw *BasicWorld) *NetworkSystem {
	w := gw.ECSWorld()
	return &NetworkSystem{
		gw:         gw,
		ghostMap:   ecs.NewMap1[mmokit.Ghost](w),
		replicaMap: ecs.NewMap1[mmokit.Replica](w),
		cellMap:    ecs.NewMap1[mmokit.CellCoord](w),
		lastSeen:   make(map[uint32]map[uint32]bool),
		buf:        make([]byte, 0, 8192),
	}
}

func (s *NetworkSystem) Name() string { return "Network" }

func (s *NetworkSystem) Update(dt float32) {
	tick := s.gw.Engine().Tick
	viewerCell := s.gw.Cell()
	cellSize := mmokit.CellSize()
	worldOffX := float32(viewerCell.SX) * cellSize
	worldOffY := float32(viewerCell.SY) * cellSize
	myNodeIdx := nodeIndexFromCell(viewerCell.SX, viewerCell.SY)

	// Count entities and players on this node.
	nodeEntities := 0
	nodePlayers := 0
	countFilter := ecs.NewFilter1[mmokit.NetworkID](s.gw.ECSWorld()).
		Without(ecs.C[mmokit.Replica](), ecs.C[mmokit.Ghost]())
	cq := countFilter.Query()
	for cq.Next() {
		nodeEntities++
		e := cq.Entity()
		if s.gw.ConnMap.HasAll(e) {
			nodePlayers++
		}
	}

	s.gw.Engine().Players.ForEach(mmokit.StateActive, func(sess *mmokit.PlayerSession) {
		entity := sess.Entity
		if entity == (mmokit.Entity{}) || !s.gw.ECSWorld().Alive(entity) {
			return
		}
		if s.ghostMap.HasAll(entity) {
			return
		}
		if !s.gw.PositionMap().HasAll(entity) {
			return
		}

		pos := s.gw.PositionMap().Get(entity)
		viewerWorldX := pos.X + worldOffX
		viewerWorldY := pos.Y + worldOffY

		s.results = s.gw.Grid.QueryRadius(pos.X, pos.Y, AoIRadius, s.results[:0])

		currentSeen := make(map[uint32]bool, len(s.results))

		// Reset buffer, write header placeholder.
		s.buf = s.buf[:0]
		s.buf = append(s.buf, make([]byte, 20)...)

		entityCount := uint16(0)

		for _, entry := range s.results {
			e := entry.Entity
			if !s.gw.ECSWorld().Alive(e) {
				continue
			}
			if !s.gw.NetworkIDMap().HasAll(e) {
				continue
			}
			netID := s.gw.NetworkIDMap().Get(e).ID
			currentSeen[netID] = true

			flags := uint8(0)
			if s.replicaMap.HasAll(e) {
				flags |= 0x01
			}
			if s.ghostMap.HasAll(e) {
				flags |= 0x02
			}

			epos := s.gw.PositionMap().Get(e)
			wx := epos.X + worldOffX
			wy := epos.Y + worldOffY

			var vx, vy float32
			if s.gw.VelocityMap().HasAll(e) {
				v := s.gw.VelocityMap().Get(e)
				vx, vy = v.X, v.Y
			}

			radius := float32(0)
			if s.gw.ColliderMap().HasAll(e) {
				radius = s.gw.ColliderMap().Get(e).Radius
			}

			ownerIdx := myNodeIdx
			if s.replicaMap.HasAll(e) {
				rep := s.replicaMap.Get(e)
				ownerIdx = parseNodeIndex(rep.SourceNodeID)
			}

			name := ""
			if s.gw.NameMap.HasAll(e) {
				name = s.gw.NameMap.Get(e).Name
			}
			nameBytes := []byte(name)
			if len(nameBytes) > 255 {
				nameBytes = nameBytes[:255]
			}

			// Append entity record.
			rec := make([]byte, 28+len(nameBytes))
			binary.BigEndian.PutUint32(rec[0:], netID)
			rec[4] = KindPlayer
			rec[5] = flags
			putF32(rec[6:], wx)
			putF32(rec[10:], wy)
			putF32(rec[14:], vx)
			putF32(rec[18:], vy)
			putF32(rec[22:], radius)
			rec[26] = ownerIdx
			rec[27] = uint8(len(nameBytes))
			copy(rec[28:], nameBytes)
			s.buf = append(s.buf, rec...)
			entityCount++
		}

		// Removed entities.
		lastSeen := s.lastSeen[sess.ConnID]
		removedCount := uint16(0)
		for netID := range lastSeen {
			if !currentSeen[netID] {
				tmp := make([]byte, 4)
				binary.BigEndian.PutUint32(tmp, netID)
				s.buf = append(s.buf, tmp...)
				removedCount++
			}
		}

		// Fill header.
		binary.BigEndian.PutUint32(s.buf[0:], tick)
		putF32(s.buf[4:], viewerWorldX)
		putF32(s.buf[8:], viewerWorldY)
		binary.BigEndian.PutUint16(s.buf[12:], entityCount)
		binary.BigEndian.PutUint16(s.buf[14:], removedCount)
		s.buf[16] = myNodeIdx
		s.buf[17] = clampU8(nodeEntities)
		s.buf[18] = clampU8(nodePlayers)
		s.buf[19] = 0

		s.lastSeen[sess.ConnID] = currentSeen

		frame := mmokit.MakeEventRaw(uint32(enginepb.ServerEventCode_SE_DELTA_WORLD_UPDATE), s.buf)
		s.gw.Engine().ConnMgr.Send(sess.ConnID, frame)
	})

	// Clean up disconnected viewers.
	for connID := range s.lastSeen {
		if s.gw.Engine().Players.ByConnID(connID) == nil {
			delete(s.lastSeen, connID)
		}
	}
}

func putF32(b []byte, v float32) {
	binary.BigEndian.PutUint32(b, math.Float32bits(v))
}

func clampU8(v int) uint8 {
	if v > 255 {
		return 255
	}
	return uint8(v)
}
```

- [ ] **Step 2: Commit**

```bash
git add examples/4node-basic/system_network.go
git commit -m "feat(4node-basic): add custom binary network system"
```

---

### Task 8: Main Entry Point

**Files:**
- Create: `examples/4node-basic/main.go`

- [ ] **Step 1: Create main.go**

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/mmokit/mmokit/pkg/mmokit"
)

func main() {
	port := flag.Int("port", 8080, "HTTP server port")
	flag.Parse()

	mmokit.SetCellSize(GridCellSize)

	cm := mmokit.NewConnManager()
	logger := mmokit.NewLogger()

	coord := mmokit.NewCoordinator(
		mmokit.GridConfig{
			MinSX: GridMinSX, MaxSX: GridMaxSX,
			MinSY: GridMinSY, MaxSY: GridMaxSY,
		},
		mmokit.EngineConfig{TickRate: TickRate},
		func(base *mmokit.WorldBase) (mmokit.GameWorld, []mmokit.System) {
			gw := NewBasicWorld(base)

			systems := []mmokit.System{
				registerInputHandlers(base.Engine(), gw),
				NewMovementSystem(gw.ECSWorld()),
				mmokit.NewPhysicsSystem(gw.ECSWorld()),
				NewSpatialSystem(gw),
				NewNetworkSystem(gw),
			}
			return gw, systems
		},
		mmokit.WithConnManager(cm),
		mmokit.WithLogger(logger),
		mmokit.WithAoIRadius(AoIRadius),
	)

	ctx, cancel := context.WithCancel(context.Background())
	coord.Start(ctx)

	gridW := GridMaxSX - GridMinSX + 1
	gridH := GridMaxSY - GridMinSY + 1

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", cm.HandleWebSocket)
	webDir := "web"
	mux.Handle("/", http.FileServer(http.Dir(webDir)))

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("4node-basic starting on http://localhost%s", addr)
	log.Printf("grid: %dx%d nodes, cell size: %.0f, AoI: %.0f", gridW, gridH, GridCellSize, AoIRadius)

	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("FATAL: http server: %v", err)
			os.Exit(1)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("shutting down...")
	cancel()
	coord.Shutdown()
}
```

- [ ] **Step 2: Verify compilation**

Run: `go vet ./examples/4node-basic/...`
Expected: PASS (all files compile)

- [ ] **Step 3: Commit**

```bash
git add examples/4node-basic/main.go
git commit -m "feat(4node-basic): add server entry point"
```

---

### Task 9: Web Client

**Files:**
- Create: `examples/4node-basic/web/index.html`

Single HTML file with all JS inline. Sections: login UI, minimal protobuf encode/decode, WebSocket, binary world update decoder, game state + interpolation, input handling, Canvas2D renderer with all debug overlays.

- [ ] **Step 1: Create web/index.html**

This is the complete client. Due to length, key sections are annotated:

```html
<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>4-Node Basic — MMOKit Demo</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{background:#111;color:#eee;font-family:'Courier New',monospace;overflow:hidden}
canvas{display:block}
#login{position:absolute;top:50%;left:50%;transform:translate(-50%,-50%);
  background:#1a1a2e;padding:30px;border-radius:12px;text-align:center;border:1px solid #333}
#login h2{margin-bottom:15px;color:#7fdbca;font-size:18px}
#login input{padding:10px;font-size:14px;background:#0d0d1a;border:1px solid #444;
  color:#eee;border-radius:6px;width:200px;font-family:inherit}
#login button{padding:10px 24px;font-size:14px;background:#2d6a4f;color:#fff;
  border:none;border-radius:6px;cursor:pointer;margin-top:12px;font-family:inherit}
#login button:hover{background:#40916c}
#hud{position:absolute;top:10px;left:10px;font-size:11px;line-height:1.7;
  background:rgba(0,0,0,0.6);padding:8px 12px;border-radius:6px;pointer-events:none}
#stats{position:absolute;top:10px;right:10px;font-size:11px;line-height:1.7;
  text-align:right;background:rgba(0,0,0,0.6);padding:8px 12px;border-radius:6px;pointer-events:none}
#legend{position:absolute;bottom:10px;left:10px;font-size:11px;line-height:1.7;
  background:rgba(0,0,0,0.6);padding:8px 12px;border-radius:6px;pointer-events:none}
</style>
</head>
<body>
<canvas id="c"></canvas>
<div id="login">
  <h2>4-Node Basic &mdash; MMOKit Demo</h2>
  <input id="nameIn" type="text" placeholder="Enter name" maxlength="20" autofocus>
  <br>
  <button id="goBtn">Connect</button>
</div>
<div id="hud" style="display:none"></div>
<div id="stats" style="display:none"></div>
<div id="legend" style="display:none"></div>

<script>
// ============================================================
// Minimal Protobuf Encode/Decode (handles only what we need)
// ============================================================
function encVarint(v){const b=[];v=v>>>0;while(v>0x7f){b.push((v&0x7f)|0x80);v>>>=7}b.push(v&0x7f);return b}
function encTag(fn,wt){return encVarint((fn<<3)|wt)}
function encUint32(fn,v){if(!v)return[];return[...encTag(fn,0),...encVarint(v)]}
function encBool(fn,v){if(!v)return[];return[...encTag(fn,0),1]}
function encBytes(fn,b){if(!b.length)return[];return[...encTag(fn,2),...encVarint(b.length),...b]}
function encString(fn,s){return encBytes(fn,[...new TextEncoder().encode(s)])}
function encFloat(fn,v){if(v===0)return[];const ab=new ArrayBuffer(4);new DataView(ab).setFloat32(0,v,true);return[...encTag(fn,5),...new Uint8Array(ab)]}

function decVarint(d,o){let v=0,s=0,p=o;while(p<d.length){const b=d[p];v|=(b&0x7f)<<s;p++;if(!(b&0x80))break;s+=7}return{v:v>>>0,n:p-o}}
function decMsg(d){const f={};let p=0;while(p<d.length){const t=decVarint(d,p);p+=t.n;const fn=t.v>>>3,wt=t.v&7;if(wt===0){const r=decVarint(d,p);p+=r.n;f[fn]=r.v}else if(wt===2){const r=decVarint(d,p);p+=r.n;f[fn]=d.slice(p,p+r.v);p+=r.v}else if(wt===5){const dv=new DataView(d.buffer,d.byteOffset+p,4);f[fn]=dv.getFloat32(0,true);p+=4}else break}return f}

function encClientEvent(code,data){return new Uint8Array([...encUint32(1,code),...encBytes(2,data)])}
function decServerEvent(d){const f=decMsg(d);return{code:f[1]||0,data:f[2]||new Uint8Array(0)}}
function encLogin(name){return new Uint8Array([...encString(1,name)])}
function encInput(dx,dy,moving,seq){return new Uint8Array([...encFloat(1,dx),...encFloat(2,dy),...encBool(3,moving),...encUint32(4,seq)])}
function decSpawned(d){const f=decMsg(d);return{id:f[1]||0,cx:f[2]||0,cy:f[3]||0,cs:f[4]||0,gw:f[5]||0,gh:f[6]||0,aoi:f[7]||0}}

// ============================================================
// Constants
// ============================================================
const SE_PLAYER_SPAWNED = 1;
const SE_DELTA_WORLD_UPDATE = 13;
const BCE_LOGIN = 101;
const BCE_PLAYER_INPUT = 100;
const NODE_COLORS = [
  {bg:'rgba(100,150,255,0.07)',fill:'#5588cc',stroke:'#6496FF',name:'Node 0 (0,0)'},
  {bg:'rgba(255,150,100,0.07)',fill:'#cc8855',stroke:'#FF9664',name:'Node 1 (1,0)'},
  {bg:'rgba(100,255,150,0.07)',fill:'#55cc88',stroke:'#64FF96',name:'Node 2 (0,1)'},
  {bg:'rgba(255,100,255,0.07)',fill:'#cc55cc',stroke:'#FF64FF',name:'Node 3 (1,1)'},
];

// ============================================================
// State
// ============================================================
const S = {
  ws:null, connected:false, myId:0,
  cellX:0, cellY:0, cellSize:2000, gridW:2, gridH:2, aoiRadius:1500,
  tick:0, nodeIdx:0, nodeEnts:0, nodePlayers:0,
  entities:new Map(), removed:[],
  lastTickTime:0, inputSeq:0, fps:0, frames:0, fpsTime:0,
};

// ============================================================
// WebSocket
// ============================================================
function connect(name) {
  const proto = location.protocol==='https:'?'wss:':'ws:';
  S.ws = new WebSocket(`${proto}//${location.host}/ws`);
  S.ws.binaryType = 'arraybuffer';
  S.ws.onopen = () => {
    const inner = encLogin(name);
    const evt = encClientEvent(BCE_LOGIN, [...inner]);
    const frame = new Uint8Array(1+evt.length);
    frame[0] = 0x00;
    frame.set(evt, 1);
    S.ws.send(frame);
  };
  S.ws.onclose = () => { S.connected=false; document.getElementById('login').style.display=''; };
  S.ws.onmessage = (e) => {
    if(!(e.data instanceof ArrayBuffer))return;
    const raw = new Uint8Array(e.data);
    if(raw.length<2||raw[0]!==0x00)return;
    const sevt = decServerEvent(raw.subarray(1));
    handleServerEvent(sevt);
  };
}

function sendInput(dx,dy,moving) {
  if(!S.ws||S.ws.readyState!==1)return;
  const inner = encInput(dx,dy,moving,S.inputSeq++);
  const evt = encClientEvent(BCE_PLAYER_INPUT,[...inner]);
  const frame = new Uint8Array(1+evt.length);
  frame[0]=0x00; frame.set(evt,1);
  S.ws.send(frame);
}

function handleServerEvent(sevt) {
  if(sevt.code===SE_PLAYER_SPAWNED) {
    const sp = decSpawned(sevt.data);
    S.myId=sp.id; S.cellX=sp.cx; S.cellY=sp.cy;
    S.cellSize=sp.cs; S.gridW=sp.gw; S.gridH=sp.gh; S.aoiRadius=sp.aoi;
    S.connected=true;
    document.getElementById('login').style.display='none';
    document.getElementById('hud').style.display='';
    document.getElementById('stats').style.display='';
    document.getElementById('legend').style.display='';
  } else if(sevt.code===SE_DELTA_WORLD_UPDATE) {
    decodeWorldUpdate(sevt.data);
  }
}

// ============================================================
// Binary World Update Decoder
// ============================================================
function decodeWorldUpdate(d) {
  const v = new DataView(d.buffer, d.byteOffset, d.byteLength);
  let p=0;
  S.tick = v.getUint32(p); p+=4;
  const vx = v.getFloat32(p); p+=4;
  const vy = v.getFloat32(p); p+=4;
  const entCount = v.getUint16(p); p+=2;
  const remCount = v.getUint16(p); p+=2;
  S.nodeIdx = d[p]; p++;
  S.nodeEnts = d[p]; p++;
  S.nodePlayers = d[p]; p++;
  p++; // pad

  const seen = new Set();
  for(let i=0;i<entCount;i++){
    const netID = v.getUint32(p); p+=4;
    const type = d[p]; p++;
    const flags = d[p]; p++;
    const x = v.getFloat32(p); p+=4;
    const y = v.getFloat32(p); p+=4;
    const evx = v.getFloat32(p); p+=4;
    const evy = v.getFloat32(p); p+=4;
    const radius = v.getFloat32(p); p+=4;
    const ownerIdx = d[p]; p++;
    const nameLen = d[p]; p++;
    let name = '';
    if(nameLen>0){ name = new TextDecoder().decode(d.subarray(p,p+nameLen)); p+=nameLen; }

    seen.add(netID);
    let ent = S.entities.get(netID);
    if(!ent){
      ent = {x,y,prevX:x,prevY:y,vx:evx,vy:evy,radius,flags,ownerIdx,name,type,
             renderX:x,renderY:y,prevFlags:flags,flashTime:0};
      S.entities.set(netID, ent);
    } else {
      ent.prevX=ent.x; ent.prevY=ent.y;
      ent.prevFlags=ent.flags;
      ent.x=x; ent.y=y; ent.vx=evx; ent.vy=evy;
      ent.radius=radius; ent.flags=flags; ent.ownerIdx=ownerIdx;
      if(name) ent.name=name; ent.type=type;
      // Flash on flag change (transfer detected)
      if(ent.prevFlags!==flags) ent.flashTime=performance.now();
    }
  }

  // Removed
  for(let i=0;i<remCount;i++){
    const id=v.getUint32(p); p+=4;
    S.entities.delete(id);
  }
  // Remove entities not in this update (left AoI)
  for(const [id] of S.entities){
    if(!seen.has(id)&&id!==S.myId) S.entities.delete(id);
  }

  S.lastTickTime = performance.now();
}

// ============================================================
// Input
// ============================================================
let mouseDown=false, mouseX=0, mouseY=0;
const canvas = document.getElementById('c');
canvas.addEventListener('mousedown',(e)=>{mouseDown=true;mouseX=e.clientX;mouseY=e.clientY});
canvas.addEventListener('mouseup',()=>{mouseDown=false});
canvas.addEventListener('mousemove',(e)=>{mouseX=e.clientX;mouseY=e.clientY});
canvas.addEventListener('contextmenu',(e)=>e.preventDefault());

setInterval(()=>{
  if(!S.connected||!S.myId)return;
  const me=S.entities.get(S.myId);
  if(!me)return;
  let dx=0,dy=0,moving=false;
  if(mouseDown){
    const w=screenToWorld(mouseX,mouseY);
    const ddx=w.x-me.renderX, ddy=w.y-me.renderY;
    const len=Math.sqrt(ddx*ddx+ddy*ddy);
    if(len>5){dx=ddx/len;dy=ddy/len;moving=true;}
  }
  sendInput(dx,dy,moving);
},50);

// ============================================================
// Camera
// ============================================================
const cam={x:0,y:0,scale:1};
function updateCamera(){
  const me=S.entities.get(S.myId);
  if(!me)return;
  cam.x=me.renderX; cam.y=me.renderY;
  cam.scale=Math.min(canvas.width,canvas.height)/3500;
}
function worldToScreen(wx,wy){
  return{x:(wx-cam.x)*cam.scale+canvas.width/2, y:(wy-cam.y)*cam.scale+canvas.height/2};
}
function screenToWorld(sx,sy){
  return{x:(sx-canvas.width/2)/cam.scale+cam.x, y:(sy-canvas.height/2)/cam.scale+cam.y};
}

// ============================================================
// Renderer
// ============================================================
const ctx = canvas.getContext('2d');

function resize(){
  canvas.width=window.innerWidth; canvas.height=window.innerHeight;
}
window.addEventListener('resize',resize); resize();

function render() {
  requestAnimationFrame(render);
  if(!S.connected){ctx.fillStyle='#111';ctx.fillRect(0,0,canvas.width,canvas.height);return;}

  // FPS
  S.frames++;
  const now=performance.now();
  if(now-S.fpsTime>=1000){S.fps=S.frames;S.frames=0;S.fpsTime=now;}

  // Interpolation: extrapolate from last tick
  const tickMs=50; // 20Hz
  const t=Math.min((now-S.lastTickTime)/tickMs, 2.0);
  for(const ent of S.entities.values()){
    ent.renderX = ent.x + ent.vx * (t * tickMs/1000);
    ent.renderY = ent.y + ent.vy * (t * tickMs/1000);
  }

  updateCamera();

  const W = S.cellSize * S.gridW;
  const H = S.cellSize * S.gridH;

  ctx.fillStyle='#0a0a14';
  ctx.fillRect(0,0,canvas.width,canvas.height);

  // --- Node ownership background tints ---
  for(let cy=0;cy<S.gridH;cy++){
    for(let cx=0;cx<S.gridW;cx++){
      const idx=cy*S.gridW+cx;
      const tl=worldToScreen(cx*S.cellSize, cy*S.cellSize);
      const br=worldToScreen((cx+1)*S.cellSize, (cy+1)*S.cellSize);
      ctx.fillStyle=NODE_COLORS[idx%4].bg;
      ctx.fillRect(tl.x,tl.y,br.x-tl.x,br.y-tl.y);
    }
  }

  // --- Cell boundary lines ---
  ctx.setLineDash([8,6]);
  ctx.lineWidth=2;
  for(let i=0;i<=S.gridW;i++){
    const p=worldToScreen(i*S.cellSize,0);
    const p2=worldToScreen(i*S.cellSize,H);
    ctx.strokeStyle='#444';
    ctx.beginPath();ctx.moveTo(p.x,p.y);ctx.lineTo(p2.x,p2.y);ctx.stroke();
  }
  for(let j=0;j<=S.gridH;j++){
    const p=worldToScreen(0,j*S.cellSize);
    const p2=worldToScreen(W,j*S.cellSize);
    ctx.strokeStyle='#444';
    ctx.beginPath();ctx.moveTo(p.x,p.y);ctx.lineTo(p2.x,p2.y);ctx.stroke();
  }
  ctx.setLineDash([]);

  // --- Cell labels ---
  ctx.font='12px monospace'; ctx.textAlign='center'; ctx.textBaseline='middle';
  for(let cy=0;cy<S.gridH;cy++){
    for(let cx=0;cx<S.gridW;cx++){
      const idx=cy*S.gridW+cx;
      const center=worldToScreen((cx+0.5)*S.cellSize,(cy+0.5)*S.cellSize);
      ctx.fillStyle=NODE_COLORS[idx%4].stroke+'40';
      ctx.font='bold 14px monospace';
      ctx.fillText(`node_${cx}_${cy}`,center.x,center.y);
    }
  }

  // --- AoI radius circle ---
  const me=S.entities.get(S.myId);
  if(me){
    const mp=worldToScreen(me.renderX,me.renderY);
    const r=S.aoiRadius*cam.scale;
    ctx.setLineDash([6,4]);
    ctx.strokeStyle='rgba(255,255,100,0.3)';
    ctx.lineWidth=1.5;
    ctx.beginPath();ctx.arc(mp.x,mp.y,r,0,Math.PI*2);ctx.stroke();
    ctx.setLineDash([]);
  }

  // --- Entities ---
  for(const [id,ent] of S.entities){
    const sp=worldToScreen(ent.renderX,ent.renderY);
    const r=Math.max(ent.radius*cam.scale, 4);
    const isReplica=(ent.flags&0x01)!==0;
    const isGhost=(ent.flags&0x02)!==0;
    const isMe=id===S.myId;
    const nc=NODE_COLORS[ent.ownerIdx%4];

    ctx.globalAlpha = isGhost ? 0.3 : 1.0;

    // Transfer flash
    const flashAge = now-ent.flashTime;
    if(flashAge<500){
      const fi=1-flashAge/500;
      ctx.fillStyle=`rgba(255,255,0,${fi*0.6})`;
      ctx.beginPath();ctx.arc(sp.x,sp.y,r+8*fi,0,Math.PI*2);ctx.fill();
    }

    // Fill
    ctx.fillStyle = isMe ? '#fff' : nc.fill;
    ctx.beginPath();ctx.arc(sp.x,sp.y,r,0,Math.PI*2);ctx.fill();

    // Stroke
    if(isReplica){
      ctx.setLineDash([3,3]);
      ctx.strokeStyle='#ff6666';
      ctx.lineWidth=2;
    } else if(isMe){
      ctx.setLineDash([]);
      ctx.strokeStyle='#7fdbca';
      ctx.lineWidth=2.5;
    } else {
      ctx.setLineDash([]);
      ctx.strokeStyle=nc.stroke;
      ctx.lineWidth=1.5;
    }
    ctx.beginPath();ctx.arc(sp.x,sp.y,r,0,Math.PI*2);ctx.stroke();
    ctx.setLineDash([]);

    // Velocity vector
    if(Math.abs(ent.vx)>1||Math.abs(ent.vy)>1){
      const vLen=Math.sqrt(ent.vx*ent.vx+ent.vy*ent.vy);
      const vScale=Math.min(vLen/300,1)*30;
      const nx=ent.vx/vLen, ny=ent.vy/vLen;
      ctx.strokeStyle='rgba(255,255,255,0.4)';
      ctx.lineWidth=1.5;
      ctx.beginPath();
      ctx.moveTo(sp.x,sp.y);
      ctx.lineTo(sp.x+nx*vScale,sp.y+ny*vScale);
      ctx.stroke();
      // Arrowhead
      const ax=sp.x+nx*vScale, ay=sp.y+ny*vScale;
      const px=-ny*4, py=nx*4;
      ctx.beginPath();ctx.moveTo(ax,ay);ctx.lineTo(ax-nx*6+px,ay-ny*6+py);ctx.lineTo(ax-nx*6-px,ay-ny*6-py);ctx.closePath();ctx.fill();
    }

    // NetID label
    ctx.font='9px monospace'; ctx.textAlign='center'; ctx.fillStyle='rgba(255,255,255,0.6)';
    ctx.fillText('#'+id, sp.x, sp.y-r-10);

    // Replica/Ghost badge
    if(isReplica){
      ctx.fillStyle='#ff6666'; ctx.font='bold 9px monospace';
      ctx.fillText('R', sp.x+r+6, sp.y-4);
    }
    if(isGhost){
      ctx.fillStyle='#ffaa44'; ctx.font='bold 9px monospace';
      ctx.fillText('G', sp.x+r+6, sp.y+6);
    }

    // Player name
    if(ent.name){
      ctx.font='11px monospace'; ctx.fillStyle=isMe?'#7fdbca':'#ccc';
      ctx.fillText(ent.name, sp.x, sp.y+r+12);
    }

    ctx.globalAlpha=1.0;
  }

  // --- HUD ---
  const hud=document.getElementById('hud');
  const nodeLabel=`node_${S.cellX}_${S.cellY}`;
  hud.innerHTML=`Tick: ${S.tick}<br>FPS: ${S.fps}<br>Node: <span style="color:${NODE_COLORS[S.nodeIdx%4].stroke}">${nodeLabel}</span><br>Entities: ${S.entities.size} visible`;

  const stats=document.getElementById('stats');
  stats.innerHTML=`<span style="color:${NODE_COLORS[S.nodeIdx%4].stroke}">${nodeLabel}</span><br>Entities: ${S.nodeEnts}<br>Players: ${S.nodePlayers}`;

  const legend=document.getElementById('legend');
  legend.innerHTML=NODE_COLORS.slice(0,S.gridW*S.gridH).map((c,i)=>{
    const cx=i%S.gridW, cy=Math.floor(i/S.gridW);
    const active=i===S.nodeIdx?' ◀':'';
    return `<span style="color:${c.stroke}">■</span> node_${cx}_${cy}${active}`;
  }).join('<br>')+
  '<br><br><span style="color:#ff6666">---</span> Replica'+
  '<br><span style="color:rgba(255,255,255,0.3)">●</span> Ghost'+
  '<br><span style="color:rgba(255,255,100,0.3)">◯</span> AoI radius';
}

// ============================================================
// Login
// ============================================================
document.getElementById('goBtn').addEventListener('click',()=>{
  const name=document.getElementById('nameIn').value.trim();
  if(!name)return;
  connect(name);
});
document.getElementById('nameIn').addEventListener('keydown',(e)=>{
  if(e.key==='Enter') document.getElementById('goBtn').click();
});

render();
</script>
</body>
</html>
```

- [ ] **Step 2: Commit**

```bash
mkdir -p examples/4node-basic/web
git add examples/4node-basic/web/index.html
git commit -m "feat(4node-basic): add single-file web client with debug overlays"
```

---

### Task 10: Build + Verify

- [ ] **Step 1: Verify Go compilation**

Run: `go vet ./examples/4node-basic/...`
Expected: PASS

- [ ] **Step 2: Run the server**

Run: `cd examples/4node-basic && go run . -port 8081`
Expected: Output like:
```
4node-basic starting on http://localhost:8081
grid: 2x2 nodes, cell size: 2000, AoI: 1500
```

- [ ] **Step 3: Test in browser**

1. Open `http://localhost:8081` in a browser tab
2. Enter name "alice", click Connect
3. Verify: circle appears, debug overlays show cell boundaries, AoI circle, node info
4. Click to move — circle moves toward click
5. Open second tab, login as "bob"
6. Move alice toward a cell boundary
7. Verify: ghost/replica markers appear during transfer, node info updates
8. Both players should see each other when in AoI range

- [ ] **Step 4: Fix any issues found during testing**

- [ ] **Step 5: Final commit**

```bash
git add -A examples/4node-basic/
git commit -m "feat(4node-basic): complete 4-node basic example with debug overlays"
```
