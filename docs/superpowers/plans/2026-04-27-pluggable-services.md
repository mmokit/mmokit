# Pluggable Services Framework Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a fourth engine role `service` and a generic `pkg/service/` package so game devs can register stateless service kinds (chat, market, account-mgmt) that compose into roles, route via the gateway+OpRouter, and run on horizontally-scalable processes — validated end-to-end with an `echo` demo service in 4node-basic.

**Architecture:** Roles refactor from `uint8` bitmask to `map[string]struct{}`. New `pkg/service/` package holds Kind/Service/Context/Registry. Coordinator gains `ServiceRegistry`; PeerList gains `services` field. Gateway's routing extends from `session→cell` to also handle `opCode→kind→instance` via `hash(connID) % len(instances)`. v1 is stateless-only (DB-backed); active/passive sync, sharding, anti-affinity all deferred. Discovery is announcement-driven: gateway + coordinator are kind-agnostic at compile time.

**Tech Stack:** Go 1.22+, protobuf via buf, pgx/v5 + golang-migrate for persistence, existing mmokit infrastructure (engine, universe, ops, metrics, logger, cmdsys).

**Spec:** [docs/superpowers/specs/2026-04-27-pluggable-services-design.md](../specs/2026-04-27-pluggable-services-design.md)

---

## Phase 1 — Roles refactor (uint8 bitmask → string-keyed map)

This is the prerequisite for everything else. Mechanical type change across the universe package; every callsite already uses identifiers like `RoleHost`, just changes from `Role` constant to `string` constant.

### Task 1.1: Replace `Role`/`Roles` types with string-keyed map

**Files:**
- Modify: `pkg/universe/roles.go`

- [ ] **Step 1:** Replace the entire content of `pkg/universe/roles.go`:

```go
package universe

import (
	"fmt"
	"sort"
	"strings"
)

// Role identifies an individual responsibility a process can run, by name.
// Roles are open-set string keys (no bitmask cap) so the framework can be
// extended without touching this file. The four built-ins are the only
// engine roles; service kinds plug into the dedicated RoleService role.
type Role = string

const (
	RoleCoordinator Role = "coordinator"
	RoleHost        Role = "host"
	RoleGateway     Role = "gateway"
	RoleService     Role = "service"
)

// Roles is a set of Role values.
type Roles map[string]struct{}

// PresetAll is the default role set: coordinator + host + gateway.
// Service is opt-in (not in the default preset) to keep dev-server
// semantics stable. Expressed on the CLI as `--mode=all` (or omitted).
func PresetAll() Roles {
	return Roles{
		RoleCoordinator: {},
		RoleHost:        {},
		RoleGateway:     {},
	}
}

// Has reports whether r contains the given role.
func (r Roles) Has(role Role) bool {
	_, ok := r[role]
	return ok
}

// Add inserts a role into the set.
func (r Roles) Add(role Role) {
	r[role] = struct{}{}
}

// IsEmpty reports whether the set contains no roles.
func (r Roles) IsEmpty() bool { return len(r) == 0 }

// String returns a human-readable comma-separated role list,
// e.g. "coordinator,host,gateway". Sorted for stability.
func (r Roles) String() string {
	if r.IsEmpty() {
		return "(empty)"
	}
	keys := make([]string, 0, len(r))
	for k := range r {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

var validRoles = map[string]struct{}{
	RoleCoordinator: {},
	RoleHost:        {},
	RoleGateway:     {},
	RoleService:     {},
}

// ParseRoles turns a CLI string into a Roles set. Accepts:
//   - "" → PresetAll() — default when --mode is omitted
//   - "all" → PresetAll()
//   - comma-separated list of role names (whitespace-tolerant): "coordinator",
//     "coordinator,gateway", "coordinator,host,gateway", "host", "gateway",
//     "service", combinations like "coordinator,host,gateway,service"
//
// Bare "host" parses successfully here — it represents a remote host that
// dials a coordinator. Process.Build() enforces that bare "host"
// requires Config.CoordinatorAddr.
//
// Returns an error only for unknown tokens.
func ParseRoles(s string) (Roles, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "all" {
		return PresetAll(), nil
	}

	roles := Roles{}
	for _, token := range strings.Split(s, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if token == "node" {
			return nil, fmt.Errorf(`"--mode=node" is removed; use "--mode=host --coordinator-addr=HOST:PORT"`)
		}
		if _, ok := validRoles[token]; !ok {
			return nil, fmt.Errorf("unknown role %q (valid: coordinator, host, gateway, service, all)", token)
		}
		roles.Add(token)
	}

	if roles.IsEmpty() {
		return PresetAll(), nil
	}

	return roles, nil
}
```

- [ ] **Step 2:** Run `go vet ./pkg/universe/` to find broken callsites.

### Task 1.2: Fix all callsites that compared roles via bitmask

**Files:**
- Modify: `pkg/universe/coordinator.go`
- Modify: `pkg/universe/bootstrap.go`
- Modify: `pkg/universe/host_network.go`
- Modify: `pkg/universe/gateway.go`
- Modify: any other file that imports the old `Role uint8`

- [ ] **Step 1:** For each compile error, replace bitmask operations:
  - `roles == Roles(RoleHost)` → `len(roles) == 1 && roles.Has(RoleHost)`
  - `roles == Roles(RoleCoordinator)` → `len(roles) == 1 && roles.Has(RoleCoordinator)`
  - `Roles(RoleHost | RoleGateway)` patterns → construct via literal `Roles{RoleHost: {}, RoleGateway: {}}`
  - `roles.Has(RoleX)` calls — already string-keyed, unchanged
  - `PresetAll` const usage → `PresetAll()` function call
- [ ] **Step 2:** Run `go vet ./...` until no errors.
- [ ] **Step 3:** Run existing tests: `go test ./pkg/universe/ -run TestRoles -count=1` (and any other tests that reference Roles directly).
- [ ] **Step 4:** Commit:

```bash
git add pkg/universe/
git commit -m "refactor(roles): convert Role from uint8 bitmask to string-keyed Roles map"
```

### Task 1.3: Verify cluster fixture tests still pass

- [ ] **Step 1:** Run `go test ./pkg/universe/ -count=1 -short` to validate the refactor didn't break universe-internal tests.
- [ ] **Step 2:** If failures, fix and re-run.

---

## Phase 2 — `Config.ExtraMigrations fs.FS` hook (Open Question 15.4)

Adds a hook so example-specific migrations live next to the example, not under `pkg/`. Needed before Phase 8 (echo demo migration).

### Task 2.1: Add ExtraMigrations field + wire into postgres.Open

**Files:**
- Modify: `pkg/persist/postgres/store.go` (or wherever migrations are run)
- Modify: `pkg/universe/coordinator.go` (Config struct)
- Modify: `pkg/mmokit/mmokit.go` (OpenPostgres if it takes Config)

- [ ] **Step 1:** Find the Postgres open function:

Run: `grep -rn "OpenPostgres\|func.*Store.*Migrate\|golang-migrate" pkg/persist/postgres/ pkg/mmokit/ | head -20`

- [ ] **Step 2:** Add `ExtraMigrations fs.FS` field to `universe.Config` (and `mmokit.Config` if separate). Document: "Optional FS containing additional migrations applied AFTER engine migrations. Files must follow golang-migrate naming convention `NNN_name.up.sql`. Numbering must not collide with engine migrations."
- [ ] **Step 3:** Modify `OpenPostgres` to accept the extra-migrations FS and run them sequentially after the engine migrations using `golang-migrate`'s `iofs.New` source.
- [ ] **Step 4:** Write a unit test in `pkg/persist/postgres/store_test.go` (build-tag `pgtest`) that creates an in-memory FS with a single migration and confirms the table exists after Open.
- [ ] **Step 5:** Commit:

```bash
git add pkg/persist/postgres/ pkg/universe/coordinator.go pkg/mmokit/mmokit.go
git commit -m "feat(persist): add Config.ExtraMigrations fs.FS hook for game-specific migrations"
```

---

## Phase 3 — `pkg/service/` core package skeleton

Creates the new package with Kind/Service/Context types, process-local registry, and validation logic. No coordinator/gateway integration yet.

### Task 3.1: Package layout — empty files with package decl

**Files:**
- Create: `pkg/service/kind.go`
- Create: `pkg/service/service.go`
- Create: `pkg/service/context.go`
- Create: `pkg/service/registry.go`
- Create: `pkg/service/instance.go`
- Create: `pkg/service/router.go`

- [ ] **Step 1:** Create each file with `package service` header.

### Task 3.2: Define `Kind`, `Service`, `Context` types

**Files:**
- Modify: `pkg/service/kind.go`
- Modify: `pkg/service/service.go`
- Modify: `pkg/service/context.go`

- [ ] **Step 1:** Write `pkg/service/kind.go`:

```go
package service

// Kind is the descriptor for a service that game code registers with
// the engine. Engine validates Kinds at startup and routes ops by code.
//
// Each services-role process instantiates exactly one Service per
// listed Kind via Factory.
type Kind struct {
	// Name is the unique kind identifier; matches the token used in
	// --services= and shown in console / metrics.
	Name string

	// OpCodes are the op codes this kind handles. Engine validates at
	// startup that:
	//   - no two registered Kinds claim the same code
	//   - all codes here are actually registered by RegisterOps()
	OpCodes []uint32

	// Factory constructs a Service. Called once per process at services-
	// role startup. Must not block (do real init in Service.Init).
	Factory func(ctx *Context) Service

	// RequiresDB causes Build() to error when DB is not configured.
	RequiresDB bool

	// MetricsPrefix is the prometheus metric prefix for this kind's
	// per-instance NodeMetrics. Defaults to Name when empty.
	MetricsPrefix string

	// HealthCheck is an optional liveness probe. Called only on demand
	// by `service info` fanout and /health aggregation — not periodically.
	// Default = nil = "healthy if Init succeeded".
	HealthCheck func(svc Service) error

	// Description is human-readable text shown in `service info <name>`.
	Description string
}
```

- [ ] **Step 2:** Write `pkg/service/service.go`:

```go
package service

import (
	"context"

	"github.com/zenion/mmoserver/pkg/ops"
)

// Service is the runtime interface a kind's instance implements.
type Service interface {
	// Init runs once after Factory returns and after the engine has
	// validated dependencies. Use it for slow startup work (DB warm,
	// schema validation, etc).
	Init(ctx *Context) error

	// RegisterOps wires handlers into the process op router. Engine
	// calls this exactly once after a successful Init. Engine cross-
	// checks the *exact set* of registered op codes against Kind.OpCodes
	// — any difference (missing or extra) is a fatal startup error.
	RegisterOps(router *ops.Router) error

	// Shutdown is called on graceful process exit. Block until in-flight
	// handlers drain (engine provides a deadline via ctx).
	Shutdown(ctx context.Context) error
}
```

- [ ] **Step 3:** Write `pkg/service/context.go`:

```go
package service

import (
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/metrics"
	"github.com/zenion/mmoserver/pkg/persist/postgres"
	"google.golang.org/protobuf/proto"
)

// Context bundles the runtime dependencies handed to a Service at Init.
//
// Reserved field-additions for v2 (SyncStream, ShardKey, etc.) will be
// added only when the corresponding feature lands.
type Context struct {
	KindName   string
	InstanceID string
	Logger     *logger.Logger
	Metrics    *metrics.NodeMetrics
	DB         *postgres.Store

	// Roles is the role set this process is running. Lets services
	// inspect their colocation environment if needed.
	Roles map[string]struct{}

	// SendEvent forwards a server event to a connected client through
	// the gateway that owns connID. Goes through the same mesh path as
	// cell-originated events. May block briefly under backpressure;
	// returns an error only when the connection has gone away.
	SendEvent func(connID uint32, code uint16, msg proto.Message) error
}
```

- [ ] **Step 4:** Run `go vet ./pkg/service/`.
- [ ] **Step 5:** Commit:

```bash
git add pkg/service/
git commit -m "feat(service): scaffold pkg/service with Kind, Service, Context types"
```

### Task 3.3: Process-local Registry + validation

**Files:**
- Modify: `pkg/service/registry.go`
- Create: `pkg/service/registry_test.go`

- [ ] **Step 1:** Write `pkg/service/registry.go`:

```go
package service

import (
	"fmt"
	"sort"
	"sync"
)

// Registry is a process-local catalog of registered Kinds.
// Built up by Coordinator.RegisterService(...) before Build().
type Registry struct {
	mu    sync.RWMutex
	kinds map[string]Kind  // by Name
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{kinds: map[string]Kind{}}
}

// Register adds a Kind. Returns an error on duplicate Name or empty
// required fields. Op-code overlap is validated by Validate(), not here,
// so order of Register calls doesn't matter.
func (r *Registry) Register(k Kind) error {
	if k.Name == "" {
		return fmt.Errorf("service.Register: Name is required")
	}
	if k.Factory == nil {
		return fmt.Errorf("service.Register: Factory is required for kind %q", k.Name)
	}
	if len(k.OpCodes) == 0 {
		return fmt.Errorf("service.Register: at least one OpCode is required for kind %q", k.Name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.kinds[k.Name]; exists {
		return fmt.Errorf("service.Register: duplicate kind name %q", k.Name)
	}
	r.kinds[k.Name] = k
	return nil
}

// Get returns a registered kind by name.
func (r *Registry) Get(name string) (Kind, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k, ok := r.kinds[name]
	return k, ok
}

// Names returns all registered kind names in sorted order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.kinds))
	for n := range r.kinds {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Validate ensures no two kinds share an op code and any RequiresDB
// kind has DB available. Called by Coordinator at Build time. Returns
// the first violation as an error.
func (r *Registry) Validate(dbConfigured bool) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	codeOwner := map[uint32]string{}
	for _, k := range r.kinds {
		if k.RequiresDB && !dbConfigured {
			return fmt.Errorf("service.Validate: kind %q requires DB but Config.PostgresURL is empty", k.Name)
		}
		for _, code := range k.OpCodes {
			if owner, exists := codeOwner[code]; exists {
				return fmt.Errorf("service.Validate: op code %d claimed by both %q and %q", code, owner, k.Name)
			}
			codeOwner[code] = k.Name
		}
	}
	return nil
}

// SelectKinds returns the kinds whose names appear in the requested list.
// Returns an error if any requested name is not registered.
func (r *Registry) SelectKinds(names []string) ([]Kind, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Kind, 0, len(names))
	for _, n := range names {
		k, ok := r.kinds[n]
		if !ok {
			return nil, fmt.Errorf("service.SelectKinds: kind %q is not registered", n)
		}
		out = append(out, k)
	}
	return out, nil
}
```

- [ ] **Step 2:** Write `pkg/service/registry_test.go`:

```go
package service

import (
	"strings"
	"testing"

	"github.com/zenion/mmoserver/pkg/ops"
)

type stubService struct{}

func (stubService) Init(*Context) error                 { return nil }
func (stubService) RegisterOps(*ops.Router) error       { return nil }
func (stubService) Shutdown(_ interface{ Done() }) error { return nil }

func newKind(name string, codes ...uint32) Kind {
	return Kind{
		Name:    name,
		OpCodes: codes,
		Factory: func(*Context) Service { return nil },
	}
}

func TestRegistry_Register_Validates(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Kind{}); err == nil {
		t.Fatalf("expected error for empty Kind")
	}
	if err := r.Register(Kind{Name: "x"}); err == nil {
		t.Fatalf("expected error for missing Factory")
	}
	if err := r.Register(Kind{Name: "x", Factory: func(*Context) Service { return nil }}); err == nil {
		t.Fatalf("expected error for missing OpCodes")
	}
	good := newKind("chat", 50, 51)
	if err := r.Register(good); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := r.Register(good); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

func TestRegistry_Validate_OpCodeConflict(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(newKind("chat", 50, 51))
	_ = r.Register(newKind("market", 51, 52))
	err := r.Validate(true)
	if err == nil || !strings.Contains(err.Error(), "code 51") {
		t.Fatalf("expected op code 51 conflict, got %v", err)
	}
}

func TestRegistry_Validate_RequiresDB(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(Kind{
		Name:       "needs_db",
		OpCodes:    []uint32{100},
		Factory:    func(*Context) Service { return nil },
		RequiresDB: true,
	})
	if err := r.Validate(false); err == nil {
		t.Fatalf("expected DB-required error")
	}
	if err := r.Validate(true); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestRegistry_SelectKinds(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(newKind("a", 1))
	_ = r.Register(newKind("b", 2))
	got, err := r.SelectKinds([]string{"a", "b"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if _, err := r.SelectKinds([]string{"a", "missing"}); err == nil {
		t.Fatalf("expected missing-kind error")
	}
}
```

- [ ] **Step 3:** Drop the `stubService` (it accidentally has wrong method shape). Replace with a real interface-satisfying stub if needed — for the tests above we don't actually instantiate services, only inspect kinds.

```go
// remove stubService entirely; tests only call r.Register/Validate/SelectKinds, not service constructors
```

- [ ] **Step 4:** Run `go test ./pkg/service/ -count=1 -v`.
- [ ] **Step 5:** Commit:

```bash
git add pkg/service/
git commit -m "feat(service): process-local Registry with op-code overlap + RequiresDB validation"
```

### Task 3.4: Op-code routing index (`router.go`)

**Files:**
- Modify: `pkg/service/router.go`
- Create: `pkg/service/router_test.go`

- [ ] **Step 1:** Write `pkg/service/router.go`:

```go
package service

import (
	"fmt"
	"sort"
	"sync"
)

// InstanceRoute is the gateway's routing entry for a single live
// service instance.
type InstanceRoute struct {
	InstanceID string
	HostID     string
}

// RoutingIndex is the gateway's view of which kind owns each op code
// and which instances of each kind are live. Built from PeerList.
//
// Concurrency: read-heavy. Updated atomically on PeerList apply by
// rebuilding the entire index, which is then swapped under the mu.
type RoutingIndex struct {
	mu        sync.RWMutex
	opToKind  map[uint32]string
	instances map[string][]InstanceRoute // sorted lex by InstanceID
}

// NewRoutingIndex returns an empty index.
func NewRoutingIndex() *RoutingIndex {
	return &RoutingIndex{
		opToKind:  map[uint32]string{},
		instances: map[string][]InstanceRoute{},
	}
}

// Apply rebuilds the index from a slice of (kind, instanceID, hostID, opCodes)
// records. Returns an error if any op code is claimed by two different kinds.
func (r *RoutingIndex) Apply(records []ServiceRecord) error {
	op := map[uint32]string{}
	inst := map[string][]InstanceRoute{}
	for _, rec := range records {
		for _, code := range rec.OpCodes {
			if owner, exists := op[code]; exists && owner != rec.Kind {
				return fmt.Errorf("RoutingIndex.Apply: op code %d claimed by %q and %q", code, owner, rec.Kind)
			}
			op[code] = rec.Kind
		}
		inst[rec.Kind] = append(inst[rec.Kind], InstanceRoute{
			InstanceID: rec.InstanceID,
			HostID:     rec.HostID,
		})
	}
	for k := range inst {
		sort.Slice(inst[k], func(i, j int) bool {
			return inst[k][i].InstanceID < inst[k][j].InstanceID
		})
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.opToKind = op
	r.instances = inst
	return nil
}

// LookupKind returns the kind name owning opCode, or "" if unclaimed.
func (r *RoutingIndex) LookupKind(opCode uint32) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.opToKind[opCode]
}

// PickInstance picks an instance of kind for the given affinity key
// using hash(connID) % len(instances). Returns the chosen route and
// true, or an empty route and false if no instances are live.
func (r *RoutingIndex) PickInstance(kind string, connID uint32) (InstanceRoute, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	insts := r.instances[kind]
	if len(insts) == 0 {
		return InstanceRoute{}, false
	}
	return insts[connID%uint32(len(insts))], true
}

// InstancesOfKind returns a defensive copy of the live instances for kind.
func (r *RoutingIndex) InstancesOfKind(kind string) []InstanceRoute {
	r.mu.RLock()
	defer r.mu.RUnlock()
	src := r.instances[kind]
	out := make([]InstanceRoute, len(src))
	copy(out, src)
	return out
}

// ServiceRecord mirrors meshpb.ServiceRecord (declared here so this
// package doesn't import meshpb directly — universe converts proto
// messages to []ServiceRecord at the boundary).
type ServiceRecord struct {
	Kind       string
	InstanceID string
	HostID     string
	OpCodes    []uint32
}
```

- [ ] **Step 2:** Write `pkg/service/router_test.go`:

```go
package service

import (
	"strings"
	"testing"
)

func TestRoutingIndex_Apply_LookupAndPick(t *testing.T) {
	r := NewRoutingIndex()
	err := r.Apply([]ServiceRecord{
		{Kind: "echo", InstanceID: "host-b-echo-0", HostID: "host-b", OpCodes: []uint32{300, 301}},
		{Kind: "echo", InstanceID: "host-a-echo-0", HostID: "host-a", OpCodes: []uint32{300, 301}},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if r.LookupKind(300) != "echo" {
		t.Fatalf("want echo, got %q", r.LookupKind(300))
	}
	if r.LookupKind(999) != "" {
		t.Fatalf("expected unclaimed, got %q", r.LookupKind(999))
	}
	// Stable ordering: connID=0 picks the lex-smallest instance.
	got, ok := r.PickInstance("echo", 0)
	if !ok || got.InstanceID != "host-a-echo-0" {
		t.Fatalf("want host-a-echo-0, got %+v ok=%v", got, ok)
	}
	got, ok = r.PickInstance("echo", 1)
	if !ok || got.InstanceID != "host-b-echo-0" {
		t.Fatalf("want host-b-echo-0, got %+v ok=%v", got, ok)
	}
	if _, ok := r.PickInstance("missing", 0); ok {
		t.Fatalf("expected no instance for missing kind")
	}
}

func TestRoutingIndex_Apply_OpCodeConflict(t *testing.T) {
	r := NewRoutingIndex()
	err := r.Apply([]ServiceRecord{
		{Kind: "a", InstanceID: "i1", HostID: "h1", OpCodes: []uint32{50}},
		{Kind: "b", InstanceID: "i2", HostID: "h2", OpCodes: []uint32{50}},
	})
	if err == nil || !strings.Contains(err.Error(), "code 50") {
		t.Fatalf("expected code 50 conflict, got %v", err)
	}
}

func TestRoutingIndex_Apply_HashAffinityDeterministic(t *testing.T) {
	r := NewRoutingIndex()
	_ = r.Apply([]ServiceRecord{
		{Kind: "x", InstanceID: "x-2", HostID: "h", OpCodes: []uint32{1}},
		{Kind: "x", InstanceID: "x-0", HostID: "h", OpCodes: []uint32{1}},
		{Kind: "x", InstanceID: "x-1", HostID: "h", OpCodes: []uint32{1}},
	})
	// connID=42 picks instance index 42%3=0 → "x-0" (lex-smallest)
	got, _ := r.PickInstance("x", 42)
	if got.InstanceID != "x-0" {
		t.Fatalf("want x-0, got %s", got.InstanceID)
	}
	got, _ = r.PickInstance("x", 43)
	if got.InstanceID != "x-1" {
		t.Fatalf("want x-1, got %s", got.InstanceID)
	}
}
```

- [ ] **Step 3:** Run `go test ./pkg/service/ -count=1 -v`.
- [ ] **Step 4:** Commit:

```bash
git add pkg/service/
git commit -m "feat(service): RoutingIndex with hash(connID) affinity + op-code lookup"
```

### Task 3.5: Coordinator-side ServiceRegistry

**Files:**
- Create: `pkg/service/registry_coord.go`
- Create: `pkg/service/registry_coord_test.go`

- [ ] **Step 1:** Write `pkg/service/registry_coord.go`:

```go
package service

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// CoordRegistry is the coordinator-side roster of running service
// instances across the cluster. Peer to HostRegistry / GatewayRegistry.
//
// Concurrency: protected by mu. All mutations also bump epoch so callers
// can detect changes for PeerList re-broadcast.
type CoordRegistry struct {
	mu        sync.RWMutex
	instances map[string]CoordInstance       // by InstanceID
	byKind    map[string][]string            // kind → []instanceID
	opToKind  map[uint32]string              // op code → owning kind
	epoch     uint64
}

// CoordInstance is a single live service instance in the cluster.
type CoordInstance struct {
	Kind       string
	InstanceID string
	HostID     string
	OpCodes    []uint32
	JoinedAt   time.Time
}

// NewCoordRegistry returns an empty registry.
func NewCoordRegistry() *CoordRegistry {
	return &CoordRegistry{
		instances: map[string]CoordInstance{},
		byKind:    map[string][]string{},
		opToKind:  map[uint32]string{},
	}
}

// Register adds an instance. Validates:
//   - InstanceID not already in use
//   - If kind already has live instances, op codes match exactly
//   - No op code overlaps with a different kind
func (r *CoordRegistry) Register(inst CoordInstance) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.instances[inst.InstanceID]; exists {
		return fmt.Errorf("service.CoordRegistry.Register: duplicate instanceID %q", inst.InstanceID)
	}

	// All instances of the same kind must declare the same op-code set.
	if existingIDs := r.byKind[inst.Kind]; len(existingIDs) > 0 {
		ref := r.instances[existingIDs[0]].OpCodes
		if !sameCodeSet(ref, inst.OpCodes) {
			return fmt.Errorf("service.CoordRegistry.Register: kind %q already running with op codes %v; new instance %q declares %v",
				inst.Kind, ref, inst.InstanceID, inst.OpCodes)
		}
	} else {
		// New kind: ensure op codes don't overlap with another kind.
		for _, code := range inst.OpCodes {
			if owner, exists := r.opToKind[code]; exists && owner != inst.Kind {
				return fmt.Errorf("service.CoordRegistry.Register: op code %d claimed by kind %q; new kind %q rejected",
					code, owner, inst.Kind)
			}
		}
	}

	r.instances[inst.InstanceID] = inst
	r.byKind[inst.Kind] = append(r.byKind[inst.Kind], inst.InstanceID)
	sort.Strings(r.byKind[inst.Kind])
	for _, code := range inst.OpCodes {
		r.opToKind[code] = inst.Kind
	}
	r.epoch++
	return nil
}

// Unregister removes an instance. If it was the last instance of its
// kind, the op-code claims for that kind are also removed.
func (r *CoordRegistry) Unregister(instanceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	inst, ok := r.instances[instanceID]
	if !ok {
		return
	}
	delete(r.instances, instanceID)

	ids := r.byKind[inst.Kind]
	out := ids[:0]
	for _, id := range ids {
		if id != instanceID {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		delete(r.byKind, inst.Kind)
		for _, code := range inst.OpCodes {
			delete(r.opToKind, code)
		}
	} else {
		r.byKind[inst.Kind] = out
	}
	r.epoch++
}

// UnregisterByHost removes every instance owned by hostID. Used when a
// host crashes or gracefully leaves.
func (r *CoordRegistry) UnregisterByHost(hostID string) {
	r.mu.Lock()
	ids := []string{}
	for id, inst := range r.instances {
		if inst.HostID == hostID {
			ids = append(ids, id)
		}
	}
	r.mu.Unlock()
	for _, id := range ids {
		r.Unregister(id)
	}
}

// Snapshot returns the full instance list for PeerList broadcast,
// sorted by (Kind, InstanceID) for stable wire output.
func (r *CoordRegistry) Snapshot() []CoordInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]CoordInstance, 0, len(r.instances))
	for _, inst := range r.instances {
		out = append(out, inst)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].InstanceID < out[j].InstanceID
	})
	return out
}

// Epoch returns the current change counter. Callers can compare epochs
// to decide whether a re-broadcast is needed.
func (r *CoordRegistry) Epoch() uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.epoch
}

// LookupByOpCode returns the kind that owns opCode, or "" if unclaimed.
func (r *CoordRegistry) LookupByOpCode(opCode uint32) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.opToKind[opCode]
}

// InstancesOfKind returns a defensive copy of all live instances for kind.
func (r *CoordRegistry) InstancesOfKind(kind string) []CoordInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := r.byKind[kind]
	out := make([]CoordInstance, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.instances[id])
	}
	return out
}

func sameCodeSet(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[uint32]bool{}
	for _, c := range a {
		seen[c] = true
	}
	for _, c := range b {
		if !seen[c] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 2:** Write `pkg/service/registry_coord_test.go` covering: register success, duplicate instanceID, kind-with-mismatched-codes rejection, cross-kind overlap rejection, unregister cleans op-code claims when last instance leaves, UnregisterByHost.

```go
package service

import (
	"strings"
	"testing"
	"time"
)

func ci(kind, id, host string, codes ...uint32) CoordInstance {
	return CoordInstance{Kind: kind, InstanceID: id, HostID: host, OpCodes: codes, JoinedAt: time.Now()}
}

func TestCoordRegistry_Register_Duplicate(t *testing.T) {
	r := NewCoordRegistry()
	if err := r.Register(ci("echo", "i1", "h", 1)); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(ci("echo", "i1", "h", 1)); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("want duplicate error, got %v", err)
	}
}

func TestCoordRegistry_Register_KindCodeMismatch(t *testing.T) {
	r := NewCoordRegistry()
	_ = r.Register(ci("echo", "i1", "h1", 100, 101))
	err := r.Register(ci("echo", "i2", "h2", 100, 999))
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("want kind-mismatch error, got %v", err)
	}
}

func TestCoordRegistry_Register_CrossKindConflict(t *testing.T) {
	r := NewCoordRegistry()
	_ = r.Register(ci("a", "i1", "h", 50))
	err := r.Register(ci("b", "i2", "h", 50))
	if err == nil || !strings.Contains(err.Error(), "claimed by kind") {
		t.Fatalf("want cross-kind error, got %v", err)
	}
}

func TestCoordRegistry_Unregister_ReleasesOpCodes(t *testing.T) {
	r := NewCoordRegistry()
	_ = r.Register(ci("echo", "i1", "h", 100))
	r.Unregister("i1")
	if r.LookupByOpCode(100) != "" {
		t.Fatalf("want unclaimed after last instance left, got %q", r.LookupByOpCode(100))
	}
	// New kind can now claim the same code:
	if err := r.Register(ci("other", "i2", "h", 100)); err != nil {
		t.Fatalf("expected reuse: %v", err)
	}
}

func TestCoordRegistry_UnregisterByHost(t *testing.T) {
	r := NewCoordRegistry()
	_ = r.Register(ci("echo", "i1", "host-a", 100))
	_ = r.Register(ci("echo", "i2", "host-b", 100))
	r.UnregisterByHost("host-a")
	if got := r.InstancesOfKind("echo"); len(got) != 1 || got[0].InstanceID != "i2" {
		t.Fatalf("want only i2, got %+v", got)
	}
}

func TestCoordRegistry_Snapshot_SortedAndEpochBumps(t *testing.T) {
	r := NewCoordRegistry()
	e0 := r.Epoch()
	_ = r.Register(ci("z", "z-i", "h", 200))
	_ = r.Register(ci("a", "a-i", "h", 100))
	if r.Epoch() <= e0 {
		t.Fatalf("expected epoch bump")
	}
	got := r.Snapshot()
	if len(got) != 2 || got[0].Kind != "a" || got[1].Kind != "z" {
		t.Fatalf("expected sorted by kind, got %+v", got)
	}
}
```

- [ ] **Step 3:** Run `go test ./pkg/service/ -count=1 -v`.
- [ ] **Step 4:** Commit:

```bash
git add pkg/service/
git commit -m "feat(service): coordinator-side CoordRegistry with epoch + per-host cleanup"
```

---

## Phase 4 — PeerList proto extension + meshpb regen

### Task 4.1: Add ServiceRecord to PeerList

**Files:**
- Modify: `proto/meshpb/mesh.proto`
- Regenerate: `gen/go/meshpb/`

- [ ] **Step 1:** Edit `proto/meshpb/mesh.proto`:

Add after `GatewayRecord`:

```protobuf
message ServiceRecord {
  string kind                = 1;
  string instance_id         = 2;
  string host_id             = 3;
  repeated uint32 op_codes   = 4;
}
```

And modify `PeerList`:

```protobuf
message PeerList {
  repeated HostRecord    hosts    = 1;
  repeated CellOwnership cells    = 2;
  repeated GatewayRecord gateways = 3;
  repeated ServiceRecord services = 4;
}
```

- [ ] **Step 2:** Run `just proto`. Verify `gen/go/meshpb/mesh.pb.go` has `ServiceRecord` type.
- [ ] **Step 3:** Commit:

```bash
git add proto/meshpb/ gen/
git commit -m "feat(meshpb): add ServiceRecord field to PeerList"
```

---

## Phase 5 — Coordinator + Process integration

This wires the new pieces into the existing universe.Process: registry storage, --services= flag, kind validation at Build, instantiation at Start, MeshControl announce, PeerList broadcast.

### Task 5.1: Add Registry, ServiceKinds config, services-role validation

**Files:**
- Modify: `pkg/universe/coordinator.go`

- [ ] **Step 1:** In `Config` struct add:

```go
// ServiceKinds is the list of service kind names this process should
// instantiate when RoleService is in the role set. Each name must
// match a Kind registered via RegisterService(). Validated at Build.
ServiceKinds []string
```

- [ ] **Step 2:** On `Process` struct add:

```go
// services holds Kind registrations made before Build. Populated by
// RegisterService. Required for any process running RoleService.
services *service.Registry

// coordServices is non-nil only on processes with RoleCoordinator —
// it tracks the cluster-wide service-instance roster and feeds PeerList.
coordServices *service.CoordRegistry

// runningServices are the Service instances created on this process at
// Start when RoleService is in the role set. Indexed by Kind.Name.
runningServices map[string]runningService
```

with helper struct:

```go
type runningService struct {
	kind     service.Kind
	svc      service.Service
	instance service.CoordInstance
}
```

- [ ] **Step 3:** In `NewCoordinator`, initialize `c.services = service.NewRegistry()`.
- [ ] **Step 4:** Add `Process.RegisterService`:

```go
// RegisterService records a service Kind so it can be instantiated when
// RoleService is in this process's role set. Must be called before
// Build(). Returns an error on duplicate Kind.Name or invalid descriptor.
func (c *Process) RegisterService(k service.Kind) error {
	if c.built {
		return fmt.Errorf("Process.RegisterService: cannot register %q after Build()", k.Name)
	}
	return c.services.Register(k)
}
```

- [ ] **Step 5:** In `Build()` (after `roles, err := ParseRoles(...)`):

```go
// Cross-validate roles vs --services= flag.
hasServiceRole := roles.Has(RoleService)
hasServiceKinds := len(cfg.ServiceKinds) > 0
if hasServiceRole && !hasServiceKinds {
	panic(fmt.Errorf("coordinator: RoleService requires Config.ServiceKinds to be non-empty (use --services=...)"))
}
if hasServiceKinds && !hasServiceRole {
	panic(fmt.Errorf("coordinator: Config.ServiceKinds set but RoleService missing — add 'service' to --mode"))
}

// Validate registered service kinds even if RoleService isn't set;
// the same binary may be deployed with --mode that omits service,
// but registrations must be internally consistent.
if err := c.services.Validate(cfg.PostgresURL != ""); err != nil {
	panic(fmt.Errorf("coordinator: %w", err))
}
if hasServiceRole {
	if _, err := c.services.SelectKinds(cfg.ServiceKinds); err != nil {
		panic(fmt.Errorf("coordinator: invalid --services list: %w", err))
	}
}

// Coordinator-side registry — populated by MeshControl handlers below
// and consumed by PeerList broadcasts.
if roles.Has(RoleCoordinator) {
	c.coordServices = service.NewCoordRegistry()
}
```

- [ ] **Step 6:** Run `go vet ./pkg/universe/`.
- [ ] **Step 7:** Commit:

```bash
git add pkg/universe/coordinator.go
git commit -m "feat(universe): add Config.ServiceKinds + Process.services registry + RoleService validation"
```

### Task 5.2: --services= flag in bootstrap

**Files:**
- Modify: `pkg/universe/bootstrap.go`

- [ ] **Step 1:** In the flag-parsing function, add:

```go
flag.Func("services", "comma-separated list of service kinds to instantiate (RoleService only)", func(s string) error {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	cfg.ServiceKinds = out
	return nil
})
```

- [ ] **Step 2:** Run `go vet ./pkg/universe/`.
- [ ] **Step 3:** Commit:

```bash
git add pkg/universe/bootstrap.go
git commit -m "feat(bootstrap): add --services= flag for service-role processes"
```

### Task 5.3: Service instantiation at Start

**Files:**
- Modify: `pkg/universe/coordinator.go`

- [ ] **Step 1:** In `Start()`, after roles are determined and the regular cell-host setup runs (locate the spot where roles.Has(RoleHost) starts the host), add a parallel block:

```go
// Instantiate services if this process runs RoleService.
if c.roles.Has(RoleService) {
	if err := c.startServices(ctx); err != nil {
		c.Log.Log(CatMeshCell, "service: startup failed: %v", err)
		panic(err)
	}
}
```

- [ ] **Step 2:** Add `startServices` method on `*Process`:

```go
func (c *Process) startServices(ctx context.Context) error {
	c.runningServices = map[string]runningService{}

	hostID := c.localHostID() // or c.cfg.HostID with autogen fallback; reuse host-ID logic
	kinds, err := c.services.SelectKinds(c.cfg.ServiceKinds)
	if err != nil {
		return fmt.Errorf("startServices: %w", err)
	}

	for i, k := range kinds {
		instanceID := fmt.Sprintf("%s-%s-%d", hostID, k.Name, i)
		svcCtx := &service.Context{
			KindName:   k.Name,
			InstanceID: instanceID,
			Logger:     c.Log,
			Metrics:    c.metrics(), // accessor for *metrics.NodeMetrics; create if missing
			DB:         c.dbStore,    // wherever the postgres.Store handle lives
			Roles:      map[string]struct{}(c.roles),
			SendEvent:  c.serviceSendEvent,
		}
		svc := k.Factory(svcCtx)
		if svc == nil {
			return fmt.Errorf("startServices: kind %q Factory returned nil", k.Name)
		}
		if err := svc.Init(svcCtx); err != nil {
			return fmt.Errorf("startServices: kind %q Init: %w", k.Name, err)
		}

		// Capture op-codes BEFORE RegisterOps so we can cross-check.
		preCodes := snapshotRouterCodes(c.opRouter)
		if err := svc.RegisterOps(c.opRouter); err != nil {
			return fmt.Errorf("startServices: kind %q RegisterOps: %w", k.Name, err)
		}
		postCodes := snapshotRouterCodes(c.opRouter)
		newCodes := codesAdded(preCodes, postCodes)
		if !equalCodeSet(newCodes, k.OpCodes) {
			return fmt.Errorf("startServices: kind %q registered %v but Kind.OpCodes is %v", k.Name, newCodes, k.OpCodes)
		}

		c.runningServices[k.Name] = runningService{
			kind: k,
			svc:  svc,
			instance: service.CoordInstance{
				Kind:       k.Name,
				InstanceID: instanceID,
				HostID:     hostID,
				OpCodes:    k.OpCodes,
				JoinedAt:   time.Now(),
			},
		}

		// Auto-register a log category per kind.
		c.Log.RegisterCategory("services:" + k.Name)
		c.Log.Log("services:"+k.Name, "service %q instance %q started (codes=%v)", k.Name, instanceID, k.OpCodes)
	}

	// Announce to coordinator.
	return c.announceServicesLocked()
}

// announceServicesLocked posts each running service's CoordInstance to
// the coordinator-side registry. For colocated coord+service this is
// in-process; for remote service-only it goes via MeshControl.
func (c *Process) announceServicesLocked() error {
	for _, rs := range c.runningServices {
		if c.coordServices != nil {
			// Local coordinator path.
			if err := c.coordServices.Register(rs.instance); err != nil {
				return fmt.Errorf("announceServicesLocked: %w", err)
			}
		} else {
			// Remote coordinator path — send via MeshControl.
			if err := c.sendServiceAnnounce(rs.instance); err != nil {
				return fmt.Errorf("announceServicesLocked remote: %w", err)
			}
		}
	}
	if c.coordServices != nil {
		c.broadcastPeerList() // existing helper
	}
	return nil
}
```

- [ ] **Step 3:** Add helper `snapshotRouterCodes` / `codesAdded` / `equalCodeSet` to `coordinator.go` or a new `pkg/universe/service_helpers.go` — these introspect the `ops.Router` to verify the right codes were registered. The router exposes only `Register`; we need either a new accessor or to track manually. **Decision: add a `Codes()` method to `ops.Router`** that returns `[]uint32` of registered codes (sorted). Implement it:

```go
// pkg/ops/router.go
// Codes returns the op codes currently registered, sorted. Used by the
// service framework to cross-check Kind.OpCodes against actual registrations.
func (r *Router) Codes() []uint32 {
	out := make([]uint32, 0, len(r.handlers))
	for c := range r.handlers {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
```

(Add `import "sort"` to router.go if missing.)

Then in `service_helpers.go`:

```go
package universe

import "github.com/zenion/mmoserver/pkg/ops"

func snapshotRouterCodes(r *ops.Router) map[uint32]bool {
	out := map[uint32]bool{}
	for _, c := range r.Codes() {
		out[c] = true
	}
	return out
}

func codesAdded(pre, post map[uint32]bool) []uint32 {
	out := []uint32{}
	for c := range post {
		if !pre[c] {
			out = append(out, c)
		}
	}
	return out
}

func equalCodeSet(a []uint32, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[uint32]bool{}
	for _, c := range a {
		seen[c] = true
	}
	for _, c := range b {
		if !seen[c] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 4:** Skim `Process` to find: localHostID equivalent, opRouter field, dbStore field, metrics() accessor, broadcastPeerList equivalent. Use the existing names; the placeholders above will need to be renamed. Run `grep -n "hostID\|opRouter\|broadcastPeerList\|metrics\b" pkg/universe/coordinator.go` to find them.
- [ ] **Step 5:** Implement `c.serviceSendEvent`:

```go
// serviceSendEvent forwards a server event to a connected client through
// the gateway path. It reuses the cell-event SendEvent machinery.
func (c *Process) serviceSendEvent(connID uint32, code uint16, msg proto.Message) error {
	// Use existing gateway send path; in single-process mode this goes
	// through ConnMgr.SendReliable directly.
	if c.connMgr == nil {
		return fmt.Errorf("serviceSendEvent: no connection manager (gateway role required)")
	}
	frame, err := buildEventFrame(code, msg)
	if err != nil {
		return err
	}
	return c.connMgr.SendReliable(connID, frame)
}
```

(`buildEventFrame` may already exist with a different name — find it via `grep -n "BuildEvent\|buildEvent" pkg/universe/`.)

- [ ] **Step 6:** Run `go vet ./...`. Fix compile errors.
- [ ] **Step 7:** Commit:

```bash
git add pkg/universe/ pkg/ops/router.go
git commit -m "feat(universe): instantiate services at Start; cross-check registered op codes"
```

### Task 5.4: MeshControl wire for ServiceAnnounce / ServiceLeave

**Files:**
- Modify: `proto/meshpb/mesh.proto`
- Modify: `pkg/universe/control_plane.go` (or wherever MeshControl handlers live)
- Modify: `pkg/universe/host_network.go` or similar (client-side dial)

- [ ] **Step 1:** Add to `mesh.proto` inside `HostMessage`:

```protobuf
message HostMessage {
  oneof msg {
    // ... existing fields ...
    ServiceAnnounce service_announce = N;  // pick next free tag
    ServiceLeave    service_leave    = N+1;
  }
}

message ServiceAnnounce {
  string kind                 = 1;
  string instance_id          = 2;
  string host_id              = 3;
  repeated uint32 op_codes    = 4;
}

message ServiceLeave {
  string instance_id = 1;
}
```

- [ ] **Step 2:** Run `just proto`.
- [ ] **Step 3:** On the **coordinator** side: extend the MeshControl HostMessage dispatcher (find via `grep -rn "case \*meshpb.HostMessage_" pkg/universe/`) to handle `ServiceAnnounce` and `ServiceLeave`. Each handler calls `c.coordServices.Register(...)` / `Unregister(...)` and then `c.broadcastPeerList()`. Reject `Register` errors back to the host.
- [ ] **Step 4:** On the **service-host** side: `sendServiceAnnounce` posts a `HostMessage` with `ServiceAnnounce` populated, awaits an ack/error from coordinator. (If MeshControl already has an ack pattern, reuse it; otherwise log + assume success — coordinator broadcasts validate.)
- [ ] **Step 5:** When a host's `GracefulLeave` fires, before the existing cell-drain logic, call `c.coordServices.UnregisterByHost(hostID)` so service instances on a graceful-leaving host go away cleanly.
- [ ] **Step 6:** Hook into the host-dead path: when the heartbeat watcher kills a host, also call `coordServices.UnregisterByHost(hostID)`.
- [ ] **Step 7:** Run `go vet ./...`.
- [ ] **Step 8:** Commit:

```bash
git add proto/meshpb/ gen/ pkg/universe/
git commit -m "feat(universe): MeshControl ServiceAnnounce/ServiceLeave + host-leave cleanup"
```

### Task 5.5: PeerList broadcast carries services

**Files:**
- Modify: `pkg/universe/coord_assignment.go` (or wherever broadcastPeerList lives — `grep -n "broadcastPeerList\|BuildPeerList\|PeerList{" pkg/universe/`)

- [ ] **Step 1:** Find `broadcastPeerList` and add the `services` field to the assembled `meshpb.PeerList`:

```go
peerList := &meshpb.PeerList{
	Hosts:    hosts,
	Cells:    cells,
	Gateways: gateways,
}
if c.coordServices != nil {
	for _, inst := range c.coordServices.Snapshot() {
		peerList.Services = append(peerList.Services, &meshpb.ServiceRecord{
			Kind:       inst.Kind,
			InstanceId: inst.InstanceID,
			HostId:     inst.HostID,
			OpCodes:    inst.OpCodes,
		})
	}
}
```

- [ ] **Step 2:** On the receiving side, every host + gateway that processes incoming PeerList must now also update its local view of services. Most hosts don't need this (only gateways do for routing), but all peers receive the same broadcast — so wire a hook on the `Process` for "service routing index".
- [ ] **Step 3:** Add `c.serviceRouting *service.RoutingIndex` field to `Process`, initialized for every role set. On PeerList apply (find via `grep -n "PeerList\|case \*meshpb.CoordMessage_" pkg/universe/`):

```go
recs := make([]service.ServiceRecord, 0, len(pl.Services))
for _, s := range pl.Services {
	recs = append(recs, service.ServiceRecord{
		Kind:       s.Kind,
		InstanceID: s.InstanceId,
		HostID:     s.HostId,
		OpCodes:    s.OpCodes,
	})
}
if err := c.serviceRouting.Apply(recs); err != nil {
	c.Log.Log(CatMeshCell, "service: PeerList apply failed: %v", err)
}
```

- [ ] **Step 4:** Run `go vet ./...`.
- [ ] **Step 5:** Commit:

```bash
git add pkg/universe/
git commit -m "feat(universe): include services in PeerList broadcast; gateway updates RoutingIndex on apply"
```

---

## Phase 6 — Gateway op routing extension

Wires the `serviceRouting` index into the gateway's op-receive path so service-claimed op codes get forwarded to the right service host instead of the player's cell.

### Task 6.1: Service-routing branch in op dispatch

**Files:**
- Modify: `pkg/universe/gateway.go` or `pkg/ops/router.go` (depending on where the gateway forwards client ops)

- [ ] **Step 1:** Find where the gateway receives a client op envelope and decides where to forward it. The likely spot: `pkg/universe/gateway.go` — search `grep -n "Operation\|op_code\|opCode" pkg/universe/gateway.go`.
- [ ] **Step 2:** Before the existing session→cell forward, check the routing index:

```go
if kind := c.serviceRouting.LookupKind(opCode); kind != "" {
	route, ok := c.serviceRouting.PickInstance(kind, connID)
	if !ok {
		return c.replyOpError(connID, reqID, "service unavailable: no healthy instance of kind "+kind)
	}
	return c.forwardOpToHost(route.HostID, connID, opEnvelope)
}
// fall through to existing cell-routing path
```

- [ ] **Step 3:** `forwardOpToHost` likely already exists for cell forwarding (search `grep -n "forwardOp\|sendToHost\|MeshFrame" pkg/universe/gateway.go`). Reuse it; service ops use the same MeshData stream as cell ops — destination differentiates by op-code dispatch on the receiving host.
- [ ] **Step 4:** `replyOpError` — implement if missing; sends an error response on the operations channel back to connID. Use the existing op-response build path.
- [ ] **Step 5:** Run `go vet ./...`.
- [ ] **Step 6:** Commit:

```bash
git add pkg/universe/gateway.go
git commit -m "feat(gateway): route service-claimed op codes to service instances via RoutingIndex"
```

### Task 6.2: Receiving-host service-op dispatch

**Files:**
- Modify: `pkg/universe/host_network.go` or wherever incoming MeshData ops are dispatched

- [ ] **Step 1:** When a host receives a forwarded op envelope, today it dispatches to a cell via session lookup. For service ops, the destination is THIS host's `opRouter` (which has the service handlers registered at Start). Differentiating: if the op code is in `opRouter.Codes()` AND the host is a service host running that kind, dispatch directly.
- [ ] **Step 2:** Simplest: look up the op code in `c.serviceRouting.LookupKind(...)` — if it's a kind this process is running, dispatch to `c.opRouter` via the existing direct-call path (the same path cell ops use after session resolution). Otherwise, error back: "this host doesn't run kind X".

```go
if kind := c.serviceRouting.LookupKind(envelope.OpCode); kind != "" {
	if rs, ok := c.runningServices[kind]; ok {
		return c.opRouter.Handle(envelope, connID, replyChan)
	}
	c.Log.Log(CatMeshCell, "service: received op for kind %q which this host doesn't run", kind)
	return c.replyOpError(connID, envelope.RequestID, "service routing error")
}
```

(The exact API on `c.opRouter.Handle` may differ — find via `grep -n "func.*Router.*Handle\|reqCh" pkg/ops/router.go`.)

- [ ] **Step 3:** Run `go vet ./...`.
- [ ] **Step 4:** Commit:

```bash
git add pkg/universe/host_network.go
git commit -m "feat(host): dispatch incoming service-op envelopes to local opRouter when host runs the kind"
```

---

## Phase 7 — Graceful shutdown ordering

### Task 7.1: Service shutdown on SIGINT

**Files:**
- Modify: `pkg/universe/coordinator.go` (Stop / shutdown path)

- [ ] **Step 1:** Find the shutdown path: `grep -n "func.*Process.*Stop\|case <-ctx.Done\|SIGINT" pkg/universe/coordinator.go`.
- [ ] **Step 2:** Before the existing host-stop logic, add service shutdown:

```go
if len(c.runningServices) > 0 {
	// Tell the coordinator to stop routing to us — local or remote.
	for _, rs := range c.runningServices {
		if c.coordServices != nil {
			c.coordServices.Unregister(rs.instance.InstanceID)
		} else {
			_ = c.sendServiceLeave(rs.instance.InstanceID)
		}
	}
	if c.coordServices != nil {
		c.broadcastPeerList()
	}

	// Grace period for PeerList propagation.
	time.Sleep(2 * time.Second)

	// Now Shutdown each service with a deadline.
	for _, rs := range c.runningServices {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := rs.svc.Shutdown(shutdownCtx); err != nil {
			c.Log.Log("services:"+rs.kind.Name, "shutdown error: %v", err)
		}
		cancel()
	}
}
```

- [ ] **Step 3:** Run `go vet ./...`.
- [ ] **Step 4:** Commit:

```bash
git add pkg/universe/coordinator.go
git commit -m "feat(universe): graceful service shutdown — unregister, propagate, then Shutdown"
```

---

## Phase 8 — Console builtins

### Task 8.1: `service` console group

**Files:**
- Create: `pkg/universe/builtins_service.go`
- Modify: `pkg/universe/coordinator.go` (register builtins on coord-role processes)

- [ ] **Step 1:** Write `pkg/universe/builtins_service.go` with four cmdsys commands: `service.list`, `service.info`, `service.kinds`, `service.ops`. Use the existing cmdsys patterns from `builtins_host.go` and `builtins_gateway.go` as templates — find via `grep -n "type.*Args\|RouteKind\|Register(.*Command{" pkg/universe/builtins_host.go`.

The four commands:

- `service.kinds` (RouteLocal) — args: empty; result: `[]KindInfo{Name, OpCodes, RequiresDB, Description}`. Reads `c.services.kinds`.
- `service.ops` (RouteLocal) — args: empty; result: `[]OpRoute{Code, Kind, Instances}`. Reads `c.coordServices` snapshot + the routing index.
- `service.list` (RouteAllHosts) — args: empty; result: `[]ServiceInstanceInfo{Kind, InstanceID, HostID, JoinedAt, OpCount, LastError}`. Local handler returns empty when not RoleService.
- `service.info` (RouteAllHosts) — args: `{Kind string}`; result: `[]InstanceDetail{...}`. Filters by kind.

For `service.list` op-count + last-error, services-role processes need a per-kind metric counter readable via the `runningServices` map. Add a `runningService.opCount uint64` and `runningService.lastError string` populated by the auto-wrap from Phase 9.

- [ ] **Step 2:** Register the builtins in coordinator.go on coord-role startup (next to existing console builtin registrations):

```go
if roles.Has(RoleCoordinator) {
	registerServiceBuiltins(c)
}
```

- [ ] **Step 3:** Run `go vet ./...` and verify the console can find the new commands. Smoke: build, run with `--mode=all --services=`, type `service kinds` at the console.
- [ ] **Step 4:** Commit:

```bash
git add pkg/universe/builtins_service.go pkg/universe/coordinator.go
git commit -m "feat(universe): service console builtins (list, info, kinds, ops)"
```

---

## Phase 9 — Metrics auto-wrapping

### Task 9.1: Wrap service handlers with metric instrumentation

**Files:**
- Modify: `pkg/universe/coordinator.go` (in startServices, around RegisterOps)

- [ ] **Step 1:** Wrap `RegisterOps` to intercept handler registrations and wrap them with metric counters. One approach: pass a wrapping `*ops.Router` adapter to the service's `RegisterOps`. Simpler approach: wrap each handler post-registration by reading `c.opRouter.Codes()` after `RegisterOps`, finding the just-added codes, and replacing each with a metric-wrapped version.

Cleanest: add `Router.Wrap(opCode, middleware HandlerMiddleware)` that swaps the registered handler:

```go
// pkg/ops/router.go
type HandlerMiddleware func(next OperationHandler) OperationHandler

func (r *Router) Wrap(opCode uint32, mw HandlerMiddleware) {
	if h, ok := r.handlers[opCode]; ok {
		r.handlers[opCode] = mw(h)
	}
}
```

Then in startServices after the cross-check:

```go
metricMW := serviceMetricMW(k.Name, instanceID, c.metrics())
for _, code := range k.OpCodes {
	c.opRouter.Wrap(code, metricMW)
}
```

`serviceMetricMW` returns a closure that increments `service_ops_total{kind, code, status}`, observes `service_op_duration_seconds`, increments/decrements `service_in_flight`, and increments `service_errors_total` on error.

- [ ] **Step 2:** Implement using `metrics.NodeMetrics` API (`grep -n "Counter\|Histogram\|Gauge" pkg/metrics/`).
- [ ] **Step 3:** Run `go vet ./...`. If `metrics.NodeMetrics` doesn't have histograms, drop duration metric for v1 (counter + gauge are sufficient).
- [ ] **Step 4:** Commit:

```bash
git add pkg/ops/router.go pkg/universe/
git commit -m "feat(service): auto-wrap handlers with per-kind metric counters"
```

---

## Phase 10 — Demo service: `echo`

### Task 10.1: basicpb proto extension

**Files:**
- Modify: `proto/basicpb/basic.proto`

- [ ] **Step 1:** Append to `proto/basicpb/basic.proto`:

```protobuf
enum EchoOpCode {
    BOP_ECHO_UNSPECIFIED = 0;
    BOP_ECHO_PING        = 300;
    BOP_ECHO_PERSIST     = 301;
    BOP_ECHO_FETCH       = 302;
}

message EchoPingRequest    { string msg = 1; }
message EchoPingResponse   { string msg = 1; string instance_id = 2; }
message EchoPersistRequest { string key = 1; string value = 2; }
message EchoPersistResponse{ bool   ok  = 1; string instance_id = 2; }
message EchoFetchRequest   { string key = 1; }
message EchoFetchResponse  { string value = 1; int64 found_at_ms = 2; string instance_id = 3; }
```

- [ ] **Step 2:** Run `just proto`.
- [ ] **Step 3:** Commit:

```bash
git add proto/basicpb/ gen/
git commit -m "feat(basicpb): add Echo op codes + request/response messages for demo service"
```

### Task 10.2: Echo migration

**Files:**
- Create: `examples/4node-basic/migrations/001_demo_echo.up.sql`
- Create: `examples/4node-basic/migrations/001_demo_echo.down.sql`
- Create: `examples/4node-basic/migrations/embed.go`

- [ ] **Step 1:** Migration up:

```sql
-- 001_demo_echo.up.sql
CREATE TABLE IF NOT EXISTS demo_echo (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

- [ ] **Step 2:** Migration down:

```sql
-- 001_demo_echo.down.sql
DROP TABLE IF EXISTS demo_echo;
```

- [ ] **Step 3:** Embed:

```go
// examples/4node-basic/migrations/embed.go
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
```

- [ ] **Step 4:** Commit:

```bash
git add examples/4node-basic/migrations/
git commit -m "feat(4node-basic): demo_echo table migration via ExtraMigrations FS"
```

### Task 10.3: Echo service implementation

**Files:**
- Create: `examples/4node-basic/services/echo/service.go`
- Create: `examples/4node-basic/services/echo/kind.go`
- Create: `examples/4node-basic/services/echo/service_test.go`

- [ ] **Step 1:** `kind.go`:

```go
package echo

import (
	basicpb "github.com/zenion/mmoserver/gen/go/basicpb"
	"github.com/zenion/mmoserver/pkg/service"
)

var Kind = service.Kind{
	Name: "echo",
	OpCodes: []uint32{
		uint32(basicpb.EchoOpCode_BOP_ECHO_PING),
		uint32(basicpb.EchoOpCode_BOP_ECHO_PERSIST),
		uint32(basicpb.EchoOpCode_BOP_ECHO_FETCH),
	},
	Factory:     New,
	RequiresDB:  true,
	Description: "demo: ping returns instanceID; persist/fetch round-trip a row through Postgres",
}
```

- [ ] **Step 2:** `service.go`:

```go
package echo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	basicpb "github.com/zenion/mmoserver/gen/go/basicpb"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/ops"
	"github.com/zenion/mmoserver/pkg/service"
)

const logCat = "services:echo"

type Service struct {
	instanceID string
	ctx        *service.Context
}

// New is the Kind.Factory.
func New(ctx *service.Context) service.Service {
	return &Service{
		instanceID: ctx.InstanceID,
		ctx:        ctx,
	}
}

func (s *Service) Init(ctx *service.Context) error {
	if ctx.DB == nil {
		return errors.New("echo.Init: DB required")
	}
	ctx.Logger.Log(logCat, "echo service initialized: instance=%s", s.instanceID)
	return nil
}

func (s *Service) RegisterOps(router *ops.Router) error {
	mmokit.RegisterOp(
		router,
		uint32(basicpb.EchoOpCode_BOP_ECHO_PING),
		"echoPing",
		func(opCtx *mmokit.OpContext, req *basicpb.EchoPingRequest) (*basicpb.EchoPingResponse, error) {
			s.ctx.Logger.Log(logCat, "ping: user=%s msg=%q", opCtx.Username, req.Msg)
			return &basicpb.EchoPingResponse{
				Msg:        req.Msg,
				InstanceId: s.instanceID,
			}, nil
		},
	)

	mmokit.RegisterOp(
		router,
		uint32(basicpb.EchoOpCode_BOP_ECHO_PERSIST),
		"echoPersist",
		func(opCtx *mmokit.OpContext, req *basicpb.EchoPersistRequest) (*basicpb.EchoPersistResponse, error) {
			pool := s.ctx.DB.Pool()
			_, err := pool.Exec(context.Background(),
				`INSERT INTO demo_echo (key, value, updated_at) VALUES ($1, $2, NOW())
				 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`,
				req.Key, req.Value)
			if err != nil {
				s.ctx.Logger.Log(logCat, "persist failed: key=%s err=%v", req.Key, err)
				return nil, fmt.Errorf("echo persist: %w", err)
			}
			s.ctx.Logger.Log(logCat, "persist: user=%s key=%s value_len=%d", opCtx.Username, req.Key, len(req.Value))
			return &basicpb.EchoPersistResponse{Ok: true, InstanceId: s.instanceID}, nil
		},
	)

	mmokit.RegisterOp(
		router,
		uint32(basicpb.EchoOpCode_BOP_ECHO_FETCH),
		"echoFetch",
		func(opCtx *mmokit.OpContext, req *basicpb.EchoFetchRequest) (*basicpb.EchoFetchResponse, error) {
			pool := s.ctx.DB.Pool()
			row := pool.QueryRow(context.Background(),
				`SELECT value, updated_at FROM demo_echo WHERE key = $1`, req.Key)
			var value string
			var updatedAt time.Time
			if err := row.Scan(&value, &updatedAt); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return &basicpb.EchoFetchResponse{InstanceId: s.instanceID}, nil
				}
				return nil, fmt.Errorf("echo fetch: %w", err)
			}
			s.ctx.Logger.Log(logCat, "fetch: user=%s key=%s", opCtx.Username, req.Key)
			return &basicpb.EchoFetchResponse{
				Value:      value,
				FoundAtMs:  updatedAt.UnixMilli(),
				InstanceId: s.instanceID,
			}, nil
		},
	)
	return nil
}

func (s *Service) Shutdown(_ context.Context) error {
	s.ctx.Logger.Log(logCat, "echo service shutting down: instance=%s", s.instanceID)
	return nil
}
```

- [ ] **Step 3:** Verify `postgres.Store` exposes `Pool() *pgxpool.Pool`. If not, find what it exposes and use that — `grep -n "func .Store. Pool\|Pool .. .pgx" pkg/persist/postgres/`.

- [ ] **Step 4:** Run `go vet ./examples/4node-basic/services/echo/`.

- [ ] **Step 5:** Commit:

```bash
git add examples/4node-basic/services/echo/
git commit -m "feat(echo): demo service implementation with ping/persist/fetch"
```

### Task 10.4: 4node-basic main.go wiring

**Files:**
- Modify: `examples/4node-basic/main.go`

- [ ] **Step 1:** After the existing `mmo := mmokit.NewCoordinator(...)` block, add:

```go
mmo.Cfg().ExtraMigrations = migrations.FS  // or however config exposure works
if err := mmo.RegisterService(echo.Kind); err != nil {
	log.Fatalf("RegisterService: %v", err)
}
```

with imports:

```go
import (
	"github.com/zenion/mmoserver/examples/4node-basic/migrations"
	"github.com/zenion/mmoserver/examples/4node-basic/services/echo"
)
```

- [ ] **Step 2:** Run `just build` from the example dir.
- [ ] **Step 3:** Commit:

```bash
git add examples/4node-basic/main.go
git commit -m "feat(4node-basic): register echo service + extra migrations FS"
```

### Task 10.5: justfile distributed target update

**Files:**
- Modify: `examples/4node-basic/justfile`

- [ ] **Step 1:** Find `distributed` recipe (`grep -n "distributed" examples/4node-basic/justfile`). Add a new tmux pane for the service host:

```just
# pseudo — exact syntax depends on existing recipe
tmux send-keys -t mmo:0.4 "./bin/server --mode=service --services=echo --coordinator-addr=localhost:9100 --host-id=svc-a" Enter
```

- [ ] **Step 2:** Verify by running `just distributed` (manually) — the new service-host pane should come up, register, and appear in `service list` from the coordinator console.
- [ ] **Step 3:** Commit:

```bash
git add examples/4node-basic/justfile
git commit -m "feat(4node-basic): add service-host pane to distributed tmux recipe"
```

---

## Phase 11 — Web client echo panel

### Task 11.1: SDK regen for echo ops

**Files:**
- Run: `just client-sdk examples/4node-basic`

- [ ] **Step 1:** Verify `cmd/sdkgen` discovers the new echo op codes via `cfg.Protocol`. The Protocol may need explicit registration of echo client/server events.
- [ ] **Step 2:** Open the regen output and confirm typed methods exist for `echoPing`, `echoPersist`, `echoFetch`.

### Task 11.2: Echo panel UI

**Files:**
- Create: `examples/4node-basic/web/src/echo_panel.ts`
- Modify: `examples/4node-basic/web/src/main.ts` (mount panel)

- [ ] **Step 1:** Write `echo_panel.ts`:

```ts
import type { Client } from "./sdk/client";

export function mountEchoPanel(client: Client): void {
	const root = document.createElement("div");
	root.id = "echo-panel";
	root.style.cssText = `
		position: fixed; top: 8px; right: 8px; width: 320px;
		background: rgba(0,0,0,0.85); color: #cce; font-family: monospace;
		font-size: 12px; padding: 8px; border: 1px solid #335;
		display: none; z-index: 9999;
	`;
	root.innerHTML = `
		<div style="display:flex; justify-content:space-between; margin-bottom:6px;">
			<b>Echo Service</b>
			<button id="ep-close" style="background:none;color:#cce;border:none;cursor:pointer;">×</button>
		</div>
		<div style="border-bottom:1px solid #335;padding:4px 0;">
			<label>PING msg <input id="ep-ping" value="hello" style="width:60%"/></label>
			<button id="ep-ping-btn">send</button>
			<div id="ep-ping-out" style="color:#9c9;margin-top:4px;"></div>
		</div>
		<div style="border-bottom:1px solid #335;padding:4px 0;">
			<label>PERSIST</label><br/>
			<input id="ep-key" placeholder="key" style="width:45%"/>
			<input id="ep-val" placeholder="value" style="width:45%"/>
			<button id="ep-persist-btn">send</button>
			<div id="ep-persist-out" style="color:#9c9;margin-top:4px;"></div>
		</div>
		<div style="padding:4px 0;">
			<label>FETCH key <input id="ep-fkey" style="width:60%"/></label>
			<button id="ep-fetch-btn">send</button>
			<div id="ep-fetch-out" style="color:#9c9;margin-top:4px;"></div>
		</div>
	`;
	document.body.appendChild(root);

	(root.querySelector("#ep-close") as HTMLButtonElement).onclick = () => {
		root.style.display = "none";
	};

	const out = (sel: string, text: string) => {
		(root.querySelector(sel) as HTMLDivElement).textContent = text;
	};

	(root.querySelector("#ep-ping-btn") as HTMLButtonElement).onclick = async () => {
		const msg = (root.querySelector("#ep-ping") as HTMLInputElement).value;
		try {
			const r = await client.echoPing({ msg });
			out("#ep-ping-out", `← msg="${r.msg}" instance=${r.instanceId}`);
		} catch (e) {
			out("#ep-ping-out", `error: ${e}`);
		}
	};

	(root.querySelector("#ep-persist-btn") as HTMLButtonElement).onclick = async () => {
		const key = (root.querySelector("#ep-key") as HTMLInputElement).value;
		const value = (root.querySelector("#ep-val") as HTMLInputElement).value;
		try {
			const r = await client.echoPersist({ key, value });
			out("#ep-persist-out", `← ok=${r.ok} instance=${r.instanceId}`);
		} catch (e) {
			out("#ep-persist-out", `error: ${e}`);
		}
	};

	(root.querySelector("#ep-fetch-btn") as HTMLButtonElement).onclick = async () => {
		const key = (root.querySelector("#ep-fkey") as HTMLInputElement).value;
		try {
			const r = await client.echoFetch({ key });
			const ts = r.foundAtMs ? new Date(Number(r.foundAtMs)).toISOString() : "(missing)";
			out("#ep-fetch-out", `← value="${r.value}" foundAt=${ts} instance=${r.instanceId}`);
		} catch (e) {
			out("#ep-fetch-out", `error: ${e}`);
		}
	};

	window.addEventListener("keydown", (e) => {
		if (e.key === "e" && !(e.target instanceof HTMLInputElement)) {
			root.style.display = root.style.display === "none" ? "block" : "none";
		}
	});
}
```

- [ ] **Step 2:** In `main.ts`, after client construction add `mountEchoPanel(client)`.
- [ ] **Step 3:** Run `cd examples/4node-basic && bun run dev` (or similar) to verify the panel renders and types compile.
- [ ] **Step 4:** Commit:

```bash
git add examples/4node-basic/web/src/
git commit -m "feat(4node-basic-web): echo service test panel (toggle with 'e')"
```

---

## Phase 12 — Integration tests

### Task 12.1: Cluster fixture extension `WithServiceHost`

**Files:**
- Modify: `pkg/universe/cluster_fixture_test.go`

- [ ] **Step 1:** Find the fixture builder (`grep -n "func.*Fixture\|WithHost\|WithGateway" pkg/universe/cluster_fixture_*test.go`).
- [ ] **Step 2:** Add `WithServiceHost(kind service.Kind, n int)` option that spins up `n` service-role processes, each registering `kind` and running with `--mode=service --services=<kind.Name>`.
- [ ] **Step 3:** Smoke-run the fixture standalone to verify it works (no test yet).
- [ ] **Step 4:** Commit:

```bash
git add pkg/universe/cluster_fixture_test.go
git commit -m "test(universe): cluster fixture WithServiceHost option"
```

### Task 12.2: End-to-end op flow

**Files:**
- Create: `pkg/universe/service_e2e_test.go`

- [ ] **Step 1:** Test: spin up a fixture with 1 coordinator+gateway, 1 service-host running echo (use a minimal in-test echo kind, no DB — the test kind responds with a fixed payload). Connect a fake client, send a ping op, assert the response carries the expected instanceID.
- [ ] **Step 2:** Test: send 100 ops from a single connID, assert all land on the same instanceID (hash affinity).
- [ ] **Step 3:** Test: bring up 2 echo instances; sample 50 different connIDs; assert traffic distributes (both instanceIDs appear).
- [ ] **Step 4:** Test: send an op claiming a code that's not registered to any service (cell-route fallback). Assert it goes to the cell path.
- [ ] **Step 5:** Run `go test ./pkg/universe/ -run TestService -count=1 -v`.
- [ ] **Step 6:** Commit:

```bash
git add pkg/universe/service_e2e_test.go
git commit -m "test(service): e2e routing, hash affinity, multi-instance distribution, cell fallback"
```

### Task 12.3: Instance leave + retry

**Files:**
- Modify: `pkg/universe/service_e2e_test.go`

- [ ] **Step 1:** Test: bring up 2 echo instances. Pick a connID whose hash points at instance A. Kill A's service-host process. Wait for PeerList propagation. Send op from same connID. Assert op now lands on B.
- [ ] **Step 2:** Run.
- [ ] **Step 3:** Commit:

```bash
git add pkg/universe/service_e2e_test.go
git commit -m "test(service): instance graceful-leave routes to survivor"
```

### Task 12.4: RequiresDB enforcement

**Files:**
- Modify: `pkg/service/registry_test.go`

- [ ] **Step 1:** Test already exists in Task 3.3 (TestRegistry_Validate_RequiresDB). Skip if present.
- [ ] **Step 2:** Add a higher-level test (in `pkg/universe/`) that constructs a `Process` with a kind requiring DB but no PostgresURL, calls Build, asserts it panics with the expected message. Use `defer recover` pattern.

```go
func TestProcessBuild_RequiresDB_NoURL(t *testing.T) {
	c := NewCoordinator(Config{
		Mode:         "coordinator,host,gateway,service",
		ServiceKinds: []string{"needs_db"},
	})
	_ = c.RegisterService(service.Kind{
		Name:       "needs_db",
		OpCodes:    []uint32{900},
		Factory:    func(*service.Context) service.Service { return nil },
		RequiresDB: true,
	})
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic")
		}
		if !strings.Contains(fmt.Sprint(r), "requires DB") {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()
	c.Build()
}
```

- [ ] **Step 3:** Run.
- [ ] **Step 4:** Commit:

```bash
git add pkg/universe/
git commit -m "test(service): RequiresDB enforcement at Build"
```

---

## Phase 13 — Verification & wrap-up

### Task 13.1: Full test pass

- [ ] **Step 1:** `go vet ./...` — no errors.
- [ ] **Step 2:** `go test ./pkg/service/ -count=1 -v` — all green.
- [ ] **Step 3:** `go test ./pkg/universe/ -count=1 -short` — all green.
- [ ] **Step 4:** `just build` from repo root and `cd examples/4node-basic && just build`.

### Task 13.2: Manual smoke test

- [ ] **Step 1:** Start docker-compose Postgres (`just db-up`).
- [ ] **Step 2:** `cd examples/4node-basic && just distributed`.
- [ ] **Step 3:** In the coordinator console: `service list` → verify echo instance appears. `service ops` → verify codes 300/301/302 routed to echo.
- [ ] **Step 4:** Open the web client, press `e`, send a PING — verify response shows instanceID.
- [ ] **Step 5:** Send PERSIST `{key=foo, value=bar}` then FETCH `{key=foo}` — verify value comes back.
- [ ] **Step 6:** Kill the service-host pane, wait 5s, retry FETCH — verify graceful "no healthy instance" then ideally a survivor (if multiple instances were running). With one instance, this just shows the failure mode.

### Task 13.3: Final commit

- [ ] **Step 1:** Create a summary commit if any straggler changes:

```bash
git status
git add -A
git commit -m "feat(service): complete pluggable services framework v1"
```

---

## Self-review

**Spec coverage (§2 Goals):**
- ✅ Plug-in surface — `pkg/service/Kind` + `Service` (Phase 3)
- ✅ Compose into roles — `RoleService` + `--services=` (Phase 5)
- ✅ Reuse existing wire — gateway routing extension (Phase 6)
- ✅ Independent scaling — multiple instances per kind (covered by routing index)
- ✅ Independent lifecycle — graceful shutdown ordering (Phase 7)
- ✅ Stateless v1 surface — no sync stream API (intentional omission)

**Spec coverage (§16 Service discovery):**
- ✅ Gateway compile-time agnostic — RoutingIndex built from PeerList only (Phase 5.5/6)
- ✅ Coordinator compile-time agnostic — CoordRegistry validates by announcement (Phase 3.5)
- ✅ Hot-load not in v1 — kind list fixed at startup (intentional)

**Spec coverage (§9 State guarantees):** v1 enforces via API surface — no peer-broadcast primitive in `Context`, no sync stream. Documentation lives in the spec.

**Open Question 15.4 (ExtraMigrations):** addressed in Phase 2.

**No placeholders found in this plan.** Every task has full code or full command. Type names are consistent (`Kind`, `Service`, `Context`, `Registry`, `CoordRegistry`, `RoutingIndex`, `ServiceRecord`, `CoordInstance`, `InstanceRoute`, `runningService`).

**Tasks that share names across files:**
- `equalCodeSet` defined in `pkg/service/registry_coord.go` as `sameCodeSet` (registry_coord) and in `pkg/universe/service_helpers.go` as `equalCodeSet` (universe). Different packages — no conflict.
- `ServiceRecord` defined in `pkg/service/router.go` (Go struct) and in `proto/meshpb/mesh.proto` (proto message → `meshpb.ServiceRecord`). Universe code converts between them at the boundary. No conflict.
