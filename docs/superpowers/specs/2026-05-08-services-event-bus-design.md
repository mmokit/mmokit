# Services Event Bus — Design

**Status:** Draft for review
**Author:** Josh Stout (with Claude)
**Date:** 2026-05-08
**Related memories:** `feedback_no_backward_compat`, `feedback_refactor_over_stopgaps`, `feedback_mmokit_facade_only`, `feedback_logging`, `project_opensource_ready`
**Related specs:** [2026-04-27-pluggable-services-design.md](2026-04-27-pluggable-services-design.md), [2026-05-01-auth-service-design.md](2026-05-01-auth-service-design.md), [2026-05-07-chat-service-design.md](2026-05-07-chat-service-design.md)

## 1. Summary

mmokit's services framework today (`pkg/service/`) exposes services as wire-routed handlers — they receive client ops, run handlers, persist state. What's missing is a **first-class lifecycle / inter-service signal mechanism**. Today, every service that needs to react to login/logout, authentication, or events from a sibling service requires a hand-written core PR: the chat service has `Process.chatHook + ChatSessionHook + InstallChatHook`; auth has `Process.AuthResolver + Gateway response interception`. Future services (presence, guild, marketplace, mail, achievements, friends list) would compound this leak — every new service-author requirement becomes new core surface area.

This design replaces those one-off core hooks with a **single generic typed pub/sub bus** owned by the services framework. Services subscribe to events at `Init` time via `service.Context.Bus`. The framework publishes well-known lifecycle events at the existing call sites; services publish their own custom events to the same bus. Cross-service signaling becomes a Go-typed contract.

**Two-tier delivery:** the bus is process-local in the simplest deployment, but cluster-wide when services run on dedicated processes. **The coordinator is in the control plane only** (subscription routing decisions); event payloads flow **directly peer-to-peer** between publisher and subscriber processes via the existing peer mesh (MeshData). No event bottleneck through coord.

## 2. Goals & non-goals

### Goals

- **One mechanism, all services.** Auth, chat, future game-dev services use the same `service.Bus`. No more per-service core fields.
- **Self-contained services.** Game devs add custom services without touching `pkg/universe/` or `pkg/mmokit/`. Subscribe + publish from `Init`; framework handles routing.
- **Process-local + cluster-wide transparent.** Service authors write the same code regardless of whether subscribers are colocated or on a remote process.
- **Coordinator in the control plane only.** Coord owns the routing table; coord never touches event payloads. Mirrors the MeshControl/MeshData split that already governs cell traffic.
- **Direct peer-to-peer data plane.** Publishers fan out events directly to subscribed peer processes via the existing MeshData fabric. One hop, no coord bottleneck.
- **Type-safe at the API surface.** Generic `Subscribe[T]` / `Publish[T]`. Compile-time typed handlers. Wire-side uses reflection-codec encoding (same primitive as typed-events).
- **Backward compat: none.** Greenfield refactor. Existing chat + auth core hooks are deleted in the same change set.

### Non-goals (v1)

- **Durability.** Bus is opportunistic notification. Events lost during process crash / network partition stay lost. Use Postgres for events that must survive.
- **Exactly-once delivery.** At-most-once on a given peer connection. De-dup via the optional `Sequence` field is observability-grade, not correctness-grade.
- **Total ordering across publishers.** Events from one publisher to one subscriber arrive in publish order. Events from different publishers may interleave.
- **External broker integration.** No Redis, NATS, or Kafka. Mmokit stays self-contained — the existing peer mesh is the broker.
- **Request/response middleware.** Auth's response-interception pattern (decorate the login response, set gateway state) is a different primitive — sibling design, not this one.
- **Runtime-typed events.** Both publisher and subscriber must have the Go event type compiled into the binary. No dynamic type registration.
- **Cross-cluster federation.** All bus traffic is intra-cluster.

## 3. Architecture

```text
Control plane (low frequency, coord-mediated)
─────────────────────────────────────────────
  Subscribe / Unsubscribe → coord
  Coord rebuilds routing table
  Coord broadcasts via PeerList (existing channel)
  Every process caches the routing table locally

Data plane (high frequency, direct peer-to-peer)
────────────────────────────────────────────────
  Publish consults local routing-table cache
  Sends DIRECTLY to interested peer processes
  via existing MeshData gRPC streams
  Coord NOT involved
```

```text
                  ┌────────────────────┐
                  │    coordinator     │
                  │                    │
                  │ Routing table:     │
                  │  T → [proc1, proc3]│
                  └─────┬───┬────┬─────┘
                        │   │    │
       ┌──Subscribe─────┘   │    └────Subscribe────┐
       │                    │                       │
       │           PeerList  │   PeerList            │
       │           (routing) │  (routing)            │
       │                    ▼                       │
   ┌───▼─────┐    ┌────────────────┐         ┌─────▼─────┐
   │ gateway │    │     svc-1      │         │   svc-2   │
   │         │    │                │         │           │
   │ pubs:   │◀══▶│ subs: chat.*   │◀═══════▶│ subs:     │
   │  Session│    │       guild.*  │  data   │  audit.*  │
   │   *Event│    │                │  plane  │           │
   └─────────┘    └────────────────┘         └───────────┘
        ▲             direct peer-to-peer
        │             gRPC streams
        │             (no coord hop)
        │
   client WS

Legend: ───── control plane (coord-mediated)
        ═════ data plane (peer-to-peer, direct)
```

### Architectural principle

> **Coordinator is the control plane.** Routing decisions, topology, registration. Low frequency, mediated. Persists across crashes via the existing rebuild-on-restart logic.
>
> **Peer mesh is the data plane.** Events, cell traffic, action dispatch. High frequency, direct peer-to-peer. Stateless from coord's perspective.
>
> Anything that flows on every game tick or every player action belongs on the data plane. Anything that flows on topology change or service lifecycle belongs on the control plane.

This principle, baked in from day one, prevents future contributors from accidentally putting hot paths through coord. The bus enforces it by construction.

## 4. Package layout

```text
pkg/service/
  bus.go              // Bus type + Subscribe[T] / Publish[T] / PublishLocal[T] generics
  events.go           // Framework-emitted event types (POD structs)
  event_codec.go      // Type registry + reflection-codec helpers
  bus_test.go         // Unit tests (process-local + simulated cluster)
  // existing: kind.go, registry.go, service.go, context.go, registry_coord.go, router.go

pkg/universe/
  service_event_router.go  // NEW: coord-side routing table + PeerList integration
  service_event_dispatch.go // NEW: per-process Bus dispatch into MeshData
  // modified: coordinator.go, mesh_control_server.go, mesh_control_client.go,
  //           mesh_gateway_client.go, host_network.go, gateway.go,
  //           peer_list.go (or wherever PeerList lives)

proto/meshpb/
  mesh.proto          // NEW message: ServiceEvent (in MeshFrame oneof)
                      // NEW messages: ServiceEventSubscribe (HostMessage)
                      // EXTEND: PeerList.event_routing field

pkg/mmokit/
  // (unchanged — facade re-exports stay; chat.go + auth.go simplify dramatically)
```

Module boundaries unchanged: `pkg/service/` is leaf-level (no `pkg/universe/` import). Universe wires the cross-process plumbing via callbacks injected into Bus at `Process.Build()` time. Same shape as how `Process` injects `DB` / `Logger` into `service.Context`.

## 5. Wire protocol

### Data-plane: `proto/meshpb/mesh.proto` — `MeshFrame.ServiceEvent`

```protobuf
message MeshFrame {
  oneof msg {
    // ... existing variants ...
    ServiceEvent service_event = 50;  // NEW
  }
}

message ServiceEvent {
  // Process ID of the publisher (gateway-id, host-id, or service-host-id).
  // Used for self-echo skip + diagnostics.
  string source_process_id = 1;

  // Fully-qualified Go type name, e.g. "service.SessionEnterEvent" or
  // "chat.ChannelCreatedEvent". The receiving process looks this up
  // in its local event-type registry to decode payload.
  string type_name = 2;

  // Reflection-codec encoded event payload. Same primitive used by
  // the typed-event server-side wire format.
  bytes payload = 3;

  // Per-publisher monotonic sequence number. For dedup + observability;
  // not used for ordering enforcement (single stream is in-order anyway).
  uint64 sequence = 4;
}
```

`ServiceEvent` rides the existing peer-mesh streams. Hosts and gateways already open MeshData streams to one another; service-host processes start a MeshData server too (see §10).

### Control-plane: `HostMessage.ServiceEventSubscribe`

```protobuf
message HostMessage {
  oneof msg {
    // ... existing ...
    ServiceEventSubscribe subscribe = 51;  // NEW
  }
}

message ServiceEventSubscribe {
  // Whole set replace — process sends the complete set of types it
  // subscribes to on every change. Idempotent; no delta protocol.
  repeated string type_names = 1;
}
```

### Control-plane: `PeerList.event_routing`

```protobuf
message PeerList {
  // ... existing host/cell/gateway/service rosters ...

  // type_name → list of process IDs that subscribed to it.
  // Updated on every subscribe-set change; broadcast on every PeerList tick.
  map<string, ProcessList> event_routing = 5;  // NEW
}

message ProcessList {
  repeated string process_ids = 1;
}
```

**Why ride PeerList:** topology + routing fan out together. PeerList already broadcasts on every host-join, gateway-join, cell-rebalance — adding subscription changes as one more trigger is mechanical. No separate broadcast machinery.

## 6. API surface

### `service.Bus`

```go
package service

// Bus is the typed publish/subscribe primitive owned by Process.
//
// Local subscribers fire synchronously inline on Publish. Remote
// delivery is async — the Bus dispatches to peer processes via the
// peer-mesh gRPC streams.
type Bus struct {
    // ... unexported ...
}

func NewBus() *Bus { ... }

// Unsubscribe removes a registered handler. Idempotent.
type Unsubscribe func()
```

### Generic API

```go
// Subscribe registers a handler for events of type T. Returns an
// unsubscribe function. Multiple handlers per type are allowed; they
// fire in registration order. Handlers run on the bus dispatch
// goroutine — keep them fast (queue heavy work elsewhere).
//
// Subscribing also tells the cluster: "this process cares about T".
// The Bus accumulates the type set; on a debounced flush (or at
// Init complete), it sends ServiceEventSubscribe to coord.
func Subscribe[T any](b *Bus, handler func(T)) Unsubscribe

// Publish broadcasts ev to every local subscriber AND every remote
// process registered for type T. Local subscribers fire inline
// (synchronous). Remote dispatch is fire-and-forget over the peer
// mesh; failures log + drop.
func Publish[T any](b *Bus, ev T)

// PublishLocal broadcasts ev to LOCAL subscribers only. Use for
// high-frequency events that only same-process services consume
// (e.g., per-tick metrics). Skips the peer-mesh fan-out entirely.
func PublishLocal[T any](b *Bus, ev T)

// RegisterEventType registers T's reflect.Type → type_name mapping.
// Required on every process that might receive T from the wire.
// Idempotent. Typically called from package init() of each service
// or framework module that defines event types.
func RegisterEventType[T any]()
```

### `service.Context` extension

```go
type Context struct {
    KindName   string
    InstanceID string
    Logger     *logger.Logger
    DB         *postgres.Store
    Roles      Roles
    Bus        *Bus  // NEW
}
```

`ctx.Bus` is the same `*Bus` shared with every other service in the same process. Cross-service publish/subscribe inside one process is automatic; cross-process is opaque to the service author.

### Service-author API example

```go
// pkg/services/chat/service.go::Init
func (s *Service) Init(ctx *service.Context) error {
    s.bus = ctx.Bus

    // Subscribe to framework events.
    service.Subscribe(ctx.Bus, func(ev service.SessionEnterEvent) {
        _, _ = s.HandleSessionEnter(nil, &ChatSessionEnterRequest{
            ConnID: ev.ConnID, UserID: ev.UserID,
            Username: ev.Username, GatewayID: ev.GatewayID,
        })
    })
    service.Subscribe(ctx.Bus, func(ev service.SessionLeaveEvent) {
        _, _ = s.HandleSessionLeave(nil, &ChatSessionLeaveRequest{
            ConnID: ev.ConnID, GatewayID: ev.GatewayID,
        })
    })

    // Subscribe to a sibling service's events (compile-time decoupled
    // — chat imports `guild` only for the event type).
    service.Subscribe(ctx.Bus, func(ev guild.GuildCreatedEvent) {
        slug := fmt.Sprintf("guild:%d", ev.GuildID)
        _, _ = s.HandleRegisterChannel(nil, &ChatRegisterChannelRequest{
            Slug: slug, Kind: ChannelKindSystemPredicate,
            Topic: ev.Name + " guild chat",
        })
    })

    // ... existing repo / bootstrap / reaper setup ...

    return nil
}

// Publishing chat's own events:
func (s *Service) handleMessageDeleted(...) {
    // ... existing ...
    service.Publish(s.bus, MessageDeletedEvent{...})
}
```

## 7. Standard framework event types

`pkg/service/events.go`:

```go
package service

// SessionEnterEvent fires after a successful auth login + cell
// dispatch. Published by the gateway. Consumed by services that
// need per-session state (chat presence, presence service,
// achievements, etc).
type SessionEnterEvent struct {
    ConnID    uint32
    UserID    string
    Username  string
    GatewayID string
}

// SessionLeaveEvent fires when a WS connection closes (any cause —
// clean disconnect, gateway crash recovery, kick).
type SessionLeaveEvent struct {
    ConnID    uint32
    UserID    string  // populated when known; empty for unauthenticated drops
    GatewayID string
}

// AuthLoginSucceededEvent fires after a successful AUTH_LOGIN /
// AUTH_REGISTER / AUTH_VALIDATE_TOKEN op. Published by the auth
// service. Useful for services that need per-login state (e.g.
// telemetry, friends-online notifications).
type AuthLoginSucceededEvent struct {
    UserID       string
    Username     string
    SessionToken string  // only populated on AUTH_LOGIN / AUTH_REGISTER; empty on validate
    ConnID       uint32
    GatewayID    string
}

// AuthLogoutEvent fires after an explicit AUTH_LOGOUT op. Note: NOT
// fired on WS close — that's SessionLeaveEvent. Logout is the
// explicit user-driven case.
type AuthLogoutEvent struct {
    UserID    string
    Username  string
    ConnID    uint32
    GatewayID string
}

// AuthRegisteredEvent fires after a successful AUTH_REGISTER op.
// Lets achievements / starter-pack / welcome-message services run
// on first-ever login.
type AuthRegisteredEvent struct {
    UserID   string
    Username string
}

// PlayerSpawnedEvent fires after a player's entity is created on
// its authoritative cell. Published by the cell host.
type PlayerSpawnedEvent struct {
    UserID, Username string
    CellID           string
    NetID            uint32
}

// PlayerDespawnedEvent fires when a player's entity is removed
// (disconnect, transfer, kick).
type PlayerDespawnedEvent struct {
    UserID string
    NetID  uint32
}

// (Future events fit additively as new POD structs in this file or
// in service-author packages. No core change required to add an
// event type — just `RegisterEventType` it on the relevant processes.)
```

Each event is a tiny POD struct, opaque to its consumers, with no shared inheritance. New event types are added by appending to this file (framework events) or by service authors in their own packages (service events).

## 8. Subscribe flow (control plane)

```text
1. Service.Init():
     ctx.Bus.Subscribe[T1](handler1)
     ctx.Bus.Subscribe[T2](handler2)
     // local Bus accumulates: typeSet = {T1, T2}

2. After all services on this process have completed Init:
     Process.flushSubscriptions()
       → coalesces typeSets from every running service
       → sends HostMessage.ServiceEventSubscribe{type_names: [T1, T2, ...]}
         to coord via MeshControl

3. coord:
     routingTable[T1] = append(routingTable[T1], processID)
     routingTable[T2] = append(routingTable[T2], processID)
     broadcastPeerList()

4. Every process receives PeerList:
     localRoutingCache[T1] = [proc-A, proc-B]
     localRoutingCache[T2] = [proc-A]
```

### Subscribe timing rules

- **Initial flush:** after all services on this process finish `Init`. `Process.startServices` already iterates kinds in order; it flushes once at the end.
- **Runtime add:** `Subscribe[T]` after `Init` (rare; mostly for tests) triggers a debounced (50ms) flush of the new whole-set to coord.
- **Unsubscribe:** the returned `Unsubscribe()` removes the local handler. If it was the last subscriber for T on this process, the next debounced flush sends a smaller set; coord rebuilds + rebroadcasts.
- **Process leave:** `GracefulLeave` (existing MeshControl flow) on shutdown causes coord to drop the process's entries from `routingTable` and rebroadcast. Crash detection (no heartbeat) does the same after the standard threshold.

### Whole-set replace, not delta

Every `ServiceEventSubscribe` carries the **complete** set of types this process subscribes to. Coord replaces — never patches. Idempotent, simpler, no sequence-number bookkeeping. The set is small (dozens of types per process); whole-set is cheap.

## 9. Publish flow (data plane)

```go
// Service author writes:
service.Publish(ctx.Bus, ChannelCreatedEvent{...})

// Inside the Bus:
func Publish[T any](b *Bus, ev T) {
    typ := typeOf(ev)

    // 1. Local subscribers — synchronous, fast
    b.mu.RLock()
    handlers := append([]func(any)(nil), b.localHandlers[typ]...)
    b.mu.RUnlock()
    for _, h := range handlers {
        h(ev)
    }

    // 2. Resolve remote peers from cached routing table
    peers := b.routingCache.Get(typ.Name())
    if len(peers) == 0 { return }  // no remote subscribers

    // 3. Encode once, fan out
    payload := ReflectMarshal(ev)
    frame := &meshpb.MeshFrame{
        Msg: &meshpb.MeshFrame_ServiceEvent{
            ServiceEvent: &meshpb.ServiceEvent{
                SourceProcessId: b.processID,
                TypeName:        typ.Name(),
                Payload:         payload,
                Sequence:        b.nextSeq(),
            },
        },
    }

    // 4. Direct peer-to-peer dispatch via existing MeshData fabric
    for _, peerID := range peers {
        if peerID == b.processID { continue }  // self — already fired locally
        if err := b.peerMesh.SendOrdered(peerID, frame); err != nil {
            b.logger.Log("services:bus", "publish to %s failed: %v", peerID, err)
        }
    }
}
```

### Receive side

Each process's MeshData receiver routes `MeshFrame.ServiceEvent` into its local Bus:

```go
case *meshpb.MeshFrame_ServiceEvent:
    se := v.ServiceEvent
    if se.SourceProcessId == process.id {
        return  // self-echo, defensive (shouldn't happen with direct dispatch)
    }
    typ, ok := bus.LookupEventType(se.TypeName)
    if !ok {
        // Type not registered on this process — silently drop. Either
        // we don't subscribe (coord shouldn't have routed) or we don't
        // have the package linked.
        return
    }
    instance := reflect.New(typ).Interface()
    if err := ReflectUnmarshal(se.Payload, instance); err != nil {
        log.Log("services:bus", "decode %s from %s: %v", se.TypeName, se.SourceProcessId, err)
        return
    }
    bus.fireLocalDispatch(typ, instance)
```

`fireLocalDispatch` runs the same code path as a local `Publish` minus the remote fan-out — local subscribers fire inline.

### Why direct peer-to-peer (vs coord-as-broker)

Coord is *not* in the data path because:

1. **No bottleneck.** A high-frequency event (per-tick metrics, frequent guild updates) would saturate coord. Direct peer-mesh distributes load across pairs.
2. **One hop, not two.** Latency halved.
3. **Coord crash doesn't drop events.** Existing peer-mesh streams keep delivering. Only NEW subscribes wait for coord to come back.
4. **Operational consistency.** Mirrors MeshData's host-to-host cell traffic — operators understand the model already.

## 10. Service-host MeshData participation

Today, only `--mode=host` and `--mode=gateway` processes run a MeshData server. For pure `--mode=service` processes, we extend the framework so they also start a MeshData server on a configurable port.

### Process startup additions

```go
// In Process.Build / startMeshData:
if c.roles.Has(RoleService) || c.roles.Has(RoleHost) || c.roles.Has(RoleGateway) {
    c.hostNetwork = NewHostNetwork(c)
    c.hostNetwork.Listen(cfg.MeshDataListen)
}
```

(Today this is gated only on RoleHost / RoleGateway. Adding RoleService is mechanical.)

### PeerList extension

The existing `PeerList.gateways` and `PeerList.hosts` rosters carry `address` strings (gRPC dial targets). Add a `services` roster (or extend `hosts` to include service-host process IDs with their MeshData addresses) so every process can dial every other process for direct event publish.

The actual `pluggable-services-design.md` already specifies a `PeerList.services` field for service-instance routing. Extend that field with the MeshData address (or add a new field) — either way, every published-to-process is dialable from PeerList state.

### Lazy dial

Each process maintains a peer-stream registry sized to "peers I've published to" — not "every peer in the cluster". First publish to peer X opens the stream; subsequent events reuse. Disconnect on dead peer (existing reconnect-on-need logic in `host_network.go`).

For an MMO cluster with O(10) processes and O(100) event types, the steady-state stream count is small.

## 11. Failure modes + delivery semantics

| Concern | Behavior |
|---|---|
| **Coord crash** | Existing publishers/subscribers keep delivering events on their established peer-mesh streams. NEW subscribes block until coord recovers. Subscribers' routing-table cache becomes stale slowly (no PeerList updates) — events for a newly-joined subscriber are missed until coord broadcasts the next PeerList. |
| **Network partition** | Publisher fails to reach a partitioned subscriber's peer-mesh stream. Logs an error; drops the event. No retry queue (use Postgres for events that must survive). |
| **Subscriber crash** | Publisher's `SendOrdered` fails on the dead stream. Subscriber's GracefulLeave (or heartbeat timeout) eventually causes coord to drop it from routing-table; PeerList rebroadcasts; publisher's local cache updates and stops trying. |
| **Slow subscriber** | Per-stream send blocks only the publisher → that-peer path. Other publishers and other peers unaffected. |
| **Duplicate delivery** | Single peer-mesh stream → at-most-once. The optional `Sequence` field lets observability detect duplicates if a coord-side glitch ever causes routing-table double-broadcast. |
| **Loops (handler publishes event that re-triggers handler)** | Same hazard as any pub/sub. Mitigation: convention. Don't publish from inside a Subscribe handler. Linter rule possible later (`AST-walk Subscribe-passed funcs for service.Publish calls`). |
| **Self-echo** | Bus skips dispatch when `SourceProcessId == self.id`. Defensive; shouldn't happen with direct dispatch but covers any future re-broadcast-style path. |
| **Type-name collisions** | Two different Go types with the same fully-qualified `reflect.Type.String()` would collide. Practically impossible (Go's type-name disambiguation handles this) but caught at `RegisterEventType[T]()` time — duplicate registration panics. |
| **Payload decode failure** | Logged + dropped. The publisher's binary diverged from the receiver's (different struct shape). Not recoverable; rebuild + redeploy. |
| **PeerList lag** | Subscribe at process A → coord → next PeerList tick → process B sees A in routingCache. There's a window where B publishes T but A doesn't yet receive (A subscribed but B's cache hasn't refreshed). Acceptable for opportunistic delivery; use database state for anything that must be consistent. |

### Delivery guarantees

- **Same-process**: synchronous, always delivered (unless handler panics — recovered + logged).
- **Cross-process**: at-most-once, fire-and-forget, ordered per (publisher, subscriber) pair.
- **Whole bus**: opportunistic. Use Postgres for must-not-lose events.

## 12. Migration plan

This is a real architectural change. The migration ships in three phases, each independently shippable + reversible.

### Phase 1 — Bus primitive (process-local only)

**Lands first; the simplest piece.** No wire-protocol change. No coord involvement.

- New: `pkg/service/bus.go` (Bus type, Subscribe/Publish/PublishLocal — local-only impl)
- New: `pkg/service/events.go` (POD framework event types)
- New: `pkg/service/event_codec.go` (RegisterEventType + reflect lookup)
- Modified: `pkg/service/service.go` — add `Bus *Bus` to `Context`
- Modified: `pkg/universe/coordinator.go` — own one `*service.Bus` per Process; thread into `serviceContext()`
- Modified: `pkg/universe/gateway.go` — replace `chatHook.OnSessionEnter/Leave` calls with `service.Publish(g.process.bus, service.SessionEnterEvent{...})`
- Modified: `pkg/services/chat/service.go::Init` — replace install-from-facade with `service.Subscribe[SessionEnterEvent]` etc
- Modified: `pkg/mmokit/chat.go` — drop `chatSessionHookImpl` + `p.InstallChatHook(...)` call

**Deletes:** `pkg/universe/coordinator.go::ChatSessionHook`, `Process.chatHook`, `Process.InstallChatHook`, `Process.ChatHook`, `pkg/services/chat/session_hook.go`, `pkg/mmokit/chat.go::chatSessionHookImpl`.

After Phase 1: chat behaves identically; chat is a normal service with no special core hooks. Auth still has its own `AuthResolver` plumbing (refactored in Phase 2).

### Phase 2 — Auth event extraction

**Auth-side parallel to Phase 1.** Replace auth's response-interception path with `AuthLoginSucceededEvent` published from auth's handlers; gateway subscribes locally to wire up `authStates[connID]` and dispatch `PlayerAssignment`.

- Auth's handlers publish `AuthLoginSucceededEvent` / `AuthLogoutEvent` / `AuthRegisteredEvent` after their existing audit/session writes.
- Gateway subscribes to `AuthLoginSucceededEvent` at startup; the handler does what `GatewayHook.NotifyLoginSuccess` does today.
- **Note:** auth's response-interception is request/response-shaped (decorate the response struct, set per-connID state at the moment the response is encoded). Pure pub/sub doesn't model this perfectly. The pragmatic answer: publish the success event AFTER the response is encoded — gateway's subscriber populates state on event arrival, which lands one async hop after the client gets the response. For the existing flows this is fine (the gateway state is consulted on the client's NEXT op, not the auth response itself).

**Deletes:** `Process.AuthResolver` field (or refactor to be subscribed-derived), `pkg/auth.GatewayHook`, related install methods.

If the response-interception turns out to need synchronous before-encode hooks, that's a sibling primitive — see §14 Open question 1.

### Phase 3 — Cluster-wide bus (peer-mesh delivery)

**The big one.** Adds wire protocol + cross-process delivery.

- Proto changes: `MeshFrame.ServiceEvent`, `HostMessage.ServiceEventSubscribe`, `PeerList.event_routing`. Run `just proto`.
- Coordinator-side: routing-table state + handlers for `ServiceEventSubscribe` (control plane).
- Coordinator-side: `event_routing` field populated in every PeerList broadcast; broadcast triggered on subscribe-set change.
- Process-side: cache PeerList's `event_routing` field; `Bus.routingCache` reads from there.
- Process-side: extend MeshData receivers (host_network's `runRecvLoop` etc) to handle `MeshFrame.ServiceEvent` → `bus.fireLocalDispatch`.
- Process-side: Bus.Publish[T] does local fan-out + lookup peers + send via `peerMesh.SendOrdered`.
- Process-side: pure `--mode=service` processes start a MeshData server (extend `Process.Build` to include RoleService in the listener gate).

**End-to-end test:** `pkg/universe/services_event_bus_e2e_test.go`. Stand up a 3-process fixture (gateway, service-host A, service-host B). Subscribe A to T, publish from gateway, assert A receives + B doesn't.

### Phase 4 — Documentation update

- Update `pluggable-services-design.md` with a "v2: Generic event bus" section pointing at this spec.
- Update `auth-service-design.md` and `chat-service-design.md` to reflect bus-based subscription.
- Add the control-plane / data-plane principle to the top-level architecture overview in CLAUDE.md.

## 13. What dies, what changes

### Deleted (no aliases per `feedback_no_backward_compat`)

**`pkg/universe/coordinator.go`:**
- `ChatSessionHook` interface
- `Process.chatHook` field
- `Process.InstallChatHook` method
- `Process.ChatHook` accessor

**`pkg/services/chat/`:**
- `session_hook.go` (the chat-side `SessionHook` interface — superseded by direct subscription)

**`pkg/mmokit/chat.go`:**
- `chatSessionHookImpl` struct
- The `p.InstallChatHook(...)` call (replaced by chat's own `Subscribe` at Init)

**`pkg/services/auth/` + `pkg/mmokit/auth.go`:** depends on Phase 2 outcome — gateway hook may stay if synchronous response-interception is genuinely needed. See §14 Open question 1.

### Wire protocol additions

- `MeshFrame.ServiceEvent` (data plane)
- `HostMessage.ServiceEventSubscribe` (control plane)
- `PeerList.event_routing` field (control plane)

No removed protos; all additions.

### `service.Context`

- Adds `Bus *Bus` field (pointer to the per-process Bus instance)

### `service.Service` interface

Unchanged. Subscribe/Publish are accessed via `Context.Bus`, not via interface methods.

## 14. Open questions

### 14.1 Auth response interception model

Auth's current `GatewayHook` decorates the AUTH_LOGIN response BEFORE it's sent to the client (it captures the SessionToken into the gateway's authStates map). Pure pub/sub doesn't easily model "before-encode" hooks — it's an after-the-fact event.

Two options:

- **A. Async-only.** Auth publishes `AuthLoginSucceededEvent` AFTER the response is encoded. Gateway's local subscriber populates `authStates[connID]` on event arrival. Race window: between auth's response-send and gateway's event-receive (microseconds, in-process). Worst case: client sees response, immediately sends next op, op arrives at gateway BEFORE the auth event → gateway treats next op as unauthenticated. Probably unobservable in practice but not zero.

- **B. Synchronous middleware primitive.** A separate "op-response middleware" primitive lets auth wire a synchronous before-encode hook on the AUTH_LOGIN response shape. `service.RegisterOpMiddleware[Req, Res](handler)` runs handler with `(opCtx, *Res)` after handler returns, BEFORE the response is encoded. Different shape from the bus; sibling primitive.

**Recommendation:** Phase 1 + 2 ship with A. If the race window proves observable in real testing, B lands as a follow-up. The pub/sub bus doesn't have to solve every cross-cutting concern — request/response middleware is a legitimately different shape.

### 14.2 Type-name versioning

`MeshFrame.ServiceEvent.type_name` is the Go reflect-type name today (e.g. `chat.ChannelCreatedEvent`). Renaming the Go struct breaks the wire. Acceptable for v1 since field renames already break clients (per the typed-op convention).

For long-term stability, consider an explicit per-event `WireID string` registered alongside the Go type — decouples wire identity from Go-source-level renames. Defer to v2.

### 14.3 Loop detection

Handler-publishes-event-that-re-triggers-handler is a real footgun. Mitigation today: convention + reviewer discipline. Future: a `bus.depth` thread-local that increments on Publish-from-handler and panics at depth > N (configurable). Defer; convention suffices for v1.

### 14.4 Per-event capability gating

Some events carry sensitive payloads (e.g. `auth.AuthLoginSucceededEvent` carries `SessionToken`). Should the bus support "only services with capability X may subscribe to type Y"? Probably yes long-term — a tiny extension to `RegisterEventType` (`RegisterEventTypeWithCapability[T](cap)`) and to the subscribe path (coord checks the subscribing process's service-account capabilities before accepting the subscribe). Defer to when service-account auth lands (currently deferred per `chat-service-design.md` §15.6).

### 14.5 Cross-cluster federation

Out of scope. If future deployments want cross-cluster events (one game running multiple coordinator clusters federated via XMPP-style), that's a separate design — likely a federation gateway service that subscribes to local bus events and forwards to remote clusters. The local bus stays unchanged.

### 14.6 Event metadata / context propagation

Should events carry an opaque "context" (trace ID, request ID, caller user_id) for distributed tracing? Probably yes long-term — every event currently has POD payload only. Add a `Metadata map[string]string` field on `ServiceEvent` (wire) that the Bus auto-populates from the publish-site's context. Defer.

## 15. Migration & rollout

### Prerequisites

- (none beyond the existing chat + auth services)

### Phased landing

**Phase 1 (bus primitive, process-local):**
- ~600 LOC added, ~100 deleted
- No wire-protocol change; works on every existing deployment shape
- Chat refactor lands in the same change set
- ~2-3 days for one engineer

**Phase 2 (auth event extraction):**
- ~150 LOC change net
- Same change set as Phase 1 if landed together; separate if Phase 1 ships first
- 1-2 days

**Phase 3 (cluster-wide peer-mesh):**
- ~800-1200 LOC added, ~50 deleted
- Wire-protocol changes (proto regeneration)
- Service-host MeshData participation
- End-to-end multi-process e2e test
- ~5-7 days for one engineer

**Phase 4 (docs):**
- ~few hours

### Estimated total scope: ~1500-1800 LOC + ~5-7 day implementation effort

This is the foundation for **every** future cluster-aware service — auction house, guild management, presence, friends list, telemetry sidecars — without per-service core PRs. The pay-back is across all subsequent service work.

## 16. Future work (deferred from v1)

- **Auth response-interception middleware primitive** (§14.1 option B)
- **Loop depth detection** (§14.3)
- **Per-event capability gating** (§14.4)
- **Cross-cluster federation** (§14.5)
- **Event metadata / trace context** (§14.6)
- **Durable bus variant** for events that must survive crashes (Postgres-backed event log; subscribers replay on reconnect). Out of scope for v1.
- **Request-response over the bus** (Bus.Call[Req, Res]) — turning the bus into a generic RPC. Probably unnecessary given the typed-op channel already exists; defer indefinitely.
- **Wire-stable event identifiers** decoupled from Go struct names (§14.2).
- **Event observability dashboard** — `service events tail`, `service events count-by-type`, etc. Console + HTTP. Small scope; nice to have.
