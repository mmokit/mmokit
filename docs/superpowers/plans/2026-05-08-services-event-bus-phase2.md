# Services Event Bus — Phase 2 Implementation Plan (Auth Event Extraction)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the auth-specific `auth.GatewayHook` plumbing with bus-based event publication. After this plan, auth's login/register/validate-token/logout success handlers publish typed events on `service.Bus`; the gateway subscribes locally to update `authStates[connID]` and dispatch `PlayerAssignment`. No more per-service core hooks for auth — auth is a normal service consuming/emitting bus events.

**Architecture:**
- Auth handlers (in `pkg/mmokit/auth.go`) publish `service.AuthLoginSucceededEvent` / `service.AuthRegisteredEvent` / `service.AuthLogoutEvent` after their underlying `Service.Handle*` returns. Same publish point that today calls `hook.NotifyXxx`.
- Gateway subscribes to `AuthLoginSucceededEvent` + `AuthLogoutEvent` at construction time. Subscriber populates `g.authStates[connID]` and calls `g.dispatchPostAuthAssignment` — same body that the synchronous `OnSuccess` callback runs today.
- Process-local Bus is **synchronous** (Phase 1 contract): the subscriber runs inline before the typed-op response is encoded. Auth + gateway are colocated by deployment (RouteGatewayLocal) so the spec §14.1 "race window" is degenerate — no async observable gap. We pick spec option **A** (async-only event publish) without the race risk because process-local subscriber dispatch is synchronous.
- `Process.AuthResolver` **stays.** It validates an existing session-token cookie at WS-upgrade time (synchronous lookup, not a notification). Pub/sub doesn't model "validate this token" cleanly; spec §13 explicitly leaves the `AuthResolver` decision open and we keep it for the right reason.

**Tech Stack:** Phase 1's `pkg/service.Bus`, the existing `pkg/services/auth` typed handlers, the gateway's existing `authStates` map + `onAuthSuccess` / `onAuthLogout` paths.

**Reference spec:** [docs/superpowers/specs/2026-05-08-services-event-bus-design.md](../specs/2026-05-08-services-event-bus-design.md) §7 (event types), §12 Phase 2, §14.1 (auth response interception model).

**Prerequisite:** Phase 1 plan ([2026-05-08-services-event-bus-phase1.md](2026-05-08-services-event-bus-phase1.md)) merged to `main`. Verify with: `grep -q "func NewBus" pkg/service/bus.go && echo "Phase 1 present"`.

**Memories that govern this work:**
- `feedback_no_backward_compat` — no aliases for `GatewayHook`. Delete it cleanly.
- `feedback_security_best_practices` — `AuthLoginSucceededEvent` carries `SessionToken`. Currently no per-event capability gating (spec §14.4 deferred). We mitigate by ensuring only same-process subscribers exist in Phase 2 (Phase 3 cluster-wide bus changes the threat surface; revisit gating then).
- `feedback_logging` — every new server-side path logs under `services:bus`.

**Phase 2 does NOT depend on Phase 3.** Phase 3 (cluster-wide peer-mesh) builds on Phase 1; Phase 2 builds on Phase 1. Either order works after Phase 1.

---

## File Structure

**New files:**
- None. Phase 2 is purely refactor — every file already exists.

**Modified files:**
- `pkg/services/auth/service.go::Init` — capture `ctx.Bus` onto the Service for handler use.
- `pkg/mmokit/auth.go:99-159` — replace `hook.NotifyXxx(...)` calls with `service.Publish[T]`.
- `pkg/universe/gateway.go` — add `subscribeToAuthEvents(bus *service.Bus)` method; call from gateway construction. Remove `installAuthHook` field/method/getter.
- `pkg/universe/coordinator.go:619` — drop `pendingAuthHook *auth.GatewayHook` field.
- `pkg/universe/coordinator.go:1200-1252` — drop `InstallGatewayAuthHook` + `installPendingAuthHook` methods.
- `pkg/universe/coordinator.go::Build` — remove the `c.installPendingAuthHook()` call.

**Deleted files:**
- `pkg/services/auth/gateway_hook.go` — entire `GatewayHook` struct + four `NotifyXxx` methods.

**Untouched (kept for the right reason):**
- `Process.AuthResolver` field + `auth.Resolver` interface — used at WS-upgrade time for cookie validation, not for login notification. Different shape from pub/sub; keeping.

---

## Task 1: Preflight — confirm Phase 1 is on main

**Files:**
- No changes. Verification only.

**Why:** Phase 2 is meaningless without Phase 1's Bus + framework events. Cheap to confirm, expensive to debug if missing.

- [ ] **Step 1: Verify Phase 1 artifacts exist**

```bash
grep -q "func NewBus" pkg/service/bus.go || echo "MISSING: pkg/service/bus.go::NewBus"
grep -q "AuthLoginSucceededEvent" pkg/service/events.go || echo "MISSING: AuthLoginSucceededEvent in events.go"
grep -q "AuthLogoutEvent" pkg/service/events.go || echo "MISSING: AuthLogoutEvent in events.go"
grep -q "AuthRegisteredEvent" pkg/service/events.go || echo "MISSING: AuthRegisteredEvent in events.go"
grep -q "Bus \*Bus" pkg/service/context.go || echo "MISSING: Bus field on service.Context"
```

Expected: no `MISSING:` lines. Any miss → halt, return to Phase 1.

- [ ] **Step 2: Verify chat already uses the bus pattern**

```bash
grep -q "service.Subscribe(ctx.Bus, func(ev service.SessionEnterEvent)" pkg/services/chat/service.go || echo "MISSING: chat subscribe pattern"
grep -q "chatHook" pkg/universe/coordinator.go && echo "MISSING: chatHook leftover from Phase 1"
```

Expected: no `MISSING:` lines. The chat refactor in Phase 1 is the template Phase 2 follows.

- [ ] **Step 3: No commit (verification only)**

---

## Task 2: Hold a `*service.Bus` reference on `auth.Service`

**Files:**
- Modify: `pkg/services/auth/service.go::Init`

**Why:** Auth's typed-op handlers are wired in `pkg/mmokit/auth.go` (the facade). They need access to the bus to publish. The cleanest plumbing is to capture `ctx.Bus` onto `*auth.Service` at Init time, then have the facade reach `liveService.Bus()`. Mirrors how chat captures `s.ctx = ctx` in Phase 1.

- [ ] **Step 1: Locate `auth.Service` struct + Init method**

Run: `grep -n "type Service struct\|func (s \*Service) Init" pkg/services/auth/service.go`

Open the file. Add a private field to the struct:

```go
type Service struct {
	// ... existing fields ...
	bus *service.Bus
}
```

- [ ] **Step 2: Capture `ctx.Bus` in `Init`**

In `func (s *Service) Init(ctx *service.Context) error`, near the top (mirror the chat pattern from Phase 1):

```go
	s.bus = ctx.Bus
```

- [ ] **Step 3: Add an exported accessor**

```go
// Bus returns the per-process service bus this auth instance was wired
// onto. Used by the mmokit facade to publish typed Auth*Event values
// from typed-op handler wrappers. Returns nil before Init.
func (s *Service) Bus() *service.Bus {
	return s.bus
}
```

- [ ] **Step 4: Verify compile**

Run: `go vet ./pkg/services/auth/...`
Expected: no errors. (Auth's existing tests don't reference Bus, so no test impact yet.)

- [ ] **Step 5: Commit**

```bash
git add pkg/services/auth/service.go
git commit -m "$(cat <<'EOF'
feat(auth): hold *service.Bus reference on auth.Service

Captured at Init from ctx.Bus; exposed via Bus() accessor so the mmokit
facade's typed-op handler wrappers can publish events on success.
Field is unused by callers in this commit.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Gateway subscribes to `AuthLoginSucceededEvent` / `AuthLogoutEvent`

**Files:**
- Modify: `pkg/universe/gateway.go` — add `subscribeToAuthEvents` method; wire it into the construction path.

**Why:** Same ordering principle as Phase 1's chat refactor: add the new consumer FIRST, then add the publisher, so the existing `hook.NotifyXxx` path keeps working until the very last step. Gateway must subscribe before any client logs in — easiest place is at gateway construction (before any goroutine accepts connections).

- [ ] **Step 1: Add the subscribe method to Gateway**

Open `pkg/universe/gateway.go`. Find the gateway construction site (search for `func newGateway` or `Gateway{` literal — the gateway is built in `coordinator.go` via something like `&Gateway{...}`). Add a new method:

```go
// subscribeToAuthEvents wires the bus subscribers that today's
// pendingAuthHook callbacks fire. Called from coordinator.Build after
// the bus is constructed and the gateway is wired. The subscribers run
// synchronously inline on Publish (process-local Phase 1 contract) so
// authStates[connID] is populated BEFORE the typed-op response leaves
// the gateway.
//
// Mirrors the body of the previous installPendingAuthHook closures.
func (g *Gateway) subscribeToAuthEvents(bus *service.Bus) {
	if bus == nil {
		return
	}
	service.Subscribe(bus, func(ev service.AuthLoginSucceededEvent) {
		uid, err := uuid.Parse(ev.UserID)
		if err != nil {
			g.log.Log(CatNetConn, "gateway: AuthLoginSucceededEvent: bad user_id %q: %v", ev.UserID, err)
			return
		}
		g.onAuthSuccess(ev.ConnID, uid, ev.Username, ev.SessionToken, 0)
	})
	service.Subscribe(bus, func(ev service.AuthLogoutEvent) {
		g.onAuthLogout(ev.ConnID)
	})
}
```

Note: the legacy `OnSuccess` callback also passed `expiresAtMs`. The Phase 1 `AuthLoginSucceededEvent` POD struct (per spec §7) does NOT include `expiresAtMs` — it's intentionally minimal. If `g.onAuthSuccess` actually needs that value, two options:

**Option A (preferred):** add `ExpiresAtMs int64` to `service.AuthLoginSucceededEvent` (small spec-aligned extension). Edit `pkg/service/events.go` and add the field. Re-register is automatic (typed registry already keys on the Go type).

**Option B:** drop the parameter from `onAuthSuccess` if no caller actually reads it.

Run: `grep -n "expiresAtMs\|ExpiresAtMs" pkg/universe/gateway.go` to check whether `onAuthSuccess`'s caller consumes the value past the field assignment.

If `expiresAtMs` is stored in `connAuthState` but never read for an authorization decision, **Option B**. Otherwise **Option A**. Document the choice in the commit.

- [ ] **Step 2: Call `subscribeToAuthEvents` from the gateway construction path**

In `pkg/universe/coordinator.go::Build`, replace the line `c.installPendingAuthHook()` call with:

```go
	if c.gateway != nil && c.bus != nil {
		c.gateway.subscribeToAuthEvents(c.bus)
	}
```

(Keep `c.installPendingAuthHook()` for now — both paths fire concurrently; the deletion happens in Task 6. After both fire, `g.authStates[connID]` is set twice with identical values, and `dispatchPostAuthAssignment` is called twice. The second `dispatchPostAuthAssignment` sees `IsReconnect=true` and is idempotent. The redundancy is intentional and short-lived — Task 6 deletes one path.)

Wait — that's a problem. `dispatchPostAuthAssignment` may NOT be idempotent (it sends a `PlayerAssignment` to the cell, and double-dispatch could cause double-spawn). Check:

```bash
grep -n "func.*dispatchPostAuthAssignment\|dispatchPostAuthAssignment" pkg/universe/gateway.go | head -10
```

Read its body. If non-idempotent, **don't run both paths concurrently**. Instead:

- For Tasks 3-5, the new bus path is wired but the gateway's subscribe handler is gated by a feature flag (e.g. `if !g.useAuthBus { return }`).
- Task 6 flips the gate and deletes the old path together.

Implement the gate as a `g.useAuthBus bool` field, default true. The legacy hook checks `if g.useAuthBus { return }` at the top of its OnSuccess closure (added in Step 3 below). This makes the cutover a single-bit change.

Actually a simpler approach: **make the new path replace the old in one commit**, and skip the redundant-firing window entirely. That's Task 6's job. For now (Task 3), only ADD the subscribe method — don't call it. Task 6 wires the call AND deletes the legacy hook in the same commit. This is cleaner; the cost is no incremental verification, but the unit test in Task 5 covers it.

**Revised Step 2: do nothing in this task. The `subscribeToAuthEvents` method is added but uncalled.**

- [ ] **Step 3: Verify compile**

Run: `go vet ./pkg/universe/...`
Expected: no errors. Method `subscribeToAuthEvents` is unused — Go's `vet` flags this. If it does:

```go
// subscribeToAuthEvents — Task 6 wires the call site.
//
//nolint:unused // wired in Task 6
func (g *Gateway) subscribeToAuthEvents(bus *service.Bus) {
```

(Use the `//nolint:unused` directive only if vet complains; otherwise omit.)

- [ ] **Step 4: Add a test that the subscribe method works in isolation**

Create or extend `pkg/universe/gateway_auth_bus_test.go`:

```go
package universe

import (
	"testing"

	"github.com/google/uuid"

	"github.com/mmokit/mmokit/pkg/service"
)

func TestGateway_SubscribeToAuthEvents_PopulatesAuthState(t *testing.T) {
	// Construct a bare gateway with the same authStates map plumbing as
	// production. Avoids the heavy fixture path — we only need to prove
	// the subscriber wires up correctly.
	g := newTestBareGateway(t) // helper added below
	bus := service.NewBus("test-proc")
	g.subscribeToAuthEvents(bus)

	uid := uuid.NewString()
	service.Publish(bus, service.AuthLoginSucceededEvent{
		ConnID:       42,
		UserID:       uid,
		Username:     "alice",
		SessionToken: "tok-xyz",
		GatewayID:    g.id,
	})

	g.authMu.Lock()
	state, ok := g.authStates[42]
	g.authMu.Unlock()
	if !ok {
		t.Fatal("authStates[42] not populated by subscribe handler")
	}
	if state.username != "alice" || state.sessionToken != "tok-xyz" {
		t.Fatalf("authStates content wrong: %+v", state)
	}

	service.Publish(bus, service.AuthLogoutEvent{ConnID: 42, GatewayID: g.id})
	g.authMu.Lock()
	_, stillThere := g.authStates[42]
	g.authMu.Unlock()
	if stillThere {
		t.Fatal("authStates[42] not cleared by AuthLogoutEvent subscriber")
	}
}
```

Add the `newTestBareGateway` helper in the same file. It allocates a `Gateway` struct with the minimum fields populated (id, log, authStates map, authMu mutex). Reference: existing test helpers in `pkg/universe/gateway*_test.go`. The test should NOT exercise `dispatchPostAuthAssignment` — that requires the full coord/host plumbing. Stub it via a test-only flag if needed:

```go
func (g *Gateway) onAuthSuccessForTest(...) { /* skip dispatchPostAuthAssignment */ }
```

OR: factor `onAuthSuccess` so the dispatch is a separate call. The test-isolation goal is "subscriber populates authStates" — dispatch is exercised by the existing e2e auth tests after Task 6.

- [ ] **Step 5: Run the test**

Run: `go test ./pkg/universe/ -run TestGateway_SubscribeToAuthEvents_PopulatesAuthState -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/universe/gateway.go pkg/universe/gateway_auth_bus_test.go pkg/service/events.go
git commit -m "$(cat <<'EOF'
feat(universe): add Gateway.subscribeToAuthEvents (unused; wired in next commit)

Subscriber populates authStates[connID] from AuthLoginSucceededEvent
and clears it on AuthLogoutEvent. Mirrors the existing pendingAuthHook
closures verbatim. Method is registered but uncalled — Task 6 wires
the call site and deletes the synchronous hook in the same change.

(Optionally also extends service.AuthLoginSucceededEvent with
ExpiresAtMs if the audit identified a real consumer.)

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Auth handlers publish events on success

**Files:**
- Modify: `pkg/mmokit/auth.go:105-152` — replace `hook.NotifyXxx(...)` calls with `service.Publish[T]`.

**Why:** Auth's wrappers in the facade are where success notifications fire today. Replacing those four call sites ports the publish behavior to the bus. The legacy hook still receives via `hook.NotifyXxx` because the wrapper still calls it — both paths fire. Task 6 deletes the legacy path.

- [ ] **Step 1: Add `service.Publish` after each `hook.NotifyXxx` call (additive)**

Edit `pkg/mmokit/auth.go`. After the `hook = p.InstallGatewayAuthHook()` line (~99), and inside each of the four wrapper closures (login at ~110, register at ~122, validate-token at ~134, logout at ~146), add the publish call.

For `handleLogin` (around line 110):

```go
	RegisterOp[auth.AuthLoginRequest, auth.AuthLoginResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *auth.AuthLoginRequest) (*auth.AuthLoginResponse, error) {
			if liveService == nil {
				return nil, errAuthServiceNotReady
			}
			resp, err := liveService.HandleLogin(opCtx, req)
			if err != nil {
				return nil, err
			}
			hook.NotifyLoginSuccess(opCtx.ConnID, resp) // legacy path — deleted in Task 6
			service.Publish(liveService.Bus(), service.AuthLoginSucceededEvent{
				UserID:       resp.UserID,
				Username:     resp.Username,
				SessionToken: resp.SessionToken,
				ConnID:       opCtx.ConnID,
				GatewayID:    opCtx.GatewayID, // verify field exists on OpContext
			})
			return resp, nil
		})
```

(Verify `OpContext.GatewayID`: run `grep -n "GatewayID" pkg/ops/opcontext.go` or wherever OpContext lives. If absent, derive from `opCtx`'s available fields — likely `opCtx.SessionKey.GatewayID` or similar.)

For `handleRegister` (around line 122) — fires both `AuthLoginSucceededEvent` AND `AuthRegisteredEvent`:

```go
			hook.NotifyRegisterSuccess(opCtx.ConnID, resp)
			service.Publish(liveService.Bus(), service.AuthLoginSucceededEvent{
				UserID:       resp.UserID,
				Username:     resp.Username,
				SessionToken: resp.SessionToken,
				ConnID:       opCtx.ConnID,
				GatewayID:    opCtx.GatewayID,
			})
			service.Publish(liveService.Bus(), service.AuthRegisteredEvent{
				UserID:   resp.UserID,
				Username: resp.Username,
			})
```

For `handleValidateToken` (around line 134) — fires `AuthLoginSucceededEvent` with empty `SessionToken` per spec §7:

```go
			hook.NotifyValidateTokenSuccess(opCtx.ConnID, resp)
			service.Publish(liveService.Bus(), service.AuthLoginSucceededEvent{
				UserID:    resp.UserID,
				Username:  resp.Username,
				// SessionToken intentionally empty: validate-token doesn't mint a token
				ConnID:    opCtx.ConnID,
				GatewayID: opCtx.GatewayID,
			})
```

For `handleLogout` (around line 146):

```go
			hook.NotifyLogoutSuccess(opCtx.ConnID)
			service.Publish(liveService.Bus(), service.AuthLogoutEvent{
				UserID:    resp.UserID,    // verify response carries UserID; if not, capture from req or session
				Username:  resp.Username,  // ditto
				ConnID:    opCtx.ConnID,
				GatewayID: opCtx.GatewayID,
			})
```

Add the import `"github.com/mmokit/mmokit/pkg/service"` to `pkg/mmokit/auth.go` if not present.

- [ ] **Step 2: Verify compile**

Run: `go vet ./pkg/mmokit/...`
Expected: no errors. If `OpContext.GatewayID` doesn't compile, fix the field reference per the actual OpContext shape.

- [ ] **Step 3: Add a unit test that publishes are observable**

Append to `pkg/mmokit/auth_bus_test.go` (new file):

```go
package mmokit

import (
	"testing"

	"github.com/mmokit/mmokit/pkg/service"
)

func TestAuthFacade_HandlerPublishesAuthLoginSucceededEvent(t *testing.T) {
	// Construct an in-process Process with auth registered against an
	// in-memory mock. Subscribe a counter to AuthLoginSucceededEvent on
	// the process's bus. Drive a login through the typed-op handler.
	// Assert one publish.
	t.Skip("requires the existing auth-mock fixture; wire when running this task")
	// Reference: pkg/services/auth/authtest/ + RegisterAuthServiceWithMock
	// pattern. The test exercises the typed-op handler wrapper directly,
	// not the WS path.
}
```

(Mark skipped until the fixture wiring is confirmed; the deletion in Task 6 catches any regression by failing the existing e2e auth test suite. The skipped test is a placeholder for future regression coverage.)

- [ ] **Step 4: Run the existing auth test suite as a regression check**

Run: `go test ./pkg/mmokit/... ./pkg/services/auth/...`
Expected: all PASS. The legacy `hook.NotifyXxx` path is still wired, so behavior is unchanged. The new `service.Publish` calls fire but no subscriber is wired (Task 3's `subscribeToAuthEvents` is unused).

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/auth.go pkg/mmokit/auth_bus_test.go
git commit -m "$(cat <<'EOF'
feat(mmokit): auth handlers publish AuthLoginSucceededEvent / Auth{Logout,Registered}Event

Replaces no observable behavior in this commit — both the legacy
hook.NotifyXxx call AND the new service.Publish fire. Task 6 deletes
the legacy path and wires the bus subscriber.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Switch over — wire the gateway subscriber, remove legacy hook fire

**Files:**
- Modify: `pkg/universe/coordinator.go::Build` — replace `c.installPendingAuthHook()` call with `c.gateway.subscribeToAuthEvents(c.bus)`.
- Modify: `pkg/mmokit/auth.go` — drop the `hook.NotifyXxx(...)` call from each wrapper.
- Modify: `pkg/mmokit/auth.go` — drop the `hook := p.InstallGatewayAuthHook()` line.

**Why:** This is the cutover. Before this commit, both paths fire (redundant but identical-effect). After this commit, only the bus path fires. The hook scaffolding is now unreachable; Task 6 deletes it.

- [ ] **Step 1: Switch the Build call**

In `pkg/universe/coordinator.go::Build`, find the line:

```go
	c.installPendingAuthHook()
```

Replace with:

```go
	if c.gateway != nil && c.bus != nil {
		c.gateway.subscribeToAuthEvents(c.bus)
	}
```

- [ ] **Step 2: Drop the four `hook.NotifyXxx` calls**

In `pkg/mmokit/auth.go`, in each wrapper (lines ~114, ~126, ~138, ~150), delete:

```go
			hook.NotifyLoginSuccess(opCtx.ConnID, resp)     // delete this
			hook.NotifyRegisterSuccess(opCtx.ConnID, resp)  // delete this
			hook.NotifyValidateTokenSuccess(opCtx.ConnID, resp) // delete this
			hook.NotifyLogoutSuccess(opCtx.ConnID)          // delete this
```

The corresponding `service.Publish(...)` calls added in Task 4 stay.

- [ ] **Step 3: Drop the `hook := p.InstallGatewayAuthHook()` line**

In `pkg/mmokit/auth.go` around line 99:

```go
	hook := p.InstallGatewayAuthHook()  // delete this
```

The `hook` variable now has no remaining references; Go will fail to compile with "hook declared but not used" — that's the regression check confirming all four `NotifyXxx` calls were removed in Step 2.

- [ ] **Step 4: Verify compile + tests pass**

Run: `go vet ./... && go test ./pkg/services/auth/... ./pkg/mmokit/... ./pkg/universe/...`
Expected: PASS. The bus subscriber populates `authStates[connID]` synchronously inline on `service.Publish` (Phase 1's Bus is process-local synchronous), so all existing auth tests work unchanged.

If a test fails because a fixture didn't construct the Bus: ensure the test path goes through `New(...)` which constructs `c.bus` (Phase 1 Task 5). Tests that hand-roll `&Process{...}` literals must also set `bus: service.NewBus("test-proc")`.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/coordinator.go pkg/mmokit/auth.go
git commit -m "$(cat <<'EOF'
refactor(universe,mmokit): cut over auth login/logout flow to service.Bus

- coordinator.Build calls Gateway.subscribeToAuthEvents(c.bus) instead
  of installPendingAuthHook
- mmokit/auth.go: drop hook.NotifyXxx calls; only service.Publish fires
- hook variable + InstallGatewayAuthHook call removed from RegisterAuthService

Behavior is identical to before — the bus path runs synchronously
inline, populating authStates[connID] before the typed-op response is
encoded. No race window per spec §14.1 option A in the colocated
auth+gateway deployment.

Legacy GatewayHook scaffolding is now unreachable; Task 6 deletes it.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Delete the dead `GatewayHook` scaffolding

**Files:**
- Delete: `pkg/services/auth/gateway_hook.go`
- Modify: `pkg/universe/coordinator.go` — drop `pendingAuthHook` field (line 619), `InstallGatewayAuthHook` (lines 1200-1205), `installPendingAuthHook` (lines 1226-1252).
- Modify: `pkg/universe/gateway.go` — drop `installAuthHook` method + any `authHook` field on Gateway.

**Why:** Greenfield refactor per `feedback_no_backward_compat`. After Task 5 nothing reaches the hook; this task removes the unreachable code.

- [ ] **Step 1: Delete `pkg/services/auth/gateway_hook.go`**

```bash
git rm pkg/services/auth/gateway_hook.go
```

- [ ] **Step 2: Drop `Process.pendingAuthHook` field**

In `pkg/universe/coordinator.go` around line 619 (before the `chatHook` line that Phase 1 already deleted), delete:

```go
	pendingAuthHook *auth.GatewayHook
```

…and any preceding doc comment lines.

- [ ] **Step 3: Drop `Process.InstallGatewayAuthHook`**

In `pkg/universe/coordinator.go` around lines 1200-1205, delete the entire `func (c *Process) InstallGatewayAuthHook() *auth.GatewayHook { ... }` definition + its preceding doc comment.

- [ ] **Step 4: Drop `Process.installPendingAuthHook`**

In `pkg/universe/coordinator.go` around lines 1226-1252, delete the entire `func (c *Process) installPendingAuthHook() { ... }` definition + its preceding doc comment.

- [ ] **Step 5: Drop `Gateway.installAuthHook` and `authHook` field**

Run: `grep -n "installAuthHook\|authHook" pkg/universe/gateway.go`

For each result:
- `g.authHook` field declaration → delete
- `g.installAuthHook(...)` method → delete
- Any internal use of `g.authHook` (e.g. `if g.authHook != nil { ... }`) → delete (those branches are now dead).

- [ ] **Step 6: Drop the `auth.GatewayHook` import from `pkg/universe/coordinator.go`**

Run: `grep -n "pkg/services/auth" pkg/universe/coordinator.go`

If the only remaining reference to `auth.*` is `auth.Resolver` (from the `AuthResolver` field), keep the import. Otherwise — actually `auth.Resolver` is still in use, so the import stays. Just confirm no `auth.GatewayHook` reference survives:

```bash
grep -n "auth.GatewayHook" pkg/ examples/ cmd/
```

Expected: no matches.

- [ ] **Step 7: Verify the full project compiles + all tests pass**

Run: `go vet ./... && just build && go test ./...`
Expected: clean.

- [ ] **Step 8: Run a manual smoke if `examples/4node-basic` registers auth**

Run: `grep -n "RegisterAuth" examples/4node-basic/main.go`

If present:

```bash
just dev
```

Open the web client, log in, send chat messages, log out, log back in. Watch the server console for `services:bus` log entries showing `AuthLoginSucceededEvent` / `AuthLogoutEvent` publishes.

If nothing observable broke: smoke pass.

- [ ] **Step 9: Commit**

```bash
git add -A
git commit -m "$(cat <<'EOF'
refactor(auth,universe): delete GatewayHook scaffolding

Auth is now a normal service consuming + emitting service.Bus events.
No engine-special-cased hooks remain. Deleted:
- pkg/services/auth/gateway_hook.go (struct + 4 NotifyXxx methods)
- pkg/universe/coordinator.go: pendingAuthHook field,
  InstallGatewayAuthHook, installPendingAuthHook
- pkg/universe/gateway.go: installAuthHook method, authHook field

Process.AuthResolver kept — it's a synchronous WS-upgrade-time
validator, not a notification, and pub/sub doesn't model it.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Plan complete — verify spec coverage

- [ ] **Step 1: Verify against spec §12 Phase 2 bullets**

| Spec bullet | Task |
|---|---|
| Auth handlers publish AuthLoginSucceededEvent / AuthLogoutEvent / AuthRegisteredEvent | Task 4 |
| Gateway subscribes to AuthLoginSucceededEvent at startup; populates authStates + dispatches PlayerAssignment | Tasks 3, 5 |
| Deletes: Process.AuthResolver field — **DEFERRED, KEPT FOR REASON** (§14.1 sync semantics) | Documented in plan |
| Deletes: pkg/auth.GatewayHook | Task 6 |
| Deletes: related install methods | Task 6 |

- [ ] **Step 2: Confirm all tests pass**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 3: Confirm `just build` succeeds**

Run: `just build`
Expected: clean.

- [ ] **Step 4: Update memory with what landed**

Save `project_services_event_bus_phase2.md` describing what shipped:
- Auth refactored to bus
- `Process.AuthResolver` kept (decision rationale: synchronous WS-upgrade lookup, not notification)
- §14.1 race window: degenerate in colocated auth+gateway because process-local Bus is synchronous

- [ ] **Step 5: Final merge**

Per `user_solo_developer`, merge to `main` directly — no PR.

---

## Out of scope for this plan

- **Phase 3 (cluster-wide peer-mesh)** — separate plan ([2026-05-08-services-event-bus-phase3.md](2026-05-08-services-event-bus-phase3.md)). Phase 2 lands cleanly without Phase 3.
- **§14.1 Option B (synchronous before-encode middleware)** — not needed in the colocated deployment. Revisit only if a future deployment splits auth onto a remote process AND a post-login race becomes observable.
- **Per-event capability gating (§14.4)** — `AuthLoginSucceededEvent` carrying `SessionToken` is fine in Phase 2 (process-local bus, only same-process subscribers exist). Becomes a real concern when Phase 3 lands cross-process delivery; revisit then.
- **Removing `Process.AuthResolver`** — explicitly KEPT in this plan. Spec §13 left this open; the right answer is "keep, because it's a synchronous validator, not a notification".
