// Package mmokit is a single-import facade for the MMO engine.
// It re-exports types from all pkg/ sub-packages so that games (and the
// internal game code) can use one import instead of 5-7 aliased ones.
//
// For ECS queries and custom systems, also import "github.com/mlange-42/ark/ecs"
// since generic types like ecs.Map1[T] and ecs.Filter2[A,B] cannot be aliased.
package mmokit

import (
	"log"

	"github.com/mlange-42/ark/ecs"
	"google.golang.org/protobuf/proto"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/metrics"
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

// Entity is a handle to an ECS entity in the Ark world.
type Entity = ecs.Entity

// ---------------------------------------------------------------------------
// Components (pkg/component)
// ---------------------------------------------------------------------------

// Position is an entity's world-space position (X, Y in world units).
type Position = component.Position

// Velocity is an entity's movement speed (X, Y in world units per second).
type Velocity = component.Velocity

// Rotation is an entity's facing direction (Angle in radians).
type Rotation = component.Rotation

// Collider defines a collision shape (circle or oriented rectangle) with a layer byte.
// For circles use Radius; for rects use Width/Height. Radius doubles as the bounding
// radius for broad-phase checks on rectangles.
type Collider = component.Collider

// NetworkID is a stable identifier for an entity that is sent to clients.
type NetworkID = component.NetworkID

// EntityKind identifies the entity type for client-side rendering (Type uint8).
type EntityKind = component.EntityKind

// Health represents an entity's hit points (Current and Max float32).
type Health = component.Health

// Shield represents shield points with regeneration rate and damage cooldown.
type Shield = component.Shield

// Lifetime tracks remaining seconds before an entity is automatically despawned.
type Lifetime = component.Lifetime

// PlayerConn links a player entity to its network connection ID (ConnID uint32).
type PlayerConn = component.PlayerConn

// CellCoord identifies which cell an entity belongs to in the server mesh grid.
type CellCoord = component.CellCoord

// Ghost marks an entity that is mid-transfer between nodes. Ghost entities are
// visible in AoI but not simulated by game systems.
type Ghost = component.Ghost

// Replica is a read-only copy of an entity owned by a neighboring node. Replicas
// participate in spatial queries and AoI but are never mutated by local systems.
type Replica = component.Replica

// Proxy is a lightweight representation of a border entity from a neighboring node.
// Unlike Replica (full state copy), a Proxy carries only position, velocity, bounding
// radius, and entity type — enough for spatial queries and collision broad-phase.
// Promoted to a full Replica on demand when a player's AoI or collision requires full state.
// Systems that exclude Ghost/Replica should also exclude Proxy.
type Proxy = component.Proxy

// Dormant marks an entity as sleeping. Dormant entities are excluded from border
// scanning, game system updates, and client replication. They wake when a player
// (local or proxy from a neighbor) enters proximity on the authoritative node.
type Dormant = component.Dormant

// TransferCooldown prevents rapid re-transfers after an entity arrives on a new node.
type TransferCooldown = component.TransferCooldown

// MoveTarget holds a click-to-move destination with cell coordinates and active flag.
type MoveTarget = component.MoveTarget

// MoveParams holds per-entity movement configuration.
type MoveParams = component.MoveParams

// DirectionInput holds WASD/joystick direction state.
type DirectionInput = component.DirectionInput

// TargetLock holds lock-on targeting state: target entity, progress (0-1), range, and locked flag.
type TargetLock = component.TargetLock

// ---------------------------------------------------------------------------
// Engine (pkg/engine)
// ---------------------------------------------------------------------------

// Engine holds core platform state shared by all systems: the ECS world,
// connection manager, logger, tick counter, performance profiler, and metrics.
type Engine = engine.Engine

// System is the interface all game systems implement. Call Update(dt) each tick.
// Embed SystemBase for automatic dependency injection via SetDeps/Init.
type System = engine.System

// SystemBase provides dependency injection for systems. Embed it in your system
// struct to get ECSWorld(), Engine(), and GameWorld() accessors. The framework
// calls SetDeps() then Init() before the first Update().
type SystemBase = engine.SystemBase

// SystemDef pairs a name with a System for registration and profiling.
type SystemDef = engine.SystemDef

// Hooks allows the game to inject behavior into the engine's tick loop.
// All hooks are nil-safe (skipped if nil). Includes OnConnect, OnDisconnect,
// ProcessLogins, PreFlush, PostFlush, ClearTickState, and PostTick.
type Hooks = engine.Hooks

// GameLoop runs the fixed-timestep (default 20 Hz) tick loop: process events,
// drain admin commands, run all systems in order, flush removals, and spawn loot.
type GameLoop = engine.GameLoop

// TickQueue is a generic per-tick event queue. Each event type T gets its own
// internal slice. Use Enqueue[T], Drain[T], and Peek[T] to interact with it.
// All operations are single-threaded (game loop only).
type TickQueue = engine.TickQueue

// Console provides an interactive server CLI with readline support, tab completion,
// command categories, and subcommand groups. All ECS access is scheduled via
// ExecOnGameLoop to ensure thread safety.
type Console = engine.Console

// Command represents a console command with name, aliases, category, usage string,
// description, handler function, and optional tab-completion function.
type Command = engine.Command

// EntityDef describes a spawnable entity type for admin tooling and console commands.
// Includes name, description, entity type byte, and a Spawn(x, y) function.
type EntityDef = engine.EntityDef

// EntityRegistry maps entity type names to their EntityDef definitions.
type EntityRegistry = engine.EntityRegistry

// TickProfile records per-tick and per-system timing for performance monitoring.
type TickProfile = engine.TickProfile

// PerfStats holds aggregated performance statistics (tick duration, system timings).
type PerfStats = engine.PerfStats

// TimingStats holds min/max/average timing statistics for a measured operation.
type TimingStats = engine.TimingStats

// NodeMetrics collects per-node observability data: tick timing, entity counts,
// and bandwidth. Write methods are zero-alloc on the hot path; Snapshot() allocates
// and is intended for low-frequency scraping.
type NodeMetrics = metrics.NodeMetrics

// LoadSnapshot is a point-in-time health report for a single node, including tick
// health, entity counts, network stats, and a composite load score (0.0-1.0+).
type LoadSnapshot = metrics.LoadSnapshot

// TickHealthSnapshot contains tick timing health metrics (duration, budget usage).
type TickHealthSnapshot = metrics.TickHealthSnapshot

// EntitySnapshot is a breakdown of entity counts: real, replica, ghost, and player.
type EntitySnapshot = metrics.EntitySnapshot

// NetworkSnapshot contains bandwidth (bytes sent/recv) and connection count metrics.
type NetworkSnapshot = metrics.NetworkSnapshot

// MetricsTimingStats holds min/max/avg timing statistics from the metrics package.
type MetricsTimingStats = metrics.TimingStats

// MetricsTickStats contains per-system tick timing breakdown for detailed profiling.
type MetricsTickStats = metrics.TickStats

// CommandGroup is a named prefix that dispatches to child subcommands.
// Example: "config set AoIRadius 500" dispatches to group "config", subcommand "set".
type CommandGroup = engine.CommandGroup

// Configurable provides runtime read/write access to a configuration struct's fields.
// Used by the built-in "config" command group for generic get/set/list.
type Configurable = engine.Configurable

// ReflectConfig wraps any struct pointer as a Configurable using reflection,
// enabling runtime get/set of exported fields by name.
type ReflectConfig = engine.ReflectConfig

// Table builds column-aligned text output for the console (headers + rows).
type Table = engine.Table

// BuiltinOpts configures which built-in console command groups to register.
// Each non-nil field enables the corresponding commands (config, entity, node, etc.).
type BuiltinOpts = engine.BuiltinOpts

// EntityOpts configures callbacks for the "entity" console command group
// (summary, list, get, remove). All callbacks run on the game loop via ExecOnGameLoop.
type EntityOpts = engine.EntityOpts

// EntityInfo is a summary of an entity returned by EntityOpts callbacks.
type EntityInfo = engine.EntityInfo

// NodeRef references a node for console command execution. Contains the node ID,
// an Exec function that runs closures on the node's game loop, and its metrics.
type NodeRef = engine.NodeRef

// PlayerManager owns player sessions and enforces lifecycle state transitions
// (pending -> active -> dead, transferring, disconnected). Supports custom states,
// guards, actions, and OnEnter/OnExit callbacks.
type PlayerManager = engine.PlayerManager

// PlayerSession tracks a single player's connection ID, username, lifecycle state,
// ECS entity, and arbitrary Data payload.
type PlayerSession = engine.PlayerSession

// PlayerState represents a player's lifecycle state (uint8). Built-in states:
// StatePending, StateActive, StateDead, StateTransferring, StateDisconnected.
// Games can define additional states via PlayerManager.
type PlayerState = engine.PlayerState

// SessionID uniquely identifies a player session (uint64).
type SessionID = engine.SessionID

// StateTransition defines a valid state change (From -> To) with an optional
// Guard (must return true) and Action (runs on transition).
type StateTransition = engine.StateTransition

// StateCallbacks provides OnEnter and OnExit hooks that fire when a player
// enters or exits a particular state.
type StateCallbacks = engine.StateCallbacks

// InputRouter dispatches client messages to registered handlers based on message
// code and player state. It implements the System interface and runs each tick
// to process all queued input.
type InputRouter = engine.InputRouter

// InputContext is passed to every input handler. Contains the PlayerSession,
// ConnID, and ECS Entity (may be zero-value for stateless players like StateDead).
type InputContext = engine.InputContext

// StateMask is a bitmask of PlayerState values (supports up to 32 states).
// Used when registering input handlers to specify which states accept a message.
type StateMask = engine.StateMask

// InputFilter is a per-state filter function for input handling.
type InputFilter = engine.InputFilter

// EnvelopeParser decodes raw bytes into (code uint32, payload []byte, error).
// Plugged into InputRouter to support different wire formats.
type EnvelopeParser = engine.EnvelopeParser

// HandlerOption configures optional per-handler behavior on an InputRouter.
type HandlerOption = engine.HandlerOption

// ---------------------------------------------------------------------------
// Universe (pkg/universe)
// ---------------------------------------------------------------------------

// CellID uniquely identifies a cell at any quadtree depth in the server mesh.
// Depth 0 is the original grid. X, Y are cell coordinates; Depth is the quadtree level.
type CellID = universe.CellID

// PartitionConfig configures dynamic cell partitioning (quadtree splitting/merging).
type PartitionConfig = universe.PartitionConfig

// DefaultPartitionConfig returns a PartitionConfig with sensible defaults for
// dynamic cell partitioning. Pass to Config.DynamicPartitioning to enable.
var DefaultPartitionConfig = universe.DefaultPartitionConfig

// Config holds all Coordinator configuration: grid dimensions (CellsX, CellsY),
// cell size, tick rate, AoI radius, world factory, console options, and more.
// Zero values use sensible defaults.
type Config = universe.Config

// GameWorld is the interface a game must implement to use the server meshing
// infrastructure. Methods handle entity serialization, transfers, replication,
// cross-node actions, and chat. Embed WorldBase for working defaults.
type GameWorld = universe.GameWorld

// WorldBase provides default implementations for all GameWorld interface methods.
// Embed it in your game world struct to get working multi-node support out of the
// box, including entity spawning, border replication, and cross-node transfers.
type WorldBase = universe.WorldBase

// Coordinator manages multiple Node instances in a grid topology, routes player
// connections to the correct node, and coordinates entity transfers between nodes.
// Call Start(ctx) to run (blocks until shutdown).
type Coordinator = universe.Coordinator

// Node is a self-contained game simulation owning one cell in the mesh grid.
// Each node runs its own ECS world, game loop, and systems independently.
type Node = universe.Node

// NodeBridge abstracts multi-node coordination: entity transfers, replica updates,
// chat relay, spawn requests, and cross-node actions. In single-node mode, use
// NoopNodeBridge.
type NodeBridge = universe.NodeBridge

// NoopNodeBridge is a no-op NodeBridge implementation for single-node mode.
// All methods are safe to call but do nothing.
type NoopNodeBridge = universe.NoopNodeBridge

// NeighborInfo describes a neighbor node's cell offset (DX, DY) relative to
// the current node. Used by border replication scanning.
type NeighborInfo = universe.NeighborInfo

// SpawnOption configures an optional component when spawning an entity via
// WorldBase.SpawnEntity (e.g. WithVelocity, WithCollider, WithRotation).
type SpawnOption = universe.SpawnOption

// BoundaryWorld is the interface needed by BoundarySystem to serialize entities
// and initiate cross-node transfers. WorldBase implements this automatically.
type BoundaryWorld = universe.BoundaryWorld

// BoundarySystem normalizes entity positions into [0, CellSize) and initiates
// cross-node transfers when entities cross cell boundaries.
type BoundarySystem = universe.BoundarySystem

// TransferFrame is the wire format for entity transfers between nodes. Contains
// core fields (position, velocity, rotation, collider, cell, IDs) plus a
// Components slice for game-specific serialized data.
type TransferFrame = universe.TransferFrame

// ComponentSlice holds a single component's serialized data (ID + bytes) within
// a TransferFrame or ReplicaFrame.
type ComponentSlice = universe.ComponentSlice

// CrossNodeAction is a request sent to the authoritative node when a local entity
// acts on a replica. The authoritative node processes it and returns an ActionResult.
type CrossNodeAction = universe.CrossNodeAction

// ActionResult is the response sent back to the originating node after a
// CrossNodeAction is processed, including success flag, payload, and side effects.
type ActionResult = universe.ActionResult

// ActionType is a game-defined uint16 identifier for a cross-node action kind.
type ActionType = universe.ActionType

// ReplicationRegistry tracks which ECS components should be replicated across nodes.
// Register components with RegisterComponent[T] to enable automatic border replication.
type ReplicationRegistry = universe.ReplicationRegistry

// ComponentReplicator handles one component type's replication: Scan serializes
// from a local entity, Apply updates an existing replica, Add attaches to a new replica.
type ComponentReplicator = universe.ComponentReplicator

// EntityKindDef describes an entity kind's components for transfer, client
// replication, and schema export. Build one per entity type using KindComponent
// and pass to WorldBase.RegisterEntityKind.
type EntityKindDef = universe.EntityKindDef

// ReplicaFrame is the wire format for a replicated entity. Always includes position
// and cell; the Components slice carries variable-length replicated component data.
type ReplicaFrame = universe.ReplicaFrame

// ReplicaApplyContext is passed when applying incoming replica data to an entity.
type ReplicaApplyContext = universe.ReplicaApplyContext

// SideEffectCollector accumulates side effects during a cross-node action execution.
// Not thread-safe (only used on the game loop goroutine).
type SideEffectCollector = universe.SideEffectCollector

// SideEffectRegistry maps side effect types to their handlers on the receiving node.
type SideEffectRegistry = universe.SideEffectRegistry

// SideEffectHandler processes a single side effect type on the originating node
// after receiving an ActionResult. The Handle closure captures typed game state.
type SideEffectHandler = universe.SideEffectHandler

// SideEffectType is a game-defined uint16 identifier for a side effect kind.
type SideEffectType = universe.SideEffectType

// ConsoleOpts provides game-specific console configuration for the Coordinator.
// All fields are optional (omit what your game doesn't need).
type ConsoleOpts = universe.ConsoleOpts

// ---------------------------------------------------------------------------
// Coords (pkg/coords)
// ---------------------------------------------------------------------------

// WorldPos is a position in the infinite universe: cell index (CellX, CellY) plus
// local offset (LX, LY) in [0, CellSize).
type WorldPos = coords.WorldPos

// CellSize returns the current cell size in world units.
func CellSize() float32 { return coords.CellSize }

// SetCellSize overrides the default cell size (call during initialization).
func SetCellSize(size float32) { coords.SetCellSize(size) }

// ---------------------------------------------------------------------------
// Net (pkg/net)
// ---------------------------------------------------------------------------

// ConnManager manages all active network connections across transports (WebSocket, UDP).
// Provides connection lookup, broadcasting, and event draining.
type ConnManager = net.ConnManager

// Transport is the interface for network transports. Implementations provide
// SendReliable, SendUnreliable, DrainInput, DrainOpInput, and Close.
type Transport = net.Transport

// Conn wraps a WebSocket connection with buffered read/write pumps.
type Conn = net.Conn

// PlayerEvent represents a player connecting or disconnecting (ConnID + flags).
type PlayerEvent = net.PlayerEvent

// EventInterceptor is called for each incoming event frame before it is queued.
// If it returns true, the message is considered handled and not queued for the game loop.
type EventInterceptor = net.EventInterceptor

// UDPServer manages a single UDP socket and dispatches packets to per-connection transports.
type UDPServer = net.UDPServer

// ---------------------------------------------------------------------------
// Spatial (pkg/spatial)
// ---------------------------------------------------------------------------

// HashGrid is an incremental spatial hash for broad-phase collision detection and
// AoI queries. Entities are registered once and updated incrementally; only
// bucket-boundary crossings trigger rehashing. Supports transient entries for
// derived spatial data (e.g. body segments) that are cleared each tick.
type HashGrid = spatial.HashGrid

// SpatialEntry stores an entity with its position, shape (circle or rect),
// bounding radius, and collision layer for use in the HashGrid.
type SpatialEntry = spatial.Entry

// ---------------------------------------------------------------------------
// Logger (pkg/logger)
// ---------------------------------------------------------------------------

// Logger provides category-based debug logging with dynamic registration.
// Categories can be enabled/disabled at runtime via the console.
type Logger = logger.Logger

// ---------------------------------------------------------------------------
// Ops (pkg/ops)
// ---------------------------------------------------------------------------

// OpRouter polls connections for channel-0x01 operation messages, parses requests,
// resolves player identity, and dispatches to registered handlers on worker goroutines.
type OpRouter = ops.Router

// PlayerSessions is a thread-safe map of connID to username, updated from
// the game loop and read by OpRouter worker goroutines.
type PlayerSessions = ops.PlayerSessions

// OpContext provides player identity (ConnID, Username) to operation handlers.
type OpContext = ops.OpContext

// ParsedRequest holds the decoded fields of an operation request (Code, RequestID, Data).
type ParsedRequest = ops.ParsedRequest

// RequestParser decodes a raw operation message into a ParsedRequest.
type RequestParser = ops.RequestParser

// ResponseFrameBuilder builds a channel-0x01 wire frame from response fields.
type ResponseFrameBuilder = ops.ResponseFrameBuilder

// ---------------------------------------------------------------------------
// Order Book (pkg/orderbook)
// ---------------------------------------------------------------------------

// OrderBookService is a generic price-time priority order matching engine.
// All methods are thread-safe. Handles order storage, matching, and lifecycle
// with no knowledge of currencies, banks, or persistence.
type OrderBookService = orderbook.Service

// OrderBookConfig holds tunable marketplace parameters: tax percentage,
// order expiry, min price, and max orders per player.
type OrderBookConfig = orderbook.Config

// Order represents a resting limit order in the order book (buy or sell).
type Order = orderbook.Order

// Trade represents a completed trade between two players.
type Trade = orderbook.Trade

// PlaceResult summarizes the outcome of placing an order: order ID, filled quantity,
// average price, and total cost.
type PlaceResult = orderbook.PlaceResult

// OrderBookView is an aggregated view of an order book for a single item,
// with sell and buy price levels.
type OrderBookView = orderbook.OrderBookView

// OrderSide identifies the side of an order (SideBuy or SideSell).
type OrderSide = orderbook.OrderSide

// ---------------------------------------------------------------------------
// Persistence (pkg/persist)
// ---------------------------------------------------------------------------

// Store is the interface for key-value persistence (get, put, delete, forEach).
// Implementations include BoltDB (OpenBolt).
type Store = persist.Store

// AsyncWriter wraps a Store for non-blocking writes from the game loop.
// Enqueue operations and they are processed by a background goroutine.
type AsyncWriter = persist.AsyncWriter

// PersistOp represents a single write or delete operation (Collection, Key, Value).
// A nil Value means delete.
type PersistOp = persist.Op

// ---------------------------------------------------------------------------
// Systems (pkg/system)
// ---------------------------------------------------------------------------

// PhysicsSystem integrates velocity into position each tick. Skips Ghost and Replica entities.
type PhysicsSystem = system.PhysicsSystem

// ReplicaDeadReckoningSystem advances replica and ghost entity positions each tick
// using their last-known velocity, keeping entities moving smoothly during inter-node
// transfers and between replication updates.
type ReplicaDeadReckoningSystem = system.ReplicaDeadReckoningSystem

// LifetimeSystem despawns entities whose Lifetime component has expired.
// Skips Ghost and Replica entities.
type LifetimeSystem = system.LifetimeSystem

// ReplicationSystem manages per-viewer AoI visibility, hash-based diff detection,
// snapshot-based delta encoding, and frame dispatch to connected clients.
type ReplicationSystem = system.ReplicationSystem

// ClickToMoveSystem moves entities toward their MoveTarget at MoveParams.MaxSpeed.
type ClickToMoveSystem = system.ClickToMoveSystem

// DirectionMoveSystem moves entities using DirectionInput at MoveParams.MaxSpeed.
type DirectionMoveSystem = system.DirectionMoveSystem

// SpatialHooks provides optional per-tick callbacks for game-specific spatial logic.
type SpatialHooks = system.SpatialHooks

// NewSpatialSystem returns a System factory for the standard spatial grid update
// with no game-specific hooks. Queries Position+Collider+NetworkID, reads Rotation
// if present, registers/updates entities in the HashGrid each tick.
//
//	coord.AddSystem("Spatial", mmokit.NewSpatialSystem())
func NewSpatialSystem() func() engine.System {
	return func() engine.System { return &system.SpatialSystem{} }
}

// NewSpatialSystemWith returns a System factory with game-specific hooks.
// The hooks function runs at Init time with the typed game world and returns
// per-tick callbacks (PreTick, OnEntity, PostTick).
//
//	coord.AddSystem("Spatial", mmokit.NewSpatialSystemWith(func(gw *MyWorld) mmokit.SpatialHooks {
//	    return mmokit.SpatialHooks{
//	        OnEntity: func(entity ecs.Entity, entry mmokit.SpatialEntry) { ... },
//	    }
//	}))
func NewSpatialSystemWith[W any](hooks func(gw W) SpatialHooks) func() engine.System {
	return func() engine.System {
		sys := &system.SpatialSystem{}
		sys.SetInitHook(func(gw any) system.SpatialHooks {
			return hooks(gw.(W))
		})
		return sys
	}
}

// NewPhysicsSystem returns a System factory for velocity→position integration.
func NewPhysicsSystem() func() engine.System {
	return func() engine.System { return &PhysicsSystem{} }
}

// NewClickToMoveSystem returns a System factory for click-to-move entity movement.
func NewClickToMoveSystem() func() engine.System {
	return func() engine.System { return &ClickToMoveSystem{} }
}

// NewDirectionMoveSystem returns a System factory for direction-input entity movement.
func NewDirectionMoveSystem() func() engine.System {
	return func() engine.System { return &DirectionMoveSystem{} }
}

// NewDeadReckoningSystem returns a System factory for replica/ghost dead reckoning.
func NewDeadReckoningSystem() func() engine.System {
	return func() engine.System { return &ReplicaDeadReckoningSystem{} }
}

// NewLifetimeSystem returns a System factory for despawning expired entities.
func NewLifetimeSystem() func() engine.System {
	return func() engine.System { return &LifetimeSystem{} }
}

// ComponentBinding is a composable binding that encodes one or more fields into
// an AutoReplicator snapshot/delta payload.
type ComponentBinding = system.ComponentBinding

// ReplicationConfig holds all dependencies for the ReplicationSystem: ECS world,
// spatial grid, viewer source, frame writer, replicator registry, AoI radius,
// ack mode, and optional lifecycle callbacks.
type ReplicationConfig = system.ReplicationConfig

// ReplicationFrame is a single tick's replication payload for one viewer.
type ReplicationFrame = system.ReplicationFrame

// FullPayload is a complete entity snapshot sent as a keyframe.
type FullPayload = system.FullPayload

// DeltaPayload is a diff-encoded entity update sent between keyframes.
type DeltaPayload = system.DeltaPayload

// ViewerInfo describes a connection that receives replicated entity state
// (ConnID, Entity, and position X/Y for AoI center).
type ViewerInfo = system.ViewerInfo

// AckMode controls how replication baselines advance: AckReliable auto-advances
// on send (TCP/WebSocket), AckExplicit waits for client acks (UDP).
type AckMode = system.AckMode

// ReplicatorRegistry maps entity type constants (uint8) to their EntityReplicator,
// which handles snapshot and delta encoding for that entity type.
type ReplicatorRegistry = system.ReplicatorRegistry

// Hasher computes a hash of entity state for diff detection in the ReplicationSystem.
type Hasher = system.Hasher

// ReplicationTier configures per-entity-type replication behavior: custom AoI
// radius, update frequency divisor, and priority weight. Implement TierProvider
// on an EntityReplicator to return a tier.
type ReplicationTier = system.ReplicationTier

// DefaultReplicationConfig returns a ReplicationConfig with the standard
// boilerplate fields pre-filled: World, SpatialGrid, Viewers, Frame, and
// GetTick. Games set game-specific fields (Replicators, AoIRadius,
// callbacks, etc.) on the returned struct before passing it to
// NewReplicationSystem.
func DefaultReplicationConfig(eng *engine.Engine, grid *spatial.HashGrid) ReplicationConfig {
	return ReplicationConfig{
		World:       eng.ECS,
		SpatialGrid: grid,
		Viewers:     system.NewPlayerViewerSource(eng.ECS, eng.Players, engine.StateActive),
		Frame:       system.NewBinaryFrameWriter(eng.ConnMgr, uint32(enginepb.ServerEventCode_SE_DELTA_WORLD_UPDATE), MakeEventRaw),
		GetTick:     func() uint32 { return eng.Tick },
	}
}

// EngineBindingsConfig configures the standard engine-level replication bindings
// returned by EngineBindings. All fields are optional — zero values use sensible defaults.
type EngineBindingsConfig struct {
	// VelQuantScale is the velocity quantization multiplier: int16 = vel * scale.
	// Higher values give more precision but lower max speed (32767 / scale).
	// Zero defaults to 100 (max ~327 units/s, precision 0.01).
	VelQuantScale float32

	// SizeQuantScale is the radius quantization multiplier: int16 = radius * scale.
	// Zero defaults to 100 (max ~327 units, precision 0.01).
	SizeQuantScale float32

	// CellSizeFn returns the current cell size. Nil defaults to coords.CellSize.
	// Set this when using dynamic cell partitioning where cell sizes change at runtime.
	CellSizeFn func() float32
}

// EngineBindings returns a ComponentBinding that bundles the standard engine-level
// replication fields: position, quantized velocity, quantized size, and mesh state.
// GridWidth is auto-discovered from the Coordinator. Games append game-specific
// Component[T] bindings after this.
//
// If cfg is omitted, all defaults are used.
func EngineBindings(w *ecs.World, coord *universe.Coordinator, cfg ...EngineBindingsConfig) ComponentBinding {
	var c EngineBindingsConfig
	if len(cfg) > 0 {
		c = cfg[0]
	}
	var gridWidth uint32
	if coord != nil {
		gridWidth = coord.GridWidth()
	}
	return system.EngineBindings(w, system.EngineBindingsConfig{
		GridWidth:      gridWidth,
		VelQuantScale:  c.VelQuantScale,
		SizeQuantScale: c.SizeQuantScale,
		CellSizeFn:     c.CellSizeFn,
	})
}

// ---------------------------------------------------------------------------
// Constructors & Functions
// ---------------------------------------------------------------------------

var (
	// DefaultEngineConfig returns the default Engine configuration (listen addr, tick rate).
	DefaultEngineConfig = engine.DefaultConfig

	// NewConsole creates an interactive server console with readline support.
	NewConsole = engine.NewConsole

	// NewEntityRegistry creates an empty EntityRegistry for registering entity types.
	NewEntityRegistry = engine.NewEntityRegistry

	// NewTickQueue creates a new per-tick typed event queue.
	NewTickQueue = engine.NewTickQueue

	// NewPlayerManager creates a PlayerManager with built-in states and transitions.
	NewPlayerManager = engine.NewPlayerManager

	// NewCommandGroup creates a named subcommand group for the console.
	NewCommandGroup = engine.NewCommandGroup

	// NewReflectConfig wraps a struct pointer as a Configurable using reflection.
	NewReflectConfig = engine.NewReflectConfig

	// NewTable creates a Table with the given column headers for console output.
	NewTable = engine.NewTable

	// FmtDuration formats a time.Duration as a human-readable string.
	FmtDuration = engine.FmtDuration

	// States returns a StateMask containing the given PlayerState values.
	States = engine.States

	// WithGuard returns a HandlerOption that adds a guard function to an input handler.
	WithGuard = engine.WithGuard

	// NewCoordinator creates a Coordinator from the given Config. Call Start(ctx) to run.
	NewCoordinator = universe.NewCoordinator

	// NewWorldBase creates a WorldBase with the given engine, cell, nodeID, AoI radius,
	// spatial grid, and replication registry. Embed in your game world struct.
	NewWorldBase = universe.NewWorldBase

	// NewReplicationRegistry creates an empty registry for cross-node component replication.
	NewReplicationRegistry = universe.NewReplicationRegistry

	// NewSideEffectRegistry creates an empty registry for cross-node side effect handlers.
	NewSideEffectRegistry = universe.NewSideEffectRegistry

	// ScanBorderWithRegistry scans border entities using a ReplicationRegistry and returns
	// serialized ReplicaFrames grouped by destination node.
	ScanBorderWithRegistry = universe.ScanBorderWithRegistry

	// ApplyReplicasWithRegistry applies incoming replica snapshots using a ReplicationRegistry.
	ApplyReplicasWithRegistry = universe.ApplyReplicasWithRegistry

	// UnmarshalCollider deserializes a Collider from bytes.
	UnmarshalCollider = universe.UnmarshalCollider

	// MarshalTransferFrame serializes a TransferFrame to bytes for cross-node transfer.
	MarshalTransferFrame = universe.MarshalTransferFrame

	// UnmarshalTransferFrame deserializes a TransferFrame from bytes.
	UnmarshalTransferFrame = universe.UnmarshalTransferFrame

	// MarshalSideEffects serializes a slice of side effects to bytes.
	MarshalSideEffects = universe.MarshalSideEffects

	// UnmarshalSideEffects deserializes side effects from bytes.
	UnmarshalSideEffects = universe.UnmarshalSideEffects

	// MeshNodeID computes the canonical node ID string for a cell coordinate.
	MeshNodeID = universe.MeshNodeID

	// NewReplicationSystem creates a ReplicationSystem with the given configuration.
	NewReplicationSystem = system.NewReplicationSystem

	// AutoReplicator creates an EntityReplicator from composable component bindings.
	AutoReplicator = system.AutoReplicator

	// EntryPosition uses the spatial entry's X/Y as two float32 fields.
	EntryPosition = system.EntryPosition

	// ViewerRelativePos computes world-absolute position relative to the viewer's cell.
	ViewerRelativePos = system.ViewerRelativePos

	// ViewerRelativePosWithCellSize is like ViewerRelativePos but uses a dynamic
	// cell size callback. Use for dynamic cell partitioning.
	ViewerRelativePosWithCellSize = system.ViewerRelativePosWithCellSize

	// QVelocity quantizes a Velocity component's X/Y as two int16 fields.
	QVelocity = system.QVelocity

	// QAngle quantizes a Rotation component's angle as a uint16 field.
	QAngle = system.QAngle

	// QSize quantizes a Collider's Radius as a uint16 field.
	QSize = system.QSize

	// MeshState writes entity mesh ownership (meshState + ownerNode) as 2 bytes.
	// Values use the enginepb.EntityMeshState enum as single source of truth.
	MeshState = system.MeshState

	// SetMoveTarget converts world-absolute coordinates to cell-local and activates.
	SetMoveTarget = system.SetMoveTarget

	// SetMoveTargetWithCellSize converts world-absolute coordinates to cell-local
	// using the given cell size. Use for dynamic cell partitioning where cell sizes vary.
	SetMoveTargetWithCellSize = system.SetMoveTargetWithCellSize

	// CancelMoveTarget deactivates movement.
	CancelMoveTarget = system.CancelMoveTarget

	// NewReplicatorRegistry creates an empty registry mapping entity types to replicators.
	NewReplicatorRegistry = system.NewReplicatorRegistry

	// NewBinaryFrameWriter creates a FrameWriter that encodes replication frames as binary.
	NewBinaryFrameWriter = system.NewBinaryFrameWriter

	// NewPlayerViewerSource creates a ViewerSource backed by PlayerManager sessions.
	NewPlayerViewerSource = system.NewPlayerViewerSource

	// CellRelativePos converts a world position to cell-relative coordinates.
	CellRelativePos = system.CellRelativePos

	// AckReliable is the AckMode for TCP/WebSocket: baselines auto-advance on send.
	AckReliable = system.AckReliable

	// AckExplicit is the AckMode for UDP: baselines advance only when AckSequence() is called.
	AckExplicit = system.AckExplicit

	// NewNodeMetrics creates a per-node metrics collector for tick timing, entity counts,
	// and bandwidth tracking.
	NewNodeMetrics = metrics.NewNodeMetrics

	// MetricsHandler returns an http.Handler that serves Prometheus-compatible metrics.
	MetricsHandler = metrics.Handler

	// NewConnManager creates a connection manager for WebSocket and UDP transports.
	NewConnManager = net.NewConnManager

	// NewUDPServer creates a UDP server bound to the given address.
	NewUDPServer = net.NewUDPServer

	// NewHashGrid creates a spatial hash grid with the given bucket size for AoI
	// queries and collision detection.
	NewHashGrid = spatial.NewHashGrid

	// NewLogger creates a Logger with the specified categories enabled by default.
	NewLogger = logger.New

	// MeshCategories lists all framework-level log categories (mesh:*, net:*, engine:*).
	// These are auto-registered by WorldBase; games can reference them for initial enable lists.
	MeshCategories = universe.MeshCategories

	// StartupCategories are always enabled so server lifecycle is visible
	// (mesh:node, engine:loop, net:conn).
	StartupCategories = universe.StartupCategories

	// NewOpRouter creates an operation router that dispatches channel-0x01 messages
	// to registered handlers on worker goroutines.
	NewOpRouter = ops.NewRouter

	// NewPlayerSessions creates a thread-safe connID-to-username map for OpRouter.
	NewPlayerSessions = ops.NewPlayerSessions

	// NewOrderBookService creates a generic order matching service with the given config.
	NewOrderBookService = orderbook.NewService

	// DefaultOrderBookConfig returns sensible marketplace defaults (2% tax, 7-day expiry, etc.).
	DefaultOrderBookConfig = orderbook.DefaultConfig

	// OpenBolt opens or creates a BoltDB database at the given path.
	OpenBolt = persist.OpenBolt

	// NewAsyncWriter wraps a Store for non-blocking writes with the given buffer size.
	NewAsyncWriter = persist.NewAsyncWriter
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

var (
	// WithVelocity sets initial velocity when spawning via WorldBase.SpawnEntity.
	WithVelocity = universe.WithVelocity

	// WithCollider attaches a Collider component when spawning via WorldBase.SpawnEntity.
	WithCollider = universe.WithCollider

	// WithEntityKind sets the EntityKind component when spawning via WorldBase.SpawnEntity.
	WithEntityKind = universe.WithEntityKind

	// WithRotation sets initial rotation when spawning via WorldBase.SpawnEntity.
	WithRotation = universe.WithRotation

	// WithComponents auto-adds zero-value components for all components registered
	// on the entity's EntityKindDef. Requires WithEntityKind to be set.
	WithComponents = universe.WithComponents

	// WithoutSpatial prevents SpawnEntity from auto-registering with the spatial grid.
	WithoutSpatial = universe.WithoutSpatial

	// ChannelEvent is the channel byte (0x00) for game event frames.
	ChannelEvent = net.ChannelEvent

	// ChannelOperation is the channel byte (0x01) for request/response operation frames.
	ChannelOperation = net.ChannelOperation

	// SideBuy represents the buy side of an order book.
	SideBuy = orderbook.SideBuy

	// SideSell represents the sell side of an order book.
	SideSell = orderbook.SideSell

	// ShapeCircle is the circle collision shape constant.
	ShapeCircle = spatial.ShapeCircle

	// ShapeRect is the oriented rectangle collision shape constant.
	ShapeRect = spatial.ShapeRect

	// StatePending is the initial player state before login is processed.
	StatePending = engine.StatePending

	// StateActive is the player state after successful login (normal gameplay).
	StateActive = engine.StateActive

	// StateDead is the player state after death (awaiting respawn).
	StateDead = engine.StateDead

	// StateTransferring is the player state during cross-node transfer.
	StateTransferring = engine.StateTransferring

	// StateDisconnected is the player state after network disconnect (grace period).
	StateDisconnected = engine.StateDisconnected

	// ErrInvalidTransition is returned when a PlayerManager state transition is not allowed.
	ErrInvalidTransition = engine.ErrInvalidTransition

	// ErrTransitionGuardFailed is returned when a state transition's guard function returns false.
	ErrTransitionGuardFailed = engine.ErrTransitionGuardFailed

	// ErrLoginPending is returned when a login is attempted while another is in progress.
	ErrLoginPending = engine.ErrLoginPending

	// ErrNotFound is returned by Store.Get when the requested key does not exist.
	ErrNotFound = persist.ErrNotFound
)

// ---------------------------------------------------------------------------
// Generic functions (can't alias generic funcs in Go)
// ---------------------------------------------------------------------------

// RegisterComponent registers an ECS component type for automatic cross-node
// replication and transfer. IDs are auto-assigned in registration order.
func RegisterComponent[T any](reg *universe.ReplicationRegistry, m *ecs.Map1[T], opts ...universe.ComponentOption[T]) {
	universe.RegisterComponent(reg, m, opts...)
}

// WithMarshal overrides the default reflection-based marshal/unmarshal for a
// registered component with custom serialization functions.
func WithMarshal[T any](marshal func(*T) []byte, unmarshal func([]byte, *T)) universe.ComponentOption[T] {
	return universe.WithMarshal(marshal, unmarshal)
}

// WithPreMarshal registers a function that runs on a copy of the component
// before marshaling. Use to sanitize or transform data before serialization.
func WithPreMarshal[T any](fn func(*T)) universe.ComponentOption[T] {
	return universe.WithPreMarshal(fn)
}

// KindComponent registers a component type on an EntityKindDef for cross-node
// transfer, auto-fill on transfer receive, and client replication.
// This mmokit wrapper also stores a ComponentBinding for auto-discovery by
// NewNetworkSystem, so games don't need to manually build AutoReplicators.
func KindComponent[T any](def *universe.EntityKindDef, m *ecs.Map1[T], opts ...universe.ComponentOption[T]) {
	universe.KindComponent(def, m, opts...)
	def.NetworkBindings = append(def.NetworkBindings, system.Component(m))
}

// BuildReplicators constructs a ReplicatorRegistry from EntityKindDefs.
// Used for schema export and auto-discovery by NewNetworkSystem. The w and coord
// parameters are needed to create EngineBindings; coord may be nil for schema export.
func BuildReplicators(w *ecs.World, coord *universe.Coordinator, defs ...universe.EntityKindDef) *system.ReplicatorRegistry {
	replicators := system.NewReplicatorRegistry()
	for _, def := range defs {
		var bindings []system.ComponentBinding
		if def.EngineBindings != nil {
			if ebCfg, ok := def.EngineBindings.(*EngineBindingsConfig); ok {
				bindings = append(bindings, EngineBindings(w, coord, *ebCfg))
			}
		} else {
			bindings = append(bindings, EngineBindings(w, coord))
		}
		for _, nb := range def.NetworkBindings {
			if cb, ok := nb.(system.ComponentBinding); ok {
				bindings = append(bindings, cb)
			}
		}
		replicators.Register(system.AutoReplicator(def.Kind, bindings...))
	}
	return replicators
}

// Enqueue adds a typed event to a TickQueue. The event is available via
// Drain[T] or Peek[T] until the queue is drained.
func Enqueue[T any](q *engine.TickQueue, event T) {
	engine.Enqueue(q, event)
}

// Drain retrieves and clears all events of type T from a TickQueue.
func Drain[T any](q *engine.TickQueue) []T {
	return engine.Drain[T](q)
}

// Peek returns all events of type T from a TickQueue without clearing them.
func Peek[T any](q *engine.TickQueue) []T {
	return engine.Peek[T](q)
}

// NewInputRouter creates an InputRouter wired to the given Engine, using protobuf
// envelope parsing. Games needing a custom parser can call engine.NewInputRouter directly.
func NewInputRouter(eng *engine.Engine) *engine.InputRouter {
	return engine.NewInputRouter(eng, ProtoEnvelopeParser)
}

// ProtoEnvelopeParser unmarshals an enginepb.ClientEvent envelope, returning
// the event code and inner payload.
func ProtoEnvelopeParser(raw []byte) (uint32, []byte, error) {
	var evt enginepb.ClientEvent
	if err := proto.Unmarshal(raw, &evt); err != nil {
		return 0, nil, err
	}
	return evt.Code, evt.Data, nil
}

// Handle registers a typed protobuf handler on an InputRouter. The message
// type T is automatically unmarshaled from the payload before the handler is called.
// The states mask controls which player states accept this message code.
// Accepts any integer event code type (proto enums are int32).
func Handle[T any, P interface {
	*T
	proto.Message
}, C engine.EventCode](
	r *engine.InputRouter, code C, states engine.StateMask,
	fn func(ctx *engine.InputContext, msg P), opts ...engine.HandlerOption) {

	// Auto-capture proto message name for schema export.
	var zero P = new(T)
	name := string(proto.MessageName(zero))
	opts = append(opts, engine.WithProtoName(name))

	engine.Handle(r, code, states,
		func(data []byte) (P, error) {
			var msg P = new(T)
			if err := proto.Unmarshal(data, msg); err != nil {
				return nil, err
			}
			return msg, nil
		},
		fn, opts...)
}

// NewNetworkSystem returns a System factory that creates a ReplicationSystem
// with DefaultReplicationConfig pre-filled. The setup function receives the
// pre-filled config and typed game world — set Replicators, AoIRadius, and
// any optional fields (callbacks, dormancy, etc.) there.
//
//	coord.AddSystem("Network", mmokit.NewNetworkSystem(func(cfg *mmokit.ReplicationConfig, gw *BasicWorld) {
//	    cfg.Replicators = setupReplication(gw)
//	    cfg.AoIRadius = AoIRadius
//	}))
func NewNetworkSystem[W any](setup func(cfg *ReplicationConfig, gw W)) func() engine.System {
	return func() engine.System {
		return &networkSystem[W]{setup: setup}
	}
}

type networkSystem[W any] struct {
	engine.SystemBase
	setup   func(cfg *ReplicationConfig, gw W)
	replSys *ReplicationSystem
}

func (s *networkSystem[W]) Init() {
	gw := s.GameWorld().(W)
	// WorldBase-based games implement spatialGridProvider via SpatialGrid().
	var grid *spatial.HashGrid
	if sp, ok := s.GameWorld().(interface{ SpatialGrid() *spatial.HashGrid }); ok {
		grid = sp.SpatialGrid()
	}
	cfg := DefaultReplicationConfig(s.Engine(), grid)
	s.setup(&cfg, gw)

	// Auto-discover replicators from registered EntityKindDefs if none were
	// set explicitly by the setup callback.
	if cfg.Replicators == nil {
		if wb, ok := s.GameWorld().(interface {
			EntityKindDefs() map[uint8]*universe.EntityKindDef
			Coordinator() *universe.Coordinator
			ECSWorld() *ecs.World
		}); ok {
			defs := wb.EntityKindDefs()
			if len(defs) > 0 {
				defSlice := make([]universe.EntityKindDef, 0, len(defs))
				for _, d := range defs {
					defSlice = append(defSlice, *d)
				}
				cfg.Replicators = BuildReplicators(wb.ECSWorld(), wb.Coordinator(), defSlice...)
			}
		}
	}

	s.replSys = NewReplicationSystem(cfg)
}

func (s *networkSystem[W]) Update(dt float32) {
	s.replSys.Update(dt)
}

// ReplicationSystem returns the underlying ReplicationSystem. Games that need
// post-init access (e.g. for farewell packets) can type-assert the System.
func (s *networkSystem[W]) ReplicationSystem() *ReplicationSystem {
	return s.replSys
}

// NewInputSystem returns a System factory for use with Coordinator.AddSystem.
// The setup function receives the InputRouter and the game world (type-asserted
// to W) — register handlers there. The framework handles router creation and
// per-tick processing.
func NewInputSystem[W any](setup func(*engine.InputRouter, W)) func() engine.System {
	return func() engine.System {
		return &inputSystem[W]{setup: setup}
	}
}

type inputSystem[W any] struct {
	engine.SystemBase
	setup  func(*engine.InputRouter, W)
	router *engine.InputRouter
}

func (s *inputSystem[W]) Init() {
	s.router = NewInputRouter(s.Engine())
	s.setup(s.router, s.GameWorld().(W))
}

func (s *inputSystem[W]) Update(dt float32) {
	s.router.ProcessInput()
}

// MakeEvent builds a channel-0x00 frame: [0x00] + ServerEvent{code, data}.
// The payload is protobuf-marshaled before being placed in the ServerEvent.
// Returns nil on marshal error.
func MakeEvent(code uint32, payload proto.Message) []byte {
	var inner []byte
	if payload != nil {
		var err error
		inner, err = proto.Marshal(payload)
		if err != nil {
			log.Printf("MakeEvent: marshal payload: %v", err)
			return nil
		}
	}
	evt := &enginepb.ServerEvent{
		Code: code,
		Data: inner,
	}
	evtData, err := proto.Marshal(evt)
	if err != nil {
		log.Printf("MakeEvent: marshal event: %v", err)
		return nil
	}
	frame := make([]byte, 1+len(evtData))
	frame[0] = ChannelEvent
	copy(frame[1:], evtData)
	return frame
}

// MakeEventRaw builds a channel-0x00 frame with raw bytes as the payload.
// Unlike MakeEvent, the data is placed directly in the ServerEvent.Data field
// without protobuf-marshaling it first. Use for custom binary wire formats.
func MakeEventRaw(code uint32, data []byte) []byte {
	evt := &enginepb.ServerEvent{
		Code: code,
		Data: data,
	}
	evtData, err := proto.Marshal(evt)
	if err != nil {
		log.Printf("MakeEventRaw: marshal event: %v", err)
		return nil
	}
	frame := make([]byte, 1+len(evtData))
	frame[0] = ChannelEvent
	copy(frame[1:], evtData)
	return frame
}

// MakeOpResponse builds a channel-0x01 frame: [0x01] + OperationResponse.
// Fields: operation code, request ID (from client), return code (0 = success),
// error message (empty on success), and serialized payload bytes.
func MakeOpResponse(code, reqID uint32, returnCode int32, errorMsg string, payload []byte) []byte {
	resp := &enginepb.OperationResponse{
		Code:       code,
		RequestId:  reqID,
		ReturnCode: returnCode,
		ErrorMsg:   errorMsg,
		Data:       payload,
	}
	respData, err := proto.Marshal(resp)
	if err != nil {
		log.Printf("MakeOpResponse: marshal response: %v", err)
		return nil
	}
	frame := make([]byte, 1+len(respData))
	frame[0] = ChannelOperation
	copy(frame[1:], respData)
	return frame
}

// Component creates a ComponentBinding by reflecting on T's net:"..." struct tags.
func Component[T any](ecsMap *ecs.Map1[T]) ComponentBinding {
	return system.Component(ecsMap)
}

// OptionalComponent is like Component but writes zero bytes if the component is absent.
func OptionalComponent[T any](ecsMap *ecs.Map1[T]) ComponentBinding {
	return system.OptionalComponent(ecsMap)
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
