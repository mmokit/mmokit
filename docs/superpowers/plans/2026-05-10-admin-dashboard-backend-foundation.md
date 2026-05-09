# Admin Dashboard — Backend Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the curl-testable backend half of the admin dashboard — `pkg/admin/` with `ClusterView` / `TopicBus` / session auth / SSE / all `/admin/api/*` HTTP routes wired to cmdsys, mounted onto the coordinator's `AdminListen` mux behind `Config.Admin.Enabled`.

**Architecture:** A new `pkg/admin/` package layered on top of the existing coordinator surface (cmdsys.Dispatcher, CommitLog, metrics snapshots, *Process accessors). All cluster reads go through a `ClusterView` interface (`LocalClusterView` is the v1 in-process impl); all live updates go through a `TopicBus` (one in-memory impl + SSE subscriber). Auth reuses `pkg/services/auth` primitives (argon2id, IP rate limiter, opaque tokens, cookie helpers) wrapped by an admin-specific session store. No frontend in this plan — the SPA is a follow-up.

**Tech Stack:** Go (`net/http`, `embed`, `encoding/json`), `pkg/services/auth` for crypto, existing `pkg/cmdsys` for command routing, existing `pkg/universe` for Process state, `pkg/logger` for category-based logging.

**Spec:** [`docs/superpowers/specs/2026-05-10-admin-dashboard-design.md`](../specs/2026-05-10-admin-dashboard-design.md)

---

## Quick orientation

Files an implementer should skim before starting:

- `pkg/universe/coordinator.go` — `*Process` struct, `c.registry`, `c.dispatcher`, `c.commitLog`, `c.MetricsHandler()`, `c.MetricsSnapshots()`, `c.Cells()`, `c.ServesClients()`
- `pkg/universe/bootstrap.go:213-239` — `startAdminHTTPListener` is where we mount; pattern for `mux.Handle(...)`
- `pkg/universe/commit_log.go` — `CommitLog`, `CommitEvent`, `Recent`, `Since`, `ByCommitID`, `ByCell`
- `pkg/universe/cmdsys_http_test.go` — pattern for HTTP handler tests (`httptest.NewRequest`, `httptest.NewRecorder`)
- `pkg/cmdsys/dispatcher.go` — `Dispatcher.Invoke(ctx, caller, verb, args)` returns `Result` with per-target `TargetResult{OK, Result, Error}`
- `pkg/cmdsys/command.go` — `Caller`, `CallerSource`, `Grant`
- `pkg/services/auth/password.go` — `HashPassword(pw, ArgonParams)`, `VerifyPassword(pw, encoded)`
- `pkg/services/auth/token.go` — `NewToken()` returns `(token, hash, err)`; `HashToken(token)` returns `hash`
- `pkg/services/auth/cookie.go` — `HTTPOpts`, `setAuthCookie`, `clearAuthCookie`, `readAuthCookie` (note: lowercase, package-internal — we'll mirror this pattern in `pkg/admin/cookie.go`)
- `pkg/services/auth/ratelimit.go` — `IPRateLimiter`, `IPRateLimitConfig`
- `pkg/universe/builtins_player.go` — pattern for cmdsys command Args/Result struct definitions
- `pkg/persist/repository.go` — `PlayerRepository` if we need offline player listings

The package import rule from CLAUDE.md: **`pkg/admin/` may import `pkg/cmdsys/`, `pkg/universe/`, `pkg/services/auth/`, `pkg/metrics/`, `pkg/logger/`, `pkg/persist/`, `gen/go/enginepb/`. It must NOT import `pkg/mmokit/` or anything in `internal/`.**

Build/test commands:

- Compile-check: `go vet ./pkg/admin/...`
- Run package tests: `go test ./pkg/admin/...`
- Full build of the example binary: `cd examples/4node-basic && just build`
- Smoke run with admin enabled: `cd examples/4node-basic && ./bin/server --admin-listen=:9101 --admin-enabled`

---

### Task 1: Add `SourceAdminHTTP` to cmdsys

**Files:**
- Modify: `pkg/cmdsys/command.go:67-71`

- [ ] **Step 1: Read the surrounding code**

```bash
sed -n '63,72p' pkg/cmdsys/command.go
```

Expected: the existing `CallerSource` block with `SourceConsole`, `SourceMeshControl`, `SourceTest`.

- [ ] **Step 2: Add the new variant**

Modify `pkg/cmdsys/command.go`:

```go
const (
	SourceConsole     CallerSource = iota // interactive server console
	SourceMeshControl                     // arriving via MeshControl gRPC
	SourceTest                            // in-process test caller
	SourceAdminHTTP                       // arriving via /admin/api/* HTTP
	// SourceChat reserved for post-chat-rework
)
```

- [ ] **Step 3: Verify compile**

Run: `go vet ./pkg/cmdsys/...`
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add pkg/cmdsys/command.go
git commit -m "cmdsys: add SourceAdminHTTP CallerSource variant"
```

---

### Task 2: Define `ClusterView` interface and snapshot types

**Files:**
- Create: `pkg/admin/view.go`

- [ ] **Step 1: Create the interface and DTOs**

```go
// Package admin provides the engine-shipped admin/observability dashboard
// HTTP API and Svelte SPA. The dashboard mounts on the coordinator's
// AdminListen mux when Config.Admin.Enabled is true.
package admin

import (
	"errors"
	"time"
)

// View errors. Implementations map their underlying errors to these so the
// HTTP layer can render consistent responses regardless of whether reads
// come from in-process state or a future remote MeshControl client.
var (
	ErrCellNotFound   = errors.New("admin: cell not found")
	ErrPlayerNotFound = errors.New("admin: player not found")
	ErrUnavailable    = errors.New("admin: cluster view unavailable")
)

// ClusterView is the read surface the admin HTTP layer consumes. v1 has one
// implementation (LocalClusterView) reading from *universe.Process. A future
// RemoteClusterView calls a MeshControl AdminQuery RPC. Handlers, panel
// registry, and tests never branch on the concrete type.
type ClusterView interface {
	Cluster() ClusterInfo
	Cells() []CellInfo
	Cell(id string) (CellInfo, error)
	Hosts() []HostInfo
	Gateways() []GatewayInfo
	Players(filter PlayerFilter) []PlayerInfo
	Player(username string) (PlayerInfo, error)
	CommitLog(query CommitQuery) []CommitEvent
	Perf(cellID string) (PerfSnapshot, error)
}

// ClusterInfo is the cluster-wide one-shot snapshot returned by GET /api/cluster.
type ClusterInfo struct {
	Now           time.Time
	HostCount     int
	GatewayCount  int
	CellCount     int
	SessionCount  int
	TotalEntities int
	RecentEvents  []CommitEvent // last ~20 events for the dashboard's at-a-glance view
}

// CellInfo describes a single cell at any quadtree depth.
type CellInfo struct {
	ID         string   // base or split ID, e.g. "0_0" or "0_0:1"
	Depth      int
	Parent     string   // empty when Depth==0
	HostID     string
	Load       float64  // 0..>1 (CompositeLoad)
	TickP99Us  int64
	TickP95Us  int64
	Entities   EntityCounts
	BytesPS    BytesPerSec
	Neighbors  []string
}

type EntityCounts struct {
	Real      int
	Replica   int
	Ghost     int
	Connected int
}

type BytesPerSec struct {
	Sent uint64
	Recv uint64
}

// HostInfo describes a host process in the cluster.
type HostInfo struct {
	ID             string
	Roles          []string // ["coordinator","host"], etc.
	State          string   // "live"|"draining"|"dead"
	IsLocal        bool
	HeartbeatAgeMS int64
	Cells          []string
	Load           float64 // composite over owned cells
	TotalEntities  int
}

// GatewayInfo describes a gateway process.
type GatewayInfo struct {
	ID           string
	Sessions     int
	BytesSentPS  uint64
	BytesRecvPS  uint64
	Mode         string // "local-shortcut"|"always-proxy"
}

// PlayerFilter is the query for Players().
type PlayerFilter struct {
	Status string // ""|"online"|"offline"
	Search string // username substring
	Limit  int    // default 100
	Offset int
}

// PlayerInfo describes a single player.
type PlayerInfo struct {
	Username  string
	Status    string  // "online"|"offline"
	HostID    string  // online only
	CellID    string  // online only
	WorldX    float32 // online only
	WorldY    float32 // online only
	LastLogin time.Time
}

// CommitQuery is the parameter for CommitLog().
type CommitQuery struct {
	N       int           // limit, 0 = default 200
	Since   time.Duration // 0 = no time filter
	Cell    string
	Commit  string
}

// CommitEvent mirrors universe.CommitEvent for the wire layer. The remote
// view impl produces these from the gRPC response without depending on
// the universe package's struct layout.
type CommitEvent struct {
	CommitID    string
	Scenario    string
	Step        string
	StepIndex   int
	Success     bool
	DurationMs  int64
	Affected    []string
	HostIDs     []string
	Error       string
	Timestamp   time.Time
}

// PerfSnapshot is per-cell tick profiling data.
type PerfSnapshot struct {
	CellID      string
	SystemNames []string
	Systems     []TimingStats
	Total       TimingStats
	SampleCount int
}

type TimingStats struct {
	LatestUs int64
	AvgUs    int64
	P50Us    int64
	P95Us    int64
	P99Us    int64
	MaxUs    int64
}
```

- [ ] **Step 2: Verify compile**

Run: `go vet ./pkg/admin/...`
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add pkg/admin/view.go
git commit -m "admin: define ClusterView interface and snapshot DTOs"
```

---

### Task 3: Implement `LocalClusterView`

**Files:**
- Create: `pkg/admin/view_local.go`
- Test: `pkg/admin/view_local_test.go`

- [ ] **Step 1: Read what universe exposes**

```bash
grep -n 'func (c \*Process) \(Cells\|Hosts\|Gateways\|MetricsSnapshots\|CommitLog\|ActiveUsers\|ActiveUserCell\|TickStats\)' pkg/universe/coordinator.go pkg/universe/perf_snapshot.go pkg/universe/host.go 2>/dev/null
```

Expected: lists the public accessors. Any missing ones — flag in the task and add a small accessor in `pkg/universe/` (separate commit before continuing).

- [ ] **Step 2: Write a fixture-driven test**

Create `pkg/admin/view_local_test.go`:

```go
package admin

import (
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/universe"
)

func TestLocalClusterView_Cluster(t *testing.T) {
	t.Parallel()
	p := newTestProcessForView(t) // helper at bottom of file

	v := NewLocalClusterView(p)
	c := v.Cluster()

	if c.Now.IsZero() {
		t.Fatalf("Now is zero")
	}
	if c.HostCount < 1 {
		t.Fatalf("expected >=1 host, got %d", c.HostCount)
	}
	if c.CellCount != 4 {
		t.Fatalf("expected 4 cells in 2x2 fixture, got %d", c.CellCount)
	}
}

func TestLocalClusterView_Cell_NotFound(t *testing.T) {
	t.Parallel()
	v := NewLocalClusterView(newTestProcessForView(t))
	if _, err := v.Cell("does_not_exist"); err != ErrCellNotFound {
		t.Fatalf("expected ErrCellNotFound, got %v", err)
	}
}

// newTestProcessForView spins up a minimal headless coordinator with a 2x2
// grid so view tests can read live state without a full game wiring.
func newTestProcessForView(t *testing.T) *universe.Process {
	t.Helper()
	cfg := universe.Config{
		Headless:   true,
		HTTPPort:   -1,
		ClusterDim: universe.ClusterDim{X: 2, Y: 2},
		// no Admin block — view doesn't need it
	}
	p, err := universe.NewProcess(cfg)
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	if err := p.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = p.ShutdownNow(time.Second) })
	return p
}
```

- [ ] **Step 3: Run the test (expect compile fail — `NewLocalClusterView` not defined)**

Run: `go test ./pkg/admin/ -run LocalClusterView -v`
Expected: build error mentioning `undefined: NewLocalClusterView`.

- [ ] **Step 4: Implement**

Create `pkg/admin/view_local.go`:

```go
package admin

import (
	"strings"
	"time"

	"github.com/zenion/mmoserver/pkg/metrics"
	"github.com/zenion/mmoserver/pkg/universe"
)

// LocalClusterView reads cluster state directly from a *universe.Process.
// Constructed once at admin server startup; methods are safe to call
// concurrently — they delegate to thread-safe Process accessors.
type LocalClusterView struct {
	p *universe.Process
}

func NewLocalClusterView(p *universe.Process) *LocalClusterView {
	return &LocalClusterView{p: p}
}

func (v *LocalClusterView) Cluster() ClusterInfo {
	cells := v.Cells()
	hosts := v.Hosts()
	gws := v.Gateways()
	totalEntities := 0
	totalSessions := 0
	for _, c := range cells {
		totalEntities += c.Entities.Real
	}
	for _, g := range gws {
		totalSessions += g.Sessions
	}
	return ClusterInfo{
		Now:           time.Now(),
		HostCount:     len(hosts),
		GatewayCount:  len(gws),
		CellCount:     len(cells),
		SessionCount:  totalSessions,
		TotalEntities: totalEntities,
		RecentEvents:  v.CommitLog(CommitQuery{N: 20}),
	}
}

func (v *LocalClusterView) Cells() []CellInfo {
	snaps := v.p.MetricsSnapshots()
	out := make([]CellInfo, 0, len(snaps))
	for id, snap := range snaps {
		out = append(out, cellInfoFromSnapshot(id, snap, v.p))
	}
	return out
}

func (v *LocalClusterView) Cell(id string) (CellInfo, error) {
	snap, ok := v.p.MetricsSnapshot(id)
	if !ok {
		return CellInfo{}, ErrCellNotFound
	}
	return cellInfoFromSnapshot(id, snap, v.p), nil
}

func cellInfoFromSnapshot(id string, snap metrics.LoadSnapshot, p *universe.Process) CellInfo {
	hostID, _ := p.HostForCellID(id)
	depth := strings.Count(id, ":")
	parent := ""
	if i := strings.LastIndex(id, ":"); i >= 0 {
		parent = id[:i]
	}
	return CellInfo{
		ID:        id,
		Depth:     depth,
		Parent:    parent,
		HostID:    hostID,
		Load:      snap.CompositeLoad,
		TickP99Us: snap.Tick.P99Duration.Microseconds(),
		TickP95Us: snap.Tick.P95Duration.Microseconds(),
		Entities: EntityCounts{
			Real:      snap.Entities.Real,
			Replica:   snap.Entities.Replica,
			Ghost:     snap.Entities.Ghost,
			Connected: snap.Entities.Connected,
		},
		BytesPS:   BytesPerSec{Sent: snap.Network.BytesSent, Recv: snap.Network.BytesRecv},
		Neighbors: p.NeighborsOf(id),
	}
}

func (v *LocalClusterView) Hosts() []HostInfo {
	hs := v.p.HostList()
	out := make([]HostInfo, 0, len(hs))
	for _, h := range hs {
		out = append(out, HostInfo{
			ID:             h.ID,
			Roles:          h.Roles,
			State:          h.State,
			IsLocal:        h.IsLocal,
			HeartbeatAgeMS: h.HeartbeatAge.Milliseconds(),
			Cells:          append([]string(nil), h.Cells...),
			Load:           h.Load,
			TotalEntities:  h.TotalEntities,
		})
	}
	return out
}

func (v *LocalClusterView) Gateways() []GatewayInfo {
	gs := v.p.GatewayList()
	out := make([]GatewayInfo, 0, len(gs))
	for _, g := range gs {
		out = append(out, GatewayInfo{
			ID:          g.ID,
			Sessions:    g.Sessions,
			BytesSentPS: g.BytesSentPS,
			BytesRecvPS: g.BytesRecvPS,
			Mode:        g.Mode,
		})
	}
	return out
}

func (v *LocalClusterView) Players(filter PlayerFilter) []PlayerInfo {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	online := v.p.ActiveUsers()
	out := make([]PlayerInfo, 0, len(online))
	for _, u := range online {
		if filter.Search != "" && !strings.Contains(u.Username, filter.Search) {
			continue
		}
		if filter.Status == "offline" {
			continue
		}
		out = append(out, PlayerInfo{
			Username:  u.Username,
			Status:    "online",
			HostID:    u.HostID,
			CellID:    u.CellID,
			WorldX:    u.WorldX,
			WorldY:    u.WorldY,
			LastLogin: u.LastLogin,
		})
	}
	// Offline lookups via PlayerRepository if accessible — kept off the hot
	// path; the v1 dashboard fetches offline players via player.list cmdsys
	// verb when the operator explicitly opts in (?status=all).
	if filter.Status == "all" || filter.Status == "offline" {
		repo := v.p.PlayerRepository()
		if repo != nil {
			records, _ := repo.ListOffline(filter.Search, limit)
			for _, r := range records {
				out = append(out, PlayerInfo{
					Username:  r.Username,
					Status:    "offline",
					LastLogin: r.LastLogin,
				})
			}
		}
	}
	if filter.Offset >= len(out) {
		return nil
	}
	end := filter.Offset + limit
	if end > len(out) {
		end = len(out)
	}
	return out[filter.Offset:end]
}

func (v *LocalClusterView) Player(username string) (PlayerInfo, error) {
	username = strings.ToLower(username)
	if u, ok := v.p.ActiveUserCellInfo(username); ok {
		return PlayerInfo{
			Username:  u.Username,
			Status:    "online",
			HostID:    u.HostID,
			CellID:    u.CellID,
			WorldX:    u.WorldX,
			WorldY:    u.WorldY,
			LastLogin: u.LastLogin,
		}, nil
	}
	repo := v.p.PlayerRepository()
	if repo == nil {
		return PlayerInfo{}, ErrPlayerNotFound
	}
	r, err := repo.GetByUsername(username)
	if err != nil {
		return PlayerInfo{}, ErrPlayerNotFound
	}
	return PlayerInfo{Username: r.Username, Status: "offline", LastLogin: r.LastLogin}, nil
}

func (v *LocalClusterView) CommitLog(q CommitQuery) []CommitEvent {
	cl := v.p.CommitLog()
	if cl == nil {
		return nil
	}
	var raws []universe.CommitEvent
	switch {
	case q.Commit != "":
		raws = cl.ByCommitID(q.Commit)
	case q.Cell != "":
		raws = cl.ByCell(q.Cell)
	case q.Since > 0:
		raws = cl.Since(time.Now().Add(-q.Since))
	default:
		n := q.N
		if n <= 0 {
			n = 200
		}
		raws = cl.Recent(n)
	}
	out := make([]CommitEvent, 0, len(raws))
	for _, r := range raws {
		out = append(out, CommitEvent{
			CommitID:   r.CommitID,
			Scenario:   r.Scenario,
			Step:       r.Step,
			StepIndex:  r.StepIndex,
			Success:    r.Success,
			DurationMs: r.DurationMs,
			Affected:   append([]string(nil), r.Affected...),
			HostIDs:    append([]string(nil), r.HostIDs...),
			Error:      r.Error,
			Timestamp:  r.Timestamp,
		})
	}
	return out
}

func (v *LocalClusterView) Perf(cellID string) (PerfSnapshot, error) {
	ts, ok := v.p.TickStatsForCell(cellID)
	if !ok {
		return PerfSnapshot{}, ErrCellNotFound
	}
	out := PerfSnapshot{
		CellID:      cellID,
		SystemNames: append([]string(nil), ts.SystemNames...),
		Systems:     make([]TimingStats, len(ts.Systems)),
		Total:       toTimingStats(ts.Total),
		SampleCount: ts.SampleCount,
	}
	for i, s := range ts.Systems {
		out.Systems[i] = toTimingStats(s)
	}
	return out, nil
}

func toTimingStats(s metrics.TimingStats) TimingStats {
	return TimingStats{
		LatestUs: s.Latest.Microseconds(),
		AvgUs:    s.Avg.Microseconds(),
		P50Us:    s.P50.Microseconds(),
		P95Us:    s.P95.Microseconds(),
		P99Us:    s.P99.Microseconds(),
		MaxUs:    s.Max.Microseconds(),
	}
}
```

- [ ] **Step 5: Add the missing universe accessors (if any)**

If Step 1 found gaps (e.g. `MetricsSnapshot(id)`, `HostList`, `GatewayList`, `NeighborsOf`, `ActiveUserCellInfo`, `TickStatsForCell`, `PlayerRepository`, `ShutdownNow`) add minimal accessors in `pkg/universe/`. Each accessor is a one-liner reading existing internal state with a read lock if needed. Commit those separately:

```bash
git add pkg/universe/<file>.go
git commit -m "universe: expose accessors needed by pkg/admin LocalClusterView"
```

- [ ] **Step 6: Run the test**

Run: `go test ./pkg/admin/ -run LocalClusterView -v`
Expected: PASS (both subtests).

- [ ] **Step 7: Commit**

```bash
git add pkg/admin/view_local.go pkg/admin/view_local_test.go
git commit -m "admin: implement LocalClusterView reading from *universe.Process"
```

---

### Task 4: Implement `TopicBus` and `Subscriber`

**Files:**
- Create: `pkg/admin/topicbus.go`
- Test: `pkg/admin/topicbus_test.go`

- [ ] **Step 1: Write the test first**

Create `pkg/admin/topicbus_test.go`:

```go
package admin

import (
	"sync"
	"testing"
	"time"
)

type recordingSub struct {
	mu     sync.Mutex
	events []busEvent
}

type busEvent struct {
	topic   string
	payload any
}

func (r *recordingSub) Topics() []string                                  { return nil } // wildcard for the helper
func (r *recordingSub) Deliver(topic string, payload any, _ time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, busEvent{topic, payload})
	return true
}

func (r *recordingSub) Close() {}

func TestTopicBus_Fanout(t *testing.T) {
	t.Parallel()
	bus := NewTopicBus(0)
	defer bus.Close()

	a := &recordingSub{}
	b := &recordingSub{}
	bus.Subscribe(a, "cells", "events")
	bus.Subscribe(b, "events")

	bus.Publish("cells", "snap1")
	bus.Publish("events", "ev1")
	bus.Drain() // for tests; flushes the publisher channel

	if got := len(a.events); got != 2 {
		t.Fatalf("a got %d events, want 2", got)
	}
	if got := len(b.events); got != 1 {
		t.Fatalf("b got %d events, want 1 (only events topic)", got)
	}
}

func TestTopicBus_SlowSubscriberDropped(t *testing.T) {
	t.Parallel()
	bus := NewTopicBus(2) // tiny per-subscriber buffer
	defer bus.Close()

	slow := &recordingSub{}
	bus.Subscribe(slow, "cells")

	for i := 0; i < 100; i++ {
		bus.Publish("cells", i)
	}
	// We don't Drain — this test is about the bus dropping when a subscriber
	// can't keep up. Wait briefly, then assert events <= 2 (the buffer).
	time.Sleep(20 * time.Millisecond)
	slow.mu.Lock()
	got := len(slow.events)
	slow.mu.Unlock()
	if got > 4 {
		t.Fatalf("slow subscriber got %d events, want <=4 (bounded buffer + transient)", got)
	}
}

func TestTopicBus_Unsubscribe(t *testing.T) {
	t.Parallel()
	bus := NewTopicBus(0)
	defer bus.Close()

	s := &recordingSub{}
	bus.Subscribe(s, "cells")
	bus.Publish("cells", "first")
	bus.Drain()
	bus.Unsubscribe(s)
	bus.Publish("cells", "second")
	bus.Drain()

	if len(s.events) != 1 {
		t.Fatalf("expected 1 event after unsubscribe, got %d", len(s.events))
	}
}
```

- [ ] **Step 2: Run the tests (expect fail)**

Run: `go test ./pkg/admin/ -run TopicBus -v`
Expected: build error mentioning `undefined: NewTopicBus`.

- [ ] **Step 3: Implement**

Create `pkg/admin/topicbus.go`:

```go
package admin

import (
	"sync"
	"time"
)

// Subscriber is the contract for any consumer of TopicBus events. Implemented
// by SSE writers (one per HTTP client) and by future remote-stream relays.
type Subscriber interface {
	// Topics returns the set this subscriber receives. Empty slice = wildcard.
	Topics() []string
	// Deliver is called once per matching publish. It must return without
	// blocking; the bus uses bounded per-subscriber channels and drops events
	// (returning false from this method) if the consumer is too slow.
	Deliver(topic string, payload any, ts time.Time) bool
	// Close is called when the subscriber is unregistered.
	Close()
}

// TopicBus is a transport-neutral pub/sub. Publishers call Publish; subscribers
// register via Subscribe. v1 is in-process; v2's remote-admin role re-publishes
// from a MeshControl stream into a process-local bus instance.
type TopicBus struct {
	mu          sync.RWMutex
	subscribers map[Subscriber]*subState
	bufSize     int
	closed      bool
}

type subState struct {
	topics map[string]struct{} // empty == wildcard
	queue  chan busMessage
	done   chan struct{}
}

type busMessage struct {
	topic   string
	payload any
	ts      time.Time
}

// NewTopicBus constructs a bus with the given per-subscriber buffer size
// (default 256 if <=0).
func NewTopicBus(bufSize int) *TopicBus {
	if bufSize <= 0 {
		bufSize = 256
	}
	return &TopicBus{
		subscribers: make(map[Subscriber]*subState),
		bufSize:     bufSize,
	}
}

func (b *TopicBus) Subscribe(s Subscriber, topics ...string) {
	st := &subState{
		topics: make(map[string]struct{}, len(topics)),
		queue:  make(chan busMessage, b.bufSize),
		done:   make(chan struct{}),
	}
	for _, t := range topics {
		st.topics[t] = struct{}{}
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		s.Close()
		return
	}
	b.subscribers[s] = st
	b.mu.Unlock()

	go b.dispatcher(s, st)
}

func (b *TopicBus) Unsubscribe(s Subscriber) {
	b.mu.Lock()
	st, ok := b.subscribers[s]
	if ok {
		delete(b.subscribers, s)
	}
	b.mu.Unlock()
	if ok {
		close(st.done)
	}
}

func (b *TopicBus) Publish(topic string, payload any) {
	msg := busMessage{topic: topic, payload: payload, ts: time.Now()}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return
	}
	for _, st := range b.subscribers {
		if len(st.topics) > 0 {
			if _, ok := st.topics[topic]; !ok {
				continue
			}
		}
		select {
		case st.queue <- msg:
		default:
			// drop on slow subscriber; surfaces in metrics later
		}
	}
}

// Drain is a test helper that waits for every currently-queued message to
// be delivered. Production code never calls this.
func (b *TopicBus) Drain() {
	b.mu.RLock()
	subs := make([]*subState, 0, len(b.subscribers))
	for _, st := range b.subscribers {
		subs = append(subs, st)
	}
	b.mu.RUnlock()
	for _, st := range subs {
		// busy-wait until the queue is empty
		for {
			if len(st.queue) == 0 {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func (b *TopicBus) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	subs := make(map[Subscriber]*subState, len(b.subscribers))
	for s, st := range b.subscribers {
		subs[s] = st
	}
	b.subscribers = nil
	b.mu.Unlock()

	for s, st := range subs {
		close(st.done)
		s.Close()
	}
}

func (b *TopicBus) dispatcher(s Subscriber, st *subState) {
	for {
		select {
		case <-st.done:
			s.Close()
			return
		case msg := <-st.queue:
			if !s.Deliver(msg.topic, msg.payload, msg.ts) {
				b.Unsubscribe(s)
				return
			}
		}
	}
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./pkg/admin/ -run TopicBus -v`
Expected: PASS (3 subtests).

- [ ] **Step 5: Commit**

```bash
git add pkg/admin/topicbus.go pkg/admin/topicbus_test.go
git commit -m "admin: TopicBus + Subscriber with bounded per-sub queues"
```

---

### Task 5: SSE writer (`Subscriber` impl)

**Files:**
- Create: `pkg/admin/sse.go`
- Test: `pkg/admin/sse_test.go`

- [ ] **Step 1: Write the test**

Create `pkg/admin/sse_test.go`:

```go
package admin

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSSEWriter_DeliversEvents(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	w := newSSEWriter(rec, ctx, []string{"cells", "events"})

	if !w.Deliver("cells", map[string]int{"x": 1}, time.Now()) {
		t.Fatalf("Deliver returned false")
	}
	if !w.Deliver("events", "ping", time.Now()) {
		t.Fatalf("Deliver returned false")
	}
	cancel()
	w.Close()

	body := rec.Body.String()
	if !strings.Contains(body, "event: cells") {
		t.Fatalf("missing cells event:\n%s", body)
	}
	if !strings.Contains(body, "event: events") {
		t.Fatalf("missing events event:\n%s", body)
	}
	if !strings.Contains(body, `"x":1`) {
		t.Fatalf("missing payload:\n%s", body)
	}
}

func TestSSEWriter_FilterByTopic(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	w := newSSEWriter(rec, context.Background(), []string{"cells"})
	w.Deliver("cells", "ok", time.Now())
	w.Deliver("hosts", "should-not-appear", time.Now())
	w.Close()
	if strings.Contains(rec.Body.String(), "should-not-appear") {
		t.Fatalf("topic filter failed:\n%s", rec.Body.String())
	}
}
```

- [ ] **Step 2: Run the test (expect fail)**

Run: `go test ./pkg/admin/ -run SSE -v`
Expected: build error `undefined: newSSEWriter`.

- [ ] **Step 3: Implement**

Create `pkg/admin/sse.go`:

```go
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// sseWriter is a Subscriber that fans events out to an HTTP client over
// the Server-Sent Events protocol. One sseWriter per dashboard tab.
type sseWriter struct {
	w        http.ResponseWriter
	flusher  http.Flusher
	ctx      context.Context
	topics   []string
	topicSet map[string]struct{}
	mu       sync.Mutex
	closed   bool
}

// newSSEWriter constructs a writer locked to the given topic filter. ctx is
// the request context — when it ends, Deliver short-circuits and the bus
// unsubscribes via the false return.
func newSSEWriter(w http.ResponseWriter, ctx context.Context, topics []string) *sseWriter {
	set := make(map[string]struct{}, len(topics))
	for _, t := range topics {
		set[t] = struct{}{}
	}
	flusher, _ := w.(http.Flusher)
	return &sseWriter{
		w:        w,
		flusher:  flusher,
		ctx:      ctx,
		topics:   append([]string(nil), topics...),
		topicSet: set,
	}
}

// writeHeaders emits the SSE response headers. Call before delivering events.
func (s *sseWriter) writeHeaders() {
	h := s.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // disable proxy buffering
	s.w.WriteHeader(http.StatusOK)
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

func (s *sseWriter) Topics() []string { return s.topics }

func (s *sseWriter) Deliver(topic string, payload any, ts time.Time) bool {
	if s.ctx.Err() != nil {
		return false
	}
	if len(s.topicSet) > 0 {
		if _, ok := s.topicSet[topic]; !ok {
			return true // not for us; keep subscription
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return true // skip malformed payload but stay subscribed
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", topic, body); err != nil {
		s.closed = true
		return false
	}
	if s.flusher != nil {
		s.flusher.Flush()
	}
	return true
}

func (s *sseWriter) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
}
```

- [ ] **Step 4: Run the test**

Run: `go test ./pkg/admin/ -run SSE -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/admin/sse.go pkg/admin/sse_test.go
git commit -m "admin: SSE writer Subscriber implementation"
```

---

### Task 6: Live publishers (metrics, commit log, presence)

**Files:**
- Create: `pkg/admin/publishers.go`

- [ ] **Step 1: Implement**

Create `pkg/admin/publishers.go`:

```go
package admin

import (
	"context"
	"time"

	"github.com/zenion/mmoserver/pkg/universe"
)

// startPublishers spawns goroutines that fan engine state changes onto the
// TopicBus. They run for the lifetime of the admin Server.
//
// Topic cadences (matches spec §6.2):
//   cells   — 4 Hz batched snapshot
//   hosts   — 1 Hz batched snapshot
//   topology — on commit (delegated through commitPublisher)
//   events  — on every CommitLog append (delegated through commitPublisher)
//   alerts  — on invariant violation (delegated through commitPublisher)
func startPublishers(ctx context.Context, p *universe.Process, view ClusterView, bus *TopicBus) {
	go cellsPublisher(ctx, view, bus)
	go hostsPublisher(ctx, view, bus)
	go commitPublisher(ctx, p, view, bus)
}

func cellsPublisher(ctx context.Context, view ClusterView, bus *TopicBus) {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			bus.Publish("cells", view.Cells())
		}
	}
}

func hostsPublisher(ctx context.Context, view ClusterView, bus *TopicBus) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			bus.Publish("hosts", view.Hosts())
		}
	}
}

// commitPublisher subscribes to the universe.CommitLog feed and republishes
// to events/topology/alerts according to the event kind.
func commitPublisher(ctx context.Context, p *universe.Process, view ClusterView, bus *TopicBus) {
	cl := p.CommitLog()
	if cl == nil {
		return
	}
	feed := cl.Subscribe() // returns <-chan universe.CommitEvent
	defer cl.Unsubscribe(feed)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-feed:
			if !ok {
				return
			}
			payload := CommitEvent{
				CommitID:   ev.CommitID,
				Scenario:   ev.Scenario,
				Step:       ev.Step,
				StepIndex:  ev.StepIndex,
				Success:    ev.Success,
				DurationMs: ev.DurationMs,
				Affected:   append([]string(nil), ev.Affected...),
				HostIDs:    append([]string(nil), ev.HostIDs...),
				Error:      ev.Error,
				Timestamp:  ev.Timestamp,
			}
			bus.Publish("events", payload)
			if isTopologyEvent(ev) {
				bus.Publish("topology", payload)
			}
			if isInvariantViolation(ev) {
				bus.Publish("alerts", payload)
			}
		}
	}
}

func isTopologyEvent(ev universe.CommitEvent) bool {
	switch ev.Scenario {
	case "Split", "Merge", "Migrate":
		return ev.Step == "topology-commit" || ev.Step == "release-donors"
	}
	return false
}

func isInvariantViolation(ev universe.CommitEvent) bool {
	return ev.Step == "invariant-violation"
}
```

- [ ] **Step 2: Add `CommitLog.Subscribe`/`Unsubscribe`**

If they don't already exist on `*universe.CommitLog`, add them in `pkg/universe/commit_log.go` — a typical fanout pattern with a `[]chan CommitEvent` slice + RWMutex. Pattern matches existing `Recent` accessor side. Commit separately:

```bash
git add pkg/universe/commit_log.go
git commit -m "universe: CommitLog.Subscribe fanout for live admin streaming"
```

- [ ] **Step 3: Verify compile**

Run: `go vet ./pkg/admin/...`
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add pkg/admin/publishers.go
git commit -m "admin: live publishers (cells, hosts, events, topology, alerts)"
```

---

### Task 7: `PanelDef` + `PanelRegistry`

**Files:**
- Create: `pkg/admin/panel.go`
- Test: `pkg/admin/panel_test.go`

- [ ] **Step 1: Write the test**

Create `pkg/admin/panel_test.go`:

```go
package admin

import (
	"reflect"
	"testing"
)

func TestPanelRegistry_RegisterAndList(t *testing.T) {
	t.Parallel()
	r := NewPanelRegistry()
	r.Register(PanelDef{ID: "cluster", Label: "Cluster", Group: "Cluster"})
	r.Register(PanelDef{ID: "marketplace", Label: "Marketplace", Group: "Game"})

	got := r.List()
	if len(got) != 2 {
		t.Fatalf("got %d panels, want 2", len(got))
	}
	if got[0].ID != "cluster" {
		t.Fatalf("expected stable order — first should be 'cluster', got %q", got[0].ID)
	}
}

func TestPanelRegistry_DuplicateRejected(t *testing.T) {
	t.Parallel()
	r := NewPanelRegistry()
	if err := r.Register(PanelDef{ID: "x", Label: "X"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(PanelDef{ID: "x", Label: "X2"}); err == nil {
		t.Fatalf("expected duplicate-ID error")
	}
}

func TestPanelDef_Roundtrip(t *testing.T) {
	t.Parallel()
	def := PanelDef{
		ID:           "cluster",
		Label:        "Cluster",
		Icon:         "globe",
		Group:        "Cluster",
		Topics:       []string{"cells", "topology"},
		Commands:     []string{"cell.split", "cell.merge"},
		InitialFetch: "/admin/api/cluster",
	}
	if !reflect.DeepEqual(def.Topics, []string{"cells", "topology"}) {
		t.Fatalf("Topics not preserved")
	}
}
```

- [ ] **Step 2: Run the test (expect fail)**

Run: `go test ./pkg/admin/ -run Panel -v`
Expected: build error `undefined: NewPanelRegistry`.

- [ ] **Step 3: Implement**

Create `pkg/admin/panel.go`:

```go
package admin

import (
	"fmt"
	"sort"
	"sync"
)

// PanelDef declares a dashboard panel. The SPA fetches the registered set
// at boot and renders any panel reflectively from this metadata. Games add
// custom panels via mmokit.RegisterAdminPanel; pkg/admin registers builtins.
type PanelDef struct {
	ID            string   // unique key; stable across releases
	Label         string   // sidebar label
	Icon          string   // lucide icon name
	Group         string   // sidebar group, e.g. "Cluster", "Diagnose", "Game"
	Topics        []string // SSE topic names this panel subscribes to
	Commands      []string // cmdsys verbs this panel exposes as toolbar buttons
	InitialFetch  string   // optional one-shot URL fetched at mount
	Component     string   // optional named Svelte component override (v2)
	Visualization string   // optional "table" (default) | "chart"
}

type PanelRegistry struct {
	mu    sync.RWMutex
	defs  map[string]PanelDef
	order []string
}

func NewPanelRegistry() *PanelRegistry {
	return &PanelRegistry{defs: make(map[string]PanelDef)}
}

func (r *PanelRegistry) Register(def PanelDef) error {
	if def.ID == "" {
		return fmt.Errorf("admin: PanelDef.ID is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.defs[def.ID]; ok {
		return fmt.Errorf("admin: panel %q already registered", def.ID)
	}
	r.defs[def.ID] = def
	r.order = append(r.order, def.ID)
	return nil
}

func (r *PanelRegistry) List() []PanelDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]PanelDef, 0, len(r.order))
	ids := append([]string(nil), r.order...)
	sort.Strings(ids)
	for _, id := range ids {
		out = append(out, r.defs[id])
	}
	return out
}
```

- [ ] **Step 4: Run the test**

Run: `go test ./pkg/admin/ -run Panel -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/admin/panel.go pkg/admin/panel_test.go
git commit -m "admin: PanelDef + PanelRegistry with stable ordering"
```

---

### Task 8: SessionStore interface + in-memory impl

**Files:**
- Create: `pkg/admin/session.go`
- Create: `pkg/admin/session_memory.go`
- Test: `pkg/admin/session_test.go`

- [ ] **Step 1: Write the test**

Create `pkg/admin/session_test.go`:

```go
package admin

import (
	"testing"
	"time"
)

func TestMemorySessionStore_CreateLookup(t *testing.T) {
	t.Parallel()
	s := NewMemorySessionStore()
	rec := SessionRecord{
		Username: "josh",
		Grants:   []string{"*.*"},
		IP:       "127.0.0.1",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	token, hash, err := s.Create(rec)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || len(hash) == 0 {
		t.Fatal("empty token or hash")
	}
	got, ok := s.Lookup(hash)
	if !ok {
		t.Fatal("lookup failed")
	}
	if got.Username != "josh" {
		t.Fatalf("user mismatch: %q", got.Username)
	}
}

func TestMemorySessionStore_Expired(t *testing.T) {
	t.Parallel()
	s := NewMemorySessionStore()
	rec := SessionRecord{Username: "x", ExpiresAt: time.Now().Add(-time.Second)}
	_, hash, _ := s.Create(rec)
	if _, ok := s.Lookup(hash); ok {
		t.Fatal("expired session looked up successfully")
	}
}

func TestMemorySessionStore_DeleteExpired(t *testing.T) {
	t.Parallel()
	s := NewMemorySessionStore()
	old := SessionRecord{Username: "old", ExpiresAt: time.Now().Add(-time.Hour)}
	cur := SessionRecord{Username: "cur", ExpiresAt: time.Now().Add(time.Hour)}
	_, hOld, _ := s.Create(old)
	_, hCur, _ := s.Create(cur)
	if err := s.DeleteExpired(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Lookup(hOld); ok {
		t.Fatal("old still present")
	}
	if _, ok := s.Lookup(hCur); !ok {
		t.Fatal("cur missing")
	}
}
```

- [ ] **Step 2: Run the test (expect fail)**

Run: `go test ./pkg/admin/ -run SessionStore -v`
Expected: build error mentioning `undefined: NewMemorySessionStore`.

- [ ] **Step 3: Implement the interface**

Create `pkg/admin/session.go`:

```go
package admin

import "time"

// SessionRecord describes a single admin session. Stored opaquely keyed by
// HashToken(token) — the raw token only ever exists in transit (cookie body).
type SessionRecord struct {
	Username   string
	Grants     []string  // cmdsys grant patterns
	IP         string
	UserAgent  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
}

// SessionStore persists admin sessions. Implementations key by HashToken(rawToken).
type SessionStore interface {
	// Create generates a fresh opaque token via auth.NewToken, stores the
	// SessionRecord under HashToken(token), and returns both the raw token
	// (for the cookie body) and the stored hash (for caller bookkeeping).
	Create(rec SessionRecord) (token string, hash []byte, err error)
	// Lookup returns the record if the hash is valid and not expired.
	Lookup(hash []byte) (SessionRecord, bool)
	// Touch updates LastSeenAt and optionally extends ExpiresAt by slidingTTL.
	Touch(hash []byte, slidingTTL time.Duration) error
	// Delete removes a single record.
	Delete(hash []byte) error
	// DeleteExpired removes all records past their ExpiresAt.
	DeleteExpired() error
}
```

- [ ] **Step 4: Implement the in-memory store**

Create `pkg/admin/session_memory.go`:

```go
package admin

import (
	"encoding/hex"
	"sync"
	"time"

	"github.com/zenion/mmoserver/pkg/services/auth"
)

// MemorySessionStore is the default SessionStore. Keyed by hex-encoded
// HashToken bytes.
type MemorySessionStore struct {
	mu      sync.RWMutex
	records map[string]SessionRecord
}

func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{records: make(map[string]SessionRecord)}
}

func (m *MemorySessionStore) Create(rec SessionRecord) (string, []byte, error) {
	token, hash, err := auth.NewToken()
	if err != nil {
		return "", nil, err
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now()
	}
	if rec.LastSeenAt.IsZero() {
		rec.LastSeenAt = rec.CreatedAt
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[hex.EncodeToString(hash)] = rec
	return token, hash, nil
}

func (m *MemorySessionStore) Lookup(hash []byte) (SessionRecord, bool) {
	m.mu.RLock()
	rec, ok := m.records[hex.EncodeToString(hash)]
	m.mu.RUnlock()
	if !ok {
		return SessionRecord{}, false
	}
	if time.Now().After(rec.ExpiresAt) {
		return SessionRecord{}, false
	}
	return rec, true
}

func (m *MemorySessionStore) Touch(hash []byte, slidingTTL time.Duration) error {
	key := hex.EncodeToString(hash)
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.records[key]
	if !ok {
		return nil
	}
	rec.LastSeenAt = time.Now()
	if slidingTTL > 0 {
		newExp := time.Now().Add(slidingTTL)
		if newExp.After(rec.ExpiresAt) {
			rec.ExpiresAt = newExp
		}
	}
	m.records[key] = rec
	return nil
}

func (m *MemorySessionStore) Delete(hash []byte) error {
	m.mu.Lock()
	delete(m.records, hex.EncodeToString(hash))
	m.mu.Unlock()
	return nil
}

func (m *MemorySessionStore) DeleteExpired() error {
	now := time.Now()
	m.mu.Lock()
	for k, rec := range m.records {
		if now.After(rec.ExpiresAt) {
			delete(m.records, k)
		}
	}
	m.mu.Unlock()
	return nil
}
```

- [ ] **Step 5: Run the test**

Run: `go test ./pkg/admin/ -run SessionStore -v`
Expected: PASS (3 subtests).

- [ ] **Step 6: Commit**

```bash
git add pkg/admin/session.go pkg/admin/session_memory.go pkg/admin/session_test.go
git commit -m "admin: SessionStore interface + MemorySessionStore"
```

---

### Task 9: Cookie helpers + auth middleware

**Files:**
- Create: `pkg/admin/cookie.go`
- Create: `pkg/admin/middleware.go`

- [ ] **Step 1: Implement cookie helpers (mirror `pkg/services/auth/cookie.go`)**

Create `pkg/admin/cookie.go`:

```go
package admin

import (
	"net/http"
	"strings"
	"time"
)

// cookieName is the admin session cookie. Distinct from the game-auth cookie
// so the two domains don't collide on shared hosts.
const cookieName = "admin_session"

// CookieOpts controls cookie attributes for both writes and reads.
type CookieOpts struct {
	Domain   string // empty = host-only
	Path     string // default "/admin"
	Secure   bool   // forced true unless dev-loopback
	SameSite http.SameSite
}

func defaultCookieOpts() CookieOpts {
	return CookieOpts{
		Path:     "/admin",
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
}

// devLoopbackRelaxed returns o with Secure=false when bind is loopback.
func devLoopbackRelaxed(o CookieOpts, bind string) CookieOpts {
	if isLoopbackBind(bind) {
		o.Secure = false
	}
	return o
}

func isLoopbackBind(bind string) bool {
	host := bind
	if i := strings.LastIndex(bind, ":"); i >= 0 {
		host = bind[:i]
	}
	host = strings.Trim(host, "[]")
	return host == "" || host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func setSessionCookie(w http.ResponseWriter, opts CookieOpts, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Domain:   opts.Domain,
		Path:     opts.Path,
		Expires:  time.Now().Add(ttl),
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   opts.Secure,
		SameSite: opts.SameSite,
	})
}

func clearSessionCookie(w http.ResponseWriter, opts CookieOpts) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Domain:   opts.Domain,
		Path:     opts.Path,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   opts.Secure,
		SameSite: opts.SameSite,
	})
}

func readSessionToken(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	return c.Value
}
```

- [ ] **Step 2: Implement the auth middleware**

Create `pkg/admin/middleware.go`:

```go
package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/services/auth"
)

type ctxKey int

const ctxKeyCaller ctxKey = 1

// requireSession is the middleware applied to every /admin/api/* route except
// /admin/api/auth/login. It looks up the session, builds a cmdsys.Caller from
// the operator's grants, and stores it on the request context.
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := readSessionToken(r)
		if token == "" {
			writeJSONError(w, http.StatusUnauthorized, "missing session")
			return
		}
		hash := auth.HashToken(token)
		rec, ok := s.sessions.Lookup(hash)
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, "invalid or expired session")
			return
		}
		// Sliding TTL bump (rate-limited to once per minute per session).
		if time.Since(rec.LastSeenAt) > time.Minute {
			_ = s.sessions.Touch(hash, s.cfg.SessionTTL)
		}
		caller := cmdsys.Caller{
			ID:     rec.Username,
			Source: cmdsys.SourceAdminHTTP,
			Grants: parseGrants(rec.Grants),
		}
		ctx := context.WithValue(r.Context(), ctxKeyCaller, caller)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func parseGrants(strs []string) []cmdsys.Grant {
	out := make([]cmdsys.Grant, 0, len(strs))
	for _, s := range strs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		allow := true
		if strings.HasPrefix(s, "!") {
			allow = false
			s = s[1:]
		}
		out = append(out, cmdsys.Grant{Pattern: s, Allow: allow})
	}
	return out
}

func callerFrom(r *http.Request) (cmdsys.Caller, bool) {
	c, ok := r.Context().Value(ctxKeyCaller).(cmdsys.Caller)
	return c, ok
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}
```

- [ ] **Step 3: Verify compile**

Run: `go vet ./pkg/admin/...`
Expected: no errors (the `*Server` type doesn't exist yet — comment out the receiver temporarily, or add a forward declaration: `type Server struct{ sessions SessionStore; cfg Config }` in admin.go now).

If `*Server` is missing, create the placeholder in `pkg/admin/admin.go`:

```go
package admin

// Server is the admin HTTP handler set. Real construction is in NewServer.
type Server struct {
	sessions SessionStore
	cfg      Config
}

// Config is the admin server config (filled in fully in Task 21).
type Config struct {
	SessionTTL  time.Duration
	BindAddr    string
	CookieOpts  CookieOpts
}
```

(That's a stub — Task 21 fleshes it out.)

- [ ] **Step 4: Commit**

```bash
git add pkg/admin/cookie.go pkg/admin/middleware.go pkg/admin/admin.go
git commit -m "admin: cookie helpers + session-required middleware"
```

---

### Task 10: Lockout + audit log

**Files:**
- Create: `pkg/admin/lockout.go`
- Create: `pkg/admin/audit.go`
- Test: `pkg/admin/audit_test.go`

- [ ] **Step 1: Lockout — thin wrapper over `auth.IPRateLimiter`**

Create `pkg/admin/lockout.go`:

```go
package admin

import (
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/zenion/mmoserver/pkg/services/auth"
)

// Lockout protects the login endpoint from brute force. Wraps the existing
// IPRateLimiter from pkg/services/auth, parameterized for admin defaults.
type Lockout struct {
	rl *auth.IPRateLimiter
}

func NewLockout(maxAttempts int, window time.Duration) *Lockout {
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	if window <= 0 {
		window = 15 * time.Minute
	}
	return &Lockout{rl: auth.NewIPRateLimiter(auth.IPRateLimitConfig{
		MaxAttempts: maxAttempts,
		Window:      window,
	})}
}

// Check returns (allowed, retryAfter). On allowed=false, the caller must
// respond 429 with Retry-After.
func (l *Lockout) Check(r *http.Request, success bool) (bool, time.Duration) {
	addr := remoteAddr(r)
	return l.rl.CheckAndCount(addr, success)
}

func remoteAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, _ := netip.ParseAddr(host)
	return addr
}
```

- [ ] **Step 2: Audit log — bounded in-memory ring**

Create `pkg/admin/audit.go`:

```go
package admin

import (
	"sync"
	"time"
)

// AuditEntry records one admin-side action (login, logout, command invoke).
type AuditEntry struct {
	TraceID    string
	Username   string
	IP         string
	Verb       string
	ArgsJSON   string
	OK         bool
	Error      string
	StartedAt  time.Time
	FinishedAt time.Time
}

// AuditLog is a bounded in-memory ring. Postgres-backed log is a v2 task —
// the interface stays stable for swap-in.
type AuditLog struct {
	mu       sync.Mutex
	ring     []AuditEntry
	head     int
	cap      int
}

func NewAuditLog(capacity int) *AuditLog {
	if capacity <= 0 {
		capacity = 4096
	}
	return &AuditLog{ring: make([]AuditEntry, capacity), cap: capacity}
}

func (a *AuditLog) Append(e AuditEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ring[a.head] = e
	a.head = (a.head + 1) % a.cap
}

// Recent returns up to n most recent entries, newest first.
func (a *AuditLog) Recent(n int) []AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n <= 0 || n > a.cap {
		n = a.cap
	}
	out := make([]AuditEntry, 0, n)
	idx := a.head - 1
	if idx < 0 {
		idx = a.cap - 1
	}
	for i := 0; i < n; i++ {
		e := a.ring[idx]
		if e.StartedAt.IsZero() {
			break
		}
		out = append(out, e)
		idx--
		if idx < 0 {
			idx = a.cap - 1
		}
	}
	return out
}
```

- [ ] **Step 3: Audit test**

Create `pkg/admin/audit_test.go`:

```go
package admin

import (
	"testing"
	"time"
)

func TestAuditLog_AppendRecent(t *testing.T) {
	t.Parallel()
	a := NewAuditLog(4)
	now := time.Now()
	for i := 0; i < 6; i++ {
		a.Append(AuditEntry{Verb: "v", Username: "u", StartedAt: now.Add(time.Duration(i) * time.Millisecond)})
	}
	got := a.Recent(10)
	if len(got) != 4 {
		t.Fatalf("expected 4 (capacity), got %d", len(got))
	}
	// Newest first → StartedAt at i=5 should be index 0.
	if !got[0].StartedAt.After(got[3].StartedAt) {
		t.Fatalf("expected newest-first ordering")
	}
}
```

- [ ] **Step 4: Run + commit**

Run: `go test ./pkg/admin/ -run Audit -v`
Expected: PASS.

```bash
git add pkg/admin/lockout.go pkg/admin/audit.go pkg/admin/audit_test.go
git commit -m "admin: lockout (auth.IPRateLimiter wrapper) + bounded audit ring"
```

---

### Task 11: API: `/admin/api/auth/*`

**Files:**
- Create: `pkg/admin/api_auth.go`
- Test: `pkg/admin/api_auth_test.go`

- [ ] **Step 1: Implement**

Create `pkg/admin/api_auth.go`:

```go
package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/zenion/mmoserver/pkg/services/auth"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	User      string    `json:"user"`
	Grants    []string  `json:"grants"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeJSONError(w, http.StatusBadRequest, "username and password required")
		return
	}

	allowed, retry := s.lockout.Check(r, false /* tentative */)
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
		writeJSONError(w, http.StatusTooManyRequests, "too many attempts")
		return
	}

	op, ok := s.operators[req.Username]
	if !ok {
		_, _ = s.lockout.Check(r, false)
		s.audit.Append(AuditEntry{
			Username: req.Username, IP: remoteAddr(r).String(), Verb: "auth.login",
			OK: false, Error: "unknown user",
			StartedAt: time.Now(), FinishedAt: time.Now(),
		})
		s.log.Log("admin", "login-fail user=%s ip=%s reason=unknown-user", req.Username, remoteAddr(r))
		writeJSONError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	ok2, err := auth.VerifyPassword(req.Password, op.PasswordHash)
	if err != nil || !ok2 {
		_, _ = s.lockout.Check(r, false)
		s.audit.Append(AuditEntry{
			Username: req.Username, IP: remoteAddr(r).String(), Verb: "auth.login",
			OK: false, Error: "bad password",
			StartedAt: time.Now(), FinishedAt: time.Now(),
		})
		s.log.Log("admin", "login-fail user=%s ip=%s reason=bad-password", req.Username, remoteAddr(r))
		writeJSONError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	// Successful login.
	_, _ = s.lockout.Check(r, true)
	expiresAt := time.Now().Add(s.cfg.SessionTTL)
	rec := SessionRecord{
		Username:  req.Username,
		Grants:    op.Grants,
		IP:        remoteAddr(r).String(),
		UserAgent: r.UserAgent(),
		ExpiresAt: expiresAt,
	}
	token, _, err := s.sessions.Create(rec)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "session create failed")
		return
	}
	setSessionCookie(w, s.cfg.CookieOpts, token, s.cfg.SessionTTL)
	s.audit.Append(AuditEntry{
		Username: req.Username, IP: rec.IP, Verb: "auth.login",
		OK: true, StartedAt: time.Now(), FinishedAt: time.Now(),
	})
	s.log.Log("admin", "login user=%s ip=%s", req.Username, rec.IP)
	writeJSON(w, http.StatusOK, loginResponse{User: req.Username, Grants: op.Grants, ExpiresAt: expiresAt})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	tok := readSessionToken(r)
	if tok != "" {
		_ = s.sessions.Delete(auth.HashToken(tok))
	}
	clearSessionCookie(w, s.cfg.CookieOpts)
	caller, _ := callerFrom(r)
	s.audit.Append(AuditEntry{
		Username: caller.ID, IP: remoteAddr(r).String(), Verb: "auth.logout",
		OK: true, StartedAt: time.Now(), FinishedAt: time.Now(),
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFrom(r)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "no session")
		return
	}
	hash := auth.HashToken(readSessionToken(r))
	rec, _ := s.sessions.Lookup(hash)
	writeJSON(w, http.StatusOK, loginResponse{
		User:      caller.ID,
		Grants:    rec.Grants,
		ExpiresAt: rec.ExpiresAt,
	})
}
```

- [ ] **Step 2: Test the login flow**

Create `pkg/admin/api_auth_test.go`:

```go
package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/services/auth"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	hash, err := auth.HashPassword("p@ssw0rd!", auth.DefaultArgonParams())
	if err != nil { t.Fatal(err) }
	return &Server{
		sessions: NewMemorySessionStore(),
		audit:    NewAuditLog(256),
		lockout:  NewLockout(5, 15*time.Minute),
		operators: map[string]OperatorConfig{
			"josh": {Username: "josh", PasswordHash: hash, Grants: []string{"*.*"}},
		},
		log:       testLogger{},
		cfg: Config{
			SessionTTL: time.Hour,
			CookieOpts: defaultCookieOpts(),
		},
	}
}

type testLogger struct{}
func (testLogger) Log(string, string, ...any) {}

func TestLogin_Success(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	body, _ := json.Marshal(loginRequest{Username: "josh", Password: "p@ssw0rd!"})
	r := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handleLogin(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Set-Cookie"), "admin_session=") {
		t.Fatalf("missing session cookie: %s", w.Header().Get("Set-Cookie"))
	}
}

func TestLogin_BadPassword(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	body, _ := json.Marshal(loginRequest{Username: "josh", Password: "wrong"})
	r := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handleLogin(w, r)
	if w.Code != 401 {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}
```

The test references `OperatorConfig` and `s.log` typed as a small Logger interface — both go into the `pkg/admin/admin.go` stub created in Task 9. Update that file to add the new types and grow the Server struct:

```go
type OperatorConfig struct {
	Username     string
	PasswordHash string
	Grants       []string
}

// Logger is the minimal logger surface admin.Server needs. The real
// pkg/logger.Logger satisfies it implicitly.
type Logger interface {
	Log(category, format string, args ...any)
}

// Update Server struct:
type Server struct {
	sessions  SessionStore
	audit     *AuditLog
	lockout   *Lockout
	operators map[string]OperatorConfig
	log       Logger
	cfg       Config
}
```

- [ ] **Step 3: Run + commit**

Run: `go test ./pkg/admin/ -run Login -v`
Expected: PASS.

```bash
git add pkg/admin/api_auth.go pkg/admin/api_auth_test.go pkg/admin/admin.go
git commit -m "admin: /api/auth/{login,logout,session} handlers + lockout"
```

---

### Task 12: API: read-only handlers (`/cluster`, `/cells`, `/hosts`, `/gateways`, `/players`, `/events`, `/perf`, `/audit`, `/panels`)

**Files:**
- Create: `pkg/admin/api_read.go`
- Test: `pkg/admin/api_read_test.go`

- [ ] **Step 1: Check whether cmdsys already has a grant matcher**

```bash
grep -n 'func.*[Gg]rant\(s\)\?.*[Mm]atch\|GrantsAllow\|MatchCapability' pkg/cmdsys/*.go
```

If a matcher exists (likely in `pkg/cmdsys/grants.go` or `rbac.go`), use it directly instead of reinventing — call sites read like `cmdsys.GrantsAllow(grants, "admin.audit")`. If none exists, the implementation in Step 2 includes a small inline matcher.

- [ ] **Step 2: Implement**

Create `pkg/admin/api_read.go`:

```go
package admin

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

func (s *Server) handleCluster(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.view.Cluster())
}

func (s *Server) handleCells(w http.ResponseWriter, r *http.Request) {
	if id := strings.TrimPrefix(r.URL.Path, "/admin/api/cells/"); id != "" && id != r.URL.Path {
		c, err := s.view.Cell(id)
		if err == ErrCellNotFound {
			writeJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, c)
		return
	}
	writeJSON(w, http.StatusOK, s.view.Cells())
}

func (s *Server) handleHosts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.view.Hosts())
}

func (s *Server) handleGateways(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.view.Gateways())
}

func (s *Server) handlePlayers(w http.ResponseWriter, r *http.Request) {
	if name := strings.TrimPrefix(r.URL.Path, "/admin/api/players/"); name != "" && name != r.URL.Path {
		p, err := s.view.Player(name)
		if err == ErrPlayerNotFound {
			writeJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, p)
		return
	}
	q := r.URL.Query()
	filter := PlayerFilter{
		Status: q.Get("status"),
		Search: q.Get("search"),
		Limit:  atoiDefault(q.Get("limit"), 100),
		Offset: atoiDefault(q.Get("offset"), 0),
	}
	writeJSON(w, http.StatusOK, s.view.Players(filter))
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cq := CommitQuery{
		N:      atoiDefault(q.Get("n"), 200),
		Cell:   q.Get("cell"),
		Commit: q.Get("commit"),
	}
	if since := q.Get("since"); since != "" {
		if d, err := time.ParseDuration(since); err == nil {
			cq.Since = d
		}
	}
	writeJSON(w, http.StatusOK, s.view.CommitLog(cq))
}

func (s *Server) handlePerf(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/admin/api/perf/")
	if id == "" || id == r.URL.Path {
		writeJSONError(w, http.StatusBadRequest, "cellID required")
		return
	}
	ps, err := s.view.Perf(id)
	if err == ErrCellNotFound {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ps)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	caller, _ := callerFrom(r)
	if !grantsAllow(caller.Grants, "admin.audit") {
		writeJSONError(w, http.StatusForbidden, "missing admin.audit grant")
		return
	}
	q := r.URL.Query()
	n := atoiDefault(q.Get("n"), 200)
	writeJSON(w, http.StatusOK, s.audit.Recent(n))
}

func (s *Server) handlePanels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.panels.List())
}

func atoiDefault(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// grantsAllow checks whether any grant matches the capability. Explicit deny
// wins. If Step 1 found a matcher in cmdsys, replace this body with a thin
// delegate (e.g. `return cmdsys.GrantsAllow(grants, capability)`).
//
// Match rules:
//   - "*.*" matches any capability
//   - "head.*" matches any capability with the same head
//   - "*.tail" matches any capability with the same tail
//   - exact "head.tail" matches only the exact capability
func grantsAllow(grants []cmdsys.Grant, capability string) bool {
	allowed := false
	for _, g := range grants {
		if !grantMatches(g.Pattern, capability) {
			continue
		}
		if !g.Allow {
			return false
		}
		allowed = true
	}
	return allowed
}

func grantMatches(pat, capability string) bool {
	if pat == "*.*" {
		return true
	}
	if !strings.Contains(pat, ".") {
		return pat == capability
	}
	patHead, patTail, _ := strings.Cut(pat, ".")
	capHead, capTail, _ := strings.Cut(capability, ".")
	switch {
	case patHead == "*":
		return patTail == "*" || patTail == capTail
	case patTail == "*":
		return patHead == capHead
	}
	return pat == capability
}
```

Update `Server` (in `admin.go`) to add the missing fields:

```go
type Server struct {
	view      ClusterView
	sessions  SessionStore
	audit     *AuditLog
	lockout   *Lockout
	operators map[string]OperatorConfig
	panels    *PanelRegistry
	log       Logger
	cfg       Config
	bus       *TopicBus
}
```

- [ ] **Step 3: Lightweight integration test**

Create `pkg/admin/api_read_test.go`:

```go
package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

type fakeView struct{}

func (fakeView) Cluster() ClusterInfo                        { return ClusterInfo{HostCount: 1, CellCount: 4} }
func (fakeView) Cells() []CellInfo                           { return []CellInfo{{ID: "0_0"}} }
func (fakeView) Cell(id string) (CellInfo, error)            {
	if id == "0_0" { return CellInfo{ID: "0_0"}, nil }
	return CellInfo{}, ErrCellNotFound
}
func (fakeView) Hosts() []HostInfo                           { return []HostInfo{{ID: "host-a"}} }
func (fakeView) Gateways() []GatewayInfo                     { return nil }
func (fakeView) Players(PlayerFilter) []PlayerInfo           { return []PlayerInfo{{Username: "josh"}} }
func (fakeView) Player(string) (PlayerInfo, error)           { return PlayerInfo{Username: "josh"}, nil }
func (fakeView) CommitLog(CommitQuery) []CommitEvent         { return nil }
func (fakeView) Perf(string) (PerfSnapshot, error)           { return PerfSnapshot{}, nil }

func TestHandleCells_List(t *testing.T) {
	t.Parallel()
	s := &Server{view: fakeView{}, panels: NewPanelRegistry()}
	r := httptest.NewRequest(http.MethodGet, "/admin/api/cells", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxKeyCaller, cmdsys.Caller{ID: "josh"}))
	w := httptest.NewRecorder()
	s.handleCells(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestHandleCells_Detail_NotFound(t *testing.T) {
	t.Parallel()
	s := &Server{view: fakeView{}}
	r := httptest.NewRequest(http.MethodGet, "/admin/api/cells/missing", nil)
	w := httptest.NewRecorder()
	s.handleCells(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}
```

- [ ] **Step 4: Run + commit**

Run: `go test ./pkg/admin/ -run HandleCells -v`
Expected: PASS.

```bash
git add pkg/admin/api_read.go pkg/admin/api_read_test.go pkg/admin/admin.go
git commit -m "admin: read-only API handlers (cluster, cells, hosts, gateways, players, events, perf, audit, panels)"
```

---

### Task 13: API: `/admin/api/commands/*` (list / describe / invoke)

**Files:**
- Create: `pkg/admin/api_commands.go`
- Test: `pkg/admin/api_commands_test.go`

- [ ] **Step 1: Implement**

Create `pkg/admin/api_commands.go`:

```go
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

// handleCommandsList — GET /admin/api/commands
func (s *Server) handleCommandsList(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		Verb        string `json:"verb"`
		Capability  string `json:"capability"`
		Description string `json:"description"`
		Route       string `json:"route"`
		Hidden      bool   `json:"hidden,omitempty"`
		Aliases     []string `json:"aliases,omitempty"`
	}
	cmds := s.registry.List()
	out := make([]entry, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, entry{
			Verb: c.Verb, Capability: string(c.Capability),
			Description: c.Description, Route: c.Route.String(),
			Hidden: c.Hidden, Aliases: c.Aliases,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCommandDescribe — GET /admin/api/commands/<verb>
func (s *Server) handleCommandDescribe(w http.ResponseWriter, r *http.Request) {
	verb := strings.TrimPrefix(r.URL.Path, "/admin/api/commands/")
	if verb == "" {
		writeJSONError(w, http.StatusBadRequest, "verb required")
		return
	}
	c, ok := s.registry.Lookup(verb)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "unknown verb")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"verb":         c.Verb,
		"capability":   c.Capability,
		"description":  c.Description,
		"route":        c.Route.String(),
		"argsSchema":   cmdsys.SchemaOfArgs(c),
		"resultSchema": cmdsys.SchemaOfResult(c),
		"usage":        c.Usage,
		"examples":     c.Examples,
	})
}

// handleCommandInvoke — POST /admin/api/commands/<verb>
func (s *Server) handleCommandInvoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	verb := strings.TrimPrefix(r.URL.Path, "/admin/api/commands/")
	if verb == "" {
		writeJSONError(w, http.StatusBadRequest, "verb required")
		return
	}
	cmd, ok := s.registry.Lookup(verb)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "unknown verb")
		return
	}

	// Build a fresh, zero-valued args struct of the registered type.
	args, err := cmdsys.NewArgs(cmd)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "args schema unavailable")
		return
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(args); err != nil && !errors.Is(err, json.ErrEOF) {
		writeJSONError(w, http.StatusBadRequest, "invalid args: "+err.Error())
		return
	}

	caller, _ := callerFrom(r)
	startedAt := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := s.dispatcher.Invoke(ctx, caller, verb, args)
	finishedAt := time.Now()

	entry := AuditEntry{
		TraceID:    res.TraceID,
		Username:   caller.ID,
		IP:         remoteAddr(r).String(),
		Verb:       verb,
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
	}
	argsJSON, _ := json.Marshal(args)
	entry.ArgsJSON = string(argsJSON)

	switch {
	case errors.Is(err, cmdsys.ErrRBACDenied):
		entry.OK = false; entry.Error = err.Error()
		s.audit.Append(entry)
		s.log.Log("admin", "cmd verb=%s user=%s ok=false err=%s", verb, caller.ID, err)
		writeJSONError(w, http.StatusForbidden, err.Error())
		return
	case errors.Is(err, cmdsys.ErrUnknownVerb):
		entry.OK = false; entry.Error = err.Error()
		s.audit.Append(entry)
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	case err != nil:
		entry.OK = false; entry.Error = err.Error()
		s.audit.Append(entry)
		s.log.Log("admin", "cmd verb=%s user=%s ok=false dur=%s err=%s", verb, caller.ID, finishedAt.Sub(startedAt), err)
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Per-target results: 1 target → unwrap; many → return slice.
	type invokeResp struct {
		OK      bool             `json:"ok"`
		Result  any              `json:"result,omitempty"`
		Targets []cmdsys.TargetResult `json:"targets,omitempty"`
		TraceID string           `json:"traceId"`
	}
	resp := invokeResp{OK: true, TraceID: res.TraceID}
	if len(res.PerTarget) == 1 {
		t := res.PerTarget[0]
		if !t.OK {
			entry.OK = false; entry.Error = t.Error
			s.audit.Append(entry)
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": t.Error, "traceId": res.TraceID})
			return
		}
		resp.Result = t.Result
	} else {
		resp.Targets = res.PerTarget
	}
	entry.OK = true
	s.audit.Append(entry)
	s.log.Log("admin", "cmd verb=%s user=%s ok=true dur=%s", verb, caller.ID, finishedAt.Sub(startedAt))
	writeJSON(w, http.StatusOK, resp)
}
```

- [ ] **Step 2: Add cmdsys helpers if missing**

`cmdsys.NewArgs(cmd)` and `cmdsys.SchemaOfArgs/SchemaOfResult` — check whether they exist:

```bash
grep -n 'func NewArgs\|func SchemaOfArgs\|func SchemaOfResult' pkg/cmdsys/*.go
```

If `NewArgs` is missing, add it to `pkg/cmdsys/coerce.go`:

```go
import "reflect"
func NewArgs(c Command) (any, error) {
	if c.Args == nil { return nil, nil }
	return reflect.New(reflect.TypeOf(c.Args)).Interface(), nil
}
```

Commit separately if `pkg/cmdsys/` was modified.

- [ ] **Step 3: Test invoke against a fake dispatcher**

Create `pkg/admin/api_commands_test.go`:

```go
package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

func TestHandleCommandInvoke_Success(t *testing.T) {
	t.Parallel()
	reg := cmdsys.NewRegistry()
	type echoArgs struct { Msg string `json:"msg"` }
	type echoResult struct { Echo string `json:"echo"` }
	if err := reg.Register(cmdsys.Command{
		Verb: "test.echo", Capability: "test.echo",
		Args: echoArgs{}, Result: echoResult{}, Route: cmdsys.RouteLocal,
		Handler: func(_ context.Context, _ *cmdsys.Env, args any) (any, error) {
			a := args.(*echoArgs)
			return echoResult{Echo: a.Msg}, nil
		},
	}); err != nil { t.Fatal(err) }
	disp := cmdsys.NewDispatcher(cmdsys.DispatcherConfig{Registry: reg})

	s := &Server{
		registry:   reg,
		dispatcher: disp,
		audit:      NewAuditLog(8),
		log:        testLogger{},
	}

	body, _ := json.Marshal(map[string]string{"msg": "hi"})
	r := httptest.NewRequest(http.MethodPost, "/admin/api/commands/test.echo", bytes.NewReader(body))
	r = r.WithContext(context.WithValue(r.Context(), ctxKeyCaller, cmdsys.Caller{
		ID: "josh", Source: cmdsys.SourceAdminHTTP,
		Grants: []cmdsys.Grant{{Pattern: "*.*", Allow: true}},
	}))
	w := httptest.NewRecorder()
	s.handleCommandInvoke(w, r)

	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		OK     bool   `json:"ok"`
		Result struct{ Echo string `json:"echo"` } `json:"result"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil { t.Fatal(err) }
	if resp.Result.Echo != "hi" {
		t.Fatalf("echo=%q", resp.Result.Echo)
	}
}
```

Add `registry`, `dispatcher` fields to `Server`:

```go
registry   *cmdsys.Registry
dispatcher *cmdsys.Dispatcher
```

- [ ] **Step 4: Run + commit**

Run: `go test ./pkg/admin/ -run CommandInvoke -v`
Expected: PASS.

```bash
git add pkg/admin/api_commands.go pkg/admin/api_commands_test.go pkg/admin/admin.go pkg/cmdsys/coerce.go
git commit -m "admin: /api/commands/{list,describe,invoke} with audit + RBAC mapping"
```

---

### Task 14: API: `/admin/api/stream`

**Files:**
- Create: `pkg/admin/api_stream.go`

- [ ] **Step 1: Implement**

Create `pkg/admin/api_stream.go`:

```go
package admin

import (
	"net/http"
	"strings"
)

// handleStream — GET /admin/api/stream?topics=cells,hosts,events
//
// Single multiplexed SSE connection. The query string scopes which topics
// arrive; client demultiplexes by `event:` line.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	if _, ok := w.(http.Flusher); !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	rawTopics := r.URL.Query().Get("topics")
	if rawTopics == "" {
		writeJSONError(w, http.StatusBadRequest, "topics= query param required")
		return
	}
	topics := splitNonEmpty(rawTopics, ",")

	writer := newSSEWriter(w, r.Context(), topics)
	writer.writeHeaders()
	s.bus.Subscribe(writer, topics...)
	defer s.bus.Unsubscribe(writer)

	// Block until the client disconnects.
	<-r.Context().Done()
}

func splitNonEmpty(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
```

- [ ] **Step 2: Verify compile**

Run: `go vet ./pkg/admin/...`
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add pkg/admin/api_stream.go
git commit -m "admin: /api/stream multiplexed SSE handler"
```

---

### Task 15: `Server` struct + `Mount` + `Config`

**Files:**
- Modify: `pkg/admin/admin.go`

- [ ] **Step 1: Replace the stub `admin.go` with the full Server**

Rewrite `pkg/admin/admin.go`:

```go
package admin

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/universe"
)

// Logger is the minimal logger surface admin.Server needs. *logger.Logger
// satisfies it implicitly.
type Logger interface {
	Log(category, format string, args ...any)
}

// Config is the construction-time bundle for NewServer.
type Config struct {
	BindAddr     string        // for cookie Secure-flag relaxing on loopback
	SessionTTL   time.Duration // default 8h
	CookieOpts   CookieOpts    // default: Path=/admin, Secure, SameSite=Strict
	LockoutMax   int           // default 5
	LockoutWin   time.Duration // default 15m
	AuditCap     int           // default 4096

	Operators []OperatorConfig
}

type OperatorConfig struct {
	Username     string
	PasswordHash string
	Grants       []string
}

// Server is the admin HTTP handler set. Construct with NewServer; mount onto
// any net/http mux with Mount.
type Server struct {
	view       ClusterView
	registry   *cmdsys.Registry
	dispatcher *cmdsys.Dispatcher
	sessions   SessionStore
	audit      *AuditLog
	lockout    *Lockout
	operators  map[string]OperatorConfig
	panels     *PanelRegistry
	bus        *TopicBus
	log        Logger

	cfg Config

	cancel context.CancelFunc
}

// ServerOpts is the runtime-injected dependency bundle. Built by the universe
// layer in bootstrap.go.
type ServerOpts struct {
	View         ClusterView
	Registry     *cmdsys.Registry
	Dispatcher   *cmdsys.Dispatcher
	SessionStore SessionStore
	Panels       *PanelRegistry
	Logger       Logger
	Process      *universe.Process // for publishers; only the Process needs to live this long
	Config       Config
}

// NewServer wires the dependencies. Caller still owns the publishers' lifetime
// — they stop when Server.Stop is called.
func NewServer(opts ServerOpts) *Server {
	cfg := opts.Config
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = 8 * time.Hour
	}
	if cfg.AuditCap == 0 {
		cfg.AuditCap = 4096
	}
	if cfg.CookieOpts == (CookieOpts{}) {
		cfg.CookieOpts = devLoopbackRelaxed(defaultCookieOpts(), cfg.BindAddr)
	}
	ops := make(map[string]OperatorConfig, len(cfg.Operators))
	for _, o := range cfg.Operators {
		ops[strings.ToLower(o.Username)] = o
	}
	bus := NewTopicBus(0)
	s := &Server{
		view:       opts.View,
		registry:   opts.Registry,
		dispatcher: opts.Dispatcher,
		sessions:   opts.SessionStore,
		audit:      NewAuditLog(cfg.AuditCap),
		lockout:    NewLockout(cfg.LockoutMax, cfg.LockoutWin),
		operators:  ops,
		panels:     opts.Panels,
		bus:        bus,
		log:        opts.Logger,
		cfg:        cfg,
	}
	if opts.Process != nil {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		startPublishers(ctx, opts.Process, opts.View, bus)
	}
	return s
}

// Stop halts the publishers and closes the bus.
func (s *Server) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.bus.Close()
}

// Mount registers all admin routes on the given mux. Pass the AdminListen
// or HTTPPort mux; the static SPA is served from /admin/.
func (s *Server) Mount(mux *http.ServeMux) {
	// Static SPA (Task 16 puts the embed.FS here).
	mux.Handle("/admin/", http.StripPrefix("/admin", http.FileServer(http.FS(staticDist))))

	// Auth — no session required for login; lockout is the only gate.
	mux.HandleFunc("/admin/api/auth/login", s.handleLogin)
	mux.Handle("/admin/api/auth/logout", s.requireSession(http.HandlerFunc(s.handleLogout)))
	mux.Handle("/admin/api/auth/session", s.requireSession(http.HandlerFunc(s.handleSession)))

	// Read API.
	mux.Handle("/admin/api/cluster", s.requireSession(http.HandlerFunc(s.handleCluster)))
	mux.Handle("/admin/api/cells", s.requireSession(http.HandlerFunc(s.handleCells)))
	mux.Handle("/admin/api/cells/", s.requireSession(http.HandlerFunc(s.handleCells)))
	mux.Handle("/admin/api/hosts", s.requireSession(http.HandlerFunc(s.handleHosts)))
	mux.Handle("/admin/api/gateways", s.requireSession(http.HandlerFunc(s.handleGateways)))
	mux.Handle("/admin/api/players", s.requireSession(http.HandlerFunc(s.handlePlayers)))
	mux.Handle("/admin/api/players/", s.requireSession(http.HandlerFunc(s.handlePlayers)))
	mux.Handle("/admin/api/events", s.requireSession(http.HandlerFunc(s.handleEvents)))
	mux.Handle("/admin/api/perf/", s.requireSession(http.HandlerFunc(s.handlePerf)))
	mux.Handle("/admin/api/audit", s.requireSession(http.HandlerFunc(s.handleAudit)))
	mux.Handle("/admin/api/panels", s.requireSession(http.HandlerFunc(s.handlePanels)))

	// Commands.
	mux.Handle("/admin/api/commands", s.requireSession(http.HandlerFunc(s.handleCommandsList)))
	mux.Handle("/admin/api/commands/", s.requireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.handleCommandDescribe(w, r)
		case http.MethodPost:
			s.handleCommandInvoke(w, r)
		default:
			writeJSONError(w, http.StatusMethodNotAllowed, "GET or POST")
		}
	})))

	// Stream.
	mux.Handle("/admin/api/stream", s.requireSession(http.HandlerFunc(s.handleStream)))
}
```

- [ ] **Step 2: Verify compile**

Run: `go vet ./pkg/admin/...`
Expected: probably one error about `staticDist` missing — that's Task 16. Stub it for now:

```go
// in admin.go, top-level:
var staticDist fs.FS = embedFallbackFS{}
type embedFallbackFS struct{}
func (embedFallbackFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }
```

(Replace with the real `embed.FS` in Task 16.)

- [ ] **Step 3: Commit**

```bash
git add pkg/admin/admin.go
git commit -m "admin: Server / NewServer / Mount with all routes wired"
```

---

### Task 16: Static SPA embed placeholder

**Files:**
- Create: `pkg/admin/static/dist.go`
- Create: `pkg/admin/static/dist/index.html`
- Create: `pkg/admin/static/dist/.gitkeep`
- Modify: `pkg/admin/admin.go` (replace `embedFallbackFS` stub)

- [ ] **Step 1: Create the embed package**

Create `pkg/admin/static/dist.go`:

```go
// Package static embeds the built admin SPA. The actual JS/CSS bundles are
// produced by `bun run build` in web-admin/ and copied here by `just admin-build`.
// v1 ships with a placeholder index.html so the backend can boot before the
// frontend lands.
package static

import "embed"

//go:embed dist
var FS embed.FS
```

Create `pkg/admin/static/dist/index.html`:

```html
<!doctype html>
<meta charset="utf-8">
<title>mmokit admin (placeholder)</title>
<style>
  body { font-family: system-ui, sans-serif; background: #0d1117; color: #cbd5e1; padding: 40px; }
  h1 { color: #7dd3fc; font-weight: 600; }
  code { background: rgba(255,255,255,0.06); padding: 2px 6px; border-radius: 4px; }
</style>
<h1>mmokit admin dashboard</h1>
<p>Backend is up. The Svelte SPA hasn&rsquo;t been built yet &mdash; this is a placeholder.</p>
<p>API is live at <code>/admin/api/*</code>. Try <code>POST /admin/api/auth/login</code> to begin.</p>
```

Create `pkg/admin/static/dist/.gitkeep` (empty file) so `dist/` exists for `go:embed` even on a fresh clone.

- [ ] **Step 2: Wire into admin.go**

In `pkg/admin/admin.go` replace the `embedFallbackFS` stub:

```go
import (
	"io/fs"

	"github.com/zenion/mmoserver/pkg/admin/static"
)

var staticDist fs.FS = mustSubFS(static.FS, "dist")

func mustSubFS(f embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
```

(Imports: add `embed` and `pkg/admin/static`.)

- [ ] **Step 3: Verify compile**

Run: `go vet ./pkg/admin/...`
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add pkg/admin/static/
git commit -m "admin: static FS embed + placeholder index.html"
```

---

### Task 17: Wire admin into universe `bootstrap.go`

**Files:**
- Modify: `pkg/universe/coordinator.go` (add `AdminConfig` to `Config`, `panelRegistry` field on `Process`)
- Modify: `pkg/universe/bootstrap.go` (mount admin server)
- Modify: `pkg/universe/flags.go` if separate, otherwise wherever flag registration lives

- [ ] **Step 1: Add `AdminConfig` to `universe.Config`**

In `pkg/universe/coordinator.go` find the `Config` struct and add:

```go
// In Config struct:
Admin AdminConfig

// New type below Config:
type AdminConfig struct {
	Enabled            bool
	SessionTTL         time.Duration
	LockoutMaxAttempts int
	LockoutWindow      time.Duration
	AuditCap           int
	Operators          []AdminOperatorConfig
}

type AdminOperatorConfig struct {
	Username     string
	PasswordHash string
	Grants       []string
}
```

- [ ] **Step 2: Add the panel registry field on Process and an accessor**

```go
// In Process struct (next to registry/dispatcher):
panelRegistry *admin.PanelRegistry
```

```go
// New file or co-located:
func (c *Process) PanelRegistry() *admin.PanelRegistry {
	return c.panelRegistry
}
```

In `Process.New` (or wherever `c.registry` is built), construct the panel registry:

```go
c.panelRegistry = admin.NewPanelRegistry()
```

Imports: add `github.com/zenion/mmoserver/pkg/admin`.

- [ ] **Step 3: Mount admin in `startAdminHTTPListener`**

In `pkg/universe/bootstrap.go`, modify `startAdminHTTPListener` (around line 219) so that when `Config.Admin.Enabled` is true, it constructs the server and mounts it on the same mux:

```go
func (c *Process) startAdminHTTPListener() {
	if c.cfg.AdminListen == "" {
		return
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", c.MetricsHandler())
	mux.Handle("/commands", handleCommandList(c.registry))
	mux.Handle("/commands/", handleCommandDescribe(c.registry))
	mux.HandleFunc("/events", handleCommitLogEvents(c.commitLog))

	// Mount the admin SPA + API.
	if c.cfg.Admin.Enabled {
		view := admin.NewLocalClusterView(c)
		opCfgs := make([]admin.OperatorConfig, 0, len(c.cfg.Admin.Operators))
		for _, o := range c.cfg.Admin.Operators {
			opCfgs = append(opCfgs, admin.OperatorConfig{
				Username:     o.Username,
				PasswordHash: o.PasswordHash,
				Grants:       o.Grants,
			})
		}
		c.adminServer = admin.NewServer(admin.ServerOpts{
			View:         view,
			Registry:     c.registry,
			Dispatcher:   c.dispatcher,
			SessionStore: admin.NewMemorySessionStore(),
			Panels:       c.panelRegistry,
			Logger:       c.Log,
			Process:      c,
			Config: admin.Config{
				BindAddr:   c.cfg.AdminListen,
				SessionTTL: c.cfg.Admin.SessionTTL,
				LockoutMax: c.cfg.Admin.LockoutMaxAttempts,
				LockoutWin: c.cfg.Admin.LockoutWindow,
				AuditCap:   c.cfg.Admin.AuditCap,
				Operators:  opCfgs,
			},
		})
		c.adminServer.Mount(mux)
		c.Log.Log(CatMeshCell, "admin: mounted /admin/ + /admin/api/* on %s", c.cfg.AdminListen)
	}

	c.adminHTTPServer = &http.Server{Addr: c.cfg.AdminListen, Handler: mux}
	c.Log.Log(CatMeshCell, "admin-http: listening on %s (roles=%s)", c.cfg.AdminListen, c.roles)

	go func() {
		err := c.adminHTTPServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.Log.Log(CatMeshCell, "admin-http: listener error: %v", err)
		}
	}()
}
```

Add `adminServer *admin.Server` to the `Process` struct, and call `c.adminServer.Stop()` from `Process.Shutdown` if non-nil.

- [ ] **Step 4: Register admin flags in bootstrap**

Find where other flags are registered (likely in `pkg/universe/bootstrap.go` near the `AdminListen` flag, line 72):

```go
fs.BoolVar(&c.Admin.Enabled, "admin-enabled",
	false, "enable admin dashboard at /admin/* (requires --admin-listen)")
```

(Operators come from a config file, not flags — Task 19 covers that.)

- [ ] **Step 5: Verify compile**

Run: `go vet ./pkg/universe/... ./pkg/admin/...`
Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add pkg/universe/coordinator.go pkg/universe/bootstrap.go
git commit -m "universe: wire admin.Server onto AdminListen mux when --admin-enabled"
```

---

### Task 18: Builtin panel registrations + log category

**Files:**
- Create: `pkg/admin/builtins.go`
- Modify: `pkg/admin/admin.go` (call `RegisterBuiltinPanels` from `NewServer`)

- [ ] **Step 1: Implement**

Create `pkg/admin/builtins.go`:

```go
package admin

// RegisterBuiltinPanels registers the v1 MVP panels declared in the spec §8.
// Idempotent on first call only; second call returns the duplicate-ID error.
func RegisterBuiltinPanels(r *PanelRegistry) error {
	defs := []PanelDef{
		{ID: "cluster", Label: "Cluster", Icon: "globe", Group: "Cluster",
			Topics: []string{"cells", "topology"},
			Commands: []string{"cell.split", "cell.merge", "cell.migrate"},
			InitialFetch: "/admin/api/cluster"},
		{ID: "hosts", Label: "Hosts", Icon: "server", Group: "Cluster",
			Topics: []string{"hosts"}, InitialFetch: "/admin/api/hosts"},
		{ID: "gateways", Label: "Gateways", Icon: "git-branch", Group: "Cluster",
			Topics: []string{"sessions"}, InitialFetch: "/admin/api/gateways"},
		{ID: "players", Label: "Players", Icon: "users", Group: "People",
			Topics: []string{"players"},
			Commands: []string{"player.tp", "player.tpto", "player.kick", "player.info"},
			InitialFetch: "/admin/api/players"},
		{ID: "performance", Label: "Performance", Icon: "activity", Group: "Diagnose",
			Topics: []string{"cells", "hosts"}},
		{ID: "events", Label: "Events", Icon: "list", Group: "Diagnose",
			Topics: []string{"events", "alerts"},
			InitialFetch: "/admin/api/events?n=100"},
		{ID: "logs", Label: "Logs", Icon: "scroll", Group: "Diagnose"},
		{ID: "settings", Label: "Settings", Icon: "settings", Group: "Config"},
	}
	for _, d := range defs {
		if err := r.Register(d); err != nil {
			return err
		}
	}
	return nil
}
```

In `pkg/admin/admin.go` `NewServer`, after constructing `s`, call:

```go
if opts.Panels != nil {
	_ = RegisterBuiltinPanels(opts.Panels)
}
```

- [ ] **Step 2: Verify compile**

Run: `go vet ./pkg/admin/...`
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add pkg/admin/builtins.go pkg/admin/admin.go
git commit -m "admin: register the 14 MVP panels as builtins"
```

---

### Task 19: `--admin-hash-password` bootstrap flag

**Files:**
- Modify: `pkg/universe/bootstrap.go` (or wherever `--dump-schema` is intercepted)

- [ ] **Step 1: Find the existing `--dump-schema` interception**

```bash
grep -n 'dump-schema\|DumpSchema\|adminHashPassword' pkg/universe/bootstrap.go
```

Expected: a block where `--dump-schema` is checked after `Build()` and the process exits.

- [ ] **Step 2: Add the new flag + interceptor**

Add to flag registration in `pkg/universe/bootstrap.go`:

```go
fs.BoolVar(&c.adminHashPassword, "admin-hash-password",
	false, "interactively prompt for a password and print its argon2id hash, then exit")
```

(Add `adminHashPassword bool` private field to `Process` next to other flag-target fields.)

In `Process.Start` (after `Build()` returns), before the `--dump-schema` block:

```go
if c.adminHashPassword {
	if err := promptAndPrintAdminHash(); err != nil {
		fmt.Fprintf(os.Stderr, "hash-password: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}
```

Add a new helper file `pkg/universe/admin_hash.go`:

```go
package universe

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/zenion/mmoserver/pkg/services/auth"
	"golang.org/x/term"
)

func promptAndPrintAdminHash() error {
	fmt.Fprint(os.Stderr, "admin password: ")
	bytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		// Fallback for non-tty (CI): read from stdin without echo guarantee.
		s := bufio.NewScanner(os.Stdin)
		if !s.Scan() {
			return errors.New("no input")
		}
		bytes = []byte(strings.TrimSpace(s.Text()))
	}
	if len(bytes) == 0 {
		return errors.New("empty password")
	}
	hash, err := auth.HashPassword(string(bytes), auth.DefaultArgonParams())
	if err != nil {
		return err
	}
	fmt.Println(hash)
	return nil
}
```

`golang.org/x/term` is in your existing `go.sum` (used elsewhere) — verify with `grep 'golang.org/x/term' go.sum`. If not present, add it via `go get golang.org/x/term`.

- [ ] **Step 3: Verify compile**

Run: `go vet ./pkg/universe/...`
Expected: no errors.

- [ ] **Step 4: Manual smoke**

```bash
cd examples/4node-basic && just build
echo 'p@ssw0rd!' | ./bin/server --admin-hash-password
```

Expected: prints a single line starting with `$argon2id$v=19$...`.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/bootstrap.go pkg/universe/admin_hash.go go.mod go.sum
git commit -m "universe: --admin-hash-password flag for operator config setup"
```

---

### Task 20: mmokit facade re-exports + `RegisterAdminPanel`

**Files:**
- Create: `pkg/mmokit/admin.go`

- [ ] **Step 1: Implement the facade**

Create `pkg/mmokit/admin.go`:

```go
package mmokit

import (
	"github.com/zenion/mmoserver/pkg/admin"
	"github.com/zenion/mmoserver/pkg/universe"
)

// AdminPanelDef is the mmokit facade alias for admin.PanelDef. Games
// register custom panels via mmokit.RegisterAdminPanel(coord, AdminPanelDef{...}).
type AdminPanelDef = admin.PanelDef

// AdminConfig is the facade alias for universe.AdminConfig.
type AdminConfig = universe.AdminConfig

// AdminOperatorConfig is the facade alias for universe.AdminOperatorConfig.
type AdminOperatorConfig = universe.AdminOperatorConfig

// RegisterAdminPanel adds a game-defined panel to the dashboard sidebar.
// The dashboard SPA reads panels from /admin/api/panels at boot and renders
// any registered panel reflectively from this metadata. Duplicate IDs return
// an error.
func RegisterAdminPanel(coord *universe.Process, def AdminPanelDef) error {
	r := coord.PanelRegistry()
	if r == nil {
		return admin.ErrUnavailable
	}
	return r.Register(def)
}
```

- [ ] **Step 2: Verify compile**

Run: `go vet ./pkg/mmokit/...`
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add pkg/mmokit/admin.go
git commit -m "mmokit: facade — AdminPanelDef + RegisterAdminPanel"
```

---

### Task 21: e2e smoke test

**Files:**
- Create: `pkg/admin/admin_e2e_test.go`

- [ ] **Step 1: Write the test**

Create `pkg/admin/admin_e2e_test.go`:

```go
package admin_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/admin"
	"github.com/zenion/mmoserver/pkg/services/auth"
	"github.com/zenion/mmoserver/pkg/universe"
)

func TestAdminE2E_LoginAndSplit(t *testing.T) {
	t.Parallel()

	hash, err := auth.HashPassword("secret123", auth.DefaultArgonParams())
	if err != nil { t.Fatal(err) }

	cfg := universe.Config{
		Headless: true,
		HTTPPort: -1,
		ClusterDim: universe.ClusterDim{X: 2, Y: 2},
		Admin: universe.AdminConfig{
			Enabled:            true,
			SessionTTL:         time.Hour,
			LockoutMaxAttempts: 5,
			LockoutWindow:      15 * time.Minute,
			Operators: []universe.AdminOperatorConfig{
				{Username: "josh", PasswordHash: hash, Grants: []string{"*.*"}},
			},
		},
	}
	p, err := universe.NewProcess(cfg)
	if err != nil { t.Fatal(err) }
	if err := p.Build(); err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = p.ShutdownNow(time.Second) })

	mux := http.NewServeMux()
	view := admin.NewLocalClusterView(p)
	srv := admin.NewServer(admin.ServerOpts{
		View: view, Registry: p.Registry(), Dispatcher: p.Dispatcher(),
		SessionStore: admin.NewMemorySessionStore(),
		Panels: p.PanelRegistry(), Logger: p.Log,
		Process: p,
		Config: admin.Config{
			BindAddr: "127.0.0.1:0",
			SessionTTL: time.Hour,
			Operators: []admin.OperatorConfig{
				{Username: "josh", PasswordHash: hash, Grants: []string{"*.*"}},
			},
		},
	})
	srv.Mount(mux)
	t.Cleanup(srv.Stop)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	jar, _ := newJar()
	client := &http.Client{Jar: jar}

	// Login.
	loginBody, _ := json.Marshal(map[string]string{"username": "josh", "password": "secret123"})
	resp, err := client.Post(ts.URL+"/admin/api/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil { t.Fatal(err) }
	if resp.StatusCode != 200 {
		t.Fatalf("login status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	// Subscribe to topology stream BEFORE issuing the split so the publish
	// fires while we're listening.
	streamCtx, cancelStream := context.WithCancel(context.Background())
	t.Cleanup(cancelStream)
	streamReq, _ := http.NewRequestWithContext(streamCtx, "GET",
		ts.URL+"/admin/api/stream?topics=topology", nil)
	streamResp, err := client.Do(streamReq)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { streamResp.Body.Close() })

	// Issue a cell split via HTTP.
	splitArgs, _ := json.Marshal(map[string]any{"CellID": "0_0"})
	splitResp, err := client.Post(ts.URL+"/admin/api/commands/cell.split", "application/json", bytes.NewReader(splitArgs))
	if err != nil { t.Fatal(err) }
	if splitResp.StatusCode != 200 {
		body, _ := io.ReadAll(splitResp.Body)
		t.Fatalf("split status=%d body=%s", splitResp.StatusCode, body)
	}
	splitResp.Body.Close()

	// Read SSE until a topology event arrives or timeout.
	deadline := time.After(5 * time.Second)
	scanner := bufio.NewScanner(streamResp.Body)
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for topology SSE event")
		default:
		}
		if !scanner.Scan() {
			t.Fatalf("stream closed: %v", scanner.Err())
		}
		line := scanner.Text()
		if strings.HasPrefix(line, "event: topology") {
			return // success
		}
	}
}

func newJar() (http.CookieJar, error) {
	return cookiejar.New(nil)
}
```

Imports: add `io`, `net/http/cookiejar`. (If you keep imports tidy, the `cookiejar` import lives next to `net/http`.)

- [ ] **Step 2: Run the test**

Run: `go test ./pkg/admin/ -run AdminE2E -v -timeout 30s`
Expected: PASS within ~2 seconds.

- [ ] **Step 3: Commit**

```bash
git add pkg/admin/admin_e2e_test.go
git commit -m "admin: e2e smoke — login + cell.split + SSE topology event"
```

---

### Task 22: CLAUDE.md update

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Add a short section under "Package Layout" → "Generic engine"**

After the `pkg/persist/` line, add:

```markdown
- `pkg/admin/` — admin/observability dashboard backend: `ClusterView` over `*Process`, `TopicBus` for live updates, `/admin/api/*` HTTP routes (auth, cluster, cells, hosts, gateways, players, events, perf, commands, stream, audit), session-cookie auth via argon2id + `pkg/services/auth` primitives, panel registry for game-extensible sidebar entries. Embedded SPA placeholder in `pkg/admin/static/dist/`; full Svelte SPA lives in `web-admin/` (separate plan).
```

Also add to the `Console lifecycle` adjacent section a one-liner under `Config.AdminListen`:

```markdown
**Admin dashboard** (`Config.Admin.Enabled = true`, requires `--admin-listen`): mounts the engine-shipped admin SPA + JSON/SSE API on the same listener. Operators are configured under `Config.Admin.Operators` with argon2id password hashes (use `--admin-hash-password` to generate). Games register custom sidebar panels via `mmokit.RegisterAdminPanel`. See [docs/superpowers/specs/2026-05-10-admin-dashboard-design.md](docs/superpowers/specs/2026-05-10-admin-dashboard-design.md).
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "CLAUDE.md: document pkg/admin and admin dashboard wiring"
```

---

## Self-review checklist (run before declaring this plan done)

- [ ] **Spec coverage:** Every section of `2026-05-10-admin-dashboard-design.md` §3, §4 (backend layout), §6 (wire API), §7 (auth), §10 (error handling), §11 (testing), §13 (logging) maps to a task above. Frontend (§5, §8 panels, §9 Phase 2 roadmap) is explicitly deferred to follow-up plans.
- [ ] **No `pkg/admin/auth_postgres.go`:** Postgres session store is queued for a follow-up plan with the audit-log Postgres migration. v1 ships memory-only; the SessionStore interface is stable for swap-in.
- [ ] **`mmokit.RegisterAdminAPI`** (custom HTTP handler under `/admin/api/game/*`) is mentioned in spec §4.2 but not in this plan — it's queued for the frontend plan since v1 has no consumer for it. Acceptable scope reduction; the SPA plan will add `mmokit.RegisterAdminAPI` alongside the panel custom-component story.
- [ ] **Logging category `admin`:** all `s.log.Log("admin", …)` calls work because `pkg/logger` auto-registers categories on first use; no separate registration step needed (per CLAUDE.md "category-based debug logging with dynamic registration").
- [ ] **Imports:** Spot-check that `pkg/admin/` does not import `pkg/mmokit` or anything in `internal/`. `grep -l 'pkg/mmokit\|/internal/' pkg/admin/*.go` should return nothing.

---

## Execution

Plan complete and saved to `docs/superpowers/plans/2026-05-10-admin-dashboard-backend-foundation.md`. Two execution options:

1. **Subagent-Driven (recommended)** — fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — execute tasks in this session using `executing-plans`, batch execution with checkpoints.

Which approach?
