# Host → Coordinator Admin-Topic Forwarding

**Date:** 2026-06-11
**Status:** Approved

## Problem

`mmokit.PublishAdminTopic` publishes to the per-process admin `TopicBus`, but the
admin SSE server (and therefore every dashboard subscriber) lives on the
coordinator process. In distributed mode, any publisher running on a remote
host-role process — `publishTunables` after `tune.set`/`tune.reset` (which are
`RouteAllHosts`, so their handlers execute on hosts), or any game panel
publisher running host-side — publishes into a bus with zero subscribers. The
event is silently lost: the `/tunables` page misses live echoes, and host-side
game panels are a distributed-mode footgun.

Single-process (`all`) mode is unaffected: the colocated host IS the
coordinator process, so the local bus publish reaches the SSE server.

## Design

Mirror the existing LogBatch path (host `logger.Hook` → `HostMessage.LogBatch`
over MeshControl → coordinator demux → `OnRemoteLogBatch` → admin bus), which
is the established precedent for host→coord admin telemetry.

### Wire format (`proto/meshpb/mesh.proto`)

```proto
message AdminTopicEvent {
  string topic        = 1;
  bytes  payload_json = 2;
}
```

Added to the `HostMessage` oneof as field `22` (next free; `51` is the
services-bus block). Payload is pre-marshaled JSON — opaque to the mesh layer,
exactly what the SSE writer emits on the other end. Regenerate with
`just proto`.

### Host side (send)

`mmokit.PublishAdminTopic(proc, topic, payload)` gains one branch:

- If the process forwards admin topics (it is a remote host-role process — the
  same processes that run the log forwarder, identified by having a live
  `controlClient`): JSON-marshal the payload and ship it via a new
  `universe.Process.ForwardAdminTopic(topic string, payloadJSON []byte) error`
  helper wrapping `controlClient.send`.
- Otherwise (coordinator-bearing or single-process): publish to the local bus,
  exactly as today.

The decision is exposed as a small predicate on `*universe.Process` (e.g.
`ForwardsAdminTopics() bool`) so mmokit never reaches into universe internals.

**Callers need zero changes.** `publishTunables` and game-side
`PublishAdminTopic` calls become distributed-transparent.

### Coordinator side (receive)

- `mesh_control_server.go` demux gains a `*meshpb.HostMessage_AdminTopicEvent`
  case invoking a new callback `Process.OnRemoteAdminTopic(fn func(topic
  string, payload []byte))` (same pattern as `OnRemoteLogBatch`).
- `mmokit.DefaultAdminServerFactory` registers the bridge next to the existing
  LogBatch bridge:

```go
c.OnRemoteAdminTopic(func(topic string, payload []byte) {
    adminBus(c).Publish(topic, json.RawMessage(payload))
})
```

`json.RawMessage` embeds verbatim when the SSE writer marshals, so dashboard
subscribers observe the identical shape a local publish would have produced.

## Error handling

Best-effort, like logs:

- Marshal failure on the host: log under the existing admin/tune category and
  drop.
- Send failure: drop; the MeshControl stream reconnects and the next event
  rides the new stream.
- No batching layer: events are operator-action frequency (worst case ~12/sec
  per host during a debounced slider drag). Direct send, provided
  `controlClient.send` is non-blocking/mutex-serialized (verify at
  implementation; add a small buffered-channel guard only if it can block).

## Accepted quirk

After a cluster-wide `tune.set`, each of N hosts echoes identical post-mutation
rows → N SSE events with the same payload. Harmless — the `/tunables` page
replaces a system's rows idempotently. Not worth a dedup layer.

## Out of scope

- Gateway-role processes as publishers (no current callers). A gateway uses a
  different client (`meshGatewayClient`) and gets its own plumbing if ever
  needed. Service-role processes share the `controlClient` path, so they gain
  forwarding for free — fine, but untested until a real publisher exists.
- Coordinator→host topic distribution (dashboards only live on the
  coordinator).
- Deduplicating per-host echoes.

## Testing

1. **Unit (receive):** the MeshControl server demux invokes the registered
   `OnRemoteAdminTopic` callback with the topic and payload bytes.
2. **E2E (distributed fixture, coordinator + remote host):** a host-side
   `PublishAdminTopic` round-trips to a coordinator-side bus subscriber with
   an identical JSON shape.
3. **Regression (single-process):** `PublishAdminTopic` on a colocated process
   still publishes locally and does not attempt a forward; existing tunable
   e2e suite stays green.
