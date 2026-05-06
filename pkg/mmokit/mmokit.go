package mmokit

// Package-level documentation lives in doc.go.

import (
	"context"
	"fmt"
	"log"

	"github.com/mlange-42/ark/ecs"
	"google.golang.org/protobuf/proto"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/cmdsys"
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
	"github.com/zenion/mmoserver/pkg/service"
	"github.com/zenion/mmoserver/pkg/spatial"
	"github.com/zenion/mmoserver/pkg/replication"
	"github.com/zenion/mmoserver/pkg/system"
	"github.com/zenion/mmoserver/pkg/universe"
)

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

// SystemBase is the generic base for all systems. Embed it with the game's
// typed world: `mmokit.SystemBase[*MyWorld]`. Engine-side systems that don't
// need world methods use `mmokit.SystemBase[any]`.
type SystemBase[W any] = engine.SystemBase[W]

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

// PlayerManager owns player sessions and enforces lifecycle state transitions
// (pending -> active -> dead, transferring, disconnected). Supports custom states,
// guards, actions, and OnEnter/OnExit callbacks.
type PlayerManager = engine.PlayerManager

// PlayerSession tracks a single player's connection ID, username, lifecycle state,
// ECS entity, and arbitrary Data payload.
type PlayerSession = engine.PlayerSession

// DebugFlag is a uint32 bitmask of enabled debug capabilities for a
// player session. Engine reserves bits 0-15; games reserve bits 16-31.
type DebugFlag = engine.DebugFlag

// DebugTopology covers cell-boundary overlay + AoI radius circle. The
// only built-in engine debug flag in v1.
const DebugTopology = engine.DebugTopology

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

// ---------------------------------------------------------------------------
// Universe (pkg/universe)
// ---------------------------------------------------------------------------

// CellID uniquely identifies a cell at any quadtree depth in the server mesh.
// Depth 0 is the original grid. X, Y are cell coordinates; Depth is the quadtree level.
type CellID = universe.CellID

// MeshCellID is the wire/internal string form of a CellID. See universe.MeshCellID
// for the typed string vs. plain string contract.
type MeshCellID = universe.MeshCellID

// InvariantMode controls how state-integrity violations are surfaced.
// Set on Config.InvariantMode. Production: InvariantOff. Dev/test:
// InvariantPanic so any latent regression fails loudly.
type InvariantMode = universe.InvariantMode

const (
	InvariantOff   = universe.InvariantOff
	InvariantLog   = universe.InvariantLog
	InvariantPanic = universe.InvariantPanic

	// StateBuiltinEnd is the first PlayerState value available for game-defined
	// custom states. Declare game states as compile-time consts off this anchor
	// so that input handler registrations (which read state IDs at process
	// startup) see correct values regardless of when PlayerManager.RegisterState
	// runs per-cell:
	//
	//	const (
	//	    StateDead    mmokit.PlayerState = mmokit.StateBuiltinEnd + iota
	//	    StateDocking
	//	    StateDocked
	//	)
	//
	// Games still need to call gw.Players.RegisterState in matching order to
	// populate name → state-ID mapping for state-name display.
	StateBuiltinEnd = engine.StateBuiltinEnd
)

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

// Config holds all Process configuration: grid dimensions (CellsX, CellsY),
// cell size, tick rate, AoI radius, world factory, console options, and more.
// Zero values use sensible defaults.
type Config = universe.Config

// GameWorld is the interface a game must implement to use the server meshing
// infrastructure. Methods handle entity serialization, transfers, replication,
// cross-cell actions, and chat. Embed Stage for working defaults.
type GameWorld = universe.GameWorld

// Stage provides default implementations for all GameWorld interface methods.
// Embed it in your game world struct to get working multi-node support out of the
// box, including entity spawning, border replication, and cross-cell transfers.
type Stage = universe.Stage

// BroadcastEvent is one queued auto-broadcast event awaiting end-of-tick
// dispatch. TypeID is the framework's reflect-codec type ID; Body is the
// reflect-codec payload; Anchors are NetIDs whose positions drive the
// AoI filter applied by the framework's network system at drain time.
// See Stage.BroadcastQueue.
type BroadcastEvent = universe.BroadcastEvent

// EncodeTypedEventFrame produces a single-event 0x00 typed-event frame:
//
//	[0x00][typeID:u32 LE][body_len:u32 LE][body]
var EncodeTypedEventFrame = universe.EncodeTypedEventFrame

// EncodeBatchedTypedEventFrame packs multiple BroadcastEvents into a single
// 0x00 typed-event frame. Returns nil for an empty list — callers must skip
// writing empty frames.
var EncodeBatchedTypedEventFrame = universe.EncodeBatchedTypedEventFrame

// WorldBase is a backward-compatibility alias for Stage. internal/game/ embeds
// *mmokit.WorldBase; this alias keeps that compiling while the rename is in flight.
// Slated for removal once internal/game is updated to embed *mmokit.Stage directly.
type WorldBase = universe.Stage

// Process manages multiple Node instances in a grid topology, routes player
// connections to the correct node, and coordinates entity transfers between nodes.
// Call Start() to run (blocks until shutdown).
type Process = universe.Process

// GrantDebug enables a debug flag on a player session both in-memory
// and in the persistent players row. Idempotent. Use from
// OnPlayerJoin to default-grant a flag to every player.
//
// The four `debug.*` console commands (grant/revoke/list/features)
// are auto-registered by Process.Build() when DBStore is configured;
// games no longer wire those manually.
var GrantDebug = universe.GrantDebug

// ErrUnknownDebugFlag is returned by GrantDebug when the supplied
// flag name isn't registered. Callers can check via errors.Is.
var ErrUnknownDebugFlag = universe.ErrUnknownDebugFlag

// ClusterCellInfo describes one cell's identity and its owning host —
// returned by Process.ClusterCells / Stage.ClusterCells. Games
// use this to build their own SE_DEBUG_INFO frames (the engine no
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
	RoleService     = universe.RoleService
)

// ParseRoles parses a CLI --mode string into a Roles bitmask. See the
// universe package for the accepted syntax and combination rules.
var ParseRoles = universe.ParseRoles

// ─── Player-target + entity-move facade ────────────────────────────────────
// These re-exports let game-side handlers use mmokit.ResolvePlayerTarget
// and Stage.MoveEntityTo without naming pkg/universe directly.

// MoveOpt configures a Stage.MoveEntityTo call. Build with the
// MoveBypassCooldown / MoveAsPlayer constructors below.
type MoveOpt = universe.MoveOpt

// MoveBypassCooldown skips HandoffCooldownTicks for an explicit teleport.
// Use for admin TP / scripted plot moves; natural boundary crossings
// keep the default cooldown.
var MoveBypassCooldown = universe.MoveBypassCooldown

// MoveAsPlayer attaches a player session's ConnID + Username to the
// emitted CrossingEvent so the destination cell can re-register the
// session on commit. Required for player-entity moves.
var MoveAsPlayer = universe.MoveAsPlayer

// PlayerTarget is the result of ResolvePlayerTarget. Exactly one of
// Online or Offline is non-nil when the player exists; both are nil
// when the player is unknown. DirtyMark is always non-nil.
type PlayerTarget = universe.PlayerTarget

// PlayerDataAccessor is the interface offline-player commands read +
// write through. Implemented by the game's persisted PlayerData.
type PlayerDataAccessor = universe.PlayerDataAccessor

// PlayerDataLocator is the universe-side hook the game installs at
// startup so ResolvePlayerTarget's offline branch can find players.
type PlayerDataLocator = universe.PlayerDataLocator

// ResolvePlayerTarget looks up a player across local cells (online
// branch) and falls back to the registered PlayerDataLocator (offline
// branch).
var ResolvePlayerTarget = universe.ResolvePlayerTarget

// RoutePlayerHomeOrOwner routes commands to the host owning an online
// player, or to a stable DB-bearing host when the player is offline.
const RoutePlayerHomeOrOwner = cmdsys.RoutePlayerHomeOrOwner

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
// Stage.SpawnEntity (e.g. WithVelocity, WithCollider, WithRotation).
type SpawnOption = universe.SpawnOption

// BoundaryWorld is the interface needed by BoundarySystem to serialize entities
// and initiate cross-cell transfers. Stage implements this automatically.
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
// acts on a replica. The authoritative node processes it.
type CrossCellAction = universe.CrossCellAction

// ActionType is a game-defined uint16 identifier for a cross-cell action kind.
type ActionType = universe.ActionType

// ReplicationRegistry tracks which ECS components should be replicated across nodes.
// Register components with RegisterComponent[T] to enable automatic border replication.
type ReplicationRegistry = universe.ReplicationRegistry

// ComponentReplicator handles one component type's replication: Scan serializes
// from a local entity, Apply updates an existing replica, Add attaches to a new replica.
type ComponentReplicator = universe.ComponentReplicator

// EntityKindDef describes an entity kind's components for transfer, client
// replication, and schema export. Built and registered automatically by
// mmokit.RegisterKind[T] from a typed component-bundle struct — game code
// never constructs one directly.
type EntityKindDef = universe.EntityKindDef

// ConsoleOpts provides game-specific console configuration for the Process.
// All fields are optional (omit what your game doesn't need).
type ConsoleOpts = universe.ConsoleOpts

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
// Command system (pkg/cmdsys)
// ---------------------------------------------------------------------------

// CommandRegistry holds typed command registrations consumed by the
// console + cmdsys dispatcher. Game code adds commands via RegisterCommand
// or by passing a *CommandRegistry to a registration function.
type CommandRegistry = cmdsys.Registry

// Command is the typed-command descriptor: verb, capability, route,
// args/result schema, and the handler closure.
type Command = cmdsys.Command

// CommandEnv carries dispatch-time context (caller identity, target cell,
// etc.) into a command handler.
type CommandEnv = cmdsys.Env

// CommandRouteKind selects how a command is dispatched (local, fanned out
// across all hosts, routed to a specific cell's owner, etc.).
type CommandRouteKind = cmdsys.RouteKind

const (
	RouteLocal         = cmdsys.RouteLocal
	RouteAllHosts      = cmdsys.RouteAllHosts
	RouteSpecificCell  = cmdsys.RouteSpecificCell
	RoutePlayerOwner   = cmdsys.RoutePlayerOwner
)

// CmdOnLoop is the ergonomic helper for cmdsys handlers that need ECS
// access — wraps engine.RunOnLoop and returns a typed result. Use:
//
//	return mmokit.CmdOnLoop(ctx, cell.Engine, func() (R, error) { ... })
func CmdOnLoop[R any](ctx context.Context, runner cmdsys.LoopRunner, fn func() (R, error)) (R, error) {
	return cmdsys.OnLoop(ctx, runner, fn)
}

// ---------------------------------------------------------------------------
// Services (pkg/service)
// ---------------------------------------------------------------------------

// ServiceKind is the descriptor a game registers to make a service kind
// available to the engine. Hand it to Process.RegisterService before Build.
type ServiceKind = service.Kind

// Service is the runtime interface a service kind's instance implements
// (Init / RegisterOps / Shutdown).
type Service = service.Service

// ServiceContext bundles the runtime dependencies handed to a Service at
// Init: logger, DB, role set, SendEvent hook, instance/kind identifiers.
type ServiceContext = service.Context

// OpCodes builds a ServiceKind.OpCodes slice from proto enum values
// (or any ~int32 / ~uint32) without per-element uint32 casts.
//
//	OpCodes: mmokit.OpCodes(
//	    basicpb.EchoOpCode_BOP_ECHO_PING,
//	    basicpb.EchoOpCode_BOP_ECHO_PERSIST,
//	)
func OpCodes[T ~int32 | ~uint32](codes ...T) []uint32 {
	return service.OpCodes(codes...)
}

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
// structurally via its Now() + TickTime methods — games pass
// coord.ClusterClock directly into DefaultReplicationConfig.
type ClusterClock = system.ClusterClock

// ClickToMoveSystem moves entities toward their MoveTarget at MoveParams.MaxSpeed.
type ClickToMoveSystem = system.ClickToMoveSystem

// DirectionMoveSystem moves entities using DirectionInput at MoveParams.MaxSpeed.
type DirectionMoveSystem = system.DirectionMoveSystem

// SpatialHooks provides optional per-tick callbacks for game-specific spatial logic.
type SpatialHooks = system.SpatialHooks

// NewSpatialSystem returns a SystemDef for the standard spatial grid update
// with no game-specific hooks. Queries Position+Collider+NetworkID, reads Rotation
// if present, registers/updates entities in the HashGrid each tick.
//
//	mmo.AddSystem(mmokit.NewSpatialSystem())
func NewSpatialSystem() SystemDef {
	return SystemDef{
		Name:    "Spatial",
		Factory: func() engine.System { return &system.SpatialSystem{} },
	}
}

// NewSpatialSystemWith returns a SystemDef with game-specific hooks.
// The hooks function runs at Init time with the typed game world and returns
// per-tick callbacks (PreTick, OnEntity, PostTick).
//
//	mmo.AddSystem(mmokit.NewSpatialSystemWith(func(gw *MyWorld) mmokit.SpatialHooks {
//	    return mmokit.SpatialHooks{
//	        OnEntity: func(entity ecs.Entity, entry mmokit.SpatialEntry) { ... },
//	    }
//	}))
func NewSpatialSystemWith[W any](hooks func(gw W) SpatialHooks) SystemDef {
	return SystemDef{
		Name: "Spatial",
		Factory: func() engine.System {
			sys := &system.SpatialSystem{}
			sys.SetInitHook(func(gw any) system.SpatialHooks {
				return hooks(gw.(W))
			})
			return sys
		},
	}
}

// NewPhysicsSystem returns a SystemDef for velocity→position integration.
func NewPhysicsSystem() SystemDef {
	return SystemDef{
		Name:    "Physics",
		Factory: func() engine.System { return &PhysicsSystem{} },
	}
}

// NewClickToMoveSystem returns a SystemDef for click-to-move entity movement.
func NewClickToMoveSystem() SystemDef {
	return SystemDef{
		Name:    "ClickToMove",
		Factory: func() engine.System { return &ClickToMoveSystem{} },
	}
}

// NewDirectionMoveSystem returns a SystemDef for direction-input entity movement.
func NewDirectionMoveSystem() SystemDef {
	return SystemDef{
		Name:    "DirectionMove",
		Factory: func() engine.System { return &DirectionMoveSystem{} },
	}
}

// NewLifetimeSystem returns a SystemDef for despawning expired entities.
func NewLifetimeSystem() SystemDef {
	return SystemDef{
		Name:    "Lifetime",
		Factory: func() engine.System { return &LifetimeSystem{} },
	}
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
// shared *universe.ClusterClock (from Coordinator/host/Stage) — it
// satisfies the small system.ClusterClock interface structurally.
func DefaultReplicationConfig(eng *engine.Engine, grid *spatial.HashGrid, clock system.ClusterClock) ReplicationConfig {
	return ReplicationConfig{
		World:          eng.ECS,
		SpatialGrid:    grid,
		Viewers:        system.NewPlayerViewerSource(eng.ECS, eng.Players, engine.StateActive),
		Frame:          system.NewBinaryFrameWriter(eng.ConnMgr, makeWorldDeltaFrame),
		GetTick:        func() uint32 { return eng.Tick },
		ClusterClock:   clock,
		TickIntervalMs: eng.TickIntervalMs(),
	}
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

	// New creates a Process from the given Config. Call Start() to run.
	New = universe.New

	// NewStage creates a Stage with the given engine, cell, nodeID, AoI radius,
	// spatial grid, and replication registry. Embed in your game world struct.
	NewStage = universe.NewStage

	// NewReplicationRegistry creates an empty registry for cross-cell component replication.
	NewReplicationRegistry = universe.NewReplicationRegistry

	// UnmarshalCollider deserializes a Collider from bytes.
	UnmarshalCollider = universe.UnmarshalCollider

	// MarshalTransferFrame serializes a TransferFrame to bytes for cross-cell transfer.
	MarshalTransferFrame = universe.MarshalTransferFrame

	// UnmarshalTransferFrame deserializes a TransferFrame from bytes.
	UnmarshalTransferFrame = universe.UnmarshalTransferFrame

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
	// These are auto-registered by Stage; games can reference them for initial enable lists.
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

	// WithExtraMigrations queues a migration source applied after engine
	// migrations. Used by direct OpenPostgres callers; service kinds
	// should attach migrations via ServiceKind.Migrations instead so the
	// engine wires them automatically.
	WithExtraMigrations = postgres.WithExtraMigrations
)

// PostgresOption customizes OpenPostgres behavior.
type PostgresOption = postgres.Option

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

var (
	// WithVelocity sets initial velocity when spawning via Stage.SpawnEntity.
	WithVelocity = universe.WithVelocity

	// WithCollider attaches a Collider component when spawning via Stage.SpawnEntity.
	WithCollider = universe.WithCollider

	// WithEntityKind sets the EntityKind component when spawning via Stage.SpawnEntity.
	WithEntityKind = universe.WithEntityKind

	// WithRotation sets initial rotation when spawning via Stage.SpawnEntity.
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

	// ErrNotFound is returned by repository Load methods when the
	// requested record doesn't exist.
	ErrNotFound = persist.ErrNotFound
)

// ---------------------------------------------------------------------------
// Generic functions (can't alias generic funcs in Go)
// ---------------------------------------------------------------------------

// RegisterComponent registers an ECS component type for automatic cross-cell
// replication and transfer. IDs are auto-assigned in registration order.
func RegisterComponent[T any](reg *universe.ReplicationRegistry, m *ecs.Map1[T], opts ...universe.ComponentOption) {
	universe.RegisterComponent(reg, m, opts...)
}

// WithMarshal overrides the default reflection-based marshal/unmarshal for a
// registered component with custom serialization functions.
func WithMarshal[T any](marshal func(*T) []byte, unmarshal func([]byte, *T)) universe.ComponentOption {
	return universe.WithMarshal(marshal, unmarshal)
}

// WithPreMarshal registers a function that runs on a copy of the component
// before marshaling. Use to sanitize or transform data before serialization.
func WithPreMarshal[T any](fn func(*T)) universe.ComponentOption {
	return universe.WithPreMarshal(fn)
}

// BuildReplicators constructs a ReplicatorRegistry from EntityKindDefs.
// Used for schema export and auto-discovery by NewNetworkSystem. When
// coord is non-nil, quant scales come from coord.Cfg(); when nil (some
// unit-test harnesses build a Stage without a Process), the same
// defaults that universe.New applies (2000 / 500) are used so schema
// and runtime agree on the wire format. Keep these in sync with the
// defaults in coordinator.New() — drift breaks the invariant that
// `--dump-schema` matches server bytes.
//
// Var-tail bindings (those implementing system.VarTailProvider) are automatically
// moved to the end of each entity's binding list so games don't need to worry
// about registration order. At most one var-tail binding is allowed per entity;
// AutoReplicator will panic if there are more.
func BuildReplicators(w *ecs.World, coord *universe.Process, defs ...universe.EntityKindDef) *system.ReplicatorRegistry {
	velScale := float32(2000)  // matches universe.New default
	sizeScale := float32(500)  // matches universe.New default
	if coord != nil {
		velScale = coord.Cfg().VelQuantScale
		sizeScale = coord.Cfg().SizeQuantScale
	}
	replicators := system.NewReplicatorRegistry()
	for _, def := range defs {
		var bindings []system.ComponentBinding
		bindings = append(bindings, system.EngineBindings(w, velScale, sizeScale))

		// Partition game bindings: var-tail bindings go to the end.
		var regular, varTails []system.ComponentBinding
		for _, cb := range def.NetworkBindings {
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


// NewNetworkSystem returns a SystemDef that creates a ReplicationSystem
// with DefaultReplicationConfig pre-filled. Replicators are auto-discovered
// from registered EntityKindDefs. AoIRadius is inherited from the coordinator
// config. Use NewNetworkSystemWith for custom configuration.
func NewNetworkSystem() SystemDef {
	return SystemDef{
		Name:    "Network",
		Factory: func() engine.System { return &defaultNetworkSystem{} },
	}
}

type defaultNetworkSystem struct {
	engine.SystemBase[any]
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

// NewNetworkSystemWith returns a SystemDef like NewNetworkSystem, but with
// a typed setup callback for custom configuration. The setup function receives
// the pre-filled config and typed game world — set Replicators, AoIRadius, and
// any optional fields (callbacks, dormancy, etc.) there.
//
//	mmo.AddSystem(mmokit.NewNetworkSystemWith(func(cfg *mmokit.ReplicationConfig, gw *MyWorld) {
//	    cfg.AoIRadius = 800
//	    cfg.OnEntityEnter = func(...) { ... }
//	}))
func NewNetworkSystemWith[W any](setup func(cfg *ReplicationConfig, gw W)) SystemDef {
	return SystemDef{
		Name:    "Network",
		Factory: func() engine.System { return &networkSystem[W]{setup: setup} },
	}
}

type networkSystem[W any] struct {
	engine.SystemBase[any]
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

// makeWorldDeltaFrame wraps the encoded delta body bytes as a typed-event
// frame for WorldDelta. Used by BinaryFrameWriter (per-tick replication)
// to publish entity-state deltas on the typed channel-0x00 path. The body
// passes through the reflection codec's []byte fast path
// ([u32 len][N bytes]), so the wire layout is:
//
//	[0x00][typeID(WorldDelta):u32 LE][bodyLen:u32 LE][u32 LE bodyByteLen][delta bytes]
//
// where bodyLen == 4 + bodyByteLen.
func makeWorldDeltaFrame(body []byte) []byte {
	return universe.BuildTypedEventFrameRaw(&WorldDelta{Body: body})
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

// RegisterProtoOp registers a typed operation handler on an OpRouter. It
// wraps ops.Register, capturing request and response proto type names for
// schema export via Router.Schema(). Prefer this over the untyped
// Router.Register for any operation that should appear in the SDK schema.
//
// Specify only the value types Req and Res; pointer types are inferred:
//
//	mmokit.RegisterProtoOp[MarketBrowseRequest, MarketOrderBookResponse](router, code, "name", handler)
//
// `code` accepts any `~int32` or `~uint32` so proto enum values flow
// through directly without a `uint32(...)` cast at the call site.
//
// Deprecated: this proto-constrained shape is being phased out by Plan 2.
// Will be removed in Plan 2 Phase 5; use RegisterOp[Req, Res] for new code.
func RegisterProtoOp[Req any, Res any, Code OpEventCode, ReqP ops.ProtoMessage[Req], ResP ops.ProtoMessage[Res]](
	r *ops.Router, code Code, name string,
	handler func(ctx *ops.OpContext, req ReqP) (ResP, error)) {
	ops.Register(r, code, name, handler)
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

// WireSystem wires a system as the coordinator does — SetDeps, BindQueries,
// Init, BuildQueries — in one call. Use in tests where you want a fully-
// initialized system without spinning up a coordinator.
func WireSystem(sys engine.System, ecsWorld *ecs.World, eng *engine.Engine, gw any) {
	type depsInjectable interface {
		SetDeps(w *ecs.World, eng *engine.Engine, gw any)
	}
	type queryBinder interface{ BindQueries(outer any) }
	type initializable interface{ Init() }
	type queryBuilder interface{ BuildQueries() }

	if di, ok := sys.(depsInjectable); ok {
		di.SetDeps(ecsWorld, eng, gw)
	}
	if qb, ok := sys.(queryBinder); ok {
		qb.BindQueries(sys)
	}
	if i, ok := sys.(initializable); ok {
		i.Init()
	}
	if qb, ok := sys.(queryBuilder); ok {
		qb.BuildQueries()
	}
}

// ProtocolOf returns the *Protocol from p.Protocol(), or nil if p is nil,
// the protocol is unset, or the stored value is not a *Protocol.
func ProtocolOf(p *Process) *Protocol {
	if p == nil {
		return nil
	}
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
