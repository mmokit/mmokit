// Package mmokit is a single-import facade for the MMO engine.
// It re-exports types from all pkg/ sub-packages so that games (and the
// internal game code) can use one import instead of 5-7 aliased ones.
//
// For ECS queries and custom systems, also import "github.com/mlange-42/ark/ecs"
// since generic types like ecs.Map1[T] and ecs.Filter2[A,B] cannot be aliased.
package mmokit

import (
	"context"
	"fmt"
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
	"github.com/zenion/mmoserver/pkg/persist/postgres"
	"github.com/zenion/mmoserver/pkg/spatial"
	"github.com/zenion/mmoserver/pkg/replication"
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
// PreFlush, PostFlush, ClearTickState, and PostTick.
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
// engine.RunOnLoop to ensure thread safety.
type Console = engine.Console

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

// CellMetrics collects per-cell observability data: tick timing, entity counts,
// and bandwidth. Write methods are zero-alloc on the hot path; Snapshot() allocates
// and is intended for low-frequency scraping.
type CellMetrics = metrics.CellMetrics

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
// (summary, list, get, remove). All callbacks run on the game loop via engine.RunOnLoop.
type EntityOpts = engine.EntityOpts

// EntityInfo is a summary of an entity returned by EntityOpts callbacks.
type EntityInfo = engine.EntityInfo

// PlayerManager owns player sessions and enforces lifecycle state transitions
// (pending -> active -> dead, transferring, disconnected). Supports custom states,
// guards, actions, and OnEnter/OnExit callbacks.
type PlayerManager = engine.PlayerManager

// PlayerSession tracks a single player's connection ID, username, lifecycle state,
// ECS entity, and arbitrary Data payload.
type PlayerSession = engine.PlayerSession

// PlayerState represents a player's lifecycle state (uint8). Built-in states:
// StatePending, StateActive, StateTransferring, StateDisconnected.
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
// ConnID, and ECS Entity (may be zero-value for players without an active entity).
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
// dynamic cell partitioning. Dynamic partitioning is OFF by default — games
// opt in by assigning this to Config.DynamicPartitioning.
var DefaultPartitionConfig = universe.DefaultPartitionConfig

// DisabledPartitionConfig returns a non-nil PartitionConfig with auto-split
// and auto-merge flags cleared. Nil DynamicPartitioning is now the canonical
// "off" — this helper only exists for callers that want a non-nil sentinel
// (e.g. to carry other runtime knobs while keeping auto-split/merge off).
var DisabledPartitionConfig = universe.DisabledPartitionConfig

// HandleLogin builds a LoginHandler from a proto message type + login event
// code. The engine handles envelope decoding, code matching, and payload
// unmarshal; the game callback only extracts the username and optional
// session data from the typed message. See universe.HandleLogin for details.
func HandleLogin[M any, PM interface {
	*M
	proto.Message
}](code uint32, extract func(PM) (string, any, error)) LoginHandler {
	return universe.HandleLogin(code, extract)
}

// ValidateUsername normalizes a raw username (trim + lowercase) and rejects
// empty names or names longer than maxLen (0 = no length cap). Returns
// ErrLoginPending on failure so the login stays queued for the next tick.
var ValidateUsername = universe.ValidateUsername

// Config holds all Process configuration: grid dimensions (CellsX, CellsY),
// cell size, tick rate, AoI radius, world factory, console options, and more.
// Zero values use sensible defaults.
type Config = universe.Config

// ClientRenderMode declares how the client SDK should render
// replication frames. See universe.ClientRenderMode.
type ClientRenderMode = universe.ClientRenderMode

// Client render mode constants.
const (
	ClientRenderInterpolated = universe.ClientRenderInterpolated
	ClientRenderSnap         = universe.ClientRenderSnap
)

// GameWorld is the interface a game must implement to use the server meshing
// infrastructure. Methods handle entity serialization, transfers, replication,
// cross-cell actions, and chat. Embed WorldBase for working defaults.
type GameWorld = universe.GameWorld

// WorldBase provides default implementations for all GameWorld interface methods.
// Embed it in your game world struct to get working multi-node support out of the
// box, including entity spawning, border replication, and cross-cell transfers.
type WorldBase = universe.WorldBase

// Process manages multiple Node instances in a grid topology, routes player
// connections to the correct node, and coordinates entity transfers between nodes.
// Call Start(ctx) to run (blocks until shutdown).
type Process = universe.Process

// ClusterCellInfo describes one cell's identity and its owning host —
// returned by Process.ClusterCells / WorldBase.ClusterCells. Games
// use this to build their own SE_CELL_TOPOLOGY frames (the engine no
// longer ships a built-in topology broadcaster).
type ClusterCellInfo = universe.ClusterCellInfo

// Role identifies a single responsibility a process can run. A process has
// a set of roles (Roles) expressed as a bitmask. See universe.ParseRoles
// for the accepted CLI syntax ("coordinator,gateway,host" etc.).
type Role = universe.Role

// Roles is a bitmask set of Role values returned by ParseRoles.
type Roles = universe.Roles

// Individual Role constants re-exported from pkg/universe for CLI plumbing.
const (
	RoleCoordinator = universe.RoleCoordinator
	RoleHost        = universe.RoleHost
	RoleGateway     = universe.RoleGateway
)

// ParseRoles parses a CLI --mode string into a Roles bitmask. See the
// universe package for the accepted syntax and combination rules.
var ParseRoles = universe.ParseRoles

// Cell is a self-contained game simulation owning one cell in the mesh grid.
// Each cell runs its own ECS world, game loop, and systems independently.
type Cell = universe.Cell

// Bridge abstracts multi-cell coordination: entity transfers, replica updates,
// chat relay, spawn requests, and cross-cell actions. In single-cell mode, use
// NoopBridge.
type Bridge = universe.Bridge

// NoopBridge is a no-op Bridge implementation for single-cell mode.
// All methods are safe to call but do nothing.
type NoopBridge = universe.NoopBridge

// NeighborInfo describes a neighbor node's cell offset (DX, DY) relative to
// the current node. Used by border replication scanning.
type NeighborInfo = universe.NeighborInfo

// SpawnOption configures an optional component when spawning an entity via
// WorldBase.SpawnEntity (e.g. WithVelocity, WithCollider, WithRotation).
type SpawnOption = universe.SpawnOption

// BoundaryWorld is the interface needed by BoundarySystem to serialize entities
// and initiate cross-cell transfers. WorldBase implements this automatically.
type BoundaryWorld = universe.BoundaryWorld

// BoundarySystem normalizes entity positions into [0, CellSize) and initiates
// cross-cell transfers when entities cross cell boundaries.
type BoundarySystem = universe.BoundarySystem

// TransferFrame is the wire format for entity transfers between nodes. Contains
// core fields (position, velocity, rotation, collider, cell, IDs) plus a
// Components slice for game-specific serialized data.
type TransferFrame = universe.TransferFrame

// ComponentSlice holds a single component's serialized data (ID + bytes) within
// a TransferFrame.
type ComponentSlice = universe.ComponentSlice

// CrossCellAction is a request sent to the authoritative node when a local entity
// acts on a replica. The authoritative node processes it and returns an ActionResult.
type CrossCellAction = universe.CrossCellAction

// ActionResult is the response sent back to the originating node after a
// CrossCellAction is processed, including success flag, payload, and side effects.
type ActionResult = universe.ActionResult

// ActionType is a game-defined uint16 identifier for a cross-cell action kind.
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

// SideEffectCollector accumulates side effects during a cross-cell action execution.
// Not thread-safe (only used on the game loop goroutine).
type SideEffectCollector = universe.SideEffectCollector

// SideEffectRegistry maps side effect types to their handlers on the receiving node.
type SideEffectRegistry = universe.SideEffectRegistry

// SideEffectHandler processes a single side effect type on the originating node
// after receiving an ActionResult. The Handle closure captures typed game state.
type SideEffectHandler = universe.SideEffectHandler

// SideEffectType is a game-defined uint16 identifier for a side effect kind.
type SideEffectType = universe.SideEffectType

// ConsoleOpts provides game-specific console configuration for the Process.
// All fields are optional (omit what your game doesn't need).
type ConsoleOpts = universe.ConsoleOpts

// LoginHandler parses login messages and returns the username.
// Return ErrLoginPending if no valid login message found yet.
type LoginHandler = universe.LoginHandler

// SpawnResolver resolves a username to a world-space spawn position. Called
// once per login on the process owning playerDB (typically the coordinator).
// Returns ok=false when the user has no saved position; the gateway then
// falls back to Config.DefaultSpawn.
type SpawnResolver = universe.SpawnResolver

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

// Location is a world-space anchor for spawn/respawn/teleport targets.
// See coords.Location for the full doc.
type Location = coords.Location

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

// OpEventCode is any integer type usable as an operation code (proto enums are int32).
type OpEventCode = ops.EventCode

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
// Persistence (pkg/persist + pkg/persist/postgres)
// ---------------------------------------------------------------------------

// PlayerRepository persists player state. See persist.PlayerRepository.
type PlayerRepository = persist.PlayerRepository

// MarketRepository persists order book state. See persist.MarketRepository.
type MarketRepository = persist.MarketRepository

// ConfigRepository persists the singleton GameConfig blob.
type ConfigRepository = persist.ConfigRepository

// PlayerSnapshot is the persistence-layer representation of a player.
type PlayerSnapshot = persist.PlayerSnapshot

// EquipmentSnapshot is the equipped-gear subset of player state.
type EquipmentSnapshot = persist.EquipmentSnapshot

// OrderRecord is the persistence-layer representation of a market order.
type OrderRecord = persist.OrderRecord

// TradeRecord is one row of the market trade audit log.
type TradeRecord = persist.TradeRecord

// ConfigSnapshot is the persistence-layer representation of the singleton config.
type ConfigSnapshot = persist.ConfigSnapshot

// PostgresStore is the PostgreSQL-backed persistence root. Open one
// via mmokit.OpenPostgres and pass its Players()/Market()/Config()
// handles to the game wiring.
type PostgresStore = postgres.Store

// ---------------------------------------------------------------------------
// Systems (pkg/system)
// ---------------------------------------------------------------------------

// PhysicsSystem integrates velocity into position each tick. Skips Ghost and Replica entities.
type PhysicsSystem = system.PhysicsSystem

// LifetimeSystem despawns entities whose Lifetime component has expired.
// Skips Ghost and Replica entities.
type LifetimeSystem = system.LifetimeSystem

// ReplicationSystem manages per-viewer AoI visibility, hash-based diff detection,
// snapshot-based delta encoding, and frame dispatch to connected clients.
type ReplicationSystem = system.ReplicationSystem

// ClusterClock is the minimum surface ReplicationSystem needs from a
// cluster-coherent wall clock. pkg/universe.ClusterClock satisfies this
// structurally via its Now() method — games pass coord.ClusterClock
// directly into DefaultReplicationConfig.
type ClusterClock = system.ClusterClock

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
type AckMode = replication.AckMode

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
// boilerplate fields pre-filled: World, SpatialGrid, Viewers, Frame,
// GetTick, and ClusterClock. Games set game-specific fields (Replicators,
// AoIRadius, callbacks, etc.) on the returned struct before passing it to
// NewReplicationSystem. The clock argument is typically the Process's
// shared *universe.ClusterClock (from Coordinator/host/WorldBase) — it
// satisfies the small system.ClusterClock interface structurally.
func DefaultReplicationConfig(eng *engine.Engine, grid *spatial.HashGrid, clock system.ClusterClock) ReplicationConfig {
	return ReplicationConfig{
		World:        eng.ECS,
		SpatialGrid:  grid,
		Viewers:      system.NewPlayerViewerSource(eng.ECS, eng.Players, engine.StateActive),
		Frame:        system.NewBinaryFrameWriter(eng.ConnMgr, uint32(enginepb.ServerEventCode_SE_DELTA_WORLD_UPDATE), MakeEventRaw),
		GetTick:      func() uint32 { return eng.Tick },
		ClusterClock: clock,
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

	// IncludeMeshState enables the MeshState binding (meshState + ownerNode bytes).
	// Disabled by default — most games should not expose mesh state to clients.
	// Enable for debug overlays that need to visualize server ownership.
	IncludeMeshState bool
}

// EngineBindings returns a ComponentBinding that bundles the standard engine-level
// replication fields: position, quantized velocity, quantized size, and mesh state.
// GridWidth is auto-discovered from the Process. Games append game-specific
// Component[T] bindings after this.
//
// If cfg is omitted, all defaults are used.
func EngineBindings(w *ecs.World, coord *universe.Process, cfg ...EngineBindingsConfig) ComponentBinding {
	var c EngineBindingsConfig
	if len(cfg) > 0 {
		c = cfg[0]
	}
	var gridWidth uint32
	if coord != nil {
		gridWidth = coord.GridWidth()
	}
	return system.EngineBindings(w, system.EngineBindingsConfig{
		GridWidth:        gridWidth,
		VelQuantScale:    c.VelQuantScale,
		SizeQuantScale:   c.SizeQuantScale,
		CellSizeFn:       c.CellSizeFn,
		IncludeMeshState: c.IncludeMeshState,
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

	// New creates a Process from the given Config. Call Start(ctx) to run.
	New = universe.New

	// NewWorldBase creates a WorldBase with the given engine, cell, nodeID, AoI radius,
	// spatial grid, and replication registry. Embed in your game world struct.
	NewWorldBase = universe.NewWorldBase

	// NewReplicationRegistry creates an empty registry for cross-cell component replication.
	NewReplicationRegistry = universe.NewReplicationRegistry

	// NewSideEffectRegistry creates an empty registry for cross-cell side effect handlers.
	NewSideEffectRegistry = universe.NewSideEffectRegistry

	// UnmarshalCollider deserializes a Collider from bytes.
	UnmarshalCollider = universe.UnmarshalCollider

	// MarshalTransferFrame serializes a TransferFrame to bytes for cross-cell transfer.
	MarshalTransferFrame = universe.MarshalTransferFrame

	// UnmarshalTransferFrame deserializes a TransferFrame from bytes.
	UnmarshalTransferFrame = universe.UnmarshalTransferFrame

	// MarshalSideEffects serializes a slice of side effects to bytes.
	MarshalSideEffects = universe.MarshalSideEffects

	// UnmarshalSideEffects deserializes side effects from bytes.
	UnmarshalSideEffects = universe.UnmarshalSideEffects

	// MeshCellID computes the canonical cell ID string for a cell coordinate.
	MeshCellID = universe.MeshCellID

	// ParseCellID parses any of the supported cell-ID string formats
	// (X_Y, dN_X_Y, cell_X_Y, cell_dN_X_Y) into a CellID.
	ParseCellID = universe.ParseCellID

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
	AckReliable = replication.AckReliable

	// AckExplicit is the AckMode for UDP: baselines advance only when AckSequence() is called.
	AckExplicit = replication.AckExplicit

	// NewCellMetrics creates a per-cell metrics collector for tick timing, entity counts,
	// and bandwidth tracking.
	NewCellMetrics = metrics.NewCellMetrics

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

	// OpenPostgres opens a PostgreSQL connection pool, pings the
	// server, runs any pending schema migrations, and returns a
	// ready-to-use PostgresStore. The caller must call Close when
	// finished.
	OpenPostgres = postgres.Open
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

	// WithFacing sets the entity's facing angle (radians) from a Location.
	WithFacing = universe.WithFacing

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

	// StateTransferring is the player state during cross-cell transfer.
	StateTransferring = engine.StateTransferring

	// StateDisconnected is the player state after network disconnect (grace period).
	StateDisconnected = engine.StateDisconnected

	// ErrInvalidTransition is returned when a PlayerManager state transition is not allowed.
	ErrInvalidTransition = engine.ErrInvalidTransition

	// ErrTransitionGuardFailed is returned when a state transition's guard function returns false.
	ErrTransitionGuardFailed = engine.ErrTransitionGuardFailed

	// ErrLoginPending is returned by LoginHandler when no login message has arrived yet.
	ErrLoginPending = universe.ErrLoginPending

	// ErrNotFound is returned by repository Load methods when the
	// requested record doesn't exist.
	ErrNotFound = persist.ErrNotFound
)

// ---------------------------------------------------------------------------
// Generic functions (can't alias generic funcs in Go)
// ---------------------------------------------------------------------------

// RegisterComponent registers an ECS component type for automatic cross-cell
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

// KindComponent registers a component type on an EntityKindDef for cross-cell
// transfer, auto-fill on transfer receive, and client replication.
// This mmokit wrapper also stores a ComponentBinding for auto-discovery by
// NewNetworkSystem, so games don't need to manually build AutoReplicators.
func KindComponent[T any](def *universe.EntityKindDef, m *ecs.Map1[T], opts ...universe.ComponentOption[T]) {
	universe.KindComponent(def, m, opts...)
	def.NetworkBindings = append(def.NetworkBindings, system.Component(m))
}

// KindComponentWithBinding registers a component type for cross-cell transfer
// (identical to KindComponent) but uses a caller-supplied ComponentBinding for
// client replication instead of the default reflection-based binding. Use for
// components that need var-tail encoding or other non-reflection serialization.
// The binding's component type must match T.
func KindComponentWithBinding[T any](def *universe.EntityKindDef, m *ecs.Map1[T], binding system.ComponentBinding, opts ...universe.ComponentOption[T]) {
	universe.KindComponent(def, m, opts...)
	def.NetworkBindings = append(def.NetworkBindings, binding)
}

// KindComponentLocalOnly registers a component that is added locally after transfer
// (via EnsureEntityKindComponents) but never serialized for cross-cell transfer or
// client network replication. Use for components like PlayerInput that are always
// created fresh on the receiving node.
func KindComponentLocalOnly[T any](def *universe.EntityKindDef, m *ecs.Map1[T]) {
	universe.KindComponentLocalOnly(def, m)
	// No NetworkBinding — not replicated to clients
}

// BuildReplicators constructs a ReplicatorRegistry from EntityKindDefs.
// Used for schema export and auto-discovery by NewNetworkSystem. The w and coord
// parameters are needed to create EngineBindings; coord may be nil for schema export.
//
// Var-tail bindings (those implementing system.VarTailProvider) are automatically
// moved to the end of each entity's binding list so games don't need to worry
// about registration order. At most one var-tail binding is allowed per entity;
// AutoReplicator will panic if there are more.
func BuildReplicators(w *ecs.World, coord *universe.Process, defs ...universe.EntityKindDef) *system.ReplicatorRegistry {
	replicators := system.NewReplicatorRegistry()
	for _, def := range defs {
		var bindings []system.ComponentBinding
		// EngineBindingsConfig.IncludeMeshState is honored as-declared on
		// the EntityKindDef. There is intentionally no runtime override:
		// schema-export and runtime must produce identical wire bytes.
		// Games that want the meshState byte on entities set
		// IncludeMeshState: true in their EntityKindDef.
		if def.EngineBindings != nil {
			if ebCfg, ok := def.EngineBindings.(*EngineBindingsConfig); ok {
				bindings = append(bindings, EngineBindings(w, coord, *ebCfg))
			}
		} else {
			var cfg EngineBindingsConfig
			bindings = append(bindings, EngineBindings(w, coord, cfg))
		}

		// Partition game bindings: var-tail bindings go to the end.
		var regular, varTails []system.ComponentBinding
		for _, nb := range def.NetworkBindings {
			cb, ok := nb.(system.ComponentBinding)
			if !ok {
				continue
			}
			if _, isVarTail := cb.(system.VarTailProvider); isVarTail {
				varTails = append(varTails, cb)
			} else {
				regular = append(regular, cb)
			}
		}
		bindings = append(bindings, regular...)
		bindings = append(bindings, varTails...)

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
// with DefaultReplicationConfig pre-filled. Replicators are auto-discovered
// from registered EntityKindDefs. AoIRadius is inherited from the coordinator
// config. Use NewNetworkSystemWith for custom configuration.
func NewNetworkSystem() func() engine.System {
	return func() engine.System {
		return &defaultNetworkSystem{}
	}
}

type defaultNetworkSystem struct {
	engine.SystemBase
	replSys *ReplicationSystem
}

func (s *defaultNetworkSystem) Init() {
	var grid *spatial.HashGrid
	if sp, ok := s.GameWorld().(interface{ SpatialGrid() *spatial.HashGrid }); ok {
		grid = sp.SpatialGrid()
	}
	cfg := DefaultReplicationConfig(s.Engine(), grid, clockFromGameWorld(s.GameWorld()))
	if ar, ok := s.GameWorld().(interface{ GetAoIRadius() float32 }); ok {
		cfg.AoIRadius = ar.GetAoIRadius()
	}
	if wb, ok := s.GameWorld().(interface{ Process() *universe.Process }); ok {
		wireBlinkDetector(&cfg, wb.Process(), s.Engine().Log)
	}
	autoDiscoverReplicators(s.GameWorld(), &cfg)
	if cfg.Replicators == nil {
		return // no entity kinds registered — nothing to replicate
	}
	s.replSys = NewReplicationSystem(cfg)
}

// clockFromGameWorld extracts the Process's shared ClusterClock when the
// game world exposes a Process() method. Returns nil when absent — e.g.
// when a test wires a GameWorld shim without a coordinator — and the
// ReplicationSystem falls back to the local wall clock.
func clockFromGameWorld(gw any) system.ClusterClock {
	if wb, ok := gw.(interface{ Process() *universe.Process }); ok {
		if p := wb.Process(); p != nil {
			return p.ClusterClock
		}
	}
	return nil
}

func (s *defaultNetworkSystem) Update(dt float32) {
	if s.replSys != nil {
		s.replSys.Update(dt)
	}
}

func (s *defaultNetworkSystem) ReplicationSystem() *ReplicationSystem {
	return s.replSys
}

// NewNetworkSystemWith returns a System factory like NewNetworkSystem, but with
// a typed setup callback for custom configuration. The setup function receives
// the pre-filled config and typed game world — set Replicators, AoIRadius, and
// any optional fields (callbacks, dormancy, etc.) there.
//
//	coord.AddSystem("Network", mmokit.NewNetworkSystemWith(func(cfg *mmokit.ReplicationConfig, gw *MyWorld) {
//	    cfg.AoIRadius = 800
//	    cfg.OnEntityEnter = func(...) { ... }
//	}))
func NewNetworkSystemWith[W any](setup func(cfg *ReplicationConfig, gw W)) func() engine.System {
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
	var grid *spatial.HashGrid
	if sp, ok := s.GameWorld().(interface{ SpatialGrid() *spatial.HashGrid }); ok {
		grid = sp.SpatialGrid()
	}
	cfg := DefaultReplicationConfig(s.Engine(), grid, clockFromGameWorld(s.GameWorld()))
	if ar, ok := s.GameWorld().(interface{ GetAoIRadius() float32 }); ok {
		cfg.AoIRadius = ar.GetAoIRadius()
	}
	if wb, ok := s.GameWorld().(interface{ Process() *universe.Process }); ok {
		wireBlinkDetector(&cfg, wb.Process(), s.Engine().Log)
	}
	s.setup(&cfg, gw)
	if cfg.Replicators == nil {
		autoDiscoverReplicators(s.GameWorld(), &cfg)
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

// wireBlinkDetector populates cfg.BlinkDetectorTicks and OnBlinkDetected
// from the coordinator (if this world has one). Called from the network
// system Init paths.
func wireBlinkDetector(cfg *ReplicationConfig, coord *universe.Process, log *logger.Logger) {
	if coord == nil {
		return
	}
	ticks := coord.BlinkDetectorTicks()
	if ticks == 0 {
		return
	}
	cfg.BlinkDetectorTicks = ticks
	cfg.OnBlinkDetected = func(connID, netID uint32, ticksSinceRemove uint64) {
		mode := coord.InvariantMode()
		if mode == universe.InvariantOff {
			return
		}
		msg := fmt.Sprintf("blink: conn=%d netID=%d ticksSinceRemove=%d",
			connID, netID, ticksSinceRemove)
		log.Log(universe.CatEventsReplication, "[BLINK] %s", msg)
		if cl := coord.CommitLog(); cl != nil {
			cl.Append(universe.CommitEvent{
				Kind:      universe.EventInvariantViolation,
				StepIndex: -1,
				Step:      "no-blink-for-conn",
				Success:   false,
				Error:     msg,
				Context: map[string]string{
					"connID":           fmt.Sprintf("%d", connID),
					"netID":            fmt.Sprintf("%d", netID),
					"ticksSinceRemove": fmt.Sprintf("%d", ticksSinceRemove),
				},
			})
		}
		if mode == universe.InvariantPanic {
			panic("invariant no-blink-for-conn violated: " + msg)
		}
	}
}

// autoDiscoverReplicators populates cfg.Replicators from registered EntityKindDefs
// if no replicators were set explicitly.
func autoDiscoverReplicators(gw any, cfg *ReplicationConfig) {
	if cfg.Replicators != nil {
		return
	}
	if wb, ok := gw.(interface {
		EntityKindDefs() map[uint8]*universe.EntityKindDef
		Process() *universe.Process
		ECSWorld() *ecs.World
	}); ok {
		defs := wb.EntityKindDefs()
		if len(defs) > 0 {
			defSlice := make([]universe.EntityKindDef, 0, len(defs))
			for _, d := range defs {
				defSlice = append(defSlice, *d)
			}
			cfg.Replicators = BuildReplicators(wb.ECSWorld(), wb.Process(), defSlice...)
		}
	}
}

// NewInputSystem returns a System factory for use with Process.AddSystem.
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

// RegisterOp registers a typed operation handler on an OpRouter. It wraps
// ops.Register, capturing request and response proto type names for schema
// export via Router.Schema(). Prefer this over the untyped Router.Register for
// any operation that should appear in the SDK schema.
//
// Specify only the value types Req and Res; pointer types are inferred:
//
//	mmokit.RegisterOp[MarketBrowseRequest, MarketOrderBookResponse](router, code, "name", handler)
func RegisterOp[Req any, Res any, ReqP ops.ProtoMessage[Req], ResP ops.ProtoMessage[Res]](
	r *ops.Router, code uint32, name string,
	handler func(ctx *ops.OpContext, req ReqP) (ResP, error)) {
	ops.Register[Req, Res, ReqP, ResP](r, code, name, handler)
}


// Component creates a ComponentBinding by reflecting on T's net:"..." struct tags.
func Component[T any](ecsMap *ecs.Map1[T]) ComponentBinding {
	return system.Component(ecsMap)
}

// OptionalComponent is like Component but writes zero bytes if the component is absent.
func OptionalComponent[T any](ecsMap *ecs.Map1[T]) ComponentBinding {
	return system.OptionalComponent(ecsMap)
}

// ---------------------------------------------------------------------------
// Test-harness shim re-exports
//
// These wrap Process.HarnessXxx methods so multi-process integration
// tests (e.g. examples/4node-basic/mesh_e2e_test.go) can seed cell layout
// without importing the universe package directly.
// ---------------------------------------------------------------------------

// HarnessWaitForHost blocks until the named host has registered with this
// (coord-role) Process's host registry, or ctx expires.
func HarnessWaitForHost(c *universe.Process, ctx context.Context, hostID string) error {
	return c.HarnessWaitForHost(ctx, hostID)
}

// HarnessDispatchCellAssign sends NetIDRangeGrant + CellAssign to the named
// host for the given cell key on this (coord-role) Process.
func HarnessDispatchCellAssign(c *universe.Process, hostID, cellKey string) {
	c.HarnessDispatchCellAssign(hostID, cellKey)
}

// HarnessBroadcastPeerList forces an immediate PeerList broadcast on this
// (coord-role) Process to all registered hosts.
func HarnessBroadcastPeerList(c *universe.Process) {
	c.HarnessBroadcastPeerList()
}

// HarnessSetSettled bypasses the 5-second settle window on this (coord-role)
// Process so manual cell assignments are not stomped by the rebalance loop.
func HarnessSetSettled(c *universe.Process) {
	c.HarnessSetSettled()
}

// HarnessWaitForCellOnLocalHost blocks until the local Host on this
// (host-role) Process owns the named cell, or ctx expires.
func HarnessWaitForCellOnLocalHost(c *universe.Process, ctx context.Context, cellKey string) error {
	return c.HarnessWaitForCellOnLocalHost(ctx, cellKey)
}

// HarnessWaitForCellToHostMap blocks until every key in wantKeys is present
// in this (host-role) Process's cellToHostMap, or ctx expires.
func HarnessWaitForCellToHostMap(c *universe.Process, ctx context.Context, wantKeys []string) error {
	return c.HarnessWaitForCellToHostMap(ctx, wantKeys)
}

// HarnessLocalHostCells returns a snapshot of all *Cell instances on the local
// Host of this (host-role) Process. Returns nil if no local host exists.
func HarnessLocalHostCells(c *universe.Process) []*universe.Cell {
	return c.HarnessLocalHostCells()
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

// ProtocolOf returns the *Protocol from p.Protocol(), or nil if unset or if
// the stored value is not a *Protocol.
func ProtocolOf(p *Process) *Protocol {
	if proto, ok := p.Protocol().(*Protocol); ok {
		return proto
	}
	return nil
}

// ServerEventsOf returns the ServerEvents registry from the protocol stored in
// p, or nil if no protocol is set or the protocol has no registry.
func ServerEventsOf(p *Process) *ServerEvents {
	if proto := ProtocolOf(p); proto != nil {
		return proto.ServerEventsRegistry()
	}
	return nil
}
