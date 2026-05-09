# Services Event Bus — Manual Test Plan

Verifies the work delivered by Phase 1, Phase 2, Phase 3, Phase 4, and the post-review follow-ups. Run end-to-end before declaring the migration "done in production."

**Prerequisites:**
- Postgres up: `just db-up`
- Build clean: `just build` (produces `bin/server` + web bundle)
- Distributed binary: `examples/4node-basic` available; `just distributed-space` recipe spawns 5 processes via tmux.
- Server console accessible (interactive REPL on stdin of any process).
- Web client available at `http://localhost:8080` after `just dev`.

Each test below has a **Goal**, **Steps**, and **Pass criteria**. Mark `[ ]` → `[x]` as you go. Skip a test only if its phase isn't deployed.

---

## Test 1 — Phase 1 single-process: chat presence flows through the bus

**Goal:** Verify the gateway publishes `SessionEnterEvent` / `SessionLeaveEvent` and the chat service consumes them via the per-process Bus (no chatHook scaffolding in the path).

- [ ] **1.1** Boot the all-in-one dev server: `just dev`
- [ ] **1.2** Open two browser tabs at `http://localhost:8080`. Log in as `alice` and `bob` (any password — dev mode).
- [ ] **1.3** In the server stdout, look for two log lines:
  - `[services:bus] publish SessionEnterEvent conn=N user=alice gw=...`
  - `[services:bus] publish SessionEnterEvent conn=N user=bob gw=...`
- [ ] **1.4** In the alice tab, send a chat message. In the bob tab, confirm it arrives. (This proves chat presence is wired — chat's bus subscriber populated `online[alice]` so fanout reaches alice's connection.)
- [ ] **1.5** Close the bob tab (or refresh).
- [ ] **1.6** Confirm log: `[services:bus] publish SessionLeaveEvent conn=N gw=...`
- [ ] **1.7** Refresh the alice tab. Confirm chat panel rehydrates.

**Pass criteria:**
- Both `services:bus` log lines fired.
- Chat messages delivered alice ↔ bob.
- No log lines mentioning `chatHook` or `InstallChatHook` (those symbols are deleted).

---

## Test 2 — Phase 2 single-process: auth login/logout/register flow through the bus

**Goal:** Verify auth typed-op handlers publish `AuthLoginSucceededEvent` / `AuthLogoutEvent` / `AuthRegisteredEvent` and the gateway's `subscribeToAuthEvents` populates `g.authStates[connID]` from them — replacing the deleted `auth.GatewayHook`.

- [ ] **2.1** With the dev server running from Test 1 (or restart it).
- [ ] **2.2** From a fresh browser tab, register a new account (`carol` / `secret`).
- [ ] **2.3** In server stdout, **AuthRegisteredEvent should NOT show in `services:bus` logs** by default. The bus event fires but no logging hook is registered on it (Phase 2 didn't add publish logs for auth events — only Session* events). Skip log inspection here.
- [ ] **2.4** Log out via the web UI (or send AUTH_LOGOUT op).
- [ ] **2.5** Log back in as `carol`.
- [ ] **2.6** Confirm: the chat panel hydrates correctly (proves `gateway.onAuthSuccess` fired via the bus subscriber, not the deleted hook).
- [ ] **2.7** Send a chat message. Confirm it delivers (proves `gateway.authStates[connID]` is populated, which gates the next op).

**Pass criteria:**
- Register, login, logout, login flow all work.
- No regressions vs Phase 0 behavior.
- `grep -rn "auth.GatewayHook\|InstallGatewayAuthHook\|installPendingAuthHook" pkg/ examples/ cmd/ internal/` returns zero matches in non-test code.

---

## Test 3 — Phase 3 distributed: cluster bus delivers events across processes

**Goal:** Verify the cluster-wide bus (proto, peer-mesh dispatch, PeerList routing-table propagation, RoleService listener) actually moves events between processes.

This test uses the existing 5-process distributed setup (1 coord + 3 hosts + 1 gateway+service) plus an ad-hoc service-host process spun up in a 6th terminal.

- [ ] **3.1** Stop the dev server. Start the distributed setup: `just distributed-space`. tmux opens with 5 panes.
- [ ] **3.2** From a 6th terminal, spawn a pure service-host:
  ```bash
  bin/server --mode=service --coordinator-addr=localhost:9100 --host-id=svc-extra \
    --postgres-url='postgres://mmo:mmo@localhost:5432/mmo?sslmode=disable'
  ```
- [ ] **3.3** In the **coord** pane (tmux), wait for the registration log:
  ```
  [mesh:cell] coordinator: host svc-extra registered (grpc=127.0.0.1:NNNNN)
  ```
- [ ] **3.4** In the **coord** pane's interactive console, type:
  ```
  host list
  ```
  Verify the table includes `svc-extra` with `SVC-ONLY` column = `yes`. Cell-bearing hosts (`space-host-0`, `space-host-1`, `space-host-2`) show `SVC-ONLY` = `-`.
- [ ] **3.5** In the **coord** console, type:
  ```
  service bus list
  ```
  Output: a routing table. The auth-service event types should NOT appear (they're not wire-eligible — see Test 5). Empty output is the steady state when no game services have registered wire-eligible events.

**Pass criteria:**
- `host list` shows `svc-extra` flagged SVC-ONLY = yes.
- `service bus list` returns successfully (even if empty).
- `svc-extra` does NOT receive a `CellAssign` (no `[mesh:cell] [cell_X_Y] cell started` log on its pane).

---

## Test 4 — Follow-up #1: service-only hosts don't get cell assignments

**Goal:** Confirm the post-review fix (`fe0153c`) — `--mode=service` standalone processes are excluded from the assignment engine.

Continue from Test 3's setup.

- [ ] **4.1** In the **svc-extra** terminal output, scan all logs since startup. Confirm: **no log line matches** `cell started for cell` or `assignCellOnNode`.
- [ ] **4.2** In the **coord** console: `cell list`. Verify all 4 cells (`cell_0_0`, `cell_0_1`, `cell_1_0`, `cell_1_1`) are owned by `space-host-0`, `space-host-1`, or `space-host-2` — never `svc-extra`.
- [ ] **4.3** In the **coord** console, attempt a manual migrate to the service-host:
  ```
  cell migrate cell_0_0 svc-extra
  ```
  Expected: a clear error message rejecting the migration ("cannot migrate to service-only host" or similar).
- [ ] **4.4** In the **coord** console, force a rebalance (whatever recipe exists for it; if none, skip this step). The cell ownership should stay on the cell-bearing hosts.

**Pass criteria:**
- `svc-extra` owns zero cells.
- Manual `cell migrate` to a service-only host is rejected.
- No empty-cell game loops on `svc-extra`'s pane.

---

## Test 5 — Hard Prerequisite #1: wire-eligibility gate keeps SessionToken local

**Goal:** Verify the wire-eligibility opt-in (`dbbcf52`) prevents `AuthLoginSucceededEvent` (which carries `SessionToken`) from federating across processes.

Continue from Test 3+4's setup.

- [ ] **5.1** From the host (outside tmux), trigger an auth login. The simplest path: open `http://localhost:8080` and log in as a test user. The auth handler runs in the gateway+service process (`space-gw-0` pane).
- [ ] **5.2** In the **space-gw-0** pane, the `service.Publish(bus, AuthLoginSucceededEvent{...})` call fires (after `hook.NotifyXxx` was deleted).
- [ ] **5.3** In the **coord** console: `service bus list`.
- [ ] **5.4** Look for `AuthLoginSucceededEvent` in the output. **It must NOT appear** in either the LocalCache or CoordRouter columns. (Auth events are not wire-eligible; the routing table never receives them.)
- [ ] **5.5** Confirm via the source code:
  ```
  grep -rn "RegisterWireEvent" pkg/ internal/ examples/ cmd/
  ```
  Expected matches: only `pkg/universe/services_event_bus_e2e_test.go::e2eBusEvent`. **No production code calls `RegisterWireEvent` for any framework event.**

**Pass criteria:**
- `service bus list` shows zero entries for `AuthLoginSucceededEvent` (or any auth/session event).
- Code grep confirms no production wire-eligible auth events.

---

## Test 6 — Phase 3 cluster delivery: end-to-end publish→receive

**Goal:** Verify a publish on one process actually reaches a subscriber on another. The Phase 3 e2e Go test (`TestServiceEventBus_E2E_SubscribeAndPublish`) covers this in isolation; this manual test exercises the same path in the live distributed setup.

This test requires writing or temporarily enabling a wire-eligible event publisher. **Skip this test if you don't want to run a one-off Go program to exercise it** — the e2e Go test is sufficient evidence.

- [ ] **6.1** Run the existing e2e test 10 times to confirm it's stable:
  ```bash
  go test ./pkg/universe/ -run TestServiceEventBus_E2E_SubscribeAndPublish -count=10 -v
  ```
- [ ] **6.2** Confirm 10/10 pass within ~5 seconds total.

**Pass criteria:**
- 10/10 e2e runs pass.
- Each run takes <500ms (proves the dial-race fix from Follow-up #2).

---

## Test 7 — Follow-up router cleanup: dead processes removed from routing

**Goal:** Verify that when a process disconnects (graceful or crash), `serviceEventRouter.RemoveProcess` fires and stale process IDs don't accumulate.

Continue from Test 3+4+5's setup.

- [ ] **7.1** In the **svc-extra** terminal, send Ctrl-C (graceful leave).
- [ ] **7.2** In the **coord** pane logs, look for:
  - `coordinator: host svc-extra requested GracefulLeave`
  - (Any service-event-router cleanup log; may or may not be visible depending on log verbosity.)
- [ ] **7.3** Re-run `host list` in coord console. Confirm `svc-extra` is gone from the listing.
- [ ] **7.4** Re-run `service bus list` in coord console. Confirm no entries reference `svc-extra` (this is the cleanup path that was bug-fixed in commit `678735e`).
- [ ] **7.5** Forceful crash test (optional): re-spawn `svc-extra`, then `kill -9` the process. Wait for liveness watcher to detect it (~3s for hosts). Confirm same cleanup behavior.

**Pass criteria:**
- After Ctrl-C, `svc-extra` removed from `host list` within 1-2s.
- `service bus list` doesn't show stale `svc-extra` entries.
- After kill -9, same cleanup happens within ~3s heartbeat-timeout window.

---

## Test 8 — Follow-up #2 stress test: subscribe-flush dial race closed

**Goal:** Verify the `controlClient.send` slow-path waits for the stream to come up, so subscribes from a freshly-spawned service-host always reach coord.

- [ ] **8.1** With the distributed cluster running (no `svc-extra`), in a loop, spawn and kill 10 service-host processes back-to-back:
  ```bash
  for i in 1 2 3 4 5 6 7 8 9 10; do
    bin/server --mode=service --coordinator-addr=localhost:9100 --host-id="svc-loop-$i" \
      --postgres-url='postgres://mmo:mmo@localhost:5432/mmo?sslmode=disable' &
    pid=$!
    sleep 0.5
    kill -INT $pid
    wait $pid 2>/dev/null
  done
  ```
- [ ] **8.2** In the coord pane, observe the registration + GracefulLeave logs for each `svc-loop-N`. None should hang.
- [ ] **8.3** Each cycle should complete in ~0.5-1s, with no hangs or "stream not ready" errors in the spawned processes' stderr.

**Pass criteria:**
- All 10 cycles complete cleanly.
- No "stream not ready" or "ServiceEventSubscribe send failed" log entries (those would indicate the dial-race fix didn't catch this path).

---

## Test 9 — Phase 4 docs: contributors find current guidance

**Goal:** Sanity-check that someone reading the project for the first time finds correct, up-to-date guidance. Quick read-through.

- [ ] **9.1** Open `CLAUDE.md`. Search for `control plane`. Verify the "control plane vs data plane" paragraph is present and references `pkg/service.Bus`.
- [ ] **9.2** Open `docs/superpowers/specs/2026-04-27-pluggable-services-design.md`. Verify §2 (or equivalent) describes the v2 generic event bus.
- [ ] **9.3** Open `docs/superpowers/specs/2026-05-01-auth-service-design.md`. Verify the top-of-file note clarifies that the GatewayHook plumbing has been replaced.
- [ ] **9.4** Open `docs/superpowers/specs/2026-05-07-chat-service-design.md`. Verify the top-of-file note clarifies the bus-based migration.

**Pass criteria:**
- All four documents reflect the current architecture.
- No reader following the docs would write code against the deleted `chatHook` / `GatewayHook` patterns.

---

## Test 10 — Regression sweep: full Go test suite

**Goal:** No regressions across the entire repository.

- [ ] **10.1** Run: `go test ./...`
- [ ] **10.2** Run: `go vet ./...`
- [ ] **10.3** Run: `just build`
- [ ] **10.4** Run with race detector on the bus + service paths:
  ```bash
  go test -race ./pkg/service/... ./pkg/services/auth/... ./pkg/services/chat/...
  go test -race ./pkg/universe/ -run "TestBus_|TestEventCodec|TestServiceEvent|TestChat_|TestGateway_SubscribeToAuthEvents|TestProcess_DispatchService|TestProcess_HasService|TestBuildPeerList|TestBroadcastPeerList|TestDeliverServiceEvent"
  ```

**Pass criteria:**
- `go test ./...` all green.
- `go vet ./...` clean.
- `just build` succeeds.
- Race detector clean on the bus-related test set. (Pre-existing races in `pkg/universe` on unrelated paths — `NetIDAllocator`, `cluster_overview` — are acceptable; they're not introduced by this work.)

---

## Final acceptance checklist

After running tests 1-10:

- [ ] All 10 tests have at least their primary criteria passing.
- [ ] No code-level references to deleted symbols (`chatHook`, `ChatSessionHook`, `InstallChatHook`, `auth.GatewayHook`, `InstallGatewayAuthHook`, `installPendingAuthHook`, `pendingAuthHook`) survive in `pkg/`, `internal/`, `cmd/`, `examples/` (comments documenting the migration are OK).
- [ ] No production code calls `RegisterWireEvent[T]()` for an auth-related event type.
- [ ] `service.bus.list` console command exists and returns successfully on every process role (`coord`, `host`, `gateway`, `service`).
- [ ] The Phase 3 e2e test (`TestServiceEventBus_E2E_SubscribeAndPublish`) passes 10/10 with no fixture workarounds.

If all checked: the services event bus migration is production-validated. Sign off and close out the project memory entries.

---

## Test plan history

- **2026-05-09:** Initial plan covering Phase 1 + 2 + 3 + 4 + Hard Prereqs + Follow-ups #1-2. Authored after the cluster bus + observability + service-host filter + dial-race fix all landed.
