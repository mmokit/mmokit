// Package mmokit is a single-import facade for the MMO engine.
// It re-exports types from all pkg/ sub-packages so that games (and the
// internal game code) can use one import instead of 5-7 aliased ones.
//
// For ECS queries and custom systems, also import "github.com/mlange-42/ark/ecs"
// since generic types like ecs.Map1[T] and ecs.Filter2[A,B] cannot be aliased.
package mmokit

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/ops"
	"github.com/zenion/mmoserver/pkg/orderbook"
	"github.com/zenion/mmoserver/pkg/persist"
	"github.com/zenion/mmoserver/pkg/spatial"
	"github.com/zenion/mmoserver/pkg/system"
	"github.com/zenion/mmoserver/pkg/universe"
)

// ---------------------------------------------------------------------------
// ECS
// ---------------------------------------------------------------------------

type Entity = ecs.Entity

// ---------------------------------------------------------------------------
// Components (pkg/component)
// ---------------------------------------------------------------------------

type Position = component.Position
type Velocity = component.Velocity
type Rotation = component.Rotation
type Collider = component.Collider
type NetworkID = component.NetworkID
type EntityKind = component.EntityKind
type Health = component.Health
type Shield = component.Shield
type Lifetime = component.Lifetime
type PlayerConn = component.PlayerConn
type SectorCoord = component.SectorCoord
type Ghost = component.Ghost
type Replica = component.Replica
type TransferCooldown = component.TransferCooldown
type MoveTarget = component.MoveTarget
type TargetLock = component.TargetLock

// ---------------------------------------------------------------------------
// Engine (pkg/engine)
// ---------------------------------------------------------------------------

type Engine = engine.Engine
type EngineConfig = engine.Config
type System = engine.System
type Hooks = engine.Hooks
type GameLoop = engine.GameLoop
type TickQueue = engine.TickQueue
type Console = engine.Console
type Command = engine.Command
type EntityDef = engine.EntityDef
type EntityRegistry = engine.EntityRegistry
type TickProfile = engine.TickProfile
type PerfStats = engine.PerfStats
type TimingStats = engine.TimingStats

// ---------------------------------------------------------------------------
// Universe (pkg/universe)
// ---------------------------------------------------------------------------

type GameWorld = universe.GameWorld
type WorldBase = universe.WorldBase
type Coordinator = universe.Coordinator
type GridConfig = universe.GridConfig
type Node = universe.Node
type NodeFactory = universe.NodeFactory
type NodeBridge = universe.NodeBridge
type NoopNodeBridge = universe.NoopNodeBridge
type NeighborInfo = universe.NeighborInfo
type CoordinatorOption = universe.CoordinatorOption
type SpawnOption = universe.SpawnOption
type BoundaryWorld = universe.BoundaryWorld
type BoundarySystem = universe.BoundarySystem
type TransferFrame = universe.TransferFrame
type ComponentSlice = universe.ComponentSlice
type ComponentID = universe.ComponentID
type CrossNodeAction = universe.CrossNodeAction
type ActionResult = universe.ActionResult
type ActionType = universe.ActionType
type ReplicationRegistry = universe.ReplicationRegistry
type ComponentReplicator = universe.ComponentReplicator
type ReplicaFrame = universe.ReplicaFrame
type ReplicaApplyContext = universe.ReplicaApplyContext
type SideEffectCollector = universe.SideEffectCollector
type SideEffectRegistry = universe.SideEffectRegistry
type SideEffectHandler = universe.SideEffectHandler
type SideEffectType = universe.SideEffectType

// ---------------------------------------------------------------------------
// Coords (pkg/coords)
// ---------------------------------------------------------------------------

type WorldPos = coords.WorldPos
type Coordssector = coords.SectorCoord

// SectorSize returns the current sector size in world units.
func SectorSize() float32 { return coords.SectorSize }

// SetSectorSize overrides the default sector size (call during initialization).
func SetSectorSize(size float32) { coords.SetSectorSize(size) }

// ---------------------------------------------------------------------------
// Net (pkg/net)
// ---------------------------------------------------------------------------

type ConnManager = net.ConnManager
type Transport = net.Transport
type Conn = net.Conn
type PlayerEvent = net.PlayerEvent
type EventInterceptor = net.EventInterceptor
type UDPServer = net.UDPServer

// ---------------------------------------------------------------------------
// Spatial (pkg/spatial)
// ---------------------------------------------------------------------------

type Grid = spatial.Grid
type SpatialEntry = spatial.Entry

// ---------------------------------------------------------------------------
// Logger (pkg/logger)
// ---------------------------------------------------------------------------

type Logger = logger.Logger

// ---------------------------------------------------------------------------
// Ops (pkg/ops)
// ---------------------------------------------------------------------------

type OpRouter = ops.Router
type PlayerSessions = ops.PlayerSessions
type OpContext = ops.OpContext
type ParsedRequest = ops.ParsedRequest
type RequestParser = ops.RequestParser
type ResponseFrameBuilder = ops.ResponseFrameBuilder

// ---------------------------------------------------------------------------
// Order Book (pkg/orderbook)
// ---------------------------------------------------------------------------

type OrderBookService = orderbook.Service
type OrderBookConfig = orderbook.Config
type Order = orderbook.Order
type Trade = orderbook.Trade
type PlaceResult = orderbook.PlaceResult
type OrderBookView = orderbook.OrderBookView
type OrderSide = orderbook.OrderSide

// ---------------------------------------------------------------------------
// Persistence (pkg/persist)
// ---------------------------------------------------------------------------

type Store = persist.Store
type AsyncWriter = persist.AsyncWriter
type PersistOp = persist.Op

// ---------------------------------------------------------------------------
// Systems (pkg/system)
// ---------------------------------------------------------------------------

type PhysicsSystem = system.PhysicsSystem
type LifetimeSystem = system.LifetimeSystem

// ---------------------------------------------------------------------------
// Constructors & Functions
// ---------------------------------------------------------------------------

var (
	// Engine
	DefaultEngineConfig = engine.DefaultConfig
	NewConsole          = engine.NewConsole
	NewEntityRegistry   = engine.NewEntityRegistry
	NewTickQueue        = engine.NewTickQueue

	// Universe
	NewCoordinator            = universe.NewCoordinator
	NewWorldBase              = universe.NewWorldBase
	NewBoundarySystem         = universe.NewBoundarySystem
	NewReplicationRegistry    = universe.NewReplicationRegistry
	NewSideEffectRegistry     = universe.NewSideEffectRegistry
	ScanBorderWithRegistry    = universe.ScanBorderWithRegistry
	ApplyReplicasWithRegistry = universe.ApplyReplicasWithRegistry
	UnmarshalCollider         = universe.UnmarshalCollider
	MarshalTransferFrame      = universe.MarshalTransferFrame
	UnmarshalTransferFrame    = universe.UnmarshalTransferFrame
	MarshalSideEffects        = universe.MarshalSideEffects
	UnmarshalSideEffects      = universe.UnmarshalSideEffects
	SectorID                  = universe.SectorID

	// Systems
	NewPhysicsSystem    = system.NewPhysicsSystem
	NewLifetimeSystem   = system.NewLifetimeSystem

	// Net
	NewConnManager = net.NewConnManager
	NewUDPServer   = net.NewUDPServer

	// Spatial
	NewGrid = spatial.NewGrid

	// Logger
	NewLogger = logger.New

	// Ops
	NewOpRouter       = ops.NewRouter
	NewPlayerSessions = ops.NewPlayerSessions

	// Order Book
	NewOrderBookService    = orderbook.NewService
	DefaultOrderBookConfig = orderbook.DefaultConfig

	// Persistence
	OpenBolt       = persist.OpenBolt
	NewAsyncWriter = persist.NewAsyncWriter
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

var (
	// Coordinator options
	WithConnManager = universe.WithConnManager
	WithLogger      = universe.WithLogger
	WithAoIRadius   = universe.WithAoIRadius

	// Spawn options
	WithVelocity   = universe.WithVelocity
	WithCollider   = universe.WithCollider
	WithEntityKind = universe.WithEntityKind
	WithRotation   = universe.WithRotation

	// Net channels
	ChannelEvent     = net.ChannelEvent
	ChannelOperation = net.ChannelOperation

	// Order book sides
	SideBuy  = orderbook.SideBuy
	SideSell = orderbook.SideSell

	// Spatial shapes
	ShapeCircle = spatial.ShapeCircle
	ShapeRect   = spatial.ShapeRect

	// Persistence errors
	ErrNotFound = persist.ErrNotFound
)

// ---------------------------------------------------------------------------
// Generic functions (can't alias generic funcs in Go)
// ---------------------------------------------------------------------------

// Enqueue adds a typed event to a TickQueue.
func Enqueue[T any](q *engine.TickQueue, event T) {
	engine.Enqueue(q, event)
}

// Drain retrieves and clears all events of a type from a TickQueue.
func Drain[T any](q *engine.TickQueue) []T {
	return engine.Drain[T](q)
}

// Peek returns events without clearing them.
func Peek[T any](q *engine.TickQueue) []T {
	return engine.Peek[T](q)
}

// CountRealEntities returns the number of entities with a NetworkID that are
// not replicas or ghosts (i.e. entities owned by this node).
func CountRealEntities(w *ecs.World) int {
	count := 0
	filter := ecs.NewFilter1[component.NetworkID](w).
		Without(ecs.C[component.Replica](), ecs.C[component.Ghost]())
	query := filter.Query()
	for query.Next() {
		count++
	}
	return count
}
