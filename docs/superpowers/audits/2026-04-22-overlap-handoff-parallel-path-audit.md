# Overlap Handoff Parallel-Path Audit

**Date:** 2026-04-22
**Branch:** overlap-handoff
**HEAD:** a18f57d
**Task:** D6 — pure audit of replicaNetIDs / netIDIdx / Shadow / MarkForRemoval call sites

---

### Call sites: replicaNetIDs[

All sites are in `pkg/universe/world_base.go` (test-file reads are assertions only):

- `world_base.go:913` — `DemoteLiveToReplica` writes `b.replicaNetIDs[netID] = ent` after a successful `netIDIdx.Demote`. The entity was Live, is now Replica; the map is the canonical lookup table for subsequent border-frame updates. ✅
- `world_base.go:1073` — `upsertBorderReplica` reads existing map entry to update position/velocity on an already-known replica entity. ✅
- `world_base.go:1128` — `upsertBorderReplica` writes `b.replicaNetIDs[netID] = ent` after creating a new replica ECS entity. ✅
- `world_base.go:1200` — `RemoveReplicaByNetID` reads + deletes the entry then calls `ECS.RemoveEntity`. Correct teardown path. ✅
- Test-file reads (`border_replication_apply_test.go`, `border_replication_stub_test.go`, `handoff_driver_test.go`) — assertion/verification only; no production write paths. ✅

---

### Call sites: netIDIdx.Enter / Exit / Lookup / Demote

- `coordinator.go:1221` — `OnEntityRemoved` callback: `netIDIdx.Exit(netID)` fires when an entity is actually removed from the ECS. This is the correct teardown signal — it fires for any removal (death, TTL expiry, MarkForRemoval flush), so the index stays consistent. ✅
- `world_base.go:283` — `LookupNetID` public helper; read-only. ✅
- `world_base.go:715` — `SpawnFromTransferCore`: `netIDIdx.Enter(frame.NetworkID, entity, presence)` where `presence` is the argument passed in by the caller. Callers are `SpawnFromTransfer` (PresenceLive) and `SpawnShadow` (PresenceShadow). Both are correct — Live for a full transfer receive, Shadow for a pre-authority warmup shadow. ✅
- `world_base.go:838` — `PromoteShadow`: `netIDIdx.Enter(netID, entity, PresenceLive)` transitions Shadow→Live. This is the only place that should do Shadow→Live on the destination side (at Commit). Code comment confirms this is the sanctioned path. ✅
- `world_base.go:879` — `DemoteLiveToReplica`: `netIDIdx.Lookup(netID)` read-only guard to confirm entity is Live before demoting. ✅
- `world_base.go:906` — `DemoteLiveToReplica`: `netIDIdx.Demote(netID, ent)` — the ONLY call site that flips Live→Replica. Called only from `DemoteLiveToReplica`, which is called only from `HandoffDriver.fireCommit`. ✅
- `world_base.go:1058` — `upsertBorderReplica` shadow fast-path: `netIDIdx.Lookup(netID)` read-only check to detect an existing Shadow and short-circuit replica creation during overlap warmup. No Enter/Exit here. ✅
- `world_base.go:1132` — `upsertBorderReplica` new-replica path: `netIDIdx.Enter(netID, ent, PresenceReplica)`. Only reached when no Shadow and no existing replica is found for netID. On `ActionRejected` (slot already Live or Shadow), the just-created ECS entity is removed in strict mode — correctly preserving the existing Live/Shadow presence. ✅
- `world_base.go:1438` — `SpawnEntity` (local authoritative spawn): `netIDIdx.Enter(nid, entity, PresenceLive)`. Local spawns are always Live. ✅

---

### Call sites: component.Shadow / shadowMap.Add / shadowMap.Remove

- `world_base.go:786` — `SpawnShadow`: `shadowMap.Add(entity, &component.Shadow{...})`. The ONLY production path that adds `component.Shadow`. Called from `handleHandoffPrepare` when the destination cell receives a Prepare message. ✅
- `world_base.go:825` — `PromoteShadow`: `shadowMap.Remove(entity)`. Called at Commit — the authorized removal path. ✅
- `border_replication_shadow_test.go:29,71` — test fixtures directly adding `component.Shadow` to bootstrap shadow entities for unit tests. Not production code. ✅
- `handoff_driver_test.go:135` — test fixture. Not production code. ✅

No other production code adds or removes `component.Shadow`. ✅

---

### Call sites: MarkForRemoval (pkg/universe/)

- `world_base.go:502` — thin delegation: `WorldBase.MarkForRemoval(e)` wraps `b.eng.MarkForRemoval(e)`. This is the public interface method; all callers below are the actual sites of interest. ✅
- `world_base.go:935` — `RemoveShadowByNetID`: marks a Shadow entity for removal (cancelled handoff or watchdog timeout). The entity carries `component.Shadow` — it is a Shadow, not a Live entity. ✅
- `world_base.go:1194` — `ExpireReplicas`: marks a Replica entity (has `component.Replica`) for removal after TTL exhaustion. The entity is a Replica (border ghost), not a Live entity. ✅
- `world_base.go:1302` — `TickGhosts`: marks a Ghost-tagged entity (`component.Ghost` filter) for removal. Ghost is a legacy marker applied to entities that have already had their authority transferred in the old direct-transfer path; it is not applied anywhere in the new overlap-handoff path on this branch. Any entity found by `TickGhosts` is therefore not a Live handoff entity. ✅
- `coordinator.go:1594` — `defaultEntityOpts.Remove` closure: admin console `entity remove <netID>` command. Scans all NetworkID entities and calls `wb.MarkForRemoval`. This is an operator-directed despawn (intentional entity destruction), not a handoff path. ✅
- `universe_test.go:51` — `mockWorld.MarkForRemoval` stub. Test infrastructure, not production. ✅

---

### Violations (if any)

None found.

---

### Suspicious (if any)

None found. All call sites follow the post-D4 overlap-handoff protocol:

1. `MarkForRemoval` for Live netID entities: not called in any transfer/handoff/border flow. Only the admin console `Remove` closure can remove a Live entity by netID, and that is an explicit operator action (genuine despawn), not a handoff step.
2. `netIDIdx.Enter(_, _, PresenceReplica)` on a Live slot: `upsertBorderReplica` correctly handles `ActionRejected` — it tears down the orphan ECS entity in strict mode rather than silently installing a second presence.
3. `netIDIdx.Demote` is called exclusively from `DemoteLiveToReplica`, which is called exclusively from `HandoffDriver.fireCommit`.
4. `component.Shadow` is added only in `SpawnShadow` and removed only in `PromoteShadow` (Commit path) or `RemoveShadowByNetID` (cancel / watchdog path).
