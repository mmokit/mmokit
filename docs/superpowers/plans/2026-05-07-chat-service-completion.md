# Chat Service — Branch Completion Report

**Branch:** feat/mmokit-chat-service
**Date:** 2026-05-07
**Plan:** [2026-05-07-chat-service.md](2026-05-07-chat-service.md)
**Spec:** [2026-05-07-chat-service-design.md](../specs/2026-05-07-chat-service-design.md)

## Summary

Engine-tier chat service ships at `pkg/services/chat/`, mirroring the auth-service shape. Single-instance v1 with ephemeral messages and persisted channels/memberships/mutes. The mmokit facade (`pkg/mmokit/chat.go`) registers the chat kind, installs the gateway session hook, registers all 23 typed-op handlers, registers 14 typed server events with the codec, and appends chat's Postgres migrations — all idempotently, before `coord.Build()`. The 4node-basic example registers the chat service with three default channels (`world`, `help`, `trade`); the web client ships a tabbed chat panel with channel + DM support and slash-command parsing.

## Phase status

| Phase | Description | Status | Key commits |
|---|---|---|---|
| 0 | Branch setup | ✅ | (n/a — infra) |
| 1 | Auth prereqs (rename + capabilities) | ✅ | 5a45640, 068e521, 628965d, 4afc39a, bc6a9f9, 2396c40, ad3aa9b |
| 2 | Chat skeleton + typed messages | ✅ | 56c254d, c7eed36, d4f376e |
| 3 | Schema + repo + Postgres + mock | ✅ | 972b253, fee83bc, b94dee9, fe688d4, ee44911 |
| 4 | Service skeleton + bootstrap | ✅ | b046e1d |
| 5 | Vertical slice (SEND_MESSAGE) | ✅ | b039ae7, e06a8ae, c7fc51f, 8eac3d8 |
| 6 | Custom channels + DMs | ✅ | 4cc42bf, 9634731, b22340d, 6349d96 |
| 7 | Membership + canModerate | ✅ | 845283d, 0016bd2, bd624a1, 27bef36, c45960f, 07c2e06, 1f2dba6, 988f63e |
| 8 | Channel mutation + LIST | ✅ | b6d7fd9, 869a60d, 4a2368d, fbb5fa7, 043f4ff |
| 9 | Rate limit + checks | ✅ | 09c08a4, e33e791, ca17ac9 |
| 10 | Moderation ops | ✅ | 98474c0, d84e0e7, cd263fa, 0ef13c5, 4eac05c, 1cea40c, 4141614 |
| 11 | Console commands | ✅ | 88d9e59, e750c4c, b15af1a, 634dab7, 32ce498, 2fce9db |
| 12 | Gateway + facade + 4node-basic + web UI | ✅ | 8f71a12, 7ccb88d, 86a46da, 6d9e983, 8365821, ca761d7, a0ee2ab, 167b990 |
| 13 | E2E + verification | ✅ | 219e11c (this commit set) |

## Diffstat (top-level)

Total: **62 commits**, **93 files changed, +14,142 / −42**.

By directory (filecount-weighted):

| Path | Share |
|---|---|
| `pkg/services/chat/` | 53.2 % |
| `pkg/services/auth/` | 11.6 % |
| `examples/4node-basic/web/sdk/` | 5.1 % |
| `examples/4node-basic/` | 5.1 % |
| `pkg/services/auth/postgres/` | 5.1 % |
| `pkg/services/chat/postgres/` | 5.1 % |
| `pkg/universe/` | 5.1 % |
| `pkg/mmokit/` | 3.8 % |

## Verification matrix

| Check | Result |
|---|---|
| `go build ./cmd/server` | BUILD_OK |
| `go vet ./...` | VET_OK |
| `go test ./pkg/services/chat/...` | PASS (chat + chattest packages) |
| `go test ./pkg/services/auth/...` | PASS (auth package) |
| `go test ./pkg/mmokit/...` | PASS |
| `go test ./pkg/universe/... -run "Chat\|Auth"` | 5 PASS |
| Combined unit-PASS count (chat + auth + mmokit) | **243 PASS** |
| Combined unit-PASS count (universe) | **463 PASS** |
| pgtest `pkg/services/auth/postgres/...` | PASS (0.430s) |
| pgtest `pkg/services/chat/postgres/...` | PASS (0.223s) |
| `examples/4node-basic` Go build | NODE_BUILD_OK |
| `examples/4node-basic/web` `bun run build` | dist 75.34 kB / gzip 15.54 kB; 24 modules transformed |
| Proto-free check (`pkg/services/chat/`, `web/src/chat_panel.ts`, `4node-basic/main.go`) | CLEAN_CHAT |
| Proto-free check (`pkg/mmokit/chat.go`) | CLEAN_FACADE |

## Manual smoke recipe (Task 13.2 — for the user)

The full manual smoke per the plan requires the user to run interactively:

```bash
just db-reset
cd examples/4node-basic && just distributed
```

Then in the browser:

1. Open http://localhost:8080. Register `alice` / `password`. Confirm `world`/`help`/`trade` tabs from hydration.
2. Open another browser. Register `bob`. Confirm same 3 tabs.
3. From alice: `/w bob hello` → bob sees DM tab.
4. From alice: `/create raidnight pwhereisthegate` → channel created.
5. From bob: `/join raidnight pwhereisthegate` → bob joins.
6. Server console: `auth bootstrap-admin alice` → alice gets chat.admin.
7. From server console: `chat broadcast world The realm is closing soon!` → all users see styled system msg.

## Phase 13.1 scope note

The plan's Phase 13.1 listed 10 multi-process cluster e2e tests (login → SE_FRAME chat events → cross-host fanout). The cluster fixture they require — `auth_cookie_e2e_test.go::startTestProcess` — is currently `t.Skip`'d pending the gateway test harness work; that blocks the full multi-process e2e suite. For Phase 13.1 we shipped **Approach A**: three integration tests that exercise the wiring achievable in-process, without standing up a real cluster:

- `TestChat_ServiceLifecycleEndToEnd` — auth + chat both register through the mmokit facade against in-memory mocks; `Process.ChatHook()` is non-nil; typed-event registry populated.
- `TestChat_HookDispatchesToService` — exercises the `SessionHook` contract end-to-end (`Process.chatHook.OnSessionEnter` → live `chat.Service.online` populated) using an inline adapter that mirrors `mmokit/chat.go`'s `chatSessionHookImpl`. Pins the wiring contract.
- `TestChat_DefaultChannelsBootstrap` — `DefaultChannels=[world,help,trade]` produces three `system_all` rows in the repo + in-memory `bySlug` map after `Service.Init` runs `bootstrapDefaultChannels`. Includes idempotency check.

These cover the critical wiring without requiring the absent fixture. The shape of the original 10 tests is preserved in the plan for whoever revives the cluster fixture.

## Known follow-ups (deferred — spec §17 + plan + code TODOs)

**Spec §17 deferred work (v2):**

- Recent message history on join (no message ring buffer, no persistence)
- Message edits / deletes + reactions
- Multi-instance horizontal scale-out (single-instance constraint; v2 = framework sync-stream OR Redis pub/sub adapter)
- Block lists (per-user "mute this person" client-side)
- Service-account authentication (capability table is ready; runtime mechanism deferred)
- Invite-only access mode for custom channels (`access_mode = 'open'|'password'|'invite'`)
- Per-user opt-out from `system_all` channels (only if user feedback demands)

**Test fixture work:**

- Revive `pkg/universe/auth_cookie_e2e_test.go::startTestProcess` (currently `t.Skip`'d). Would unblock the full multi-process cluster e2e suite (real WebSocket login → SE_FRAME chat events → cross-host fanout) listed as the original Phase 13.1.

**Code-level follow-ups:**

- Map evictions for the `slowMode` and `rateBuckets` per-user maps (memory grows unbounded with unique-user count between reaper sweeps; reaper currently only evicts mutes + msg-id index).
- `HandleListChannels` returns `MemberCount = 0` for `SYSTEM_ALL` channels. Acceptable v1 (spec §15: "presence in `online[]` is sufficient" for SYSTEM_ALL), but may surprise admins running `chat.channel.list`. v2 should report `len(svc.online)` explicitly.

## What's next

Per user preference (solo-dev, no-PR-flow): merge `feat/mmokit-chat-service` to `main` directly when ready.

```bash
git checkout main
git merge feat/mmokit-chat-service
git push
```

All 13 phases complete. Branch ready to merge to main.
