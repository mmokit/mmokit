# Admin-Topic Forwarding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Host-side `mmokit.PublishAdminTopic` calls reach the coordinator's admin SSE bus in distributed mode, instead of silently no-oping.

**Architecture:** Mirror the LogBatch path: a new `HostMessage.AdminTopicEvent{topic, payload_json}` rides the existing MeshControl bidi stream from remote-host processes to the coordinator, where a demux case invokes a `Process.OnRemoteAdminTopic` callback that mmokit bridges into the coordinator's `TopicBus` as `json.RawMessage`. Send side is best-effort and never blocks: a `sendIfReady` variant drops when the control stream is down instead of waiting up to 5s like `send`.

**Tech Stack:** Go, protobuf (`buf generate` via `just proto`), gRPC MeshControl stream, existing `pkg/universe` distributed test fixture.

**Spec:** `docs/superpowers/specs/2026-06-11-admin-topic-forwarding-design.md`

---

## File Structure

- Modify: `proto/meshpb/mesh.proto` — `AdminTopicEvent` message + `HostMessage` oneof field 22
- Generated: `gen/go/meshpb/*` — via `just proto`, never hand-edited
- Modify: `pkg/universe/mesh_control_client.go` — `sendIfReady` (non-blocking send variant)
- Create: `pkg/universe/admin_topic_forward.go` — `ForwardsAdminTopics`, `ForwardAdminTopic`, `OnRemoteAdminTopic` (keeps coordinator.go from growing)
- Modify: `pkg/universe/coordinator.go` — `remoteAdminTopic` atomic callback field on `Process` (next to `remoteLogBatch`, ~line 494)
- Modify: `pkg/universe/mesh_control_server.go` — demux case (next to `HostMessage_LogBatch`, ~line 288)
- Create: `pkg/universe/admin_topic_forward_test.go` — e2e round-trip over the distributed fixture
- Modify: `pkg/mmokit/admin.go` — forward branch in `PublishAdminTopic`, `remoteAdminTopicBridge` helper, bridge registration in `DefaultAdminServerFactory`
- Modify: `pkg/mmokit/mmokit.go` — register `catAdmin` log category (next to `registerTuneVerbs`, ~line 809)
- Modify: `pkg/mmokit/admin_test.go` — bridge unit test (reuses existing `fakeSub`)

---

### Task 1: Wire format

**Files:**
- Modify: `proto/meshpb/mesh.proto`

- [ ] **Step 1: Add the oneof field**

In `message HostMessage`, after `LogBatch log_batch = 21;` (line 37):

```proto
    AdminTopicEvent   admin_topic_event   = 22;  // admin SSE: host → coord game-panel/tunables topic publish
```

- [ ] **Step 2: Add the message definition**

After `message LogEntry` (~line 120):

```proto
// AdminTopicEvent carries one host-side mmokit.PublishAdminTopic call to the
// coordinator's admin TopicBus. payload_json is the pre-marshaled payload —
// opaque to the mesh layer; the coordinator re-publishes it verbatim as
// json.RawMessage so SSE subscribers see the same shape as a local publish.
// Best-effort: dropped when the control stream is down.
message AdminTopicEvent {
  string topic        = 1;
  bytes  payload_json = 2;
}
```

- [ ] **Step 3: Regenerate**

Run: `just proto`
Expected: exits 0; `gen/go/meshpb/mesh.pb.go` now contains `HostMessage_AdminTopicEvent` and `AdminTopicEvent`.

- [ ] **Step 4: Verify compilation**

Run: `go vet ./gen/... ./pkg/universe/`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add proto/meshpb/mesh.proto gen/go/meshpb/
git commit -m "feat(meshpb): AdminTopicEvent host→coord message"
```

---

### Task 2: Universe plumbing (send + receive), TDD via distributed fixture

**Files:**
- Create: `pkg/universe/admin_topic_forward_test.go`
- Create: `pkg/universe/admin_topic_forward.go`
- Modify: `pkg/universe/mesh_control_client.go` (after `send`, ~line 429)
- Modify: `pkg/universe/coordinator.go` (Process struct, next to `remoteLogBatch` field ~line 494)
- Modify: `pkg/universe/mesh_control_server.go` (demux, next to `HostMessage_LogBatch` case ~line 288)

- [ ] **Step 1: Write the failing e2e test**

`pkg/universe/admin_topic_forward_test.go`:

```go
package universe

import (
	"bytes"
	"testing"
	"time"
)

// TestAdminTopicForwarding_HostToCoord proves the full host→coord path: a
// remote-host process ships an AdminTopicEvent over the real MeshControl
// stream and the coordinator's OnRemoteAdminTopic callback receives topic +
// payload verbatim. Uses the distributed fixture (separate host-role
// processes over gRPC), the same harness as the S6/S7 capstones.
func TestAdminTopicForwarding_HostToCoord(t *testing.T) {
	dfx := newDistributedFixture(t, FixtureConfig{
		CellsX:  2,
		CellsY:  1,
		HostIDs: []string{"h1", "h2"},
	}).(*distributedFixture)
	coord := dfx.Coord()

	type evt struct {
		topic   string
		payload []byte
	}
	got := make(chan evt, 4)
	coord.OnRemoteAdminTopic(func(topic string, payload []byte) {
		select {
		case got <- evt{topic, payload}:
		default:
		}
	})

	host := dfx.hosts["h1"]
	if !host.ForwardsAdminTopics() {
		t.Fatal("remote host-role process must report ForwardsAdminTopics() == true")
	}
	if coord.ForwardsAdminTopics() {
		t.Fatal("coordinator process must report ForwardsAdminTopics() == false")
	}

	want := []byte(`{"system":"wave","rows":[{"Field":"Amplitude","Value":"42"}]}`)

	// Re-send until delivered: the control stream is up once the fixture
	// returns, but sendIfReady legitimately drops during any reconnect blip,
	// so a single-shot send would be flaky by design.
	deadline := time.After(10 * time.Second)
	for {
		if err := host.ForwardAdminTopic("tunables", want); err != nil {
			t.Logf("ForwardAdminTopic (will retry): %v", err)
		}
		select {
		case e := <-got:
			if e.topic != "tunables" {
				t.Errorf("topic = %q, want %q", e.topic, "tunables")
			}
			if !bytes.Equal(e.payload, want) {
				t.Errorf("payload = %s, want %s", e.payload, want)
			}
			return
		case <-time.After(200 * time.Millisecond):
			// not yet — send again
		case <-deadline:
			t.Fatal("AdminTopicEvent never reached the coordinator callback")
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./pkg/universe/ -run TestAdminTopicForwarding_HostToCoord -count=1`
Expected: compile FAIL — `coord.OnRemoteAdminTopic undefined`, `host.ForwardsAdminTopics undefined`, `host.ForwardAdminTopic undefined`.

- [ ] **Step 3: Add the callback field to Process**

In `pkg/universe/coordinator.go`, directly after the `remoteLogBatch func([]RemoteLogEntry)` field (~line 494):

```go
	// remoteAdminTopic is invoked when a host forwards an AdminTopicEvent
	// over MeshControl (host-side mmokit.PublishAdminTopic in distributed
	// mode). Atomic so it can be installed while the control server is
	// already running (tests do this); fires on the controlServer goroutine
	// and must not block. nil = drop. Wired by
	// mmokit.DefaultAdminServerFactory.
	remoteAdminTopic atomic.Pointer[func(topic string, payload []byte)]
```

Add `"sync/atomic"` to coordinator.go's imports if not already present.

- [ ] **Step 4: Add sendIfReady to the control client**

In `pkg/universe/mesh_control_client.go`, after `send` (~line 429):

```go
// sendIfReady sends msg only when the control stream is currently up,
// returning an error immediately (no reconnect wait) when it is down. For
// best-effort telemetry callers (admin topic events) that must never stall —
// unlike send, which blocks up to streamReadyTimeout waiting for the first
// stream. Same sendMu serialization, so ordering is preserved relative to
// other senders.
func (c *meshControlClient) sendIfReady(msg *meshpb.HostMessage) error {
	c.connMu.Lock()
	stream := c.stream
	c.connMu.Unlock()
	if stream == nil {
		return fmt.Errorf("mesh control: stream down, message dropped")
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return stream.Send(msg)
}
```

- [ ] **Step 5: Create the Process API**

`pkg/universe/admin_topic_forward.go`:

```go
package universe

import (
	"fmt"

	meshpb "github.com/zenion/mmokit/gen/go/meshpb"
)

// ForwardsAdminTopics reports whether this process should ship admin topic
// publishes to a remote coordinator instead of publishing to its local bus:
// true only for processes that dialed a remote coordinator (--mode=host or
// --mode=service with --coordinator-addr), which never run the admin SSE
// server themselves. Coordinator-bearing and single-process (`all`) setups
// return false and publish locally.
func (c *Process) ForwardsAdminTopics() bool {
	return c.controlClient != nil
}

// ForwardAdminTopic ships one pre-marshaled admin topic event to the
// coordinator over MeshControl. Best-effort, like the log forwarder: when the
// control stream is down the event is dropped with an error rather than
// blocking on reconnect — admin telemetry must never stall a caller (which
// may be the game loop).
func (c *Process) ForwardAdminTopic(topic string, payloadJSON []byte) error {
	if c.controlClient == nil {
		return fmt.Errorf("universe: ForwardAdminTopic: process has no control client (not a remote host)")
	}
	return c.controlClient.sendIfReady(&meshpb.HostMessage{
		Msg: &meshpb.HostMessage_AdminTopicEvent{
			AdminTopicEvent: &meshpb.AdminTopicEvent{Topic: topic, Payload: payloadJSON},
		},
	})
}

// OnRemoteAdminTopic installs the callback invoked when a host forwards an
// AdminTopicEvent over MeshControl. Stored atomically so it may be installed
// after the control server is live. The callback fires on the controlServer
// goroutine and must not block; payload is the sender's pre-marshaled JSON.
// Wired by mmokit.DefaultAdminServerFactory.
func (c *Process) OnRemoteAdminTopic(fn func(topic string, payload []byte)) {
	c.remoteAdminTopic.Store(&fn)
}
```

- [ ] **Step 6: Add the demux case**

In `pkg/universe/mesh_control_server.go`, after the `case *meshpb.HostMessage_LogBatch:` block (~line 300):

```go
			case *meshpb.HostMessage_AdminTopicEvent:
				if ev := v.AdminTopicEvent; ev != nil {
					if fn := s.coord.remoteAdminTopic.Load(); fn != nil {
						(*fn)(ev.Topic, ev.Payload)
					}
				}
```

- [ ] **Step 7: Run the test to verify it passes**

Run: `go test ./pkg/universe/ -run TestAdminTopicForwarding_HostToCoord -count=1 -race`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add pkg/universe/admin_topic_forward.go pkg/universe/admin_topic_forward_test.go pkg/universe/mesh_control_client.go pkg/universe/mesh_control_server.go pkg/universe/coordinator.go
git commit -m "feat(universe): host→coord admin topic forwarding over MeshControl"
```

---

### Task 3: mmokit branch + coordinator bridge

**Files:**
- Modify: `pkg/mmokit/admin.go` (`PublishAdminTopic` ~line 107; `DefaultAdminServerFactory` next to the `OnRemoteLogBatch` bridge ~line 165)
- Modify: `pkg/mmokit/mmokit.go` (~line 809, next to `registerTuneVerbs`)
- Modify: `pkg/mmokit/admin_test.go`

- [ ] **Step 1: Write the failing bridge test**

Append to `pkg/mmokit/admin_test.go` (reuses the existing `fakeSub` helper; add `"encoding/json"` to the imports):

```go
// TestRemoteAdminTopicBridge_PublishesRawJSON proves the coordinator-side
// bridge re-publishes a forwarded payload verbatim as json.RawMessage, so SSE
// subscribers observe the identical shape a local publish would produce.
func TestRemoteAdminTopicBridge_PublishesRawJSON(t *testing.T) {
	t.Parallel()
	proc := &universe.Process{}
	sub := &fakeSub{topics: []string{"tunables"}, notify: make(chan struct{}, 1)}
	adminBus(proc).Subscribe(sub, sub.topics...)

	fn := remoteAdminTopicBridge(proc)
	want := `{"system":"wave","rows":[]}`
	fn("tunables", []byte(want))

	sub.wait(t, 1, 2*time.Second)
	sub.mu.Lock()
	defer sub.mu.Unlock()
	raw, ok := sub.received[0].payload.(json.RawMessage)
	if !ok {
		t.Fatalf("payload type = %T, want json.RawMessage", sub.received[0].payload)
	}
	if string(raw) != want {
		t.Errorf("payload = %s, want %s", raw, want)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./pkg/mmokit/ -run TestRemoteAdminTopicBridge -count=1`
Expected: compile FAIL — `remoteAdminTopicBridge` undefined.

- [ ] **Step 3: Implement the mmokit side**

In `pkg/mmokit/admin.go`, add `"encoding/json"` to imports, add the category constant near the top of the file:

```go
// catAdmin tags engine-side admin plumbing logs (topic forward drops, etc.).
// Registered in mmokit.New.
const catAdmin = "admin"
```

Replace the body of `PublishAdminTopic` (keep the existing doc comment, append the forwarding note):

```go
// PublishAdminTopic publishes payload on topic to the admin dashboard's
// SSE multiplexer. Game-registered admin panels subscribe to topics by
// name (PanelDef.Topics) — this is the matching push surface.
//
// No-op when no subscribers are listening. Safe to call from any
// goroutine. The bus is per-Process so test fixtures get isolation.
//
// On a remote host-role process (distributed mode) the local bus has no
// subscribers — the admin SSE server lives on the coordinator — so the
// payload is JSON-marshaled and forwarded over MeshControl instead.
// Best-effort: dropped (with a catAdmin log line) when the control
// stream is down. Callers need no changes either way.
func PublishAdminTopic(coord *universe.Process, topic string, payload any) {
	if coord.ForwardsAdminTopics() {
		b, err := json.Marshal(payload)
		if err != nil {
			coord.Log.Log(catAdmin, "PublishAdminTopic: marshal topic %q: %v", topic, err)
			return
		}
		if err := coord.ForwardAdminTopic(topic, b); err != nil {
			coord.Log.Log(catAdmin, "PublishAdminTopic: topic %q dropped: %v", topic, err)
		}
		return
	}
	adminBus(coord).Publish(topic, payload)
}
```

Add the bridge helper below it:

```go
// remoteAdminTopicBridge returns the OnRemoteAdminTopic callback that
// re-publishes forwarded host events onto this coordinator's local bus. The
// payload is the sender's pre-marshaled JSON; json.RawMessage embeds it
// verbatim when the SSE writer marshals, so dashboard subscribers see the
// same shape as a local publish.
func remoteAdminTopicBridge(c *universe.Process) func(topic string, payload []byte) {
	bus := adminBus(c)
	return func(topic string, payload []byte) {
		bus.Publish(topic, json.RawMessage(payload))
	}
}
```

In `DefaultAdminServerFactory`, directly after the `c.OnRemoteLogBatch(...)` registration (~line 175):

```go
		// Bridge remote-host admin topic publishes (tunables echoes, game
		// panel data) into the same bus the SSE multiplexer reads.
		c.OnRemoteAdminTopic(remoteAdminTopicBridge(c))
```

In `pkg/mmokit/mmokit.go`, next to the `registerTuneVerbs` call (~line 809):

```go
	proc.Log.RegisterCategories(catAdmin)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/mmokit/ -run 'TestRemoteAdminTopicBridge|TestPublishAdminTopic' -count=1 -race`
Expected: PASS — both the new bridge test and the pre-existing `TestPublishAdminTopic_RoutesToBus` (which now also exercises the `ForwardsAdminTopics() == false` branch on a bare `*Process`).

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/admin.go pkg/mmokit/admin_test.go pkg/mmokit/mmokit.go
git commit -m "feat(mmokit): PublishAdminTopic forwards to coord from remote hosts"
```

---

### Task 4: Full verification

- [ ] **Step 1: Vet + full affected suites**

Run: `go vet ./... && go test ./pkg/universe/ ./pkg/mmokit/ ./pkg/admin/ -count=1`
Expected: clean vet; all PASS (universe suite takes a few minutes — S6/S7 fixtures).

- [ ] **Step 2: Build**

Run: `just build`
Expected: exits 0, binary in `bin/`.

- [ ] **Step 3: Done — no commit needed unless fixes were required**

If any step surfaced a fix, commit it with a `fix:` message referencing the failing test.
